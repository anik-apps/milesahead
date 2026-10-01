// Package seeddata loads the repository's card data (cmd/seed-cards and
// cmd/seed-structured) and a set of made-up articles into a database. It is
// for tests and local previews: everything it writes is public card data or
// invented, never a copy of production.
package seeddata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// repoFile returns the path of a file relative to the repository root.
func repoFile(rel string) string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", rel)
}

type section struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

type card struct {
	Slug          string    `json:"slug"`
	Title         string    `json:"title"`
	Category      string    `json:"category"`
	Issuer        string    `json:"issuer"`
	Sections      []section `json:"sections"`
	ScoutsVerdict string    `json:"scouts_verdict"`
	Keywords      []string  `json:"keywords"`
	ImageURL      string    `json:"image_url"`
	ApplyURL      string    `json:"apply_url"`
	Status        string    `json:"status"`
}

// StructuredCard is one entry of cmd/seed-structured/structured-data.json.
type StructuredCard struct {
	Slug             string          `json:"slug"`
	AnnualFeeAmount  int             `json:"annual_fee_amount"`
	AnnualFeeNote    string          `json:"annual_fee_note"`
	WelcomeBonusText string          `json:"welcome_bonus_text"`
	EarningRates     json.RawMessage `json:"earning_rates"`
	Credits          json.RawMessage `json:"credits"`
	Benefits         json.RawMessage `json:"benefits"`
	Protections      json.RawMessage `json:"protections"`
	TransferPartners json.RawMessage `json:"transfer_partners"`
}

// Cards loads every card from cmd/seed-cards/cards.json through the normal
// write path (so the search index is filled), then its comparison data from
// cmd/seed-structured/structured-data.json. It returns the number of cards.
func Cards(d *db.Database) (int, error) {
	raw, err := os.ReadFile(repoFile("cmd/seed-cards/cards.json"))
	if err != nil {
		return 0, err
	}
	var cards []card
	if err := json.Unmarshal(raw, &cards); err != nil {
		return 0, fmt.Errorf("parsing cards.json: %w", err)
	}
	for _, c := range cards {
		sections, _ := json.Marshal(c.Sections)
		keywords, _ := json.Marshal(c.Keywords)
		if _, err := d.CreateReferencePage(&db.ReferencePage{
			Slug: c.Slug, Title: c.Title, Category: c.Category, Issuer: c.Issuer,
			SectionsJSON: string(sections), RecentChangesJSON: "[]", PendingUpdatesJSON: "[]",
			ScoutsVerdict: c.ScoutsVerdict, KeywordsJSON: string(keywords),
			ImageURL: c.ImageURL, ApplyURL: c.ApplyURL, Status: c.Status,
		}); err != nil {
			return 0, fmt.Errorf("card %s: %w", c.Slug, err)
		}
	}

	raw, err = os.ReadFile(repoFile("cmd/seed-structured/structured-data.json"))
	if err != nil {
		return 0, err
	}
	var structured []StructuredCard
	if err := json.Unmarshal(raw, &structured); err != nil {
		return 0, fmt.Errorf("parsing structured-data.json: %w", err)
	}
	for _, s := range structured {
		earning := MergeTransferPartners(NormalizeEarningRates(s.EarningRates), s.TransferPartners)
		if _, err := d.DB().Exec(`UPDATE reference_pages SET annual_fee_amount = ?, annual_fee_note = ?, welcome_bonus_text = ?,
			earning_rates_json = ?, credits_json = ?, benefits_json = ?, protections_json = ? WHERE slug = ?`,
			s.AnnualFeeAmount, s.AnnualFeeNote, s.WelcomeBonusText,
			string(earning), string(NormalizeCredits(s.Credits)),
			string(NormalizeBenefitsOrProtections(s.Benefits)), string(NormalizeBenefitsOrProtections(s.Protections)),
			s.Slug); err != nil {
			return 0, fmt.Errorf("structured %s: %w", s.Slug, err)
		}
	}
	return len(cards), nil
}

// Article is a made-up article for tests and previews.
type Article struct {
	Slug, Title, Type, TLDR, Content string
	// DaysAgo is how long ago it was published.
	DaysAgo int
	// EndsInDays sets the offer end date relative to now (negative: ended).
	// Zero means the article has no end date.
	EndsInDays int
}

// Articles are invented stories with realistic titles, written so that the
// search tests have something to find for every query they exercise.
var Articles = []Article{
	{"csr-150k-offer", "Chase Sapphire Reserve Drops 150K Points Bonus for New Cardholders", "deal",
		"The Sapphire Reserve's welcome offer jumps to 150,000 Ultimate Rewards points.",
		"<p>Chase raised the Sapphire Reserve welcome bonus to 150,000 points after $6,000 in spend. The $795 annual fee stays, and the 8x on Chase Travel remains the headline earn rate.</p>", 3, 40},
	{"csr-vs-plat", "Sapphire Reserve or Platinum? How the $795 and $895 Cards Compare", "summary",
		"Two premium cards, two very different credit books.",
		"<p>The Reserve leans on travel earning and Sapphire Lounges, while the Amex Platinum stacks monthly credits and Centurion Lounges. Pick by how you travel.</p>", 12, 0},
	{"csp-100k", "Chase Sapphire Preferred 100K Welcome Bonus Is Back", "deal",
		"The Sapphire Preferred returns to its best public offer.",
		"<p>New applicants for the Sapphire Preferred can earn 100,000 points after $5,000 in purchases within three months.</p>", 6, 25},
	{"hyatt-ratio-cut", "Chase Cuts Hyatt Transfer Ratio for Sapphire Preferred and Ink", "summary",
		"Ultimate Rewards to World of Hyatt moves from 1:1 to 4:3 for some cards.",
		"<p>Chase will change the Hyatt transfer ratio for the Sapphire Preferred and Ink Business Preferred. The Sapphire Reserve keeps 1:1 transfers to Hyatt for now.</p>", 9, 0},
	{"bilt-hyatt", "Bilt Follows Chase, Devalues Hyatt Transfers to 4:3", "summary",
		"Bilt points moving to Hyatt will be worth less starting next month.",
		"<p>Bilt Rewards says transfers to World of Hyatt will move to a 4:3 ratio. Other airline partners are unchanged.</p>", 8, 0},
	{"hyatt-kyoto", "Hyatt Regency Kyoto Checks Out for Good Next Spring", "summary",
		"A favourite award stay closes for renovation.",
		"<p>Hyatt Regency Kyoto will close in May for a two-year renovation. Points bookings after that date will be cancelled.</p>", 20, 0},
	{"plat-saks", "Amex Platinum Card to Remove Saks Fifth Avenue Credit", "summary",
		"The semiannual Saks credit ends on July 1.",
		"<p>American Express is removing the Saks credit from the Platinum Card. Amex Platinum cardholders keep the hotel, Resy and streaming credits.</p>", 30, 0},
	{"amex-airline-discounts", "American Express Rebrands Airline Discounts — Here's What Actually Changed", "summary",
		"International Airline Program fares get a new name.",
		"<p>Amex Platinum holders still get discounted premium fares on partner airlines; the program simply has a new name.</p>", 15, 0},
	{"plat-lounge-guest", "Centurion Lounges Tighten Guest Rules for Platinum Members", "summary",
		"Free guests now need $75,000 in annual spend.",
		"<p>American Express Platinum members must spend $75,000 a year to bring guests into Centurion lounges for free.</p>", 18, 0},
	{"sapphire-lounge-dfw", "Chase Sapphire Lounge Opens at DFW", "summary",
		"The newest Sapphire Lounge adds a Texas hub.",
		"<p>The lounge is open to Sapphire Reserve cardholders and Priority Pass members, with a two-guest limit.</p>", 4, 0},
	{"lounge-network-2026", "Every Airport Lounge Network Ranked for 2026", "summary",
		"Centurion, Sapphire and Capital One lounges, side by side.",
		"<p>We visited the major lounge networks this year. Capital One lounges were the quietest; Centurion lounges had the best food.</p>", 25, 0},
	{"marriott-65", "Chase Dishes Out a 65% Transfer Bonus to Marriott", "deal",
		"A rare transfer bonus to Marriott Bonvoy, through the end of the month.",
		"<p>Chase Ultimate Rewards members get a 65% transfer bonus when moving points to Marriott Bonvoy.</p>", 2, 20},
	{"rove-aeroplan", "Rove Adds Aeroplan With a 25% Transfer Bonus", "deal",
		"A new partner launches with a bonus.",
		"<p>Rove Miles now transfer to Aeroplan, with a 25% transfer bonus to celebrate the launch.</p>", 5, 14},
	{"bilt-rent-day", "Bilt Rent Day: Up to 200% Transfer Bonuses to Hilton and Amtrak", "deal",
		"This month's Rent Day transfer bonuses are the biggest yet.",
		"<p>Bilt members get transfer bonuses of up to 200% to Hilton Honors and 125% to Amtrak Guest Rewards for one day.</p>", 1, 1},
	{"c1-jal", "Capital One 25% Transfer Bonus to Japan Airlines", "deal",
		"Capital One miles to JAL Mileage Bank get a 25% boost.",
		"<p>Capital One is running a 25% transfer bonus to Japan Airlines Mileage Bank.</p>", 60, -30},
	{"amex-avios", "American Express 30% Transfer Bonus to British Airways and Aer Lingus", "deal",
		"Membership Rewards to Avios gets a 30% bonus.",
		"<p>Amex Membership Rewards members get a 30% transfer bonus to British Airways Executive Club and Aer Lingus AerClub.</p>", 70, -45},
	{"citi-lhw", "Citi ThankYou 25% Transfer Bonus to Leading Hotels of the World", "deal",
		"A niche hotel transfer bonus from Citi.",
		"<p>Citi ThankYou points get a 25% transfer bonus to Leading Hotels of the World Leaders Club.</p>", 80, -60},
	{"venture-x-savor", "Capital One Venture X and Savor: The Two-Card Combo That Works", "summary",
		"How the Venture X pairs with the Savor for dining and travel.",
		"<p>The Venture X earns 2x everywhere and 10x on hotels through Capital One Travel; the Savor adds 3% on dining.</p>", 14, 0},
	{"freedom-flex-250", "Chase Freedom Flex $250 Bonus Returns", "deal",
		"The Freedom Flex brings back a $250 cash bonus.",
		"<p>Earn $250 after $500 in purchases in three months with the Chase Freedom Flex.</p>", 7, 30},
	{"custom-cash-dining", "Is the Citi Custom Cash Still the Best Dining Card?", "summary",
		"5% on your top category, capped at $500 a cycle.",
		"<p>The Custom Cash earns 5% on your top eligible category each billing cycle, up to $500 in spend.</p>", 22, 0},
	{"world-of-hyatt-card", "World of Hyatt Card Adds Free Night After $15K Spend", "deal",
		"A second Category 1-4 free night for big spenders.",
		"<p>World of Hyatt cardholders earn a second free night after $15,000 in annual spend.</p>", 11, 0},
	{"aspire-175k", "Hilton Aspire 175K Offer Ends Tonight", "deal",
		"The Hilton Honors Aspire's elevated bonus is ending.",
		"<p>The Hilton Honors American Express Aspire offers 175,000 points after $6,000 in spend.</p>", 90, -70},
}

// SeedArticles stores Articles as approved articles, with publish and offer
// end dates relative to now.
func SeedArticles(d *db.Database, now time.Time) error {
	for _, a := range Articles {
		id, err := d.CreateGeneratedArticle(&db.GeneratedArticle{
			Title: a.Title, Slug: a.Slug, Type: a.Type, Content: a.Content, TLDR: a.TLDR,
			Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
			KeyDetails: "[]", Links: "[]", RelatedCardsJSON: "[]", TagsJSON: "[]",
		})
		if err != nil {
			return fmt.Errorf("article %s: %w", a.Slug, err)
		}
		var ends any
		if a.EndsInDays != 0 {
			ends = now.AddDate(0, 0, a.EndsInDays).Format("2006-01-02")
		}
		published := now.AddDate(0, 0, -a.DaysAgo).UTC().Format("2006-01-02 15:04:05")
		if _, err := d.DB().Exec(`UPDATE generated_articles SET published_at = ?, created_at = ?, expires_on = ? WHERE id = ?`,
			published, published, ends, id); err != nil {
			return err
		}
	}
	return nil
}

// --- Normalising structured card data ---
// Moved here from cmd/seed-structured so the seed command, tests and
// previews store the comparison data identically.

// ratePattern matches a leading rate like "3x", "1.5%", "10x".
var ratePattern = regexp.MustCompile(`^(\d+(?:\.\d+)?[x%])`)

// upToPattern matches "Up to 2x ..." style strings.
var upToPattern = regexp.MustCompile(`^Up to (\d+(?:\.\d+)?[x%])`)

// cashbackMatchPattern matches "1% cash back (2% effective in first year with Cashback Match)".
var cashbackMatchPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?%) cash back \((\d+(?:\.\d+)?%) effective.*Cashback Match\)`)

// CleanRate extracts the short rate from a verbose string and returns (rate, note).
// If the string is already clean, it returns it unchanged with empty note.
func CleanRate(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return raw, ""
	}

	// Special case: "1% cash back (2% effective in first year with Cashback Match)"
	if m := cashbackMatchPattern.FindStringSubmatch(raw); m != nil {
		return m[1], m[2] + " effective with Cashback Match"
	}

	// "Up to 2x Hyatt points (if dining is a top-3 spending category)"
	if m := upToPattern.FindStringSubmatch(raw); m != nil {
		rest := strings.TrimSpace(raw[len(m[0]):])
		note := "Up to " + m[1]
		if rest != "" {
			note = "Up to " + m[1] + " " + rest
		}
		return m[1], note
	}

	// "3x Atmos Rewards points on Alaska Airlines" → rate "3x", note "Atmos Rewards points on Alaska Airlines"
	if m := ratePattern.FindStringSubmatch(raw); m != nil {
		rest := strings.TrimSpace(raw[len(m[0]):])
		if rest == "" {
			return m[1], ""
		}
		return m[1], rest
	}

	// Patterns like "1% (5% when rotating category, ...)" — rate is "1%", rest is note
	if idx := strings.Index(raw, " ("); idx > 0 {
		prefix := raw[:idx]
		if ratePattern.MatchString(prefix) {
			return prefix, strings.TrimSpace(raw[idx+1:])
		}
	}

	return raw, ""
}

// NormalizeEarningRates converts flat string values like "2x" into {"rate":"2x","note":""}.
// Also normalizes "niche" strings into arrays, and cleans verbose rate strings.
func NormalizeEarningRates(raw json.RawMessage) json.RawMessage {
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
				rate, note := CleanRate(val)
				m[k] = map[string]string{"rate": rate, "note": note}
			}
		case map[string]interface{}:
			// Already an object — clean the rate field if verbose
			rateStr, _ := val["rate"].(string)
			noteStr, _ := val["note"].(string)
			if rateStr != "" && rateStr != "N/A" {
				cleanedRate, extraNote := CleanRate(rateStr)
				if cleanedRate != rateStr {
					val["rate"] = cleanedRate
					if noteStr == "" && extraNote != "" {
						val["note"] = extraNote
					}
					m[k] = val
				}
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// NormalizeCredits converts flat string values like "$200 credit" into {"amount":"$200 credit","note":""}.
func NormalizeCredits(raw json.RawMessage) json.RawMessage {
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

// NormalizeBenefitsOrProtections converts flat string values into {"value":"...","note":""}.
// Also handles boolean values for fields like no_ftf.
func NormalizeBenefitsOrProtections(raw json.RawMessage) json.RawMessage {
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

// MergeTransferPartners adds transfer_partners into the earning_rates JSON
// so the comparison page can show them together.
func MergeTransferPartners(earningRaw, partnersRaw json.RawMessage) json.RawMessage {
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
