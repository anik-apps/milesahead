package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
)

// TestCriticWiring: critic_disable_thinking reaches the critic's requests and
// only those, and every critic in the chain (plus critic2) is probed.
func TestCriticWiring(t *testing.T) {
	var kwargs []bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, present := body["chat_template_kwargs"]
		kwargs = append(kwargs, present)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":90,\"issues\":[],\"factual_check\":true,\"attribution_check\":true,\"completeness_check\":true,\"hallucination_check\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	for _, on := range []bool{false, true} {
		kwargs = nil
		cfg := &config.Config{AI: config.AIConfig{
			CriticModel:           "critic-a, critic-b",
			Critic2Model:          "critic-2",
			CriticDisableThinking: on,
		}}
		hf := ai.NewHFClient("token", 0)
		hf.BaseURL = ts.URL
		criticClient := newCriticClient(cfg, hf)

		v, err := ai.NewCritic(criticClient, cfg.AI.CriticModel).Verify("article",
			[]ai.SourceArticle{{Name: "TPG", Content: "x"}})
		if err != nil {
			t.Fatalf("on=%v: Verify: %v", on, err)
		}
		if v.Model != "critic-a" {
			t.Errorf("on=%v: scored by %q, want critic-a", on, v.Model)
		}
		if _, err := hf.Generate("critic-a", "enrichment prompt", ai.KeyDetailTokens); err != nil {
			t.Fatalf("on=%v: base Generate: %v", on, err)
		}
		if want := []bool{on, false}; !reflect.DeepEqual(kwargs, want) {
			t.Errorf("on=%v: chat_template_kwargs present per request = %v, want %v", on, kwargs, want)
		}
	}

	cfg := &config.Config{AI: config.AIConfig{CriticModel: "a, b", Critic2Model: ""}}
	if got, want := criticModels(cfg), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("criticModels = %v, want %v", got, want)
	}
	cfg.AI.Critic2Model = "c"
	if got, want := criticModels(cfg), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("criticModels = %v, want %v", got, want)
	}
}

// TestCriticWiring_ChainRetriesEachCriticOnce: with a fallback critic, a critic
// that keeps failing is retried once, not max_retries times, before the next
// one is tried. Each gateway timeout costs ~5 minutes in production.
func TestCriticWiring_ChainRetriesEachCriticOnce(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls[body.Model]++
		mu.Unlock()
		if body.Model == "critic-a" {
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\":90,\"issues\":[],\"factual_check\":true,\"attribution_check\":true,\"completeness_check\":true,\"hallucination_check\":true}"},"finish_reason":"stop"}]}`))
	}))
	defer ts.Close()

	cfg := &config.Config{AI: config.AIConfig{CriticModel: "critic-a,critic-b", MaxRetries: 3}}
	hf := ai.NewHFClient("token", cfg.AI.MaxRetries)
	hf.BaseURL = ts.URL
	v, err := ai.NewCritic(newCriticClient(cfg, hf), cfg.AI.CriticModel).Verify("article",
		[]ai.SourceArticle{{Name: "TPG", Content: "x"}})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Model != "critic-b" {
		t.Errorf("scored by %q, want the fallback critic-b", v.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := calls["critic-a"]; got != 1+criticChainRetries {
		t.Errorf("critic-a called %d times; want %d (one try plus %d retry)", got, 1+criticChainRetries, criticChainRetries)
	}
}
