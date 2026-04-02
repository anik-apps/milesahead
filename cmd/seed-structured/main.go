// seed-structured populates structured comparison data (earning rates, credits,
// benefits, protections, transfer partners) for reference pages from a JSON file.
// Usage: go run ./cmd/seed-structured [-db path] [-force] [-dry-run]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

type cardData struct {
	Slug                string          `json:"slug"`
	AnnualFeeAmount     int             `json:"annual_fee_amount"`
	AnnualFeeNote       string          `json:"annual_fee_note"`
	WelcomeBonusText    string          `json:"welcome_bonus_text"`
	EarningRates        json.RawMessage `json:"earning_rates"`
	Credits             json.RawMessage `json:"credits"`
	Benefits            json.RawMessage `json:"benefits"`
	Protections         json.RawMessage `json:"protections"`
	TransferPartners    json.RawMessage `json:"transfer_partners"`
}

func main() {
	dbPath := flag.String("db", "./data/milesahead.db", "path to SQLite database")
	force := flag.Bool("force", false, "overwrite existing structured data")
	dryRun := flag.Bool("dry-run", false, "preview without writing")
	flag.Parse()

	// Find structured-data.json next to binary or source file
	exe, _ := os.Executable()
	jsonPath := filepath.Join(filepath.Dir(exe), "structured-data.json")
	if _, err := os.Stat(jsonPath); err != nil {
		_, thisFile, _, _ := runtime.Caller(0)
		jsonPath = filepath.Join(filepath.Dir(thisFile), "structured-data.json")
	}

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		log.Fatalf("Failed to read structured-data.json: %v", err)
	}

	var cards []cardData
	if err := json.Unmarshal(raw, &cards); err != nil {
		log.Fatalf("Failed to parse JSON: %v", err)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	updated, skipped, notFound := 0, 0, 0

	for _, c := range cards {
		page, err := database.GetReferencePageBySlug(c.Slug)
		if err != nil {
			fmt.Printf("  ? %s — not found in DB\n", c.Slug)
			notFound++
			continue
		}

		// Skip if already has data and not forcing
		if !*force && page.EarningRatesJSON != "{}" && page.EarningRatesJSON != "" {
			if *dryRun {
				fmt.Printf("  - %s — already has data (skip)\n", c.Slug)
			}
			skipped++
			continue
		}

		if *dryRun {
			fmt.Printf("  + %s — would update (fee=$%d, bonus=%s)\n", c.Slug, c.AnnualFeeAmount, truncate(c.WelcomeBonusText, 40))
			updated++
			continue
		}

		// Normalize JSON fields before writing
		normalizedEarning := normalizeEarningRates(c.EarningRates)
		normalizedCredits := normalizeCredits(c.Credits)
		normalizedBenefits := normalizeBenefitsOrProtections(c.Benefits, "value")
		normalizedProtections := normalizeBenefitsOrProtections(c.Protections, "value")

		// Build earning_rates with transfer_partners merged in
		earningWithPartners := mergeTransferPartners(normalizedEarning, c.TransferPartners)

		// Update structured fields
		now := time.Now()
		_, err = database.DB().Exec(`
			UPDATE reference_pages SET
				annual_fee_amount = ?,
				annual_fee_note = ?,
				welcome_bonus_text = ?,
				welcome_bonus_verified_at = ?,
				earning_rates_json = ?,
				credits_json = ?,
				benefits_json = ?,
				protections_json = ?,
				updated_at = CURRENT_TIMESTAMP
			WHERE slug = ?`,
			c.AnnualFeeAmount,
			c.AnnualFeeNote,
			c.WelcomeBonusText,
			now,
			string(earningWithPartners),
			string(normalizedCredits),
			string(normalizedBenefits),
			string(normalizedProtections),
			c.Slug,
		)
		if err != nil {
			fmt.Printf("  ERROR %s: %v\n", c.Slug, err)
			continue
		}

		updated++
		fmt.Printf("  + %s\n", c.Slug)
	}

	fmt.Printf("\nDone: %d updated, %d skipped, %d not found\n", updated, skipped, notFound)
	if skipped > 0 && !*force {
		fmt.Println("Use -force to overwrite existing data.")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// normalizeEarningRates converts flat string values like "2x" into {"rate":"2x","note":""}.
// Also normalizes "niche" strings into arrays.
func normalizeEarningRates(raw json.RawMessage) json.RawMessage {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	for k, v := range m {
		if k == "niche" {
			m[k] = normalizeNiche(v)
			continue
		}
		switch val := v.(type) {
		case string:
			if val == "N/A" || val == "" {
				m[k] = map[string]string{"rate": "N/A", "note": ""}
			} else {
				m[k] = map[string]string{"rate": val, "note": ""}
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// normalizeCredits converts flat string values like "$200 credit" into {"amount":"$200 credit","note":""}.
func normalizeCredits(raw json.RawMessage) json.RawMessage {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	for k, v := range m {
		if k == "niche" {
			m[k] = normalizeNiche(v)
			continue
		}
		switch val := v.(type) {
		case string:
			if val == "N/A" || val == "" {
				m[k] = map[string]string{"amount": "", "note": ""}
			} else {
				m[k] = map[string]string{"amount": val, "note": ""}
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// normalizeBenefitsOrProtections converts flat string values into {"value":"...","note":""}.
// Also handles boolean values for fields like no_ftf.
func normalizeBenefitsOrProtections(raw json.RawMessage, _ string) json.RawMessage {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	for k, v := range m {
		if k == "niche" {
			m[k] = normalizeNiche(v)
			continue
		}
		switch val := v.(type) {
		case string:
			if val == "N/A" || val == "" {
				m[k] = map[string]string{"value": "N/A", "note": ""}
			} else {
				m[k] = map[string]string{"value": val, "note": ""}
			}
		case bool:
			boolStr := "No"
			if val {
				boolStr = "Yes"
			}
			m[k] = map[string]string{"value": boolStr, "note": ""}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// normalizeNiche converts a "niche" field: if it's a string, convert to array format.
// "N/A" or "" becomes []. Any other string becomes [{"label":"...","note":""}].
func normalizeNiche(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		if val == "N/A" || val == "" {
			return []interface{}{}
		}
		return []map[string]string{{"label": val, "note": ""}}
	default:
		return v // already an array or other valid type
	}
}

// mergeTransferPartners adds transfer_partners into the earning_rates JSON
// so the comparison page can show them together.
func mergeTransferPartners(earningRaw, partnersRaw json.RawMessage) json.RawMessage {
	var earning map[string]interface{}
	if err := json.Unmarshal(earningRaw, &earning); err != nil {
		return earningRaw
	}

	var partners interface{}
	if err := json.Unmarshal(partnersRaw, &partners); err == nil {
		earning["transfer_partners"] = partners
	}

	merged, err := json.Marshal(earning)
	if err != nil {
		return earningRaw
	}
	return merged
}
