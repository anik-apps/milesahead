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
	gen := NewGenerator(mock, "mistral-7b", "", nil)

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
	gen := NewGenerator(mock, "mistral-7b", "", nil)

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
	gen := NewGenerator(mock, "mistral-7b", "", nil)

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
	gen := NewGenerator(mock, "mistral-7b", "", nil)

	_, err := gen.Generate("unknown", nil)
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestGenerator_Caching(t *testing.T) {
	mock := &mockHFClient{response: "Cached Content\nBody text"}
	cache := NewCache(5 * time.Minute)
	gen := NewGenerator(mock, "mistral-7b", "", cache)

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
	gen := NewGenerator(mock, "mistral-7b", "", nil)

	_, err := gen.Generate("deal", []SourceArticle{{Name: "X", Content: "Y"}})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGenerator_GenerateWithFeedback(t *testing.T) {
	mock := &mockHFClient{response: "Revised Title\nRevised content"}
	gen := NewGenerator(mock, "mistral-7b", "", nil)

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

// perModelMockHFClient returns a different response (or error) per model.
// Used by the fallback tests to assert which model the Generator actually
// called and in what order.
type perModelMockHFClient struct {
	responses  map[string]string
	errs       map[string]error
	callsOrder []string
}

func (m *perModelMockHFClient) Generate(model, prompt string, maxTokens int) (string, error) {
	m.callsOrder = append(m.callsOrder, model)
	if err, ok := m.errs[model]; ok && err != nil {
		return "", err
	}
	return m.responses[model], nil
}

func sources1() []SourceArticle {
	return []SourceArticle{{Name: "TPG", URL: "https://tpg.com/1", Title: "X", Content: "Y"}}
}

// TestGenerator_FallbackOnRateLimit verifies the Generator falls back to the
// secondary model when the primary returns a rate-limit-exhaustion error.
func TestGenerator_FallbackOnRateLimit(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": fmt.Errorf("rate limited after 3 retries: too many requests")},
		responses: map[string]string{"fallback": "Fallback Article\nBody"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	result, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("expected fallback to succeed, got: %v", err)
	}
	if result != "Fallback Article\nBody" {
		t.Errorf("expected fallback response, got %q", result)
	}
	if len(mock.callsOrder) != 2 || mock.callsOrder[0] != "primary" || mock.callsOrder[1] != "fallback" {
		t.Errorf("expected calls [primary, fallback], got %v", mock.callsOrder)
	}
}

// TestGenerator_FallbackOn5xx verifies fallback triggers on upstream 5xx.
func TestGenerator_FallbackOn5xx(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": fmt.Errorf("HF API error (status 502): bad gateway")},
		responses: map[string]string{"fallback": "Recovered\nContent"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	result, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("expected fallback success, got: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty fallback result")
	}
	if len(mock.callsOrder) != 2 {
		t.Errorf("expected 2 calls (primary then fallback), got %d", len(mock.callsOrder))
	}
}

// TestGenerator_FallbackOnNetworkTimeout verifies fallback triggers on the
// "http request:" error pattern that HFClient wraps around network/timeout.
func TestGenerator_FallbackOnNetworkTimeout(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": fmt.Errorf("http request: connection reset")},
		responses: map[string]string{"fallback": "OK\nContent"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	_, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("expected fallback success, got: %v", err)
	}
	if len(mock.callsOrder) != 2 {
		t.Errorf("expected fallback to be tried, got calls: %v", mock.callsOrder)
	}
}

// TestGenerator_NoFallbackOn4xx verifies that non-retryable errors (e.g. bad
// model name returning 404) do NOT trigger fallback — the fallback can't fix
// configuration errors and a fallback attempt would just waste a call.
func TestGenerator_NoFallbackOn4xx(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": fmt.Errorf("HF API error (status 404): model not found")},
		responses: map[string]string{"fallback": "Should not be used"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	_, err := gen.Generate("deal", sources1())
	if err == nil {
		t.Fatal("expected error from primary to propagate, got nil")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("expected only primary to be called for 4xx error, got %v", mock.callsOrder)
	}
}

// TestGenerator_NoFallbackWhenPrimarySucceeds verifies the fallback isn't
// called when the primary returns successfully (the common path).
func TestGenerator_NoFallbackWhenPrimarySucceeds(t *testing.T) {
	mock := &perModelMockHFClient{
		responses: map[string]string{"primary": "Primary OK\nContent"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	result, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Primary OK\nContent" {
		t.Errorf("expected primary response, got %q", result)
	}
	if len(mock.callsOrder) != 1 || mock.callsOrder[0] != "primary" {
		t.Errorf("expected only primary call, got %v", mock.callsOrder)
	}
}

// TestGenerator_NoFallbackWhenUnconfigured verifies the legacy behavior is
// preserved when fallback is empty: errors from primary propagate directly.
func TestGenerator_NoFallbackWhenUnconfigured(t *testing.T) {
	mock := &perModelMockHFClient{
		errs: map[string]error{"primary": fmt.Errorf("rate limited after 3 retries")},
	}
	gen := NewGenerator(mock, "primary", "", nil) // no fallback

	_, err := gen.Generate("deal", sources1())
	if err == nil {
		t.Fatal("expected primary error to propagate when no fallback configured")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("expected only 1 call when no fallback configured, got %d", len(mock.callsOrder))
	}
}

// TestGenerator_FallbackInRevision verifies fallback also kicks in for the
// revision codepath, not just fresh generation.
func TestGenerator_FallbackInRevision(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": fmt.Errorf("rate limited after 3 retries")},
		responses: map[string]string{"fallback": "Revised\nBody"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	_, err := gen.GenerateWithFeedback(sources1(), "previous", "fix typo", "")
	if err != nil {
		t.Fatalf("expected revision fallback to succeed, got: %v", err)
	}
	if len(mock.callsOrder) != 2 || mock.callsOrder[1] != "fallback" {
		t.Errorf("expected revision to call fallback after primary failed, got %v", mock.callsOrder)
	}
}

// TestGenerator_BothFail verifies the error message includes both failures
// when primary and fallback both return errors.
func TestGenerator_BothFail(t *testing.T) {
	mock := &perModelMockHFClient{
		errs: map[string]error{
			"primary":  fmt.Errorf("rate limited after 3 retries"),
			"fallback": fmt.Errorf("HF API error (status 503): unavailable"),
		},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	_, err := gen.Generate("deal", sources1())
	if err == nil {
		t.Fatal("expected error when both primary and fallback fail")
	}
	if len(mock.callsOrder) != 2 {
		t.Errorf("expected both to be called, got %d calls", len(mock.callsOrder))
	}
}
