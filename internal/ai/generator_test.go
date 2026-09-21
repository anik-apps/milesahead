package ai

import (
	"fmt"
	"testing"
	"time"
)

// mockHFClient records calls and returns a canned response.
type mockHFClient struct {
	callCount  int
	lastModel  string
	lastPrompt string
	response   string
	err        error
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
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 429, Body: "too many requests", Retries: 3})},
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
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 502, Body: "bad gateway", Retries: 3})},
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

// TestGenerator_NoFallbackOn4xx verifies that a malformed-request error does
// NOT trigger fallback: every model in the chain would reject it identically,
// so retrying just multiplies failed calls.
//
// This deliberately no longer uses 404. It used to, on the reasoning that a
// missing model is a config error a fallback cannot fix. That reasoning did not
// survive contact with the provider: it answers 404 for most models its own
// catalogue advertises, and 410 for models it retires underneath a running
// deployment. Both are cases where another model is the only fix, so they now
// walk the chain (see TestGenerator_FallbackOnRetiredModel). Errors that really
// are chain-wide -- 400 here, 401 in TestGenerator_NoFallbackOnAuthError --
// still short-circuit.
func TestGenerator_NoFallbackOn4xx(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 400, Body: "malformed request"})},
		responses: map[string]string{"fallback": "Should not be used"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	_, err := gen.Generate("deal", sources1())
	if err == nil {
		t.Fatal("expected error from primary to propagate, got nil")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("expected only primary to be called for a malformed request, got %v", mock.callsOrder)
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
		errs: map[string]error{"primary": error(&APIError{StatusCode: 429, Body: "too many requests", Retries: 3})},
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
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 429, Body: "too many requests", Retries: 3})},
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
			"primary":  error(&APIError{StatusCode: 429, Body: "too many requests", Retries: 3}),
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

// TestGenerator_FallbackOnRetiredModel is the regression test for the outage
// that motivated the fallback chain: the provider retired the primary model and
// answered 410 Gone. The old classifier treated any non-retryable 4xx as
// unfixable and skipped the fallback entirely, so the site published nothing
// for 26 days while a working fallback sat in the config.
func TestGenerator_FallbackOnRetiredModel(t *testing.T) {
	gone := error(&APIError{StatusCode: 410, Model: "openai/gpt-oss-120b",
		Body: `{"detail":"The model 'openai/gpt-oss-120b' has reached its end of life"}`})
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": gone},
		responses: map[string]string{"fallback": "Recovered\nContent"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	result, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("410 should fall back to another model, got: %v", err)
	}
	if result != "Recovered\nContent" {
		t.Errorf("expected fallback response, got %q", result)
	}
	if len(mock.callsOrder) != 2 || mock.callsOrder[1] != "fallback" {
		t.Errorf("expected calls [primary, fallback], got %v", mock.callsOrder)
	}
}

// TestGenerator_FallbackOnMissingModel covers 404, which is what this provider
// returns for most of the models its own catalogue advertises.
func TestGenerator_FallbackOnMissingModel(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 404, Body: "Not Found"})},
		responses: map[string]string{"fallback": "Recovered\nContent"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	if _, err := gen.Generate("deal", sources1()); err != nil {
		t.Fatalf("404 should fall back, got: %v", err)
	}
	if len(mock.callsOrder) != 2 {
		t.Errorf("expected 2 calls, got %v", mock.callsOrder)
	}
}

// TestGenerator_WalksWholeFallbackChain verifies the chain is tried in order
// rather than stopping at the first fallback. Both the primary and the single
// configured fallback were dead simultaneously in the real incident.
func TestGenerator_WalksWholeFallbackChain(t *testing.T) {
	gone := error(&APIError{StatusCode: 410, Body: "gone"})
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": gone, "fb1": gone, "fb2": gone},
		responses: map[string]string{"fb3": "Third\nFallback"},
	}
	gen := NewGenerator(mock, "primary", "fb1, fb2, fb3", nil)

	result, err := gen.Generate("deal", sources1())
	if err != nil {
		t.Fatalf("expected the chain to reach fb3, got: %v", err)
	}
	if result != "Third\nFallback" {
		t.Errorf("expected fb3 response, got %q", result)
	}
	want := []string{"primary", "fb1", "fb2", "fb3"}
	if len(mock.callsOrder) != 4 {
		t.Fatalf("expected calls %v, got %v", want, mock.callsOrder)
	}
	for i, m := range want {
		if mock.callsOrder[i] != m {
			t.Errorf("call %d: want %q, got %q", i, m, mock.callsOrder[i])
		}
	}
}

// TestGenerator_NoFallbackOnAuthError verifies we still do NOT burn the chain on
// errors every model would fail identically. A bad token is a config bug; trying
// four more models just multiplies the failed calls.
func TestGenerator_NoFallbackOnAuthError(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"primary": error(&APIError{StatusCode: 401, Body: "Invalid username or password"})},
		responses: map[string]string{"fallback": "should not be used"},
	}
	gen := NewGenerator(mock, "primary", "fallback", nil)

	if _, err := gen.Generate("deal", sources1()); err == nil {
		t.Fatal("expected auth error to propagate")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("auth error must not try fallbacks, got calls %v", mock.callsOrder)
	}
}

// TestPreflight_FlagsRetiredModel verifies boot-time detection of the exact
// failure that went unnoticed for 26 days.
func TestPreflight_FlagsRetiredModel(t *testing.T) {
	mock := &perModelMockHFClient{
		errs: map[string]error{
			"dead-primary": fmt.Errorf("HF API error (status 410): end of life"),
			"dead-fb":      error(&APIError{StatusCode: 404, Body: "Not Found"}),
		},
		responses: map[string]string{"live-critic": "OK"},
	}
	results := Preflight(mock, "dead-primary", []string{"dead-fb"}, "live-critic")

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if GenerationViable(results) {
		t.Error("no generator or fallback answered; generation must not be reported viable")
	}
	for _, r := range results {
		wantOK := r.Model == "live-critic"
		if r.OK != wantOK {
			t.Errorf("%s: OK=%v, want %v", r.Model, r.OK, wantOK)
		}
	}
}

// TestPreflight_ViableWhenOnlyFallbackLives covers the degraded-but-working
// case: the primary is gone but the chain can still publish.
func TestPreflight_ViableWhenOnlyFallbackLives(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"dead-primary": &APIError{StatusCode: 410, Body: "gone"}},
		responses: map[string]string{"live-fb": "OK", "live-critic": "OK"},
	}
	results := Preflight(mock, "dead-primary", []string{"live-fb"}, "live-critic")
	if !GenerationViable(results) {
		t.Error("a live fallback means generation is still viable")
	}
}

// TestClassifyAPIError covers every failure shape HFClient.Generate can return,
// by status code rather than by prose. Two of these were silently unclassified
// when classification was done by substring-matching the formatted message, and
// both would have reproduced the 26-day outage:
//   - a retirement announced as HTTP 200 with an error body (StatusCode 0)
//   - a model returning empty content, which is not an API error at all
func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		name          string
		err           *APIError
		wantGone      bool
		wantTransient bool
	}{
		{"retired model", &APIError{StatusCode: 410, Body: "end of life"}, true, false},
		{"missing model", &APIError{StatusCode: 404, Body: "Not Found"}, true, false},
		{"throttled", &APIError{StatusCode: 429, Body: "slow down", Retries: 3}, false, true},
		{"upstream 500", &APIError{StatusCode: 500, Body: "internal", Retries: 3}, false, true},
		{"upstream 502", &APIError{StatusCode: 502, Body: "bad gateway", Retries: 3}, false, true},
		{"not implemented is permanent", &APIError{StatusCode: 501, Body: "nope"}, false, false},
		{"auth failure", &APIError{StatusCode: 401, Body: "bad token"}, false, false},
		{"malformed request", &APIError{StatusCode: 400, Body: "bad request"}, false, false},

		// HTTP 200 with an error object: no status to switch on.
		{"200-with-error naming retirement",
			&APIError{Body: "The model has reached its end of life"}, true, false},
		{"200-with-error, cause unknown",
			&APIError{Body: "internal routing failure"}, false, true},
		{"empty content from a reasoning model",
			&APIError{Body: "model returned empty content (token budget may have gone to reasoning)"},
			false, true},

		// The response body is untrusted free text and must never drive
		// classification: this 400 previously read as a retired model and burned
		// the whole fallback chain.
		{"400 whose body mentions status 404",
			&APIError{StatusCode: 400, Body: `{"detail":"upstream returns status 404"}`}, false, false},
		{"403 whose body mentions status 500",
			&APIError{StatusCode: 403, Body: "quota exceeded, see docs status 500"}, false, false},
	}
	for _, tc := range cases {
		if got := isModelUnavailableError(tc.err); got != tc.wantGone {
			t.Errorf("%s: ModelGone = %v, want %v", tc.name, got, tc.wantGone)
		}
		if got := isRetryableModelError(tc.err); got != tc.wantTransient {
			t.Errorf("%s: Transient = %v, want %v", tc.name, got, tc.wantTransient)
		}
		wantTry := tc.wantGone || tc.wantTransient
		if got := shouldTryAnotherModel(tc.err); got != wantTry {
			t.Errorf("%s: shouldTryAnotherModel = %v, want %v", tc.name, got, wantTry)
		}
	}
}

// TestClassifyNonAPIError covers failures raised outside HFClient, where there
// is no status code to read.
func TestClassifyNonAPIError(t *testing.T) {
	if !isRetryableModelError(fmt.Errorf("http request: dial tcp: i/o timeout")) {
		t.Error("a transport failure should be retried against another model")
	}
	if isRetryableModelError(fmt.Errorf("some unrelated failure")) {
		t.Error("an unrecognised error must not burn the fallback chain")
	}
	// Wrapped APIErrors must still classify, since Generate wraps them.
	wrapped := fmt.Errorf("generator: %w", &APIError{StatusCode: 410, Body: "gone"})
	if !isModelUnavailableError(wrapped) {
		t.Error("errors.As must see through wrapping")
	}
}

// TestShouldTryAnotherModel_RetiredAndMissing pins the two statuses that caused
// the 26-day outage, using the provider's real response wording.
func TestShouldTryAnotherModel_RetiredAndMissing(t *testing.T) {
	retired := error(&APIError{StatusCode: 410, Model: "openai/gpt-oss-120b",
		Body: `{"detail":"The model 'openai/gpt-oss-120b' has reached its end of life"}`})
	missing := error(&APIError{StatusCode: 404, Body: `{"title":"Not Found"}`})
	if !shouldTryAnotherModel(retired) {
		t.Error("a retired model (410) must fall through to the next model")
	}
	if !shouldTryAnotherModel(missing) {
		t.Error("a missing model (404) must fall through to the next model")
	}
	if shouldTryAnotherModel(&APIError{StatusCode: 401, Body: "bad token"}) {
		t.Error("auth errors must not burn the fallback chain")
	}
}
