package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
	"github.com/kumaranik/milesahead/internal/storydedup"
)

// fakePipeline writes an article titled after the cluster's first source.
type fakePipeline struct {
	mu    sync.Mutex
	calls int
}

func (p *fakePipeline) Process(_ string, sources []ai.SourceArticle) (*ai.PipelineResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &ai.PipelineResult{
		Title: sources[0].Title, Content: "<p>body</p>", TLDR: "Summary of " + sources[0].Title,
		Type: "news", Verification: &ai.VerificationResult{Score: 90}, PassedThreshold: true,
	}, nil
}

// storyAI answers the story check. reply may contain %d, replaced with the
// ID in matchID (known only once the first article exists).
type storyAI struct {
	mu      sync.Mutex
	reply   string
	err     error
	matchID int64
	prompts []string
}

func (s *storyAI) Generate(_, prompt string, _ int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	if s.err != nil {
		return "", s.err
	}
	if strings.Contains(s.reply, "%d") {
		return fmt.Sprintf(s.reply, s.matchID), nil
	}
	return s.reply, nil
}

func (s *storyAI) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

type scanFixture struct {
	t        *testing.T
	db       *db.Database
	gen      *clusterGenerator
	pipeline *fakePipeline
	ai       *storyAI
	sources  []int64
	n        int
}

func newScanFixture(t *testing.T, mode string) *scanFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	f := &scanFixture{t: t, db: database, pipeline: &fakePipeline{}, ai: &storyAI{}}
	for _, name := range []string{"Blog A", "Blog B", "Blog C"} {
		id, err := database.CreateSource(&db.Source{Name: name, URL: "https://" + name, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		f.sources = append(f.sources, id)
	}
	cfg := &config.Config{Scanner: config.ScannerConfig{StoryDedup: mode}}
	f.gen = newClusterGenerator(cfg, database, f.pipeline, article.NewManager(database, nil),
		f.ai, []string{"fast"}, cluster.DefaultThreshold)
	return f
}

// raw stores a scanned article from source index src (0-2).
func (f *scanFixture) raw(src int, title string) int64 {
	f.t.Helper()
	f.n++
	id, err := f.db.CreateRawArticle(&db.RawArticle{
		SourceID: f.sources[src], Title: title, URL: fmt.Sprintf("https://x/%d", f.n),
		Content: "About " + title, Hash: fmt.Sprintf("hash-%d", f.n),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *scanFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.DB().Exec(query, args...); err != nil {
		f.t.Fatal(err)
	}
}

// age moves raw articles out of the 48h clustering window.
func (f *scanFixture) age(ids ...int64) {
	for _, id := range ids {
		f.exec(`UPDATE raw_articles SET created_at = datetime('now', '-50 hours') WHERE id = ?`, id)
	}
}

func (f *scanFixture) articleCount() int {
	f.t.Helper()
	var n int
	if err := f.db.DB().QueryRow(`SELECT COUNT(*) FROM generated_articles`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// generate runs one cluster made of the given raws through the generator,
// reporting whether it created an article.
func (f *scanFixture) generate(ids ...int64) (created bool, articleID int64) {
	f.t.Helper()
	raws, err := f.db.GetRawArticlesByIDs(ids)
	if err != nil {
		f.t.Fatal(err)
	}
	var c cluster.ArticleCluster
	for _, r := range raws {
		c.Articles = append(c.Articles, cluster.Article{ID: r.ID, Title: r.Title, SourceID: r.SourceID})
	}
	created = f.gen.generate(c)
	if created {
		if err := f.db.DB().QueryRow(`SELECT MAX(id) FROM generated_articles`).Scan(&articleID); err != nil {
			f.t.Fatal(err)
		}
	}
	return created, articleID
}

func counterValue(t *testing.T, decision, mode string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.StoryDedupDecisionsTotal.WithLabelValues(decision, mode).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// captureLogs records JSON log lines for the rest of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func decisionLogs(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "story dedup decision" {
			out = append(out, m)
		}
	}
	return out
}

// TestScan_LateCoverageDoesNotResurfaceAfterRolloff is the 48h rolloff bug
// (production 1040->1064, 1043->1068, 1050->1074, 1051->1075). Late coverage
// clustered with an article's sources, so the raw-ID check skipped the
// cluster -- but nothing recorded the late raws. Once the article's own
// sources aged out of the window, the late raws formed a cluster that shared
// no raw with it and the story was written again. The story check is off
// here: the raw-level record alone must stop it.
func TestScan_LateCoverageDoesNotResurfaceAfterRolloff(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupOff)

	r1 := f.raw(0, "Citi ThankYou adds Japan Airlines transfer partner")
	r2 := f.raw(1, "Citi ThankYou adds Japan Airlines as transfer partner")
	if got := f.gen.run(); got != 1 {
		t.Fatalf("first scan created %d articles, want 1", got)
	}
	var original int64
	f.db.DB().QueryRow(`SELECT id FROM generated_articles`).Scan(&original)

	// Late coverage arrives and clusters with the original sources.
	late1 := f.raw(2, "Citi ThankYou Japan Airlines transfer partner added")
	late2 := f.raw(0, "Citi ThankYou Japan Airlines transfer partner now live")
	if got := f.gen.run(); got != 0 {
		t.Fatalf("second scan created %d articles, want 0 (sources overlap)", got)
	}
	covered, err := f.db.ListCoveredRaws(original)
	if err != nil {
		t.Fatal(err)
	}
	if len(covered) != 2 || covered[0].RawArticleID != late1 || covered[1].RawArticleID != late2 ||
		covered[0].Reason != db.CoverReasonRawOverlap {
		t.Fatalf("covered = %+v, want the two late raws as raw_overlap", covered)
	}

	// The original sources leave the 48h window; only the late raws remain.
	f.age(r1, r2)
	if got := f.gen.run(); got != 0 {
		t.Errorf("after rolloff the late coverage created %d articles, want 0", got)
	}
	if n := f.articleCount(); n != 1 {
		t.Errorf("articles = %d, want 1", n)
	}

	// The article's public source list is still what it was written from.
	linked, _ := f.db.GetLinkedRawArticles(original)
	if len(linked) != 2 {
		t.Errorf("article sources = %d, want 2", len(linked))
	}
}

// A cluster sharing no vocabulary with any recent article is new: no AI call.
func TestScan_StoryCheck_NoCandidatesGeneratesWithoutAI(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupBlock)
	f.raw(0, "Hyatt fall promotion offers bonus points")
	f.raw(1, "Hyatt bonus points fall promotion is live")
	if created, _ := f.generate(1, 2); !created {
		t.Fatal("first article not created")
	}
	a := f.raw(0, "Amex Platinum raises annual fee")
	b := f.raw(2, "American Express Platinum annual fee increase")
	if created, _ := f.generate(a, b); !created {
		t.Error("unrelated cluster not created")
	}
	if f.ai.calls() != 0 {
		t.Errorf("AI called %d times, want 0", f.ai.calls())
	}
}

// sameStory sets up the same-scan split (production 1120/1121): one Alaska
// story, written from one cluster, then a second cluster of the same story.
// It returns the first article's ID and the second cluster's raw IDs.
func sameStory(f *scanFixture) (int64, []int64) {
	f.t.Helper()
	a := f.raw(0, "Alaska Airlines adds premium cabins to more routes")
	b := f.raw(1, "Alaska expands premium class cabins across its network")
	created, first := f.generate(a, b)
	if !created {
		f.t.Fatal("first article not created")
	}
	f.ai.matchID = first
	c := f.raw(2, "Alaska premium class coming to Hawaii flights")
	d := f.raw(0, "Alaska Airlines premium cabin expansion reaches Hawaii routes")
	return first, []int64{c, d}
}

const dupReply = `{"same_story_as": %d, "materially_new_development": false, "reason": "same premium cabin expansion"}`

// Shadow mode: the duplicate is logged and counted as would_block, and still
// generated. The first article was created moments earlier in the same scan,
// and is a candidate.
func TestScan_StoryCheck_ShadowLogsAndStillGenerates(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupShadow)
	f.ai.reply = dupReply
	logs := captureLogs(t)
	before := counterValue(t, storydedup.DecisionDuplicate, config.StoryDedupShadow)

	first, second := sameStory(f)
	if created, _ := f.generate(second...); !created {
		t.Fatal("shadow mode must still generate")
	}
	if f.ai.calls() != 1 {
		t.Fatalf("AI called %d times, want 1", f.ai.calls())
	}
	if got := counterValue(t, storydedup.DecisionDuplicate, config.StoryDedupShadow) - before; got != 1 {
		t.Errorf("duplicate/shadow counter +%v, want +1", got)
	}

	lines := decisionLogs(t, logs)
	if len(lines) != 2 {
		t.Fatalf("got %d decision log lines, want 2 (one per cluster)", len(lines))
	}
	l := lines[1]
	if l["would_block"] != true || l["blocked"] != false || l["decision"] != "duplicate" ||
		l["mode"] != "shadow" || l["stage"] != "ai" {
		t.Errorf("decision line = %v", l)
	}
	if l["matched_article_id"] != float64(first) || l["ai_same_story_as"] != float64(first) {
		t.Errorf("matched_article_id = %v, want %d", l["matched_article_id"], first)
	}
	ids, _ := l["cluster_raw_ids"].([]any)
	if len(ids) != 2 || ids[0] != float64(second[0]) {
		t.Errorf("cluster_raw_ids = %v, want %v", l["cluster_raw_ids"], second)
	}
	if cands, _ := l["candidates"].([]any); len(cands) == 0 {
		t.Errorf("candidates missing: %v", l)
	}
	if covered, _ := f.db.ListCoveredRaws(first); len(covered) != 0 {
		t.Errorf("shadow mode recorded covered raws: %+v", covered)
	}
}

// Block mode: the duplicate is not generated, and its raws are recorded as
// covered by the matched article, so its later coverage is stopped too.
func TestScan_StoryCheck_BlockSkipsAndCovers(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupBlock)
	f.ai.reply = dupReply
	first, second := sameStory(f)

	if created, _ := f.generate(second...); created {
		t.Fatal("block mode generated a duplicate")
	}
	if n := f.articleCount(); n != 1 {
		t.Errorf("articles = %d, want 1", n)
	}
	covered, _ := f.db.ListCoveredRaws(first)
	if len(covered) != 2 || covered[0].Reason != db.CoverReasonStoryMatch {
		t.Errorf("covered = %+v, want the cluster's 2 raws as story_match", covered)
	}
	if linked, _ := f.db.GetLinkedRawArticles(first); len(linked) != 2 {
		t.Errorf("article sources = %d, want 2 (unchanged)", len(linked))
	}

	// The blocked raws now fail the raw-ID check with no AI call.
	calls := f.ai.calls()
	e := f.raw(1, "Alaska premium cabins: everything about the Hawaii expansion")
	if created, _ := f.generate(append(second, e)...); created {
		t.Error("covered raws seeded a new article")
	}
	if f.ai.calls() != calls {
		t.Error("the raw-ID check should have stopped this cluster before the AI")
	}
}

func TestScan_StoryCheck_BlockModeVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reply       string
		err         error
		wantCreated bool
		wantLabel   string
	}{
		{"follow-up", `{"same_story_as": %d, "materially_new_development": true, "reason": "now bookable"}`, nil, true, storydedup.DecisionFollowUp},
		{"different story", `{"same_story_as": null, "materially_new_development": false, "reason": "x"}`, nil, true, storydedup.DecisionNew},
		{"invalid id", `{"same_story_as": 999999, "materially_new_development": false, "reason": "x"}`, nil, true, storydedup.DecisionErrorFallback},
		// The fallback needs 0.55; this pair scores well below it.
		{"ai error", "", fmt.Errorf("gateway timeout"), true, storydedup.DecisionErrorFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScanFixture(t, config.StoryDedupBlock)
			f.ai.reply, f.ai.err = tc.reply, tc.err
			_, second := sameStory(f)
			before := counterValue(t, tc.wantLabel, config.StoryDedupBlock)
			if created, _ := f.generate(second...); created != tc.wantCreated {
				t.Errorf("created = %v, want %v", created, tc.wantCreated)
			}
			if got := counterValue(t, tc.wantLabel, config.StoryDedupBlock) - before; got != 1 {
				t.Errorf("%s counter +%v, want +1", tc.wantLabel, got)
			}
		})
	}
}

// With the AI down, a near-identical rewrite (an edited headline makes a new
// raw row) scores above 0.55 and is blocked on the lexical score alone.
func TestScan_StoryCheck_FallbackBlocksNearIdentical(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupBlock)
	f.ai.err = fmt.Errorf("503")
	a := f.raw(0, "Hyatt fall promotion offers bonus points on every stay")
	b := f.raw(1, "Hyatt bonus points fall promotion now live for members")
	if created, _ := f.generate(a, b); !created {
		t.Fatal("first article not created")
	}
	c := f.raw(0, "Hyatt fall promotion offers bonus points on every stay (updated)")
	d := f.raw(2, "Hyatt fall promotion: bonus points for members now live")
	if created, _ := f.generate(c, d); created {
		t.Error("fallback did not block a near-identical story")
	}
}

// Rejected articles are candidates (a rejected duplicate is still a
// duplicate); articles older than 7 days are not.
func TestScan_StoryCheck_CandidateWindow(t *testing.T) {
	t.Run("rejected counts", func(t *testing.T) {
		f := newScanFixture(t, config.StoryDedupBlock)
		f.ai.reply = dupReply
		first, second := sameStory(f)
		f.exec(`UPDATE generated_articles SET status = ? WHERE id = ?`, db.StatusRejected, first)
		if created, _ := f.generate(second...); created {
			t.Error("duplicate of a rejected article was generated")
		}
		if p := f.ai.prompts[len(f.ai.prompts)-1]; !strings.Contains(p, fmt.Sprintf("id %d ", first)) {
			t.Errorf("rejected article not offered to the AI:\n%s", p)
		}
	})
	t.Run("older than 7 days ignored", func(t *testing.T) {
		f := newScanFixture(t, config.StoryDedupBlock)
		f.ai.reply = dupReply
		first, second := sameStory(f)
		f.exec(`UPDATE generated_articles SET created_at = datetime('now', '-8 days') WHERE id = ?`, first)
		if created, _ := f.generate(second...); !created {
			t.Error("an 8-day-old article blocked a new one")
		}
		if f.ai.calls() != 0 {
			t.Errorf("AI called %d times, want 0", f.ai.calls())
		}
	})
}

// Off: no check, no AI call, no decision counted.
func TestScan_StoryCheck_Off(t *testing.T) {
	f := newScanFixture(t, config.StoryDedupOff)
	f.ai.reply = dupReply
	_, second := sameStory(f)
	if created, _ := f.generate(second...); !created {
		t.Error("off mode skipped generation")
	}
	if f.ai.calls() != 0 {
		t.Errorf("AI called %d times, want 0", f.ai.calls())
	}
}

// The story check runs on the short-call chain, like the other side jobs.
func TestClusterGenerator_UsesEnrichmentChain(t *testing.T) {
	cfg := &config.Config{
		AI:      config.AIConfig{CriticModel: "critic", EnrichmentModel: "fast-a, fast-b"},
		Scanner: config.ScannerConfig{StoryDedup: config.StoryDedupShadow},
	}
	g := newClusterGenerator(cfg, nil, nil, nil, &storyAI{}, enrichmentModels(cfg), 0.3)
	if strings.Join(g.stories.Models, ",") != "fast-a,fast-b" || g.storyMode != "shadow" {
		t.Errorf("models = %v, mode = %q", g.stories.Models, g.storyMode)
	}
}
