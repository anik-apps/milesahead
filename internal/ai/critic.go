package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// VerificationResult holds the critic model's assessment of a generated article.
type VerificationResult struct {
	Score              int      `json:"score"`
	Issues             []string `json:"issues"`
	FactualCheck       bool     `json:"factual_check"`
	AttributionCheck   bool     `json:"attribution_check"`
	CompletenessCheck  bool     `json:"completeness_check"`
	HallucinationCheck bool     `json:"hallucination_check"`
	// Model is the critic model that produced this result -- with a chain,
	// not necessarily the first. Set by the Critic, never parsed from the reply.
	Model string `json:"-"`
}

// Critic uses an HF model to verify generated articles against their sources.
type Critic struct {
	client HFClientInterface
	// models is tried in order through TryModels, moving on when a model is
	// overloaded (429/5xx, timeout) or gone (404/410).
	//
	// The critic was a single model, and it is the one call on the article path
	// with no substitute: an error drops the article from the scan outright
	// (a low score only makes it a draft). On 2026-09-29/30 the lone critic,
	// deepseek-ai/deepseek-v4.1-flash, answered every call with a gateway 504,
	// so every article generated in that window was thrown away.
	models []string
}

// NewCritic creates a new Critic. model is one model or a comma-separated
// chain, parsed exactly as the generator fallbacks are (SplitModels).
func NewCritic(client HFClientInterface, model string) *Critic {
	return &Critic{client: client, models: SplitModels(model)}
}

// Models returns the critic chain, in the order it is tried.
func (c *Critic) Models() []string {
	return append([]string(nil), c.models...)
}

// Verify sends the generated article and sources to the critic chain and
// returns a structured verification result, recording which model scored it.
func (c *Critic) Verify(generatedArticle string, sources []SourceArticle) (*VerificationResult, error) {
	return c.verify("critic", CriticPrompt(generatedArticle, sources))
}

// Verify2 runs the second critic prompt (reasoning/consistency focused).
func (c *Critic) Verify2(generatedArticle string, sources []SourceArticle) (*VerificationResult, error) {
	return c.verify("critic2", Critic2Prompt(generatedArticle, sources))
}

// verify runs a critic prompt through the chain. Only a failed call moves on
// to the next model; a reply without parseable JSON is an error, as before.
func (c *Critic) verify(label, prompt string) (*VerificationResult, error) {
	// 4096 tokens: long enough that HFClient gives the call the full request
	// timeout rather than ShortCallTimeout -- critics take tens of seconds.
	resp, model, err := tryModels(c.client, c.models, prompt, 4096)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	jsonStr := extractJSON(resp)
	if jsonStr == "" {
		return nil, fmt.Errorf("%s (model %s): no JSON object found in response: %s", label, model, resp)
	}

	var result VerificationResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("%s (model %s): parse response: %w", label, model, err)
	}
	result.Model = model

	return &result, nil
}

// ExtractJSON finds the first JSON object ({...}) in s, for callers outside
// this package that parse a model's JSON reply.
func ExtractJSON(s string) string { return extractJSON(s) }

// extractJSON finds the first JSON object ({...}) in the given string.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
