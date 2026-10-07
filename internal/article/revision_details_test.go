package article

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// A rewrite stores its own key details in place of the old ones and clears
// the chart, so neither keeps a number the rewrite corrected.
func TestProcessRevision_RefreshesKeyDetailsAndChart(t *testing.T) {
	database := setupTestDB(t)
	mock := &mockHFClient{responses: []string{
		"IHG Select Fee Goes to $95\n\nTLDR: The fee is $95.\n\nKEY_DETAILS_START\n- Annual fee: $95\nKEY_DETAILS_END\n\n<p>The fee rises to $95.</p>",
		`{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}}
	mgr := NewManager(database, ai.NewPipeline(ai.NewGenerator(mock, "m", "", nil), ai.NewCritic(mock, "m"), 80))

	srcID, err := database.CreateSource(&db.Source{Name: "S", URL: "https://example.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	rawID, err := database.CreateRawArticle(&db.RawArticle{SourceID: srcID, Title: "Raw", URL: "https://example.com/r",
		Content: "The fee rises to $95.", PublishedAt: time.Now(), Hash: "refresh-details-hash"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: "IHG Select Fee Goes to $99", Slug: "t-refresh",
		Content: "<p>The fee rises to $99.</p>", Type: "news", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		KeyDetails: `[{"Label":"Annual fee","Value":"$99"}]`})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.LinkArticleSources(id, []int64{rawID}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE generated_articles SET infographic_json = ? WHERE id = ?`,
		`{"kind":"change","headline":"Fee","rows":[{"label":"Old","value":"$49"},{"label":"New","value":"$99"}]}`, id); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RequestRevision(id, "The new fee is $95, not $99", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ProcessRevision(id); err != nil {
		t.Fatal(err)
	}

	var kd, chart string
	if err := database.DB().QueryRow(`SELECT key_details, COALESCE(infographic_json,'') FROM generated_articles WHERE id = ?`, id).
		Scan(&kd, &chart); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(kd, `"Value":"$95"`) || strings.Contains(kd, "$99") {
		t.Errorf("key details = %s; want the rewrite's", kd)
	}
	if chart != "" {
		t.Errorf("chart = %s; want it cleared to be drawn again", chart)
	}
}
