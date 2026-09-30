package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func createRaw(t *testing.T, d *Database, srcID int64, title string) int64 {
	t.Helper()
	id, err := d.CreateRawArticle(&RawArticle{
		SourceID: srcID, Title: title, URL: "https://test.com/" + title,
		Content: "c", PublishedAt: time.Now().UTC(), Hash: "h-" + title,
	})
	if err != nil {
		t.Fatalf("CreateRawArticle(%q): %v", title, err)
	}
	return id
}

func createArticle(t *testing.T, d *Database, title, status string) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: title, Slug: fmt.Sprintf("slug-%s-%d", title, time.Now().UnixNano()),
		Content: "c", Type: "news", Status: status, SourcesJSON: "[]", RevisionNumber: 1,
		TLDR: "tldr " + title,
	})
	if err != nil {
		t.Fatalf("CreateGeneratedArticle(%q): %v", title, err)
	}
	return id
}

func TestArticleForRawIDs(t *testing.T) {
	d := openTestDB(t)
	src := createTestSource(t, d)
	r1 := createRaw(t, d, src, "r1")
	r2 := createRaw(t, d, src, "r2")
	r3 := createRaw(t, d, src, "r3")
	r4 := createRaw(t, d, src, "r4")
	older := createArticle(t, d, "older", StatusApproved)
	newer := createArticle(t, d, "newer", StatusPending)
	if err := d.LinkArticleSources(older, []int64{r1, r2}); err != nil {
		t.Fatal(err)
	}
	if err := d.LinkArticleSources(newer, []int64{r3}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		ids    []int64
		want   int64
		wantOK bool
	}{
		{"empty", nil, 0, false},
		{"no overlap", []int64{r4}, 0, false},
		{"one source", []int64{r3, r4}, newer, true},
		{"most overlap wins", []int64{r1, r2, r3}, older, true},
		{"tie goes to newer", []int64{r1, r3}, newer, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := d.ArticleForRawIDs(tc.ids)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || ok != tc.wantOK {
				t.Errorf("ArticleForRawIDs(%v) = %d, %v; want %d, %v", tc.ids, got, ok, tc.want, tc.wantOK)
			}
		})
	}

	// A covered raw matches exactly like a linked one.
	if _, err := d.CoverRawArticles(older, []int64{r4}, CoverReasonRawOverlap); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.ArticleForRawIDs([]int64{r4})
	if err != nil || !ok || got != older {
		t.Errorf("covered raw: got %d, %v, %v; want %d, true", got, ok, err, older)
	}
}

// TestCoverRawArticles_KeepsSourceListUnchanged: covered raws are for the
// duplicate check only. The article's public source list is what it was
// written from, and must not grow when late coverage is recorded.
func TestCoverRawArticles_KeepsSourceListUnchanged(t *testing.T) {
	d := openTestDB(t)
	src := createTestSource(t, d)
	r1 := createRaw(t, d, src, "r1")
	r2 := createRaw(t, d, src, "r2")
	late1 := createRaw(t, d, src, "late1")
	late2 := createRaw(t, d, src, "late2")
	art := createArticle(t, d, "story", StatusApproved)
	other := createArticle(t, d, "other", StatusApproved)
	if err := d.LinkArticleSources(art, []int64{r1, r2}); err != nil {
		t.Fatal(err)
	}

	n, err := d.CoverRawArticles(art, []int64{r1, r2, late1, late2}, CoverReasonRawOverlap)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("added %d, want 2 (the linked raws are skipped)", n)
	}
	// The first cover wins; covering again, even for another article, adds nothing.
	if n, err := d.CoverRawArticles(other, []int64{late1}, CoverReasonStoryMatch); err != nil || n != 0 {
		t.Errorf("re-cover: added %d, %v; want 0", n, err)
	}

	covered, err := d.ListCoveredRaws(art)
	if err != nil {
		t.Fatal(err)
	}
	if len(covered) != 2 || covered[0].RawArticleID != late1 || covered[1].RawArticleID != late2 ||
		covered[0].Reason != CoverReasonRawOverlap {
		t.Errorf("covered = %+v, want late1 and late2 as raw_overlap", covered)
	}

	linked, err := d.GetLinkedRawArticles(art)
	if err != nil {
		t.Fatal(err)
	}
	if len(linked) != 2 {
		t.Errorf("linked sources = %d, want 2: covering must not add public sources", len(linked))
	}
	if c, _ := d.CountArticleSources(art); c != 2 {
		t.Errorf("CountArticleSources = %d, want 2", c)
	}
}

func TestListStoryCandidates(t *testing.T) {
	d := openTestDB(t)
	src := createTestSource(t, d)
	r1 := createRaw(t, d, src, "Alaska adds premium cabins")
	r2 := createRaw(t, d, src, "Alaska premium class expands")
	rejected := createArticle(t, d, "rejected one", StatusRejected)
	draft := createArticle(t, d, "draft one", StatusDraft)
	old := createArticle(t, d, "old one", StatusApproved)
	if err := d.LinkArticleSources(rejected, []int64{r1, r2}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE generated_articles SET created_at = datetime('now', '-8 days') WHERE id = ?`, old); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListStoryCandidates(time.Now().Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]StoryCandidate{}
	for _, c := range got {
		ids[c.ID] = c
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates %+v, want the rejected and draft articles", len(got), got)
	}
	if _, ok := ids[old]; ok {
		t.Error("an article older than the window was returned")
	}
	if c := ids[rejected]; c.Status != StatusRejected || len(c.RawTitles) != 2 || c.TLDR != "tldr rejected one" {
		t.Errorf("rejected candidate = %+v, want status rejected, 2 raw titles and its TL;DR", c)
	}
	if c := ids[draft]; len(c.RawTitles) != 0 {
		t.Errorf("draft candidate raw titles = %v, want none", c.RawTitles)
	}
}

// Reopening an existing database must not fail on the covered_raws migration,
// and must keep its rows.
func TestCoveredRaws_MigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	src := createTestSource(t, d)
	r := createRaw(t, d, src, "r")
	art := createArticle(t, d, "a", StatusPending)
	if _, err := d.CoverRawArticles(art, []int64{r}, CoverReasonStoryMatch); err != nil {
		t.Fatal(err)
	}
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer d.Close()
	if got, ok, err := d.ArticleForRawIDs([]int64{r}); err != nil || !ok || got != art {
		t.Errorf("after reopen: %d, %v, %v; want %d", got, ok, err, art)
	}
}
