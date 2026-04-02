package db

import (
	"testing"
	"time"
)

func TestListPublishedArticlesByTag(t *testing.T) {
	d := openTestDB(t)

	// Insert articles with different tags.
	a1 := &GeneratedArticle{
		Title: "Credit Card Deal", Slug: "cc-deal-1", Content: "content",
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `["credit-cards", "points-deals"]`,
	}
	a2 := &GeneratedArticle{
		Title: "Airline News", Slug: "airline-news-1", Content: "content",
		Type: "summary", Status: StatusApproved, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `["airlines"]`,
	}
	a3 := &GeneratedArticle{
		Title: "Hotel Deal", Slug: "hotel-deal-1", Content: "content",
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `["hotels", "points-deals"]`,
	}
	// Draft article should not appear in results.
	a4 := &GeneratedArticle{
		Title: "Draft Article", Slug: "draft-1", Content: "content",
		Type: "deal", Status: StatusDraft, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `["credit-cards"]`,
	}

	for _, a := range []*GeneratedArticle{a1, a2, a3, a4} {
		id, err := d.CreateGeneratedArticle(a)
		if err != nil {
			t.Fatalf("CreateGeneratedArticle(%q): %v", a.Title, err)
		}
		// Set published_at for approved articles.
		if a.Status == StatusApproved {
			d.db.Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, time.Now().UTC(), id)
		}
	}

	// Query by "credit-cards" — should return only a1 (not draft a4).
	results, err := d.ListPublishedArticlesByTag("credit-cards", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesByTag(credit-cards): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 article for credit-cards, got %d", len(results))
	}
	if len(results) > 0 && results[0].Title != "Credit Card Deal" {
		t.Errorf("expected 'Credit Card Deal', got %q", results[0].Title)
	}

	// Query by "points-deals" — should return a1 and a3.
	results, err = d.ListPublishedArticlesByTag("points-deals", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesByTag(points-deals): %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 articles for points-deals, got %d", len(results))
	}

	// Query by "airlines" — should return only a2.
	results, err = d.ListPublishedArticlesByTag("airlines", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesByTag(airlines): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 article for airlines, got %d", len(results))
	}

	// Query by non-existent tag — should return empty.
	results, err = d.ListPublishedArticlesByTag("bank-bonuses", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesByTag(bank-bonuses): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 articles for bank-bonuses, got %d", len(results))
	}
}

func TestListApprovedArticlesSince(t *testing.T) {
	d := openTestDB(t)

	now := time.Now().UTC()

	// Insert an approved article with a recent published_at.
	a1 := &GeneratedArticle{
		Title: "Recent Article", Slug: "recent-1", Content: "content",
		Type: "summary", Status: StatusApproved, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `[]`,
	}
	id1, err := d.CreateGeneratedArticle(a1)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}
	d.db.Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, now.Add(-1*time.Hour), id1)

	// Insert an approved article with an old published_at.
	a2 := &GeneratedArticle{
		Title: "Old Article", Slug: "old-1", Content: "content",
		Type: "summary", Status: StatusApproved, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `[]`,
	}
	id2, err := d.CreateGeneratedArticle(a2)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}
	d.db.Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, now.Add(-48*time.Hour), id2)

	// Insert a draft article (should not appear).
	a3 := &GeneratedArticle{
		Title: "Draft Article", Slug: "draft-2", Content: "content",
		Type: "summary", Status: StatusDraft, SourcesJSON: "[]",
		RevisionNumber: 1, TagsJSON: `[]`,
	}
	id3, err := d.CreateGeneratedArticle(a3)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}
	d.db.Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, now.Add(-30*time.Minute), id3)

	// Query since 24 hours ago — should return only a1.
	since := now.Add(-24 * time.Hour)
	results, err := d.ListApprovedArticlesSince(since)
	if err != nil {
		t.Fatalf("ListApprovedArticlesSince: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 article, got %d", len(results))
	}
	if len(results) > 0 && results[0].Title != "Recent Article" {
		t.Errorf("expected 'Recent Article', got %q", results[0].Title)
	}

	// Query since 72 hours ago — should return both approved articles (a1 and a2).
	since = now.Add(-72 * time.Hour)
	results, err = d.ListApprovedArticlesSince(since)
	if err != nil {
		t.Fatalf("ListApprovedArticlesSince: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 articles, got %d", len(results))
	}
}
