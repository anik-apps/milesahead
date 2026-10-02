package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/mail"
)

// modelLog is a model client that records which models were called. Models
// in gone answer 410, as the retired critic did; everything else answers
// with something each side job can parse.
type modelLog struct {
	mu    sync.Mutex
	calls []string
	gone  map[string]bool
}

func (m *modelLog) Generate(model, prompt string, _ int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, model)
	if m.gone[model] {
		return "", &ai.APIError{StatusCode: 410, Model: model, Body: "end of life"}
	}
	switch {
	case strings.Contains(prompt, `"updated_content"`):
		return `{"updated_content": "<p>100,000 points</p>", "summary": "Bonus raised"}`, nil
	case strings.Contains(prompt, `"scores"`):
		return `{"scores":[{"index":0,"score":8,"reason":"r"}]}`, nil
	default:
		return "[1]", nil
	}
}

func (m *modelLog) take() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := strings.Join(m.calls, ",")
	m.calls = nil
	return s
}

type nopSender struct{}

func (nopSender) SendNewsletter(string, string, []mail.Recipient) (int, error) { return 0, nil }

// TestSideJobs_UseTheEnrichmentChainNotTheCritic is the production wiring:
// image text-scoring, the newsletter pick and card-update detection all ran on
// the critic model, and all failed when it was retired on 2026-09-28. The
// replacement critic takes minutes per reply, and these are 30s short calls,
// so they must run on the enrichment chain -- and fall over its retired first
// entry to the next.
func TestSideJobs_UseTheEnrichmentChainNotTheCritic(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{AI: config.AIConfig{
		CriticModel:     "critic",
		EnrichmentModel: "retired, live",
	}}
	client := &modelLog{gone: map[string]bool{"retired": true}}
	side := newAISideJobs(cfg, database, client, nopSender{}, "vision", enrichmentModels(cfg))

	// Image text-scoring.
	if _, err := side.imageScorer.Score("Lounge", "A lounge", []images.ImageCandidate{{URL: "a"}}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got := client.take(); got != "retired,live" {
		t.Errorf("image scoring called %s; want retired,live", got)
	}

	// Card-update detection, then the newsletter pick over the same article.
	sections, _ := json.Marshal([]db.ReferenceSection{{Key: "signup_bonus", Title: "Bonus", Content: "60k"}})
	if _, err := database.CreateReferencePage(&db.ReferencePage{
		Slug: "sapphire-preferred", Title: "Sapphire Preferred", Category: "credit-cards",
		Issuer: "Chase", SectionsJSON: string(sections), RecentChangesJSON: "[]",
		PendingUpdatesJSON: "[]", KeywordsJSON: "[]", Status: "published",
	}); err != nil {
		t.Fatalf("create page: %v", err)
	}
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Sapphire Preferred bonus rises", Slug: "sp", Content: "Now 100,000 points.",
		Type: "deal", Status: db.StatusDraft, SourcesJSON: "[]",
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	if err := side.cardDetector.DetectAndDraft(context.Background(), id); err != nil {
		t.Fatalf("DetectAndDraft: %v", err)
	}
	if got := client.take(); got != "retired,live" {
		t.Errorf("card detection called %s; want retired,live", got)
	}

	for _, s := range []string{db.StatusPending, db.StatusApproved} {
		if err := database.UpdateArticleStatus(id, s); err != nil {
			t.Fatalf("status %s: %v", s, err)
		}
	}
	if _, err := side.newsletter.BuildWeekly(); err != nil {
		t.Fatalf("BuildWeekly: %v", err)
	}
	if got := client.take(); got != "retired,live" {
		t.Errorf("newsletter pick called %s; want retired,live", got)
	}
}

// With enrichment_model unset, the short-call chain falls back to the fast
// generator fallbacks, with the slow critic last rather than first.
func TestEnrichmentModels_CriticIsLastResort(t *testing.T) {
	cfg := &config.Config{AI: config.AIConfig{
		CriticModel:            "critic",
		FallbackGeneratorModel: "fast-a,fast-b",
	}}
	if got, want := enrichmentModels(cfg), []string{"fast-a", "fast-b", "critic"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unset: %v, want %v", got, want)
	}
	cfg.AI.EnrichmentModel = "e1,e2"
	if got, want := enrichmentModels(cfg), []string{"e1", "e2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("set: %v, want %v", got, want)
	}
}
