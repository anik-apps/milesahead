package ai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTryModels_AllFailedNamesEveryModel(t *testing.T) {
	mock := &perModelMockHFClient{errs: map[string]error{
		"first":  &APIError{StatusCode: 404, Model: "first", Body: "Not Found"},
		"second": &APIError{StatusCode: 410, Model: "second", Body: "end of life"},
	}}
	_, err := TryModels(mock, []string{"first", "second"}, "p", 64)
	if err == nil {
		t.Fatal("want an error when every model fails")
	}
	for _, want := range []string{"first (retired)", "second (retired)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Model != "first" {
		t.Errorf("the first model's error is no longer wrapped: %v", err)
	}
}

func TestCritic_ChainFailureIsErrCriticUnavailable(t *testing.T) {
	mock := &perModelMockHFClient{errs: map[string]error{
		"a": &APIError{StatusCode: 404, Model: "a"},
		"b": &APIError{StatusCode: 410, Model: "b"},
	}}
	_, err := NewCritic(mock, "a,b").Verify("article", nil)
	if !errors.Is(err, ErrCriticUnavailable) {
		t.Fatalf("err = %v, want ErrCriticUnavailable", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Errorf("the APIError is no longer reachable through the critic error: %v", err)
	}
}

func TestCritic_UnparseableReplyIsNotErrCriticUnavailable(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{"a": "I think it is fine."}}
	_, err := NewCritic(mock, "a").Verify("article", nil)
	if err == nil || errors.Is(err, ErrCriticUnavailable) {
		t.Fatalf("err = %v, want a parse error that is not ErrCriticUnavailable", err)
	}
}

func TestCritic_UnparseableReplyIsRecordedAsBadReply(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(makeChatResponse("Looks accurate to me."))
	}))
	defer ts.Close()
	log := &callLog{}
	c := NewHFClient("t", 0)
	c.BaseURL = ts.URL

	if _, err := NewCritic(c.WithStep("critic", log), "a").Verify("article", nil); err == nil {
		t.Fatal("want a parse error")
	}
	calls := log.all()
	if len(calls) != 2 || calls[0].Outcome != OutcomeOK || calls[1].Outcome != OutcomeBadReply {
		t.Errorf("recorded %+v, want ok then bad_reply", calls)
	}
}

// With WithConfirmedOK, each critic call records exactly one outcome: ok only
// when the reply parsed, bad_reply when it did not. No ok precedes a bad_reply.
func TestCritic_ConfirmedClientRecordsOneOutcomePerCall(t *testing.T) {
	replies := []string{"Looks accurate to me.", passingCriticJSON}
	n := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(makeChatResponse(replies[n]))
		n++
	}))
	defer ts.Close()
	log := &callLog{}
	c := NewHFClient("t", 0)
	c.BaseURL = ts.URL
	critic := NewCritic(c.WithStep("critic", log).WithConfirmedOK(), "a")

	if _, err := critic.Verify("article", nil); err == nil {
		t.Fatal("want a parse error for the prose reply")
	}
	if _, err := critic.Verify("article", nil); err != nil {
		t.Fatalf("valid JSON: %v", err)
	}
	calls := log.all()
	if len(calls) != 2 || calls[0].Outcome != OutcomeBadReply || calls[1].Outcome != OutcomeOK {
		t.Errorf("recorded %+v, want bad_reply then ok", calls)
	}
}

func TestPipeline_DetailClientCarriesTheKeyDetailsCall(t *testing.T) {
	main := &perModelMockHFClient{responses: map[string]string{"critic": passingCriticJSON}}
	details := &perModelMockHFClient{responses: map[string]string{"details": `{"key_details":[],"links":[]}`}}
	p := NewPipeline(NewGenerator(main, "gen", "", nil), NewCritic(main, "critic"), 80)
	p.SetDetailModels([]string{"details"})
	p.SetDetailClient(details)

	p.extractDetails("Title", "Content")

	if len(details.callsOrder) != 1 || details.callsOrder[0] != "details" {
		t.Errorf("detail client calls = %v, want [details]", details.callsOrder)
	}
	for _, m := range main.callsOrder {
		if m == "details" {
			t.Error("the key-details call still went through the critic's client")
		}
	}
}

// A 400 is about this article (prompt too large, content filter), not the
// critic list, so it must not trip the scan breaker.
func TestCritic_ArticleRefusalIsNotErrCriticUnavailable(t *testing.T) {
	mock := &perModelMockHFClient{errs: map[string]error{
		"a": &APIError{StatusCode: 400, Model: "a", Body: "prompt too long"},
	}}
	_, err := NewCritic(mock, "a,b").Verify("article", nil)
	if err == nil || errors.Is(err, ErrCriticUnavailable) {
		t.Fatalf("err = %v, want a plain error that is not ErrCriticUnavailable", err)
	}
}

// A retryable failure on every critic (here a gateway's status-less error) means
// no critic answered, even though its outcome is "error".
func TestCritic_RetryableErrorOnEveryModelIsErrCriticUnavailable(t *testing.T) {
	busy := func(m string) error { return &APIError{Model: m, Body: "upstream busy", Kind: OutcomeError} }
	mock := &perModelMockHFClient{errs: map[string]error{"a": busy("a"), "b": busy("b")}}
	_, err := NewCritic(mock, "a,b").Verify("article", nil)
	if !errors.Is(err, ErrCriticUnavailable) {
		t.Fatalf("err = %v, want ErrCriticUnavailable", err)
	}
}
