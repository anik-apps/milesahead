package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/health"
	"github.com/kumaranik/milesahead/internal/mail"
)

type capturedAlerts struct {
	mu   sync.Mutex
	sent []mail.ModelAlert
}

func (c *capturedAlerts) SendModelAlert(_ string, a mail.ModelAlert) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, a)
	return nil
}

// The 2026-10-20 outage, replayed against a fake provider: the generator
// answers, both critics are gone. One urgent email, the critic list down, and
// the scan stops after one article instead of writing every cluster.
func TestOutageReplay_DeadCriticsAlertOnceAndStopTheScan(t *testing.T) {
	var mu sync.Mutex
	generatorCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Model {
		case "super":
			w.WriteHeader(404)
			w.Write([]byte(`{"status":404,"title":"Not Found"}`))
		case "kimi":
			w.WriteHeader(410)
			w.Write([]byte(`{"detail":"The model 'kimi' has reached its end of life"}`))
		default:
			mu.Lock()
			generatorCalls++
			mu.Unlock()
			resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{
				"content": "TITLE: A story\nTLDR: s\n\nBody [Source 1]"}, "finish_reason": "stop"}}}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer provider.Close()

	f := newScanFixture(t, config.StoryDedupOff)
	cfg := &config.Config{}
	cfg.AI.GeneratorModel, cfg.AI.CriticModel = "gen", "super,kimi"
	alerts := &capturedAlerts{}
	base := ai.NewHFClient("token", 0)
	base.BaseURL = provider.URL
	worker := health.New(health.Options{DB: f.db, Steps: health.Steps(cfg), AdminURL: "https://x/admin/models"})
	worker.SetAlerting(alerts, "ops@example.com", "")

	// As in production, the critic's client confirms its own successes.
	pipeline := ai.NewPipeline(ai.NewGenerator(base.WithStep(health.StepGenerator, worker), "gen", "", nil),
		ai.NewCritic(base.WithStep(health.StepCritic, worker).WithConfirmedOK(), "super,kimi"), 75)
	f.gen.pipeline = pipeline
	f.raw(0, "Chase Sapphire Reserve adds new airport lounge access benefit")
	f.raw(1, "Chase Sapphire Reserve adds new airport lounge access benefit for cardholders")
	f.raw(0, "United MileagePlus announces award chart devaluation for partners")
	f.raw(2, "United MileagePlus announces award chart devaluation for partner flights")

	f.gen.run()
	worker.Drain()

	mu.Lock()
	calls := generatorCalls
	mu.Unlock()
	if calls != 1 {
		t.Errorf("generator calls = %d, want 1: the scan should stop once every critic fails", calls)
	}
	alerts.mu.Lock()
	sent := append([]mail.ModelAlert(nil), alerts.sent...)
	alerts.mu.Unlock()
	if len(sent) != 1 || !sent[0].Urgent || !strings.Contains(sent[0].Subject, "Critic is down") {
		t.Fatalf("alerts %+v, want one urgent 'Critic is down'", sent)
	}
	snap := worker.Snapshot()
	if !snap.Stopped {
		t.Error("the Models tab does not show publishing stopped")
	}
}
