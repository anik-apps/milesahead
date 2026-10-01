// backfill-scouts-take generates Scout's Take for approved articles that are missing it.
// Usage: go run ./cmd/backfill-scouts-take [-db path] [-api-key key] [-api-url url] [-persona name] [-model name] [-dry-run]
//
// Each take goes through ai.GenerateScoutsTake, the call the enrichment
// reconciler makes, so a take the site would refuse there (empty, or citing a
// numbered source) is refused here too: it is logged and the article skipped.
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

func main() {
	dbPath := flag.String("db", "./data/milesahead.db", "path to SQLite database")
	apiKey := flag.String("api-key", "", "NVIDIA/HuggingFace API key")
	apiURL := flag.String("api-url", "", "API base URL (optional, uses default if empty)")
	persona := flag.String("persona", "Scout", "persona name for Scout's Take")
	model := flag.String("model", "meta/llama-3.1-8b-instruct", "model name, or a comma-separated fallback chain")
	dryRun := flag.Bool("dry-run", false, "print which articles would be updated without making changes")
	flag.Parse()

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	articles, err := database.ListArticlesByStatus(db.StatusApproved)
	if err != nil {
		log.Fatalf("failed to list approved articles: %v", err)
	}

	// Filter to articles missing Scout's Take.
	var missing []db.GeneratedArticle
	for _, a := range articles {
		if a.ScoutsTake == "" {
			missing = append(missing, a)
		}
	}

	fmt.Printf("Found %d approved articles, %d missing Scout's Take.\n", len(articles), len(missing))

	if *dryRun {
		fmt.Println("Dry-run mode — no changes will be made.")
		for _, a := range missing {
			fmt.Printf("  [would update] id=%d slug=%s title=%q\n", a.ID, a.Slug, a.Title)
		}
		return
	}

	if *apiKey == "" {
		log.Fatal("-api-key is required when not using -dry-run")
	}

	hfClient := ai.NewHFClient(*apiKey, 3)
	if *apiURL != "" {
		hfClient.BaseURL = *apiURL
	}

	succeeded, failed, skipped := backfill(database, hfClient, ai.SplitModels(*model), *persona, missing, time.Second)
	fmt.Printf("\nDone: %d succeeded, %d failed, %d skipped.\n", succeeded, failed, skipped)
}

// backfill writes a Scout's Take for each article. A take is produced by
// ai.GenerateScoutsTake and nothing else, so it passes the same checks as one
// written by the enrichment reconciler. It returns how many takes were
// written, how many could not be saved, and how many articles were skipped
// because no usable take came back.
func backfill(database *db.Database, client ai.HFClientInterface, models []string, persona string,
	articles []db.GeneratedArticle, pause time.Duration) (succeeded, failed, skipped int) {
	for i, a := range articles {
		if i > 0 {
			time.Sleep(pause)
		}

		take, err := ai.GenerateScoutsTake(client, models, persona, a.Title, a.TLDR, a.Content)
		if err != nil {
			log.Printf("WARN no usable Scout's Take for id=%d slug=%s: %v; skipping", a.ID, a.Slug, err)
			skipped++
			continue
		}

		_, dbErr := database.DB().Exec(
			`UPDATE generated_articles SET scouts_take = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			take, a.ID,
		)
		if dbErr != nil {
			log.Printf("ERROR updating db for id=%d slug=%s: %v; skipping", a.ID, a.Slug, dbErr)
			failed++
			continue
		}

		fmt.Printf("  [ok] id=%d slug=%s\n", a.ID, a.Slug)
		succeeded++
	}
	return succeeded, failed, skipped
}
