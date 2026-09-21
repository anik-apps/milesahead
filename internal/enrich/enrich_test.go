package enrich

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// scriptedClient answers by prompt content, so one mock can serve the several
// different enrichment calls a pass makes.
type scriptedClient struct {
	fail  map[string]error  // substring of prompt -> error to return
	reply map[string]string // substring of prompt -> canned reply
	calls []string
}

func (c *scriptedClient) Generate(model, prompt string, maxTokens int) (string, error) {
	c.calls = append(c.calls, model)
	for frag, err := range c.fail {
		if strings.Contains(prompt, frag) {
			return "", err
		}
	}
	for frag, out := range c.reply {
		if strings.Contains(prompt, frag) {
			return out, nil
		}
	}
	return "", fmt.Errorf("no scripted reply for prompt")
}

func testDB(t *testing.T) *db.Database {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedArticle(t *testing.T, d *db.Database) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Citi adds JAL", Slug: "citi-jal", Content: "Body text about transfers.",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

// TestRun_PartialFailureStillKeepsWhatSucceeded is the core property. Inline,
// the steps were independent best-effort calls and a failure simply lost the
// field. Reconciled, a failing step must not discard the siblings that worked,
// and the article must remain eligible for another pass.
func TestRun_PartialFailureStillKeepsWhatSucceeded(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{
		fail: map[string]error{
			"Extract key details": fmt.Errorf("provider exploded"),
		},
		reply: map[string]string{
			"Scout":                "Worth it only if you fly JAL.",
			"topic tags":           `["transfer-bonuses"]`,
			"image search queries": `["jal plane"]`,
		},
	}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	var take, tags, details string
	var attempts int
	err := d.DB().QueryRow(`SELECT scouts_take, tags_json, COALESCE(key_details,''), enrichment_attempts
		FROM generated_articles WHERE id = ?`, id).Scan(&take, &tags, &details, &attempts)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if take == "" {
		t.Error("Scout's Take succeeded and must be kept despite the sibling failure")
	}
	if !strings.Contains(tags, "transfer-bonuses") {
		t.Errorf("tags should be stored, got %q", tags)
	}
	if strings.Contains(details, "Label") {
		t.Errorf("key_details failed and must not be written, got %q", details)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// TestRun_StopsAfterMaxAttempts verifies the budget actually binds, so a
// permanently unenrichable article stops consuming API calls.
func TestRun_StopsAfterMaxAttempts(t *testing.T) {
	d := testDB(t)
	seedArticle(t, d)

	client := &scriptedClient{fail: map[string]error{"": fmt.Errorf("always down")}}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.MaxAttempts = 2

	s.Run()
	s.Run()
	callsAfterBudget := len(client.calls)
	s.Run() // should now select nothing

	if len(client.calls) != callsAfterBudget {
		t.Errorf("reconciler kept calling after the attempt budget: %d then %d calls",
			callsAfterBudget, len(client.calls))
	}

	pending, exhausted, err := d.CountDegradedArticles(2)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if pending != 0 || exhausted != 1 {
		t.Errorf("pending=%d exhausted=%d, want 0 and 1", pending, exhausted)
	}
}

// TestRun_UsesFallbackModelWhenPrimaryIsDown covers the reason enrichment got
// its own chain: when one model's backend degrades, every article in that
// window used to lose its fields permanently.
func TestRun_UsesFallbackModelWhenPrimaryIsDown(t *testing.T) {
	d := testDB(t)
	seedArticle(t, d)

	client := &failFirstModelClient{
		down:  "primary",
		reply: "Worth it only if you fly JAL.",
	}
	s := New(d, client, []string{"primary", "backup"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	var take string
	if err := d.DB().QueryRow(
		"SELECT scouts_take FROM generated_articles WHERE id = 1").Scan(&take); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if take == "" {
		t.Error("the fallback model was available and should have produced a take")
	}
	if !client.usedBackup {
		t.Error("expected the chain to fall through to the backup model")
	}
}

type failFirstModelClient struct {
	down       string
	reply      string
	usedBackup bool
}

func (c *failFirstModelClient) Generate(model, prompt string, maxTokens int) (string, error) {
	if model == c.down {
		// Must be a real *ai.APIError: classification switches on StatusCode,
		// not on the message text. An error merely SHAPED like an API error is
		// deliberately not recognised -- that is the point of the typed error,
		// since matching interpolated provider text used to misclassify both
		// ways.
		return "", &ai.APIError{StatusCode: 503, Model: model, Body: "overloaded"}
	}
	c.usedBackup = true
	if strings.Contains(prompt, "Scout") {
		return c.reply, nil
	}
	if strings.Contains(prompt, "topic tags") {
		return `["transfer-bonuses"]`, nil
	}
	return `{"key_details":[{"label":"Bonus","value":"30%"}],"links":[]}`, nil
}

// TestRun_SkipsOverlappingPass guards against passes piling up.
//
// robfig/cron's "@every" does not skip a tick when the previous run is still
// going, and a pass can outlast its interval whenever the provider is degraded
// — observed in production, where one field spent minutes retrying a timing-out
// model. Overlapping passes would multiply load on an already-struggling
// provider and race each other's attempt counters.
func TestRun_SkipsOverlappingPass(t *testing.T) {
	d := testDB(t)
	seedArticle(t, d)

	started := make(chan struct{})
	release := make(chan struct{})
	client := &blockingClient{started: started, release: release}

	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run()
	}()
	<-started // first pass is now inside a model call

	s.Run() // second pass must return immediately, not queue behind the first

	if got := client.calls.Load(); got != 1 {
		t.Errorf("overlapping pass made extra model calls: %d, want 1", got)
	}

	// Join before returning. testDB closes the database in t.Cleanup, and a
	// pass still in flight would query it after teardown -- the same "goroutine
	// outlives its database" bug this change fixes in the web handlers.
	close(release)
	<-done
}

type blockingClient struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func (c *blockingClient) Generate(model, prompt string, maxTokens int) (string, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.started) })
	<-c.release
	return "", fmt.Errorf("released")
}
