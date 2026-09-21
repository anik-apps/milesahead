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
