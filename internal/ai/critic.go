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
}

// Critic uses an HF model to verify generated articles against their sources.
type Critic struct {
	client HFClientInterface
	model  string
}

// NewCritic creates a new Critic.
func NewCritic(client HFClientInterface, model string) *Critic {
	return &Critic{client: client, model: model}
}

// Verify sends the generated article and sources to the critic model and
// returns a structured verification result.
func (c *Critic) Verify(generatedArticle string, sources []SourceArticle) (*VerificationResult, error) {
	prompt := CriticPrompt(generatedArticle, sources)

	resp, err := c.client.Generate(c.model, prompt, 512)
	if err != nil {
		return nil, fmt.Errorf("critic: %w", err)
	}

	jsonStr := extractJSON(resp)
	if jsonStr == "" {
		return nil, fmt.Errorf("critic: no JSON object found in response: %s", resp)
	}

	var result VerificationResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("critic: parse response: %w", err)
	}

	return &result, nil
}

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
