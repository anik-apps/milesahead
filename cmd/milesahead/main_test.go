package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
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

// TestShutdownBudgetFitsSystemdStopTimeout: the shutdown waits run one after
// another, so their sum must stay inside systemd's default 90s TimeoutStopSec
// or a stop during a long AI rewrite ends in SIGKILL.
func TestShutdownBudgetFitsSystemdStopTimeout(t *testing.T) {
	const systemdDefaultStopTimeout = 90 * time.Second
	if shutdownBudget >= systemdDefaultStopTimeout {
		t.Errorf("shutdown budget %v is not inside systemd's %v stop timeout",
			shutdownBudget, systemdDefaultStopTimeout)
	}
}

// TestSchedulerStopIsBoundedDuringLongJob: a job that outlasts the bound (an AI
// rewrite with retries) must not hold shutdown.
func TestSchedulerStopIsBoundedDuringLongJob(t *testing.T) {
	sched := scheduler.New()
	sched.SetStartupDelay(time.Millisecond)
	started := make(chan struct{})
	release := make(chan struct{})
	sched.AddRevisionJob(time.Hour, func() {
		close(started)
		<-release
	})
	sched.Start()
	<-started

	begin := time.Now()
	if waitBounded(sched.Stop, 50*time.Millisecond) {
		t.Error("Stop returned while the job was still running")
	}
	if time.Since(begin) > time.Second {
		t.Error("bounded stop did not give up at its bound")
	}
	close(release) // let the abandoned Stop finish
}

// scoreAll is a text model that gives every candidate the same score.
type scoreAll struct{}

func (scoreAll) Generate(_, _ string, _ int) (string, error) {
	return `{"scores":[{"index":0,"score":8,"reason":"r"}]}`, nil
}

// toHost sends every request to one test server.
type toHost struct{ addr string }

func (h toHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = h.addr
	return http.DefaultTransport.RoundTrip(req)
}

// Integration, production wiring: with vision scoring configured, the
// thumbnail of a Wikimedia candidate is warmed, then downloaded by the scorer,
// then sent to the vision endpoint. Without the warm-up the download was the
// first request for a new size, met a slow render or a 429, and the
// candidate took the x0.6 penalty.
func TestImageScoring_WarmsThumbnailsBeforeVision(t *testing.T) {
	const thumb = "/wikipedia/commons/thumb/6/6c/Lobby.jpg/500px-Lobby.jpg"
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	wikimedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("wikimedia " + r.URL.Path)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("j"))
	}))
	defer wikimedia.Close()
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("vision")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"the lobby\"}"}}]}`))
	}))
	defer vision.Close()

	cfg := &config.Config{AI: config.AIConfig{HFBaseURL: vision.URL, HFAPIToken: "token", EnrichmentModel: "text"}}
	searcher, scorer := newImageScoring(cfg, scoreAll{}, "vision", enrichmentModels(cfg))
	searcher.SetTransport(toHost{wikimedia.Listener.Addr().String()})
	scorer.SetImageTransport(toHost{wikimedia.Listener.Addr().String()})

	const wm = "https://upload.wikimedia.org"
	if _, err := scorer.Score("A hotel lobby", "Lobby", []images.ImageCandidate{
		{URL: wm + "/wikipedia/commons/thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg", Thumbnail: wm + thumb, Source: "wikimedia"},
	}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ", "); got != fmt.Sprintf("wikimedia %s, wikimedia %s, vision", thumb, thumb) {
		t.Errorf("requests: %s; want the thumbnail warmed, downloaded, then the vision call", got)
	}
}
