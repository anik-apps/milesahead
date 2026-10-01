package db

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestListPublishedArticlesFiltered_ByTag(t *testing.T) {
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
	results, err := d.ListPublishedArticlesFiltered("", "credit-cards", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesFiltered(tag credit-cards): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 article for credit-cards, got %d", len(results))
	}
	if len(results) > 0 && results[0].Title != "Credit Card Deal" {
		t.Errorf("expected 'Credit Card Deal', got %q", results[0].Title)
	}

	// Query by "points-deals" — should return a1 and a3.
	results, err = d.ListPublishedArticlesFiltered("", "points-deals", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesFiltered(tag points-deals): %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 articles for points-deals, got %d", len(results))
	}

	// Query by "airlines" — should return only a2.
	results, err = d.ListPublishedArticlesFiltered("", "airlines", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesFiltered(tag airlines): %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 article for airlines, got %d", len(results))
	}

	// Query by non-existent tag — should return empty.
	results, err = d.ListPublishedArticlesFiltered("", "bank-bonuses", 10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticlesFiltered(tag bank-bonuses): %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 articles for bank-bonuses, got %d", len(results))
	}
}

// Type and tag apply together; either alone, or neither, still works.
func TestListPublishedArticlesFiltered_TypeAndTag(t *testing.T) {
	d := openTestDB(t)

	seed := []*GeneratedArticle{
		{Title: "Hotel News", Slug: "hotel-news", Type: "summary", Status: StatusApproved, TagsJSON: `["hotels"]`},
		{Title: "Hotel Deal", Slug: "hotel-deal", Type: "deal", Status: StatusApproved, TagsJSON: `["hotels", "points-deals"]`},
		{Title: "Airline News", Slug: "airline-news", Type: "summary", Status: StatusApproved, TagsJSON: `["airlines"]`},
		{Title: "Draft Hotel News", Slug: "draft-hotel-news", Type: "summary", Status: StatusDraft, TagsJSON: `["hotels"]`},
		{Title: "Broken Tags", Slug: "broken-tags", Type: "summary", Status: StatusApproved, TagsJSON: `not json`},
		// Valid JSON but not an array: json_each would expand these into a
		// "hotels" value, so they must not match the hotels filter.
		{Title: "String Tags", Slug: "string-tags", Type: "deal", Status: StatusApproved, TagsJSON: `"hotels"`},
		{Title: "Object Tags", Slug: "object-tags", Type: "deal", Status: StatusApproved, TagsJSON: `{"a": "hotels"}`},
	}
	for i, a := range seed {
		a.Content, a.SourcesJSON, a.RevisionNumber = "content", "[]", 1
		id, err := d.CreateGeneratedArticle(a)
		if err != nil {
			t.Fatalf("CreateGeneratedArticle(%q): %v", a.Title, err)
		}
		if _, err := d.db.Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Duration(i)*time.Hour), id); err != nil {
			t.Fatal(err)
		}
	}

	titles := func(articleType, tag string) []string {
		t.Helper()
		got, err := d.ListPublishedArticlesFiltered(articleType, tag, 20, 0)
		if err != nil {
			t.Fatalf("ListPublishedArticlesFiltered(%q, %q): %v", articleType, tag, err)
		}
		var out []string
		for _, a := range got {
			out = append(out, a.Title)
		}
		return out
	}
	for _, c := range []struct {
		articleType, tag string
		want             []string
	}{
		{"summary", "hotels", []string{"Hotel News"}},
		{"deal", "hotels", []string{"Hotel Deal"}},
		{"", "hotels", []string{"Hotel News", "Hotel Deal"}},
		{"summary", "", []string{"Hotel News", "Airline News", "Broken Tags"}},
		{"", "", []string{"Hotel News", "Hotel Deal", "Airline News", "Broken Tags", "String Tags", "Object Tags"}},
		{"deal", "airlines", nil},
		{"nonsense", "", nil},
	} {
		if got := titles(c.articleType, c.tag); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("type %q, tag %q: got %q, want %q", c.articleType, c.tag, got, c.want)
		}
	}

	// The limit still caps the page.
	if got, err := d.ListPublishedArticlesFiltered("", "", 2, 0); err != nil || len(got) != 2 {
		t.Errorf("limit 2: got %d articles, err %v", len(got), err)
	}
}

// Only types with an approved article are listed.
func TestListPublishedArticleTypes(t *testing.T) {
	d := openTestDB(t)
	for _, a := range []*GeneratedArticle{
		{Title: "A", Slug: "a", Type: "deal", Status: StatusApproved},
		{Title: "B", Slug: "b", Type: "deal", Status: StatusApproved},
		{Title: "C", Slug: "c", Type: "summary", Status: StatusApproved},
		{Title: "D", Slug: "d", Type: "roundup", Status: StatusPending},
	} {
		a.Content, a.SourcesJSON, a.RevisionNumber, a.TagsJSON = "content", "[]", 1, "[]"
		if _, err := d.CreateGeneratedArticle(a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.ListPublishedArticleTypes()
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "deal,summary" {
		t.Errorf("got %q, want deal and summary", got)
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
