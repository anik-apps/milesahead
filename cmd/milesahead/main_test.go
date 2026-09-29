package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/scheduler"
)

type fakeAIClient struct{}

func (fakeAIClient) Generate(model, prompt string, maxTokens int) (string, error) {
	if model == "critic-model" {
		return `{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`, nil
	}
	return "Revised Title\nRevised content [Source]", nil
}

// TestScheduleRevisionReconciler_ResumesStuckRevisionAfterStartup is the deploy
// case end to end: an article left in revision_requested by a restart must be
// rewritten by the startup pass, without waiting a full interval (an hour here).
func TestScheduleRevisionReconciler_ResumesStuckRevisionAfterStartup(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	client := fakeAIClient{}
	pipeline := ai.NewPipeline(ai.NewGenerator(client, "gen-model", "", nil),
		ai.NewCritic(client, "critic-model"), 80)
	mgr := article.NewManager(database, pipeline)

	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Stuck", Slug: "stuck-abc123", Content: "Old content.", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	if err := mgr.RequestRevision(id, "Add the annual fee", "email"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	sched := scheduler.New()
	sched.SetStartupDelay(10 * time.Millisecond)
	scheduleRevisionReconciler(context.Background(), sched, mgr, time.Hour)
	sched.Start()
	defer sched.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for {
		a, err := database.GetGeneratedArticle(id)
		if err != nil {
			t.Fatalf("get article: %v", err)
		}
		if a.Status != db.StatusRevisionRequested {
			if a.RevisionNumber != 2 {
				t.Errorf("revision_number = %d, want 2", a.RevisionNumber)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("revision reconciler did not process the stuck article after startup")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestScheduleRevisionReconciler_SkipsAfterShutdown: once main's context is
// cancelled, a pass that fires must not start rewriting.
func TestScheduleRevisionReconciler_SkipsAfterShutdown(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	mgr := article.NewManager(database, nil)

	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Stuck", Slug: "stuck-def456", Content: "Old.", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.RequestRevision(id, "fix", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sched := scheduler.New()
	sched.SetStartupDelay(time.Millisecond)
	scheduleRevisionReconciler(ctx, sched, mgr, time.Hour)
	sched.Start()
	time.Sleep(100 * time.Millisecond)
	sched.Stop()

	// With a nil pipeline any attempt would have been recorded as a failure.
	pending, err := database.ListPendingRevisions()
	if err != nil || len(pending) != 1 || pending[0].Failures != 0 {
		t.Errorf("pending = %+v, %v; want untouched", pending, err)
	}
}

func TestWaitBounded(t *testing.T) {
	if !waitBounded(func() {}, time.Second) {
		t.Error("waitBounded = false for work that finished at once")
	}
	release := make(chan struct{})
	defer close(release)
	start := time.Now()
	if waitBounded(func() { <-release }, 50*time.Millisecond) {
		t.Error("waitBounded = true for work still running")
	}
	if time.Since(start) > time.Second {
		t.Error("waitBounded did not give up at its bound")
	}
}
