// seed-cards loads credit card reference pages from cards.json into the database.
// Usage: go run ./cmd/seed-cards [-db path/to/milesahead.db] [-force]
//
// By default, skips cards whose slug already exists. Use -force to overwrite.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite"
)

type Section struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type Card struct {
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Category      string    `json:"category"`
	Issuer        string    `json:"issuer"`
	Sections      []Section `json:"sections"`
	ScoutsVerdict string    `json:"scouts_verdict"`
	Keywords      []string  `json:"keywords"`
	ImageURL      string    `json:"image_url"`
	ApplyURL      string    `json:"apply_url"`
	Status        string    `json:"status"`
}

func main() {
	dbPath := flag.String("db", "./data/milesahead.db", "path to SQLite database")
	force := flag.Bool("force", false, "overwrite existing cards")
	flag.Parse()

	// Find cards.json relative to this source file.
	_, thisFile, _, _ := runtime.Caller(0)
	jsonPath := filepath.Join(filepath.Dir(thisFile), "cards.json")

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		log.Fatalf("Failed to read cards.json: %v", err)
	}

	var cards []Card
	if err := json.Unmarshal(data, &cards); err != nil {
		log.Fatalf("Failed to parse cards.json: %v", err)
	}

	db, err := sql.Open("sqlite", *dbPath+"?_journal_mode=WAL")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	inserted, updated, skipped := 0, 0, 0

	for _, card := range cards {
		sectionsJSON, _ := json.Marshal(card.Sections)
		keywordsJSON, _ := json.Marshal(card.Keywords)

		// Check if card exists.
		var existingID int64
		err := db.QueryRow("SELECT id FROM reference_pages WHERE slug = ?", card.Slug).Scan(&existingID)

		if err == sql.ErrNoRows {
			// Insert new card.
			_, err = db.Exec(
				`INSERT INTO reference_pages (slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, status)
				 VALUES (?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, ?, ?)`,
				card.Slug, card.Title, card.Category, card.Issuer,
				string(sectionsJSON), card.ScoutsVerdict, string(keywordsJSON),
				card.ImageURL, card.ApplyURL, card.Status,
			)
			if err != nil {
				log.Printf("ERROR inserting %s: %v", card.Slug, err)
				continue
			}
			inserted++
			fmt.Printf("  + %s\n", card.Title)
		} else if err == nil && *force {
			// Update existing card.
			_, err = db.Exec(
				`UPDATE reference_pages SET title = ?, category = ?, issuer = ?, sections_json = ?, scouts_verdict = ?, keywords_json = ?, image_url = ?, apply_url = ?, status = ?, updated_at = CURRENT_TIMESTAMP
				 WHERE id = ?`,
				card.Title, card.Category, card.Issuer,
				string(sectionsJSON), card.ScoutsVerdict, string(keywordsJSON),
				card.ImageURL, card.ApplyURL, card.Status, existingID,
			)
			if err != nil {
				log.Printf("ERROR updating %s: %v", card.Slug, err)
				continue
			}
			updated++
			fmt.Printf("  ~ %s (updated)\n", card.Title)
		} else {
			skipped++
		}
	}

	fmt.Printf("\nDone: %d inserted, %d updated, %d skipped (already exist)\n", inserted, updated, skipped)
	if skipped > 0 && !*force {
		fmt.Println("Use -force to overwrite existing cards.")
	}
}
