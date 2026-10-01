package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedEarningRates reads every card's earning_rates from the seed file, the
// same data the live site was loaded from.
func seedEarningRates(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "seed-structured", "structured-data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cards []struct {
		Slug  string          `json:"slug"`
		Rates json.RawMessage `json:"earning_rates"`
	}
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, c := range cards {
		out[c.Slug] = string(c.Rates)
	}
	return out
}

func details(rates []earnRate) []string {
	out := make([]string, len(rates))
	for i, r := range rates {
		out[i] = r.Detail()
	}
	return out
}

// Ranked by value, with the real seed data. The old summary walked a fixed
// category order and read the un-suffixed travel keys, so Amex Platinum came
// out as "1x Dining · 1x Groceries".
func TestRankEarnRates_SeedCards(t *testing.T) {
	rates := seedEarningRates(t)
	for slug, want := range map[string][]string{
		// Flights earn 5x however booked; hotels only through Amex Travel.
		"amex-platinum": {"5x flights", "5x hotels via Amex Travel", "1x everything else"},
		// Same-rate categories share a line, the caps are flagged, and a portal
		// note that names no portal borrows the card's.
		"amex-gold": {"4x dining & groceries (capped)", "3x flights", "5x hotels via Amex Travel", "2x car rentals & other travel via Amex Travel", "1x everything else"},
		// Direct rates first, then the portal; four travel categories read as "travel".
		"chase-sapphire-reserve": {"4x flights & hotels", "3x dining", "8x travel via Chase Travel", "1x everything else"},
		// A flat card leads with its flat rate.
		"capital-one-venture-x":   {"2x everything", "10x hotels & car rentals via Capital One Travel", "5x flights & other travel via Capital One Travel"},
		"citi-double-cash":        {"2% everything", "5% travel via the issuer's portal"},
		"wells-fargo-active-cash": {"2% everything"},
		// Conditions stay attached to the rate.
		"citi-custom-cash": {"5% dining, groceries & more (top category only)", "1% everything else"},
		"bilt-obsidian":    {"3x dining & groceries (choose one)", "2x travel", "4x hotels via Bilt Travel", "3x flights via Bilt Travel", "1x everything else"},
		// "no cap" is not a cap.
		"chase-freedom-flex": {"3% dining", "5% travel via Chase Travel", "1% everything else"},
		// A portal named only by its site.
		"citi-strata-premier": {"3x dining, groceries & more", "10x travel via cititravel.com", "1x everything else"},
		// Stored as bare strings rather than {rate, note}.
		"united-quest":              {"4x flights", "2x dining, hotels & more", "5x hotels via the issuer's portal", "1x everything else"},
		"hilton-honors-amex-aspire": {"14x hotels", "7x dining, flights & car rentals", "3x everything else"},
	} {
		got := details(rankEarnRates(rates[slug]))
		if strings.Join(got, " | ") != strings.Join(want, " | ") {
			t.Errorf("%s:\n got  %q\n want %q", slug, got, want)
		}
	}
}

// Every seeded card gets a summary, and it never leads with "everything else".
func TestRankEarnRates_EveryCard(t *testing.T) {
	for slug, rates := range seedEarningRates(t) {
		got := rankEarnRates(rates)
		if len(got) == 0 {
			t.Errorf("%s: no earning summary", slug)
			continue
		}
		if got[0].Label == "everything else" {
			t.Errorf("%s: summary leads with %q", slug, got[0].Detail())
		}
		for i := 1; i < len(got); i++ {
			if got[i].Portal == "" && !got[i].Base && got[i-1].Portal != "" {
				t.Errorf("%s: a direct rate %q follows a portal rate", slug, got[i].Detail())
			}
		}
	}
}

// The normalised shape the seeder writes to the database ranks the same.
func TestRankEarnRates_NormalisedShape(t *testing.T) {
	stored := `{"dining":{"rate":"3x","note":""},"airlines_direct":{"rate":"2x","note":""},"airlines_portal":{"rate":"5x","note":"Via Chase Travel"},"hotels_direct":{"rate":"N/A","note":""},"hotels_portal":{"rate":"5x","note":""},"base":{"rate":"1x","note":""},"niche":[{"label":"5x on Lyft","note":""}]}`
	got := strings.Join(details(rankEarnRates(stored)), " | ")
	want := "3x dining | 2x flights | 5x flights & hotels via Chase Travel | 1x everything else"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRankEarnRates_EmptyOrBroken(t *testing.T) {
	for _, in := range []string{"", "{}", "not json", `{"base":{"rate":"N/A"}}`} {
		if got := rankEarnRates(in); len(got) != 0 {
			t.Errorf("rankEarnRates(%q) = %v, want nothing", in, got)
		}
	}
	// Older rows stored travel rates without the _direct suffix.
	if got := details(rankEarnRates(`{"airlines":{"rate":"3x"},"base":{"rate":"1x"}}`)); strings.Join(got, "|") != "3x flights|1x everything else" {
		t.Errorf("unsuffixed travel key: got %q", got)
	}
}

func TestIsCapped(t *testing.T) {
	for note, want := range map[string]bool{
		"U.S. supermarkets, up to $25,000/yr then 1x":   true,
		"Booked directly with airlines, up to $500K/yr": false, // too high to matter
		"Combined $150K cap":                            false,
		"$500/billing cycle cap":                        true,
		"Including takeout and delivery; no cap":        false,
		"Unlimited":                                     false,
		"3x on first $6,000/yr combined, then 2x":       true,
		"": false,
	} {
		if got := isCapped(note); got != want {
			t.Errorf("isCapped(%q) = %v, want %v", note, got, want)
		}
	}
}

// The article embed reads the same ranking.
func TestFormatTopEarning(t *testing.T) {
	rates := seedEarningRates(t)
	for slug, want := range map[string]string{
		"amex-platinum":    "5x flights · 5x hotels via Amex Travel",
		"amex-gold":        "4x dining & groceries (capped) · 3x flights",
		"citi-double-cash": "2% everything · 5% travel via the issuer's portal",
	} {
		if got := formatTopEarning(rates[slug], 2); got != want {
			t.Errorf("formatTopEarning(%s) = %q, want %q", slug, got, want)
		}
	}
	if got := formatTopEarning("", 2); got != "" {
		t.Errorf("no rates: got %q", got)
	}
}
