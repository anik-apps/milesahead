package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

	result, err := client.Generate("test-model", "prompt", 500)
	if err != nil {
		t.Fatalf("expected retry success, got: %v", err)
	}
	if result != "Success after retry." {
		t.Errorf("unexpected: %s", result)
	}
}
