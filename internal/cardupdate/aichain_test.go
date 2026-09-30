package cardupdate

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// chainClient answers from live, fails every model in gone with a 410, and
// records the order models were called in.
type chainClient struct {
	calls []string
	gone  map[string]bool
	live  string
}

func (c *chainClient) Generate(model, _ string, _ int) (string, error) {
	c.calls = append(c.calls, model)
	if c.gone[model] {
		return "", &ai.APIError{StatusCode: 410, Model: model, Body: "end of life"}
	}
	return c.live, nil
}

// TestDetectAndDraft_FallsOverARetiredModel: detection used to name one model
// (the critic), so its retirement failed every section check and no card page
// was offered an update.
func TestDetectAndDraft_FallsOverARetiredModel(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	sections, _ := json.Marshal([]db.ReferenceSection{
		{Key: "signup_bonus", Title: "Sign-up bonus", Content: "<p>60,000 points</p>"},
	})
	pageID, err := database.CreateReferencePage(&db.ReferencePage{
		Slug: "sapphire-preferred", Title: "Sapphire Preferred", Category: "credit-cards",
		Issuer: "Chase", SectionsJSON: string(sections), RecentChangesJSON: "[]",
		PendingUpdatesJSON: "[]", KeywordsJSON: `["sapphire preferred"]`, Status: "published",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Sapphire Preferred bonus rises to 100,000 points", Slug: "sp-100k",
		Content: "The Sapphire Preferred offer is now 100,000 points.", Type: "deal",
		Status: db.StatusDraft, SourcesJSON: "[]",
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}

	client := &chainClient{
		gone: map[string]bool{"retired": true},
		live: `{"updated_content": "<p>100,000 points</p>", "summary": "Bonus raised"}`,
	}
	if err := NewDetector(database, client, []string{"retired", "live"}).
		DetectAndDraft(context.Background(), articleID); err != nil {
		t.Fatalf("DetectAndDraft: %v", err)
	}

	if calls := strings.Join(client.calls, ","); calls != "retired,live" {
		t.Errorf("calls = %s; want retired then live", calls)
	}
	page, err := database.GetReferencePage(pageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	var pending []db.PendingUpdate
	if err := json.Unmarshal([]byte(page.PendingUpdatesJSON), &pending); err != nil {
		t.Fatalf("pending updates: %v", err)
	}
	if len(pending) != 1 || pending[0].ProposedContent != "<p>100,000 points</p>" {
		t.Errorf("pending updates = %+v; want the fallback model's draft", pending)
	}
}
