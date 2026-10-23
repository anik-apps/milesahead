package ai

import (
	"errors"
	"fmt"
	"strings"
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

const passingCriticJSON = `{"score": 95, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`

// TestCritic_ChainFallsOver: on 2026-09-30 the lone critic answered every call
// with a gateway 504 and every article in the window was dropped. The critic
// is a chain now, and the result names the model that actually scored.
func TestCritic_ChainFallsOver(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"503 overloaded", &APIError{StatusCode: 503, Model: "first", Body: "overloaded", Retries: 3}},
		{"504 gateway timeout", &APIError{StatusCode: 504, Model: "first", Body: "gateway timeout", Retries: 3}},
		{"410 retired", &APIError{StatusCode: 410, Model: "first", Body: "end of life"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &perModelMockHFClient{
				errs:      map[string]error{"first": tc.err},
				responses: map[string]string{"second": passingCriticJSON},
			}
			critic := NewCritic(mock, "first, second")

			for name, verify := range map[string]func(string, []SourceArticle) (*VerificationResult, error){
				"Verify": critic.Verify, "Verify2": critic.Verify2,
			} {
				mock.resetCalls()
				v, err := verify("article", sources1())
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if v.Score != 95 || v.Model != "second" {
					t.Errorf("%s: score %d from %q; want 95 from second", name, v.Score, v.Model)
				}
				if got := strings.Join(mock.calls(), ","); got != "first,second" {
					t.Errorf("%s: calls = %s; want first,second", name, got)
				}
			}
		})
	}
}

func TestCritic_ChainAllFail(t *testing.T) {
	mock := &perModelMockHFClient{errs: map[string]error{
		"first":  &APIError{StatusCode: 504, Model: "first", Body: "gateway timeout", Retries: 3},
		"second": &APIError{StatusCode: 503, Model: "second", Body: "overloaded", Retries: 3},
	}}
	_, err := NewCritic(mock, "first,second").Verify("article", sources1())
	if err == nil {
		t.Fatal("expected an error when every critic fails")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 504 {
		t.Errorf("err = %v; want it to wrap the first critic's 504", err)
	}
	if got := strings.Join(mock.calls(), ","); got != "first,second" {
		t.Errorf("calls = %s; want first,second", got)
	}
}

// A request the provider rejects outright (400) will be rejected by every
// model behind the same gateway, so the chain does not burn the next critic.
func TestCritic_ChainStopsOnBadRequest(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"first": &APIError{StatusCode: 400, Model: "first", Body: "bad request"}},
		responses: map[string]string{"second": passingCriticJSON},
	}
	if _, err := NewCritic(mock, "first,second").Verify("article", sources1()); err == nil {
		t.Fatal("expected the 400 to be returned")
	}
	if got := strings.Join(mock.calls(), ","); got != "first" {
		t.Errorf("calls = %s; want first only", got)
	}
}

// A single model is a one-element chain, which is what cmd/ab-models and the
// tests have always passed.
func TestCritic_SingleModel(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{"only": passingCriticJSON}}
	critic := NewCritic(mock, "only")
	if got := critic.Models(); len(got) != 1 || got[0] != "only" {
		t.Errorf("Models() = %v", got)
	}
	v, err := critic.Verify("article", sources1())
	if err != nil || v.Model != "only" {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
}

// The critic's reply is untrusted text; a "model" key in it must not
// overwrite which model actually scored.
func TestCritic_ModelIsNotParsedFromReply(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{
		"real": `{"model": "spoofed", "score": 80, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}}
	v, err := NewCritic(mock, "real").Verify("article", sources1())
	if err != nil {
		t.Fatal(err)
	}
	if v.Model != "real" {
		t.Errorf("Model = %q; want real", v.Model)
	}
}
