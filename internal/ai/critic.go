package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

// ErrCriticUnavailable is wrapped by every critic error where no model in the
// critic chain answered: retired, overloaded, timed out, refused. A reply that
// came back but would not parse is a different error. The scan stops
// generating when it sees this (a critic that cannot answer would fail every
// remaining article too), and a revision treats it as "try later".
var ErrCriticUnavailable = errors.New("no critic answered")

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
		return nil, fmt.Errorf("%s: %w: %w", label, ErrCriticUnavailable, err)
	}

	jsonStr := extractJSON(resp)
	if jsonStr == "" {
		c.reportBadReply(model, "no JSON object in critic reply")
		return nil, fmt.Errorf("%s (model %s): no JSON object found in response: %s", label, model, resp)
	}

	var reply criticReply
	if err := json.Unmarshal([]byte(jsonStr), &reply); err != nil {
		c.reportBadReply(model, "critic JSON did not parse: "+err.Error())
		return nil, fmt.Errorf("%s (model %s): parse response: %w", label, model, err)
	}
	c.reportOK(model)
	result := reply.VerificationResult
	result.Issues = make([]string, 0, len(reply.Issues))
	for _, issue := range reply.Issues {
		result.Issues = append(result.Issues, string(issue))
	}
	result.Model = model
	if dropped := dropPassedChecks(&result); len(dropped) > 0 {
		slog.Info("dropped critic issues that confirm a passed check", "label", label,
			"model", model, "dropped", len(dropped), "kept", len(result.Issues))
	}

	return &result, nil
}

// reportBadReply tells model health that model answered with a reply the
// critic could not use, when the client is a labelled HFClient.
func (c *Critic) reportBadReply(model, detail string) {
	if r, ok := c.client.(interface{ ReportBadReply(model, detail string) }); ok {
		r.ReportBadReply(model, detail)
	}
}

// reportOK confirms to model health that model's reply parsed, when the
// client is a labelled HFClient that waits for confirmation.
func (c *Critic) reportOK(model string) {
	if r, ok := c.client.(interface{ ReportOK(model string) }); ok {
		r.ReportOK(model)
	}
}

// criticReply is a verdict as the critic sends it. Its issues field shadows
// VerificationResult.Issues so each item can be read leniently.
type criticReply struct {
	VerificationResult
	Issues []criticIssue `json:"issues"`
}

// criticIssue is one item of a critic's "issues" array. The contract is a
// string, but asked for what is wrong and how to fix it, a critic has replied
// with {"problem": ..., "fix": ...} objects instead. A parse error drops the
// article from the scan, so an object is flattened into one string rather
// than rejected.
type criticIssue string

func (ci *criticIssue) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*ci = criticIssue(s)
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		// Neither a string nor an object: keep the raw JSON as the text.
		*ci = criticIssue(strings.TrimSpace(string(b)))
		return nil
	}
	var parts []string
	used := map[string]bool{}
	take := func(prefix string, keys ...string) {
		for _, k := range keys {
			if v, ok := obj[k].(string); ok && strings.TrimSpace(v) != "" && !used[k] {
				used[k] = true
				parts = append(parts, prefix+strings.TrimSpace(v))
			}
		}
	}
	take("", "issue", "problem", "description", "text", "location")
	take("Fix: ", "fix", "suggestion", "recommendation")
	if len(parts) == 0 {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		take("", keys...)
	}
	if len(parts) == 0 {
		*ci = criticIssue(strings.TrimSpace(string(b)))
		return nil
	}
	*ci = criticIssue(strings.Join(parts, " "))
	return nil
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
