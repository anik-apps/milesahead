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
		{Name: "OMAAT", URL: "https://omaat.com/1", Title: "ANA Bonus", Content: "40% transfer bonus"},
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
	// formatContent strips the title line and wraps body in HTML
	expected := "<p>Transfer bonus to ANA [TPG]</p>"
	if result.Content != expected {
		t.Errorf("content mismatch: expected %q, got %q", expected, result.Content)
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
		{Name: "VFTW", URL: "https://vftw.com/1", Title: "Deals", Content: "more deals"},
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

func TestPipeline_Process_SingleSourceRejected(t *testing.T) {
	gen := NewGenerator(&mockHFClient{response: "Title\nContent"}, "model", nil)
	critic := NewCritic(&mockHFClient{response: `{"score":90,"issues":[],"factual_check":true,"attribution_check":true,"completeness_check":true,"hallucination_check":true}`}, "model")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Deal", Content: "info"},
	}

	_, err := pipeline.Process("deal", sources)
	if err != ErrNotEnoughSources {
		t.Errorf("expected ErrNotEnoughSources, got: %v", err)
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
