package cardupdate

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestMatchCards(t *testing.T) {
	t.Parallel()

	article := &db.GeneratedArticle{
		ID:      1,
		Title:   "Chase Sapphire Preferred 80K Bonus Is Back",
		Content: "The Chase Sapphire Preferred card is offering an 80,000 point signup bonus. The Chase Sapphire Reserve also sees changes.",
	}

	pages := []db.ReferencePage{
		{
			ID:           1,
			Title:        "Chase Sapphire Preferred",
			KeywordsJSON: `["chase sapphire preferred", "csp"]`,
		},
		{
			ID:           2,
			Title:        "Amex Gold Card",
			KeywordsJSON: `["amex gold", "american express gold"]`,
		},
		{
			ID:           3,
			Title:        "Chase Sapphire Reserve",
			KeywordsJSON: `["chase sapphire reserve", "csr"]`,
		},
	}

	matched := matchCards(article, pages, 3)
	if len(matched) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matched))
	}
	if matched[0].ID != 1 {
		t.Errorf("expected first match to be Chase Sapphire Preferred (ID=1), got ID=%d", matched[0].ID)
	}
	if matched[1].ID != 3 {
		t.Errorf("expected second match to be Chase Sapphire Reserve (ID=3), got ID=%d", matched[1].ID)
	}
}

func TestMatchCards_MaxLimit(t *testing.T) {
	t.Parallel()

	article := &db.GeneratedArticle{
		ID:      1,
		Title:   "Big Chase and Amex news",
		Content: "Chase Sapphire Preferred, Chase Sapphire Reserve, Amex Gold, and Amex Platinum all affected.",
	}

	pages := []db.ReferencePage{
		{ID: 1, Title: "Chase Sapphire Preferred", KeywordsJSON: `["chase sapphire preferred"]`},
		{ID: 2, Title: "Chase Sapphire Reserve", KeywordsJSON: `["chase sapphire reserve"]`},
		{ID: 3, Title: "Amex Gold Card", KeywordsJSON: `["amex gold"]`},
		{ID: 4, Title: "Amex Platinum", KeywordsJSON: `["amex platinum"]`},
	}

	matched := matchCards(article, pages, 3)
	if len(matched) != 3 {
		t.Fatalf("expected 3 matches (max limit), got %d", len(matched))
	}
}

func TestMatchCards_NoMatch(t *testing.T) {
	t.Parallel()

	article := &db.GeneratedArticle{
		ID:      1,
		Title:   "Hilton Points Sale",
		Content: "Hilton is running a points sale through May.",
	}

	pages := []db.ReferencePage{
		{ID: 1, Title: "Chase Sapphire Preferred", KeywordsJSON: `["chase sapphire preferred"]`},
	}

	matched := matchCards(article, pages, 3)
	if len(matched) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matched))
	}
}

func TestAppendRecentChange(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Create a reference page.
	page := &db.ReferencePage{
		Slug:               "test-card",
		Title:              "Test Card",
		Category:           "credit-cards",
		Issuer:             "Test Issuer",
		SectionsJSON:       "[]",
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		KeywordsJSON:       `["test card"]`,
		Status:             "published",
	}
	id, err := database.CreateReferencePage(page)
	if err != nil {
		t.Fatalf("failed to create reference page: %v", err)
	}

	// Append a change.
	change := db.RecentChange{
		Date:        "2026-03-31",
		Summary:     "Test article published",
		ArticleID:   42,
		ArticleSlug: "test-article",
	}
	if err := database.AppendRecentChange(id, change); err != nil {
		t.Fatalf("AppendRecentChange failed: %v", err)
	}

	// Verify.
	updated, err := database.GetReferencePage(id)
	if err != nil {
		t.Fatalf("GetReferencePage failed: %v", err)
	}

	var changes []db.RecentChange
	if err := json.Unmarshal([]byte(updated.RecentChangesJSON), &changes); err != nil {
		t.Fatalf("failed to parse recent changes: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected 1 change, got %d", len(changes))
	}
	if changes[0].Summary != "Test article published" {
		t.Errorf("expected summary 'Test article published', got %q", changes[0].Summary)
	}

	// Append 11 more changes to test the max-10 cap.
	for i := 0; i < 11; i++ {
		c := db.RecentChange{
			Date:        "2026-03-31",
			Summary:     "Change " + string(rune('A'+i)),
			ArticleID:   int64(i + 100),
			ArticleSlug: "slug",
		}
		if err := database.AppendRecentChange(id, c); err != nil {
			t.Fatalf("AppendRecentChange iteration %d failed: %v", i, err)
		}
	}

	updated, err = database.GetReferencePage(id)
	if err != nil {
		t.Fatalf("GetReferencePage failed: %v", err)
	}
	if err := json.Unmarshal([]byte(updated.RecentChangesJSON), &changes); err != nil {
		t.Fatalf("failed to parse recent changes: %v", err)
	}
	if len(changes) != 10 {
		t.Fatalf("expected max 10 changes, got %d", len(changes))
	}
}
