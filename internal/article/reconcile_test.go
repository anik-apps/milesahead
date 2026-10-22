package article

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

const (
	fakeGenModel    = "gen-model"
	fakeCriticModel = "critic-model"
	fakeCriticJSON  = `{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`
)

// fakeRevisionClient is a concurrency-safe AI client for revision tests. The
// critic always passes; the generator can be made to fail, panic, or block
// until released, so tests can hold a rewrite in flight.
type fakeRevisionClient struct {
	mu       sync.Mutex
	genCalls int
	fail     bool
	panics   bool
	// gate, if set, blocks every generator call until it is closed.
	gate chan struct{}
	// started receives once per generator call, before blocking on gate.
	started chan struct{}
}

func (f *fakeRevisionClient) Generate(model, prompt string, maxTokens int) (string, error) {
	if model == fakeCriticModel {
		return fakeCriticJSON, nil
	}
	f.mu.Lock()
	f.genCalls++
	fail, panics, gate, started := f.fail, f.panics, f.gate, f.started
	f.mu.Unlock()

	if started != nil {
		started <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	if panics {
		panic("generator exploded")
	}
	if fail {
		return "", errors.New("provider returned 503")
	}
	return "Revised Title\nRevised content with better details [Source]", nil
}

func (f *fakeRevisionClient) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.genCalls
}

func (f *fakeRevisionClient) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

// setupFileDB opens a file-backed database. Concurrency tests need it: with
// ":memory:" every pooled connection is a separate, empty database.
func setupFileDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func newFakeManager(t *testing.T, database *db.Database, client *fakeRevisionClient) *Manager {
	t.Helper()
	gen := ai.NewGenerator(client, fakeGenModel, "", nil)
	critic := ai.NewCritic(client, fakeCriticModel)
	mgr := NewManager(database, ai.NewPipeline(gen, critic, 80))
	// Join background rewrites before the database is closed (LIFO cleanup).
	t.Cleanup(mgr.WaitRevisions)
	return mgr
}

// createRevisableArticle creates a linked, verified article in the given status.
func createRevisableArticle(t *testing.T, database *db.Database, status string) int64 {
	t.Helper()
	srcID, err := database.CreateSource(&db.Source{Name: "S", URL: "https://s.example", Active: true})
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	rawID, err := database.CreateRawArticle(&db.RawArticle{
		SourceID: srcID, Title: "T", URL: "https://s.example/a", Content: "Source content.",
		PublishedAt: time.Now(), Hash: "h-" + GenerateSlug("x"),
	})
	if err != nil {
		t.Fatalf("create raw: %v", err)
	}
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Original", Slug: GenerateSlug("original"), Content: "Original content.",
		Type: "deal", Status: status, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	if err := database.LinkArticleSources(id, []int64{rawID}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if _, err := database.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: id, RevisionNumber: 1, Score: 60, IssuesJSON: `["thin"]`,
		FactualCheck: "pass", AttributionCheck: "fail", CompletenessCheck: "fail", HallucinationCheck: "pass",
	}); err != nil {
		t.Fatalf("verification: %v", err)
	}
	return id
}

// assertRevisedOnce checks the article was rewritten exactly once.
func assertRevisedOnce(t *testing.T, database *db.Database, id int64) {
	t.Helper()
	a, err := database.GetGeneratedArticle(id)
	if err != nil {
		t.Fatalf("get article: %v", err)
	}
	if a.Status != db.StatusPending {
		t.Errorf("status = %q, want %q", a.Status, db.StatusPending)
	}
	if a.RevisionNumber != 2 {
		t.Errorf("revision_number = %d, want 2", a.RevisionNumber)
	}
	if a.Title != "Revised Title" {
		t.Errorf("title = %q, want the rewrite's", a.Title)
	}
	revs, err := database.ListRevisionsForArticle(id)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("revision history rows = %d, want exactly 1", len(revs))
	}
}

func pendingRevision(t *testing.T, database *db.Database, id int64) db.PendingRevision {
	t.Helper()
	all, err := database.ListPendingRevisions()
	if err != nil {
		t.Fatalf("list pending revisions: %v", err)
	}
	for _, p := range all {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("article %d is not waiting for a revision", id)
	return db.PendingRevision{}
}

// TestProcessPendingRevisions_ProcessesStuckArticle is the production bug: an
// article in revision_requested whose rewrite never ran (the button's goroutine
// failed or was killed by a deploy) must be picked up by the reconciler.
func TestProcessPendingRevisions_ProcessesStuckArticle(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	// No kick: this is the state a failed or killed goroutine leaves behind.

	res := mgr.ProcessPendingRevisions(context.Background())
	if res.Processed != 1 || res.Failed != 0 || res.Skipped != 0 {
		t.Fatalf("pass = %+v, want 1 processed", res)
	}
	assertRevisedOnce(t, database, id)

	// Nothing left: a second pass does no work.
	if res := mgr.ProcessPendingRevisions(context.Background()); res != (RevisionPassResult{}) {
		t.Errorf("second pass = %+v, want no work", res)
	}
	if client.calls() != 1 {
		t.Errorf("generator calls = %d, want 1", client.calls())
	}
}

// TestProcessPendingRevisions_SkipsInFlight: the Revise button's rewrite and the
// reconciler must never rewrite the same article concurrently.
func TestProcessPendingRevisions_SkipsInFlight(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	mgr.StartRevision(id) // the button
	<-client.started      // its rewrite is now in the AI call

	// Bounded wait: if the guard were missing, the pass would start its own
	// rewrite and block behind the gate rather than skip.
	passDone := make(chan RevisionPassResult, 1)
	go func() { passDone <- mgr.ProcessPendingRevisions(context.Background()) }()
	select {
	case res := <-passDone:
		if res.Skipped != 1 || res.Processed != 0 || res.Failed != 0 {
			t.Errorf("pass during an in-flight rewrite = %+v, want 1 skipped", res)
		}
	case <-time.After(5 * time.Second):
		close(client.gate)
		t.Fatal("reconciler started a second rewrite of an in-flight article")
	}

	close(client.gate)
	mgr.WaitRevisions()

	assertRevisedOnce(t, database, id)
	if client.calls() != 1 {
		t.Errorf("generator calls = %d, want 1 (the reconciler must not start a second rewrite)", client.calls())
	}
	if p := pendingRevisionCount(t, database); p != 0 {
		t.Errorf("%d articles still waiting", p)
	}
}

func pendingRevisionCount(t *testing.T, database *db.Database) int {
	t.Helper()
	all, err := database.ListPendingRevisions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	return len(all)
}

// TestProcessRevision_ConcurrentCallsRewriteOnce: however many callers race,
// one rewrite runs and one revision is stored.
func TestProcessRevision_ConcurrentCallsRewriteOnce(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{gate: make(chan struct{}), started: make(chan struct{}, 8)}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	first := make(chan error, 1)
	go func() { first <- mgr.ProcessRevision(id) }()
	<-client.started

	const others = 5
	var wg sync.WaitGroup
	errs := make(chan error, others)
	for i := 0; i < others; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- mgr.ProcessRevision(id)
		}()
	}
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case <-time.After(5 * time.Second):
		close(client.gate)
		t.Fatal("concurrent callers started their own rewrites instead of returning ErrRevisionInFlight")
	}
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrRevisionInFlight) {
			t.Errorf("concurrent call returned %v, want ErrRevisionInFlight", err)
		}
	}

	close(client.gate)
	if err := <-first; err != nil {
		t.Fatalf("first rewrite: %v", err)
	}
	assertRevisedOnce(t, database, id)

	// Once finished, a late caller finds nothing to do rather than rewriting
	// again, and that is not counted as a failure.
	err := mgr.ProcessRevision(id)
	if !errors.Is(err, ErrNotAwaitingRevision) {
		t.Errorf("late call = %v, want ErrNotAwaitingRevision", err)
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.RevisionNumber != 2 {
		t.Errorf("revision_number = %d after a late call, want 2", a.RevisionNumber)
	}
}

// TestProcessPendingRevisions_BoundedRetries: an article whose rewrite keeps
// failing backs off, then is left for the admin instead of being retried every
// five minutes forever. A new request gives it a fresh budget.
func TestProcessPendingRevisions_BoundedRetries(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{fail: true}
	mgr := newFakeManager(t, database, client)
	mgr.RevisionMaxFailures = 3

	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	ctx := context.Background()
	for attempt := 1; attempt <= 3; attempt++ {
		res := mgr.ProcessPendingRevisions(ctx)
		if res.Failed != 1 {
			t.Fatalf("attempt %d: pass = %+v, want 1 failed", attempt, res)
		}
		p := pendingRevision(t, database, id)
		if p.Failures != attempt {
			t.Fatalf("attempt %d: failures = %d", attempt, p.Failures)
		}
		if want := now.Add(RevisionBackoff(attempt)); !p.RetryAfter.Equal(want) {
			t.Errorf("attempt %d: retry after %v, want %v", attempt, p.RetryAfter, want)
		}
		if p.LastError == "" {
			t.Errorf("attempt %d: last error not recorded", attempt)
		}

		if attempt < 3 {
			// Not due yet: the next tick must not call the provider.
			before := client.calls()
			if res := mgr.ProcessPendingRevisions(ctx); res.Backoff != 1 || client.calls() != before {
				t.Errorf("attempt %d: pass inside backoff = %+v, calls %d -> %d",
					attempt, res, before, client.calls())
			}
		}
		now = now.Add(RevisionBackoff(attempt) + time.Second)
	}

	// Budget spent: left alone however much time passes.
	before := client.calls()
	now = now.Add(24 * time.Hour)
	res := mgr.ProcessPendingRevisions(ctx)
	if res.Exhausted != 1 || res.Failed != 0 || client.calls() != before {
		t.Errorf("pass after exhausting retries = %+v, calls %d -> %d", res, before, client.calls())
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.Status != db.StatusRevisionRequested {
		t.Errorf("status = %q; an exhausted article stays revision_requested for the admin", a.Status)
	}
	if !mgr.RevisionGaveUp(pendingRevision(t, database, id).Failures) {
		t.Error("RevisionGaveUp = false for an exhausted article")
	}

	// The admin asks again, and the provider has recovered.
	client.setFail(false)
	if err := mgr.RequestRevision(id, "Try again please", "admin"); err != nil {
		t.Fatalf("second RequestRevision: %v", err)
	}
	if p := pendingRevision(t, database, id); p.Failures != 0 || !p.RetryAfter.IsZero() || p.LastError != "" {
		t.Errorf("new request did not reset the budget: %+v", p)
	}
	if res := mgr.ProcessPendingRevisions(ctx); res.Processed != 1 {
		t.Fatalf("pass after re-request = %+v, want 1 processed", res)
	}
	assertRevisedOnce(t, database, id)
}

// TestProcessRevision_PanicCountsAsFailure: a rewrite that panics is recorded
// like any failure, so it is bounded too rather than retried forever.
func TestProcessRevision_PanicCountsAsFailure(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{panics: true}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	if err := mgr.ProcessRevision(id); err == nil {
		t.Fatal("expected an error from a panicking rewrite")
	}
	if p := pendingRevision(t, database, id); p.Failures != 1 {
		t.Errorf("failures = %d, want 1", p.Failures)
	}
	// The guard was released: the article is not stuck as in-flight.
	if !mgr.claimRevision(id) {
		t.Error("in-flight guard still held after a panic")
	}
	mgr.releaseRevision(id)
}

// TestProcessPendingRevisions_OverlappingPassSkipped: a pass that outlasts the
// interval must not have a second pass start alongside it.
func TestProcessPendingRevisions_OverlappingPassSkipped(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	done := make(chan RevisionPassResult, 1)
	go func() { done <- mgr.ProcessPendingRevisions(context.Background()) }()
	<-client.started

	if res := mgr.ProcessPendingRevisions(context.Background()); res != (RevisionPassResult{}) {
		t.Errorf("overlapping pass = %+v, want it skipped outright", res)
	}
	close(client.gate)
	if res := <-done; res.Processed != 1 {
		t.Errorf("first pass = %+v, want 1 processed", res)
	}
}

// TestProcessPendingRevisions_StopsOnShutdown: a cancelled context starts no
// new rewrites.
func TestProcessPendingRevisions_StopsOnShutdown(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mgr.ProcessPendingRevisions(ctx)
	if client.calls() != 0 {
		t.Errorf("rewrite started after shutdown began")
	}
}

// TestEmailFeedback_ProcessedByReconciler: email replies set revision_requested
// and, before this fix, nothing ever rewrote the article.
func TestEmailFeedback_ProcessedByReconciler(t *testing.T) {
	database := setupFileDB(t)
	mgr := newFakeManager(t, database, &fakeRevisionClient{})

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "The bonus is 60k, not 75k", "email"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	if res := mgr.ProcessPendingRevisions(context.Background()); res.Processed != 1 {
		t.Fatalf("pass = %+v, want 1 processed", res)
	}
	assertRevisedOnce(t, database, id)
}

// TestUnpublishAndRevise_ProcessedByReconciler covers "Unpublish & revise" from
// the admin and from email, which also never started a rewrite.
func TestUnpublishAndRevise_ProcessedByReconciler(t *testing.T) {
	for _, source := range []string{"admin", "email"} {
		t.Run(source, func(t *testing.T) {
			database := setupFileDB(t)
			mgr := newFakeManager(t, database, &fakeRevisionClient{})

			id := createRevisableArticle(t, database, db.StatusApproved)
			if err := mgr.UnpublishAndRevise(id, "Out of date, refresh it", source); err != nil {
				t.Fatalf("UnpublishAndRevise: %v", err)
			}
			if res := mgr.ProcessPendingRevisions(context.Background()); res.Processed != 1 {
				t.Fatalf("pass = %+v, want 1 processed", res)
			}
			assertRevisedOnce(t, database, id)
		})
	}
}

// TestStartRevision_ProcessesInBackground: the immediate kick used by the
// button, email replies and unpublish-and-revise.
func TestStartRevision_ProcessesInBackground(t *testing.T) {
	database := setupFileDB(t)
	mgr := newFakeManager(t, database, &fakeRevisionClient{})

	id := createRevisableArticle(t, database, db.StatusApproved)
	if err := mgr.UnpublishAndRevise(id, "Refresh it", "email"); err != nil {
		t.Fatalf("UnpublishAndRevise: %v", err)
	}
	mgr.StartRevision(id)
	mgr.WaitRevisions()
	assertRevisedOnce(t, database, id)
}

// TestProcessRevision_NewerFeedbackDuringRewrite: feedback sent while a rewrite
// is running must not be lost. The first result is discarded and the rewrite
// runs again against the newest feedback.
func TestProcessRevision_NewerFeedbackDuringRewrite(t *testing.T) {
	database := setupFileDB(t)
	gate := make(chan struct{})
	client := &fakeRevisionClient{gate: gate, started: make(chan struct{}, 4)}
	mgr := newFakeManager(t, database, client)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "First note", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	mgr.StartRevision(id)
	<-client.started

	// The admin sends more notes mid-rewrite; that click's kick is turned away.
	if err := mgr.RequestRevision(id, "Second note", "admin"); err != nil {
		t.Fatalf("second RequestRevision: %v", err)
	}
	if err := mgr.ProcessRevision(id); !errors.Is(err, ErrRevisionInFlight) {
		t.Fatalf("kick during rewrite = %v, want ErrRevisionInFlight", err)
	}

	client.mu.Lock()
	client.gate = nil
	client.started = nil
	client.mu.Unlock()
	close(gate)
	mgr.WaitRevisions()

	assertRevisedOnce(t, database, id)
	if client.calls() != 2 {
		t.Errorf("generator calls = %d, want 2 (rerun for the newer feedback)", client.calls())
	}
	feedback, _ := database.ListFeedbackForArticle(id)
	revs, _ := database.ListRevisionsForArticle(id)
	if len(revs) == 1 && revs[0].FeedbackID != feedback[0].ID {
		t.Errorf("stored revision answers feedback %d, want the newest (%d)", revs[0].FeedbackID, feedback[0].ID)
	}
}

func TestRevisionBackoff(t *testing.T) {
	tests := map[int]time.Duration{
		0:  0,
		1:  5 * time.Minute,
		2:  10 * time.Minute,
		3:  20 * time.Minute,
		4:  40 * time.Minute,
		5:  time.Hour,
		50: time.Hour,
	}
	for failures, want := range tests {
		if got := RevisionBackoff(failures); got != want {
			t.Errorf("RevisionBackoff(%d) = %v, want %v", failures, got, want)
		}
	}
}

// TestProcessRevisionIfDue_RechecksBackoff: a pass works from a list read before
// its earlier rewrites ran. If an article failed since (say, a request's kick),
// the pass must honour the new backoff rather than retry it immediately.
func TestProcessRevisionIfDue_RechecksBackoff(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{fail: true}
	mgr := newFakeManager(t, database, client)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	mgr.now = func() time.Time { return now }

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	stale := pendingRevision(t, database, id) // what a pass would have listed

	if err := mgr.ProcessRevision(id); err == nil { // the kick fails
		t.Fatal("expected the kick to fail")
	}
	if stale.Failures != 0 {
		t.Fatalf("stale snapshot should predate the failure: %+v", stale)
	}

	calls := client.calls()
	if err := mgr.processRevisionIfDue(context.Background(), id); !errors.Is(err, errRevisionNotDue) {
		t.Errorf("processRevisionIfDue = %v, want errRevisionNotDue", err)
	}
	if client.calls() != calls {
		t.Error("retried inside the backoff window")
	}
	if p := pendingRevision(t, database, id); p.Failures != 1 {
		t.Errorf("failures = %d; a not-due skip must not be charged", p.Failures)
	}
}

func requestRevision(t *testing.T, mgr *Manager, database *db.Database) int64 {
	t.Helper()
	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	return id
}

// TestProcessRevision_AnswersNewestFeedbackByID: if the clock stepped back
// between two notes, the newest note has the older created_at. The rewrite must
// still answer it -- by id, as the store's compare-and-set does -- or the store
// never matches and the article is rewritten over and over.
func TestProcessRevision_AnswersNewestFeedbackByID(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)
	id := requestRevision(t, mgr, database)

	res, err := database.DB().Exec(`INSERT INTO admin_feedback (generated_article_id, feedback_text, source, created_at)
		VALUES (?, 'Newer note, older clock', 'admin', datetime('now', '-1 hour'))`, id)
	if err != nil {
		t.Fatalf("insert feedback: %v", err)
	}
	newest, _ := res.LastInsertId()

	if err := mgr.ProcessRevision(id); err != nil {
		t.Fatalf("ProcessRevision: %v", err)
	}
	assertRevisedOnce(t, database, id)
	revs, _ := database.ListRevisionsForArticle(id)
	if len(revs) == 1 && revs[0].FeedbackID != newest {
		t.Errorf("answered feedback %d, want the newest by id (%d)", revs[0].FeedbackID, newest)
	}
	if client.calls() != 1 {
		t.Errorf("generator calls = %d, want 1", client.calls())
	}
}

// TestProcessRevision_RepeatedSupersessionIsCharged: a store that keeps missing
// with no newer feedback is a failure, not a skip -- otherwise every kick and
// pass would spend AI calls on the article, uncounted, forever.
func TestProcessRevision_RepeatedSupersessionIsCharged(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)
	mgr.applyRevision = func(db.AppliedRevision) (bool, error) { return false, nil }
	id := requestRevision(t, mgr, database)

	err := mgr.ProcessRevision(id)
	if err == nil || isRevisionSkip(err) {
		t.Fatalf("ProcessRevision = %v, want a charged failure", err)
	}
	if p := pendingRevision(t, database, id); p.Failures != 1 {
		t.Errorf("failures = %d, want 1", p.Failures)
	}
	if client.calls() != 2 {
		t.Errorf("generator calls = %d, want 2 (one rerun, then give up)", client.calls())
	}
}

// TestProcessRevision_SupersededRetriesTerminate: feedback arriving during every
// rewrite does not keep one call rewriting forever.
func TestProcessRevision_SupersededRetriesTerminate(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)
	var id int64
	mgr.applyRevision = func(db.AppliedRevision) (bool, error) {
		_, err := database.CreateAdminFeedback(&db.AdminFeedback{GeneratedArticleID: id, FeedbackText: "and another thing", Source: "admin"})
		return false, err
	}
	id = requestRevision(t, mgr, database)

	if err := mgr.ProcessRevision(id); !errors.Is(err, ErrRevisionSuperseded) {
		t.Fatalf("ProcessRevision = %v, want ErrRevisionSuperseded after the rerun limit", err)
	}
	if client.calls() != maxSupersededRetries {
		t.Errorf("generator calls = %d, want %d", client.calls(), maxSupersededRetries)
	}
	if p := pendingRevision(t, database, id); p.Failures != 0 {
		t.Errorf("failures = %d; genuinely newer feedback is not a failure", p.Failures)
	}
}

// TestProcessRevision_NoRerunAfterShutdown: a cancelled context stops the rerun
// loop, so shutdown is not held by further rewrites.
func TestProcessRevision_NoRerunAfterShutdown(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)
	var id int64
	mgr.applyRevision = func(db.AppliedRevision) (bool, error) {
		_, err := database.CreateAdminFeedback(&db.AdminFeedback{GeneratedArticleID: id, FeedbackText: "more", Source: "admin"})
		return false, err
	}
	id = requestRevision(t, mgr, database)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.processRevisionIfDue(ctx, id); !errors.Is(err, ErrRevisionSuperseded) {
		t.Fatalf("processRevisionIfDue = %v", err)
	}
	if client.calls() != 1 {
		t.Errorf("generator calls = %d, want 1 (no rerun once shutdown began)", client.calls())
	}
}

// TestProcessRevision_RerunsWhenFeedbackArrivesAsStored covers feedback that
// lands just after the rewrite is stored, while the in-flight guard is still
// held -- the request's own kick is turned away, so this call must go again.
func TestProcessRevision_RerunsWhenFeedbackArrivesAsStored(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{}
	mgr := newFakeManager(t, database, client)
	var id int64
	first := true
	mgr.applyRevision = func(r db.AppliedRevision) (bool, error) {
		ok, err := database.ApplyRevision(r)
		if first && ok && err == nil {
			first = false
			if err := mgr.RequestRevision(id, "One more thing", "email"); err != nil {
				return ok, err
			}
		}
		return ok, err
	}
	id = requestRevision(t, mgr, database)

	if err := mgr.ProcessRevision(id); err != nil {
		t.Fatalf("ProcessRevision: %v", err)
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.Status != db.StatusPending || a.RevisionNumber != 3 {
		t.Errorf("status %q rev %d, want pending rev 3 (rerun for the late feedback)", a.Status, a.RevisionNumber)
	}
	if client.calls() != 2 {
		t.Errorf("generator calls = %d, want 2", client.calls())
	}
}

// TestProcessRevisionIfDue_ExhaustedIsNotBackoff: the pass must report an
// article that ran out of retries since its list was read as exhausted.
func TestProcessRevisionIfDue_ExhaustedIsNotBackoff(t *testing.T) {
	database := setupFileDB(t)
	client := &fakeRevisionClient{fail: true}
	mgr := newFakeManager(t, database, client)
	mgr.RevisionMaxFailures = 1
	id := requestRevision(t, mgr, database)

	if err := mgr.ProcessRevision(id); err == nil {
		t.Fatal("expected failure")
	}
	if err := mgr.processRevisionIfDue(context.Background(), id); !errors.Is(err, errRevisionExhausted) {
		t.Errorf("processRevisionIfDue = %v, want errRevisionExhausted", err)
	}
}

// criticGoneClient is fakeRevisionClient with every critic retired.
type criticGoneClient struct{ *fakeRevisionClient }

func (c criticGoneClient) Generate(model, prompt string, n int) (string, error) {
	if model == fakeCriticModel {
		return "", &ai.APIError{StatusCode: 404, Model: model, Body: "Not Found"}
	}
	return c.fakeRevisionClient.Generate(model, prompt, n)
}

// A critic outage is not the rewrite's fault: it must not use up the
// article's retry budget and drop it into the admin's to-do.
func TestProcessPendingRevisions_CriticOutageIsNotCharged(t *testing.T) {
	database := setupFileDB(t)
	client := criticGoneClient{&fakeRevisionClient{}}
	mgr := NewManager(database, ai.NewPipeline(ai.NewGenerator(client, fakeGenModel, "", nil),
		ai.NewCritic(client, fakeCriticModel), 80))
	t.Cleanup(mgr.WaitRevisions)

	id := createRevisableArticle(t, database, db.StatusPending)
	if err := mgr.RequestRevision(id, "Add the annual fee", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	res := mgr.ProcessPendingRevisions(context.Background())
	if res.Failed != 0 {
		t.Errorf("pass = %+v, want no failure charged", res)
	}
	if p := pendingRevision(t, database, id); p.Failures != 0 {
		t.Errorf("failures = %d, want 0", p.Failures)
	}
}
