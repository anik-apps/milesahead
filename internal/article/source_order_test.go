package article

import (
	"fmt"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// TestLoadSourceArticles_GenerationOrder: a revision presents an article's
// sources in the order the scan wrote it from (source id, then raw id), not
// raw id alone, so a "Source N" carried over from the original text maps to
// the same outlet the generator meant.
func TestLoadSourceArticles_GenerationOrder(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	first, err := database.CreateSource(&db.Source{Name: "One Mile at a Time", URL: "https://a.example", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := database.CreateSource(&db.Source{Name: "Loyalty Lobby", URL: "https://b.example", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	// The later source's article was scraped first, so it has the lower raw id.
	var rawIDs []int64
	for i, src := range []int64{second, first, second} {
		id, err := database.CreateRawArticle(&db.RawArticle{SourceID: src, Title: fmt.Sprint("raw ", i),
			URL: fmt.Sprintf("https://x.example/%d", i), Content: "c", PublishedAt: time.Now(),
			Hash: fmt.Sprintf("order-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		rawIDs = append(rawIDs, id)
	}
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: "T", Slug: "t-order",
		Content: "c", Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.LinkArticleSources(articleID, rawIDs); err != nil {
		t.Fatal(err)
	}

	sources, err := mgr.loadSourceArticles(articleID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sources {
		got = append(got, s.Name+"/"+s.Title)
	}
	want := []string{"One Mile at a Time/raw 1", "Loyalty Lobby/raw 0", "Loyalty Lobby/raw 2"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("sources = %v, want %v", got, want)
	}
}
