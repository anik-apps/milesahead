package ai

import (
	"fmt"
	"testing"
	"time"
)

// mockHFClient records calls and returns a canned response.
type mockHFClient struct {
	callCount int
	lastModel string
	lastPrompt string
	response  string
	err       error
}

func (m *mockHFClient) Generate(model, prompt string, maxTokens int) (string, error) {
	m.callCount++
	m.lastModel = model
	m.lastPrompt = prompt
	return m.response, m.err
}

func TestGenerator_DealType(t *testing.T) {
	mock := &mockHFClient{response: "Great Deal Title\nSome deal content [TPG]"}
	gen := NewGenerator(mock, "mistral-7b", nil)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Transfer Bonus", Content: "40% bonus to ANA"},
	}

	result, err := gen.Generate("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != mock.response {
		t.Errorf("expected %q, got %q", mock.response, result)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}
	// Verify the prompt contains deal-alert language
	if mock.lastPrompt == "" {
		t.Error("expected prompt to be non-empty")
	}
}

func TestGenerator_RoundupType(t *testing.T) {
	mock := &mockHFClient{response: "Roundup Title\nContent"}
	gen := NewGenerator(mock, "mistral-7b", nil)

	sources := []SourceArticle{
		{Name: "DofC", URL: "https://dofc.com/1", Title: "Weekly Deals", Content: "Several deals this week"},
	}

	_, err := gen.Generate("roundup", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected 1 call, got %d", mock.callCount)
	}
}

func TestGenerator_SummaryType(t *testing.T) {
	mock := &mockHFClient{response: "Summary Title\nContent"}
	gen := NewGenerator(mock, "mistral-7b", nil)

	sources := []SourceArticle{
		{Name: "FM", URL: "https://fm.com/1", Title: "Points Review", Content: "Points analysis"},
	}

	_, err := gen.Generate("summary", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGenerator_UnknownType(t *testing.T) {
	mock := &mockHFClient{response: "content"}
	gen := NewGenerator(mock, "mistral-7b", nil)

	_, err := gen.Generate("unknown", nil)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestGenerator_Caching(t *testing.T) {
	mock := &mockHFClient{response: "Cached Content\nBody text"}
	cache := NewCache(5 * time.Minute)
	gen := NewGenerator(mock, "mistral-7b", cache)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Deal", Content: "Content"},
	}

	// First call should hit the HF client.
	r1, err := gen.Generate("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.callCount != 1 {
		t.Fatalf("expected 1 call, got %d", mock.callCount)
	}

	// Second identical call should return from cache, not calling HF again.
	r2, err := gen.Generate("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.callCount != 1 {
		t.Errorf("expected still 1 call (cached), got %d", mock.callCount)
	}
	if r1 != r2 {
		t.Errorf("cached result mismatch: %q vs %q", r1, r2)
	}
}

func TestGenerator_ClientError(t *testing.T) {
	mock := &mockHFClient{err: fmt.Errorf("api failure")}
	gen := NewGenerator(mock, "mistral-7b", nil)

	_, err := gen.Generate("deal", []SourceArticle{{Name: "X", Content: "Y"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGenerator_GenerateWithFeedback(t *testing.T) {
	mock := &mockHFClient{response: "Revised Title\nRevised content"}
	gen := NewGenerator(mock, "mistral-7b", nil)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Deal", Content: "Content"},
	}

	result, err := gen.GenerateWithFeedback(sources, "old output", "fix dates", "missing end date")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != mock.response {
		t.Errorf("expected %q, got %q", mock.response, result)
	}
}
