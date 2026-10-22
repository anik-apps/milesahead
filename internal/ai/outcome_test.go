package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// callLog is a Recorder that keeps every call.
type callLog struct {
	mu    sync.Mutex
	calls []ModelCall
}

func (l *callLog) RecordModelCall(c ModelCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, c)
}

func (l *callLog) all() []ModelCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ModelCall(nil), l.calls...)
}

func TestGenerate_RecordsEachOutcome(t *testing.T) {
	cases := []struct {
		name   string
		handle func(w http.ResponseWriter)
		want   Outcome
		status int
	}{
		{"ok", func(w http.ResponseWriter) { json.NewEncoder(w).Encode(makeChatResponse("fine")) }, OutcomeOK, 200},
		{"404", func(w http.ResponseWriter) { w.WriteHeader(404); w.Write([]byte(`{"detail":"Not Found"}`)) }, OutcomeRetired, 404},
		{"410", func(w http.ResponseWriter) { w.WriteHeader(410); w.Write([]byte(`{"detail":"end of life"}`)) }, OutcomeRetired, 410},
		{"retired in a 200", func(w http.ResponseWriter) {
			w.Write([]byte(`{"error":{"message":"The model 'm' has reached its end of life"}}`))
		}, OutcomeRetired, 200},
		{"401", func(w http.ResponseWriter) { w.WriteHeader(401) }, OutcomeAuth, 401},
		{"403", func(w http.ResponseWriter) { w.WriteHeader(403) }, OutcomeAuth, 403},
		{"400", func(w http.ResponseWriter) { w.WriteHeader(400) }, OutcomeError, 400},
		{"429", func(w http.ResponseWriter) { w.WriteHeader(429) }, OutcomeOverloaded, 429},
		{"503", func(w http.ResponseWriter) { w.WriteHeader(503) }, OutcomeOverloaded, 503},
		{"504", func(w http.ResponseWriter) { w.WriteHeader(504) }, OutcomeTimeout, 504},
		{"empty", func(w http.ResponseWriter) { json.NewEncoder(w).Encode(makeChatResponse("")) }, OutcomeBadReply, 200},
		{"truncated", func(w http.ResponseWriter) {
			json.NewEncoder(w).Encode(makeChatResponseWithFinish("half a sent", "length"))
		}, OutcomeBadReply, 200},
		{"not json", func(w http.ResponseWriter) { w.Write([]byte("<html>gateway</html>")) }, OutcomeBadReply, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { tc.handle(w) }))
			defer ts.Close()
			log := &callLog{}
			c := NewHFClient("token", 0)
			c.BaseURL = ts.URL
			c.SetBackoff(time.Millisecond)

			_, err := c.WithStep("critic", log).Generate("m", "p", 4096)

			if got := OutcomeOf(err); got != tc.want {
				t.Errorf("OutcomeOf(err) = %q, want %q (err %v)", got, tc.want, err)
			}
			calls := log.all()
			if len(calls) != 1 {
				t.Fatalf("recorded %d calls, want 1", len(calls))
			}
			got := calls[0]
			if got.Step != "critic" || got.Model != "m" || got.Source != SourceCall {
				t.Errorf("recorded %+v, want step critic, model m, source call", got)
			}
			if got.Outcome != tc.want || got.HTTPStatus != tc.status {
				t.Errorf("recorded outcome %q status %d, want %q %d", got.Outcome, got.HTTPStatus, tc.want, tc.status)
			}
			if (tc.want == OutcomeOK) != (got.Detail == "") {
				t.Errorf("detail %q for outcome %q", got.Detail, got.Outcome)
			}
		})
	}
}

func TestGenerate_RecordsATimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer ts.Close()
	log := &callLog{}
	c := NewHFClient("token", 0)
	c.BaseURL = ts.URL
	c.SetRequestTimeout(50 * time.Millisecond)

	_, err := c.WithStep("generator", log).Generate("m", "p", 4096)

	if got := OutcomeOf(err); got != OutcomeTimeout {
		t.Errorf("OutcomeOf(err) = %q, want timeout (err %v)", got, err)
	}
	if calls := log.all(); len(calls) != 1 || calls[0].Outcome != OutcomeTimeout {
		t.Errorf("recorded %+v, want one timeout", calls)
	}
}

func TestWithStep_OneRecordPerCallAfterRetries(t *testing.T) {
	n := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(makeChatResponse("fine"))
	}))
	defer ts.Close()
	log := &callLog{}
	c := NewHFClient("token", 2)
	c.BaseURL = ts.URL
	c.SetBackoff(time.Millisecond)

	if _, err := c.WithStep("enrichment", log).Generate("m", "p", 512); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if calls := log.all(); len(calls) != 1 || calls[0].Outcome != OutcomeOK {
		t.Errorf("recorded %+v, want exactly one ok after the retry", calls)
	}
}

func TestWithStep_LabelSurvivesCopiesAndLeavesTheBaseUnlabelled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(makeChatResponse("fine"))
	}))
	defer ts.Close()
	log := &callLog{}
	base := NewHFClient("token", 0)
	base.BaseURL = ts.URL
	labelled := base.WithStep("critic", log).WithMaxRetries(1).WithRequestTimeout(time.Minute).WithThinkingDisabled()

	base.Generate("m", "p", 64)
	labelled.Generate("m", "p", 64)

	calls := log.all()
	if len(calls) != 1 || calls[0].Step != "critic" {
		t.Errorf("recorded %+v, want one call from the labelled copy only", calls)
	}
}

func TestReportBadReply_RecordsOnlyOnALabelledClient(t *testing.T) {
	log := &callLog{}
	NewHFClient("t", 0).ReportBadReply("m", "ignored")
	NewHFClient("t", 0).WithStep("critic", log).ReportBadReply("m", "no JSON object in critic reply")

	calls := log.all()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	if c := calls[0]; c.Outcome != OutcomeBadReply || c.Step != "critic" || c.Detail != "no JSON object in critic reply" {
		t.Errorf("recorded %+v", c)
	}
}
