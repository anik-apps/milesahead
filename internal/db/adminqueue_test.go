package db

import (
	"path/filepath"
	"testing"
	"time"
)

func openQueueDB(t *testing.T) *Database {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestLatestVerificationsAndSources(t *testing.T) {
	d := openQueueDB(t)
	src, _ := d.CreateSource(&Source{Name: "Feed One", URL: "https://a.example", Active: true})
	raw, _ := d.CreateRawArticle(&RawArticle{SourceID: src, Title: "Post", URL: "https://a.example/p", Content: "c", PublishedAt: time.Now(), Hash: "h"})
	id, _ := d.CreateGeneratedArticle(&GeneratedArticle{Title: "A", Slug: "a", Content: "c", Type: "deal", Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 2})
	other, _ := d.CreateGeneratedArticle(&GeneratedArticle{Title: "B", Slug: "b", Content: "c", Type: "deal", Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1})
	d.LinkArticleSources(id, []int64{raw})
	d.CreateVerificationResult(&VerificationResult{GeneratedArticleID: id, RevisionNumber: 1, Score: 50, IssuesJSON: "[]"})
	d.CreateVerificationResult(&VerificationResult{GeneratedArticleID: id, RevisionNumber: 2, Score: 90, IssuesJSON: "[]"})

	vs, err := d.LatestVerifications([]int64{id, other})
	if err != nil {
		t.Fatal(err)
	}
	if vs[id].Score != 90 {
		t.Errorf("latest score = %d, want revision 2's 90", vs[id].Score)
	}
	if _, ok := vs[other]; ok {
		t.Error("an article with no critic result should be absent")
	}
	ls, err := d.LinkedSourcesForArticles([]int64{id, other})
	if err != nil {
		t.Fatal(err)
	}
	if len(ls[id]) != 1 || ls[id][0].SourceName != "Feed One" || ls[id][0].URL != "https://a.example/p" {
		t.Errorf("sources = %+v", ls[id])
	}
}

func TestNavCounts(t *testing.T) {
	d := openQueueDB(t)
	page := &ReferencePage{Slug: "c", Title: "C", Category: "travel", Issuer: "X", SectionsJSON: "[]", RecentChangesJSON: "[]",
		PendingUpdatesJSON: `[{"id":1},{"id":2}]`, FlaggedFieldsJSON: `[{"field":"slug"}]`, KeywordsJSON: "[]", Status: "published"}
	if _, err := d.CreateReferencePage(page); err != nil {
		t.Fatal(err)
	}
	if n, err := d.CardWorkCount(); err != nil || n != 3 {
		t.Errorf("CardWorkCount = %d, %v; want 3", n, err)
	}

	live, _ := d.CreateSource(&Source{Name: "Live", URL: "https://l.example", Active: true})
	d.CreateSource(&Source{Name: "Silent", URL: "https://s.example", Active: true})
	d.CreateSource(&Source{Name: "Paused", URL: "https://p.example", Active: false})
	d.CreateRawArticle(&RawArticle{SourceID: live, Title: "t", URL: "u", Content: "c", PublishedAt: time.Now(), Hash: "h1"})
	if n, err := d.StaleSourceCount(time.Now().Add(-72 * time.Hour)); err != nil || n != 1 {
		t.Errorf("StaleSourceCount = %d, %v; want 1 (the silent active one)", n, err)
	}

	d.CreateNewsletter(&Newsletter{Date: "2026-09-26", Status: "draft"})
	d.CreateNewsletter(&Newsletter{Date: "2026-10-03", Status: "auto_approved"})
	d.CreateNewsletter(&Newsletter{Date: "2026-10-10", Status: "draft"})
	nl, err := d.UpcomingNewsletter("2026-10-01")
	if err != nil || nl == nil || nl.Date != "2026-10-03" {
		t.Errorf("UpcomingNewsletter = %+v, %v; want the Oct 3 issue", nl, err)
	}
	if nl, _ := d.UpcomingNewsletter("2026-10-11"); nl != nil {
		t.Errorf("no issue is upcoming after Oct 10, got %+v", nl)
	}
}
