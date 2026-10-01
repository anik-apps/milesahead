package db

import (
	"fmt"
	"testing"
)

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

// A tag repeated in tags_json counts once: neither the topic match nor the
// shared-tag count is inflated by it.
func TestListRelatedArticles_DuplicateTagCountsOnce(t *testing.T) {
	d := openTestDB(t)
	self := relatedFixture(t, d, "self", `["hotels","airlines"]`, "", StatusApproved, 1)
	relatedFixture(t, d, "dup", `["hotels","hotels","hotels"]`, "", StatusApproved, 2) // newer, one distinct shared tag
	relatedFixture(t, d, "both", `["hotels","airlines"]`, "", StatusApproved, 30)      // older, two shared tags

	got, err := d.ListRelatedArticles(self, "hotels", []string{"hotels", "airlines"}, "2026-09-23", 3)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, a := range got {
		slugs = append(slugs, a.Slug)
	}
	if want := "both,dup"; fmt.Sprint(slugs) != fmt.Sprint([]string{"both", "dup"}) {
		t.Errorf("related = %v, want %s", slugs, want)
	}
}
