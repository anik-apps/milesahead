package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeChatResponse(content string) chatResponse {
	return chatResponse{
		Choices: []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}{
			{Message: struct {
				Content string `json:"content"`
			}{Content: content}},
		},
	}
}

func TestHFClient_Generate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth header")
		}
		// Verify it posts to /chat/completions
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := makeChatResponse("This is a generated article.")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL

	result, err := client.Generate("test-model", "Write an article", 500)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result != "This is a generated article." {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestHFClient_RateLimit(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		resp := makeChatResponse("Success after retry.")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL
	client.SetBackoff(time.Millisecond) // keep the suite fast; see TestHFClient_BackoffOutlastsAProviderBlip

	result, err := client.Generate("test-model", "prompt", 500)
	if err != nil {
		t.Fatalf("expected retry success, got: %v", err)
	}
	if result != "Success after retry." {
		t.Errorf("unexpected: %s", result)
	}
}

// TestHFClient_BackoffOutlastsAProviderBlip pins the production retry budget.
//
// The original schedule was 100ms/200ms/400ms: 700ms of total patience. On
// 2026-09-21 a provider-side degradation lasting tens of seconds blew through
// all three attempts for four separate enrichment calls, and the article was
// published missing its key details, Scout's Take and image. The identical
// prompts succeeded on the same model minutes later, so the model was fine --
// the client simply gave up almost immediately.
func TestHFClient_BackoffOutlastsAProviderBlip(t *testing.T) {
	const wantMinimumPatience = 10 * time.Second

	c := NewHFClient("token", 3)
	total, d := time.Duration(0), c.backoff
	for i := 0; i < 3; i++ {
		total += d
		d *= 2
	}
	if total < wantMinimumPatience {
		t.Errorf("3 retries wait %v in total; a provider blip needs at least %v "+
			"of patience to ride out", total, wantMinimumPatience)
	}
}

// TestHFClient_RetriesTransportErrors is the regression test for the defect that
// silently degraded articles for two and a half months.
//
// From 2026-07-01 the provider's latency crossed the client timeout on roughly a
// quarter of enrichment calls. Every one failed as "context deadline exceeded"
// and was NEVER retried: the loop only ever retried HTTP status codes, so a
// transport error returned on the first attempt. The article was then published
// missing its Scout's Take, key details or image, at WARN level, with nothing
// recording which articles were affected.
func TestHFClient_RetriesTransportErrors(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Hijack and drop the connection: a transport-level failure, which
			// surfaces to the client as an error from Do rather than a status.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(makeChatResponse("Recovered after transport failures."))
	}))
	defer ts.Close()

	client := NewHFClient("token", 3)
	client.BaseURL = ts.URL
	client.SetBackoff(time.Millisecond)

	result, err := client.Generate("test-model", "prompt", 256)
	if err != nil {
		t.Fatalf("transport errors must be retried, got: %v", err)
	}
	if result != "Recovered after transport failures." {
		t.Errorf("unexpected result: %q", result)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 dropped + 1 success), got %d", attempts)
	}
}

// TestHFClient_SmallCallsGetShorterDeadline verifies enrichment calls do not
// inherit the generator's budget.
//
// The generator legitimately runs for minutes, so its timeout is long. A 256
// token Scout's Take sharing that budget means a hung call blocks for the full
// generator timeout before failing -- and the scheduler runs articles back to
// back, so it stalls everything queued behind it.
func TestHFClient_SmallCallsGetShorterDeadline(t *testing.T) {
	c := NewHFClient("token", 3)
	c.SetRequestTimeout(15 * time.Minute) // a generator-sized budget

	if got := c.timeoutFor(4096); got != 15*time.Minute {
		t.Errorf("generation call got %v, want the configured 15m", got)
	}
	if got := c.timeoutFor(256); got != SmallCallTimeout {
		t.Errorf("enrichment call got %v, want the small-call cap %v", got, SmallCallTimeout)
	}
	// A configured timeout shorter than the cap must still win -- the cap is a
	// ceiling for small calls, not a floor.
	c.SetRequestTimeout(30 * time.Second)
	if got := c.timeoutFor(256); got != 30*time.Second {
		t.Errorf("got %v, want the configured 30s to take precedence", got)
	}
}
