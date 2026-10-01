package db

import (
	"fmt"
	"testing"
	"time"
)

// The article page lists its sources by outlet, in the order the article was
// written from: by source, then by raw article -- not by raw id alone.
func TestListArticleSourceLinks_OutletsInGenerationOrder(t *testing.T) {
	d := openTestDB(t)
	first, err := d.CreateSource(&Source{Name: "One Mile at a Time", URL: "https://a.example", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateSource(&Source{Name: "Doctor of Credit", URL: "https://b.example", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	var raws []int64
	for i, src := range []int64{second, first, second} {
		id, err := d.CreateRawArticle(&RawArticle{SourceID: src, Title: fmt.Sprint("story ", i),
			URL: fmt.Sprintf("https://x.example/%d", i), Content: "c", PublishedAt: time.Now(), Hash: fmt.Sprint("h", i)})
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, id)
	}
	art, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "t", Content: "c", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.LinkArticleSources(art, raws); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListArticleSourceLinks(art)
	if err != nil {
		t.Fatal(err)
	}
	want := []ArticleSourceLink{
		{"One Mile at a Time", "story 1", "https://x.example/1"},
		{"Doctor of Credit", "story 0", "https://x.example/0"},
		{"Doctor of Credit", "story 2", "https://x.example/2"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sources = %v, want %v", got, want)
	}

	none, err := d.ListArticleSourceLinks(art + 100)
	if err != nil || len(none) != 0 {
		t.Errorf("article with no sources: %v, %v", none, err)
	}
}

// relatedFixture publishes an article with the given tags and end date, newest
// last, and returns its id.
func relatedFixture(t *testing.T, d *Database, slug, tagsJSON, expiresOn string, status string, minutesAgo int) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: slug, Slug: slug, Content: "c", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1, TagsJSON: tagsJSON})
	if err != nil {
		t.Fatal(err)
	}
	if status == StatusApproved {
		if err := d.UpdateArticleStatus(id, StatusApproved); err != nil {
			t.Fatal(err)
		}
	}
	var exp any
	if expiresOn != "" {
		exp = expiresOn
	}
	if _, err := d.DB().Exec(`UPDATE generated_articles SET expires_on = ?, published_at = datetime('now', ?) WHERE id = ?`,
		exp, fmt.Sprintf("-%d minutes", minutesAgo), id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Related stories: approved, other than the article itself, sharing a tag;
// live offers before ended ones, then the article's topic, then more shared
// tags, then the newest.
func TestListRelatedArticles_Rules(t *testing.T) {
	d := openTestDB(t)
	const today = "2026-09-23"
	self := relatedFixture(t, d, "self", `["credit-cards","transfer-bonuses"]`, "", StatusApproved, 1)
	relatedFixture(t, d, "ended-topic", `["credit-cards"]`, "2026-09-19", StatusApproved, 2)
	relatedFixture(t, d, "live-other-tag", `["transfer-bonuses"]`, "", StatusApproved, 3)
	relatedFixture(t, d, "live-topic-old", `["credit-cards"]`, "2026-09-30", StatusApproved, 50)
	relatedFixture(t, d, "live-topic-new", `["credit-cards"]`, "2026-09-23", StatusApproved, 10) // last day is live
	relatedFixture(t, d, "live-both", `["credit-cards","transfer-bonuses"]`, "", StatusApproved, 60)
	relatedFixture(t, d, "unrelated", `["hotels"]`, "", StatusApproved, 4)
	relatedFixture(t, d, "pending", `["credit-cards"]`, "", StatusPending, 5)
	relatedFixture(t, d, "bad-json", `not json`, "", StatusApproved, 6)

	got, err := d.ListRelatedArticles(self, "credit-cards", []string{"credit-cards", "transfer-bonuses"}, today, 10)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, a := range got {
		slugs = append(slugs, a.Slug)
	}
	want := []string{"live-both", "live-topic-new", "live-topic-old", "live-other-tag", "ended-topic"}
	if fmt.Sprint(slugs) != fmt.Sprint(want) {
		t.Errorf("related = %v, want %v", slugs, want)
	}

	top, err := d.ListRelatedArticles(self, "credit-cards", []string{"credit-cards", "transfer-bonuses"}, today, 3)
	if err != nil || len(top) != 3 {
		t.Fatalf("limit 3: got %d, %v", len(top), err)
	}
	if none, err := d.ListRelatedArticles(self, "", nil, today, 3); err != nil || len(none) != 0 {
		t.Errorf("no tags should find nothing: %v, %v", none, err)
	}
}
