package ai

import (
	"fmt"
	"testing"
)

func TestCritic_Verify(t *testing.T) {
	mock := &mockHFClient{
		response: `{"score": 85, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}
	critic := NewCritic(mock, "llama-3-8b")

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus Deal", Content: "40% transfer bonus"},
	}

	result, err := critic.Verify("Generated article text [TPG]", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 85 {
		t.Errorf("expected score 85, got %d", result.Score)
	}
	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(result.Issues))
	}
	if !result.FactualCheck {
		t.Error("expected FactualCheck to be true")
	}
	if !result.AttributionCheck {
		t.Error("expected AttributionCheck to be true")
	}
	if !result.CompletenessCheck {
		t.Error("expected CompletenessCheck to be true")
	}
	if !result.HallucinationCheck {
		t.Error("expected HallucinationCheck to be true")
	}
}

func TestCritic_VerifyWithIssues(t *testing.T) {
	mock := &mockHFClient{
		response: `{"score": 45, "issues": ["Missing end date", "Orphan citation [FM]"], "factual_check": false, "attribution_check": true, "completeness_check": false, "hallucination_check": true}`,
	}
	critic := NewCritic(mock, "llama-3-8b")

	result, err := critic.Verify("Bad article", []SourceArticle{{Name: "TPG", Content: "data"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 45 {
		t.Errorf("expected score 45, got %d", result.Score)
	}
	if len(result.Issues) != 2 {
		t.Errorf("expected 2 issues, got %d", len(result.Issues))
	}
	if result.FactualCheck {
		t.Error("expected FactualCheck to be false")
	}
	if result.CompletenessCheck {
		t.Error("expected CompletenessCheck to be false")
	}
}

func TestCritic_VerifyExtraText(t *testing.T) {
	// Model sometimes wraps JSON in extra text.
	mock := &mockHFClient{
		response: `Here is the verification result:
{"score": 90, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}
Hope that helps!`,
	}
	critic := NewCritic(mock, "llama-3-8b")

	result, err := critic.Verify("article", []SourceArticle{{Name: "X", Content: "Y"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Score != 90 {
		t.Errorf("expected score 90, got %d", result.Score)
	}
}

func TestCritic_VerifyClientError(t *testing.T) {
	mock := &mockHFClient{err: fmt.Errorf("api error")}
	critic := NewCritic(mock, "llama-3-8b")

	_, err := critic.Verify("article", []SourceArticle{{Name: "X", Content: "Y"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCritic_VerifyBadJSON(t *testing.T) {
	mock := &mockHFClient{response: "not json at all"}
	critic := NewCritic(mock, "llama-3-8b")

	_, err := critic.Verify("article", []SourceArticle{{Name: "X", Content: "Y"}})
	if err == nil {
		t.Fatal("expected error for bad JSON")
	}
}
