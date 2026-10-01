package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// takeClient answers a Scout's Take prompt by the article title it contains.
type takeClient struct {
	byTitle map[string]string
}

func (c *takeClient) Generate(model, prompt string, maxTokens int) (string, error) {
	for title, take := range c.byTitle {
		if strings.Contains(prompt, title) {
			return take, nil
		}
	}
	return "", nil
}

func TestBackfill_RefusesNumberedSourceRefs(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	ids := map[string]int64{}
	for _, title := range []string{"Alpha Bonus", "Bravo Bonus", "Charlie Bonus"} {
		id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: title,
			Slug: strings.ToLower(strings.ReplaceAll(title, " ", "-")), Content: "<p>body</p>",
			Type: "deal", Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1, TLDR: "tldr"})
		if err != nil {
			t.Fatal(err)
		}
		ids[title] = id
	}
	articles, err := database.ListArticlesByStatus(db.StatusApproved)
	if err != nil {
		t.Fatal(err)
	}

	client := &takeClient{byTitle: map[string]string{
		"Alpha Bonus":   "Source 1 is right: transfer now.",
		"Bravo Bonus":   "Transfer now if you fly BA; the 20% bonus is rare.",
		"Charlie Bonus": "   ",
	}}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	succeeded, failed, skipped := backfill(database, client, []string{"m"}, "Scout", articles, 0)
	if succeeded != 1 || failed != 0 || skipped != 2 {
		t.Errorf("succeeded=%d failed=%d skipped=%d; want 1, 0, 2", succeeded, failed, skipped)
	}

	takes := map[string]string{}
	for title, id := range ids {
		a, err := database.GetGeneratedArticle(id)
		if err != nil {
			t.Fatal(err)
		}
		takes[title] = a.ScoutsTake
	}
	if takes["Alpha Bonus"] != "" {
		t.Errorf("a take citing Source 1 was stored: %q", takes["Alpha Bonus"])
	}
	if takes["Charlie Bonus"] != "" {
		t.Errorf("a blank take was stored: %q", takes["Charlie Bonus"])
	}
	if takes["Bravo Bonus"] != "Transfer now if you fly BA; the 20% bonus is rare." {
		t.Errorf("clean take = %q", takes["Bravo Bonus"])
	}
	if !strings.Contains(logs.String(), "refers to a source by number") {
		t.Errorf("refusal not logged:\n%s", logs.String())
	}
}
