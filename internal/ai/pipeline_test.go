package ai

import (
	"testing"
)

func TestPipeline_Process_AboveThreshold(t *testing.T) {
	genMock := &mockHFClient{response: "Deal Alert: 40% Bonus\nTransfer bonus to ANA [TPG]"}
	criticMock := &mockHFClient{
		response: `{"score": 90, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus", Content: "40% bonus to ANA"},
	}

	result, err := pipeline.Process("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PassedThreshold {
		t.Error("expected PassedThreshold to be true for score 90 >= threshold 80")
	}
	if result.Verification.Score != 90 {
		t.Errorf("expected score 90, got %d", result.Verification.Score)
	}
	if result.Type != "deal" {
		t.Errorf("expected type 'deal', got %q", result.Type)
	}
	if result.Title != "Deal Alert: 40% Bonus" {
		t.Errorf("expected title 'Deal Alert: 40%% Bonus', got %q", result.Title)
	}
	if result.Content != genMock.response {
		t.Errorf("content mismatch")
	}
}

func TestPipeline_Process_BelowThreshold(t *testing.T) {
	genMock := &mockHFClient{response: "Bad Article Title\nSome content"}
	criticMock := &mockHFClient{
		response: `{"score": 40, "issues": ["Missing citations", "Hallucinated data"], "factual_check": false, "attribution_check": false, "completeness_check": false, "hallucination_check": false}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "DofC", URL: "https://dofc.com/1", Title: "Deals", Content: "deals info"},
	}

	result, err := pipeline.Process("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PassedThreshold {
		t.Error("expected PassedThreshold to be false for score 40 < threshold 80")
	}
	// adjustScore penalizes failed checks: 40 - (4 * 15) = -20, capped to 0
	if result.Verification.Score != 0 {
		t.Errorf("expected adjusted score 0, got %d", result.Verification.Score)
	}
}

func TestPipeline_ProcessWithFeedback(t *testing.T) {
	genMock := &mockHFClient{response: "Revised Title\nRevised content [TPG]"}
	criticMock := &mockHFClient{
		response: `{"score": 95, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus", Content: "40% bonus"},
	}

	result, err := pipeline.ProcessWithFeedback(sources, "old article", "fix dates", "missing end date")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PassedThreshold {
		t.Error("expected PassedThreshold to be true")
	}
	if result.Title != "Revised Title" {
		t.Errorf("expected title 'Revised Title', got %q", result.Title)
	}
}
