package db

import (
	"testing"
)

func TestSyncArticleFTS(t *testing.T) {
	d := openTestDB(t)

	// Create an article.
	a := &GeneratedArticle{
		Title:            "Test Article About <b>Travel</b> Rewards",
		Slug:             "test-travel-rewards",
		Content:          "<p>Earn points on every purchase with this card.</p>",
		Type:             "article",
		Status:           StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		TLDR:             "Great travel card for beginners",
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	}
	id, err := d.CreateGeneratedArticle(a)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}

	// Verify FTS was populated (CreateGeneratedArticle calls SyncArticleFTS).
	var count int
	err = d.db.QueryRow(`SELECT COUNT(*) FROM articles_fts WHERE rowid = ?`, id).Scan(&count)
	if err != nil {
		t.Fatalf("query articles_fts: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 FTS entry, got %d", count)
	}
}

func TestSearchArticles_StatusFilter(t *testing.T) {
	d := openTestDB(t)

	// Create an approved article.
	approved := &GeneratedArticle{
		Title:            "Chase Sapphire Preferred Review",
		Slug:             "chase-sapphire-preferred",
		Content:          "The Chase Sapphire Preferred is a top travel card.",
		Type:             "article",
		Status:           StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		TLDR:             "Great mid-tier travel card",
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	}
	_, err := d.CreateGeneratedArticle(approved)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle (approved): %v", err)
	}

	// Create a draft article with similar content.
	draft := &GeneratedArticle{
		Title:            "Chase Freedom Unlimited Review",
		Slug:             "chase-freedom-unlimited",
		Content:          "The Chase Freedom Unlimited offers great cash back.",
		Type:             "article",
		Status:           StatusDraft,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		TLDR:             "Solid cash back card from Chase",
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	}
	_, err = d.CreateGeneratedArticle(draft)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle (draft): %v", err)
	}

	// Search for "Chase" — should only return the approved article.
	results, err := d.SearchArticles(`"Chase"`, "", "2026-01-01", 10, 0)
	if err != nil {
		t.Fatalf("SearchArticles: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	if len(results) > 0 && results[0].Slug != "chase-sapphire-preferred" {
		t.Errorf("expected slug chase-sapphire-preferred, got %s", results[0].Slug)
	}
}

func TestSyncCardFTS(t *testing.T) {
	d := openTestDB(t)

	page := &ReferencePage{
		Slug:               "amex-gold",
		Title:              "Amex Gold Card",
		Category:           "travel",
		Issuer:             "American Express",
		SectionsJSON:       `[{"key":"overview","title":"Overview","content":"<p>Great dining card</p>"}]`,
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		ScoutsVerdict:      "Top pick for dining rewards",
		KeywordsJSON:       `["amex","gold"]`,
		ImageURL:           "",
		ApplyURL:           "",
		Status:             "published",
	}
	id, err := d.CreateReferencePage(page)
	if err != nil {
		t.Fatalf("CreateReferencePage: %v", err)
	}

	// Verify FTS was populated.
	var count int
	err = d.db.QueryRow(`SELECT COUNT(*) FROM cards_fts WHERE rowid = ?`, id).Scan(&count)
	if err != nil {
		t.Fatalf("query cards_fts: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 FTS entry, got %d", count)
	}

	// Search should find it.
	results, err := d.SearchCards(`"dining"`, "", 10, 0)
	if err != nil {
		t.Fatalf("SearchCards: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
}

func TestDeleteArticleFTS(t *testing.T) {
	d := openTestDB(t)

	a := &GeneratedArticle{
		Title:            "Article to Delete",
		Slug:             "article-to-delete",
		Content:          "This article will be removed from FTS.",
		Type:             "article",
		Status:           StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		TLDR:             "Temporary article",
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	}
	id, err := d.CreateGeneratedArticle(a)
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}

	// Delete from FTS.
	if err := d.DeleteArticleFTS(id); err != nil {
		t.Fatalf("DeleteArticleFTS: %v", err)
	}

	// Verify it's gone.
	var count int
	err = d.db.QueryRow(`SELECT COUNT(*) FROM articles_fts WHERE rowid = ?`, id).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 FTS entries after delete, got %d", count)
	}
}
