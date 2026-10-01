package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// indexPage is a published card with just the fields the index reads.
func indexPage(slug, title, cat, issuer string, fee int, rates, bonus string) db.ReferencePage {
	return db.ReferencePage{
		Slug: slug, Title: title, Category: cat, Issuer: issuer,
		ImageURL: "/static/img/cards/" + slug + ".png", Status: "published",
		AnnualFeeAmount: fee, EarningRatesJSON: rates, WelcomeBonusText: bonus,
	}
}

func indexFixture() []db.ReferencePage {
	flat2 := `{"base":{"rate":"2%"}}`
	return []db.ReferencePage{
		indexPage("plat", "Platinum", "travel", "Amex", 895, `{"airlines_direct":{"rate":"5x"},"base":{"rate":"1x"}}`, "As high as 175,000 MR points after $12,000 spend in 6 months"),
		indexPage("gold", "Gold", "travel", "Amex", 325, `{"dining":{"rate":"4x"},"base":{"rate":"1x"}}`, "Up to 100,000 MR points after $8,000 spend"),
		indexPage("csp", "Sapphire Preferred", "travel", "Chase", 95, `{"dining":{"rate":"3x"},"base":{"rate":"1x"}}`, "75,000 UR points after $5,000 spend in 3 months"),
		indexPage("edge-100", "Edge Hundred", "travel", "Chase", 100, flat2, "$100 bonus after $500 spend"),
		indexPage("edge-400", "Edge Four Hundred", "hotel", "Chase", 400, flat2, "$100 bonus after $500 spend"),
		indexPage("edge-401", "Edge Four Oh One", "hotel", "Bank of America", 401, flat2, "$100 bonus after $500 spend"),
		indexPage("dc", "Double Cash", "cash-back", "Citi", 0, flat2, "$200 cash back (20,000 ThankYou Points) after $1,500 spend in 6 months"),
		indexPage("ac", "Active Cash", "cash-back", "Wells Fargo", 0, flat2, "$100 cash rewards bonus after $500 spend in 3 months"),
		indexPage("one", "SavorOne", "cash-back", "Capital One", 39, `{"dining":{"rate":"3%"},"base":{"rate":"1%"}}`, "No welcome bonus"),
		// No structured data yet: its $0 means "unknown", not "no fee".
		indexPage("blank", "Blank Card", "cash-back", "Citi", 0, "", ""),
	}
}

func slugsOf(tiles []cardTile) []string {
	out := make([]string, len(tiles))
	for i, t := range tiles {
		out[i] = t.Slug
	}
	return out
}

func shownSlugs(ix cardsIndex) []string {
	if !ix.Grouped {
		return slugsOf(ix.Cards)
	}
	var out []string
	for _, g := range ix.Groups {
		out = append(out, slugsOf(g.Cards)...)
	}
	return out
}

func q(s string) url.Values {
	v, _ := url.ParseQuery(s)
	return v
}

func chip(t *testing.T, chips []indexChip, label string) indexChip {
	t.Helper()
	for _, c := range chips {
		if c.Label == label {
			return c
		}
	}
	t.Fatalf("no chip %q", label)
	return indexChip{}
}

func TestCardsIndex_Unfiltered(t *testing.T) {
	ix := buildCardsIndex(indexFixture(), q(""))
	if !ix.Grouped || ix.Count != 10 || ix.CountText != "10 cards" || len(ix.Active) != 0 {
		t.Fatalf("unfiltered: grouped=%v count=%d active=%v", ix.Grouped, ix.Count, ix.Active)
	}
	var names []string
	for _, g := range ix.Groups {
		names = append(names, g.Name)
	}
	if got := strings.Join(names, ","); got != "Travel & Premium,Cash Back,Hotel" {
		t.Errorf("groups in categoryOrder: got %s", got)
	}
	// Within a group, by name rather than database order.
	if got := strings.Join(slugsOf(ix.Groups[0].Cards), ","); got != "edge-100,gold,plat,csp" {
		t.Errorf("travel group by name: got %s", got)
	}
	if c := chip(t, ix.Categories, "All"); !c.Active || c.Count != 10 || c.URL != "/cards/" {
		t.Errorf("All chip: %+v", c)
	}
	if c := chip(t, ix.Categories, "Cash Back"); c.Count != 4 || c.URL != "/cards/?cat=cash-back" {
		t.Errorf("Cash Back chip: %+v", c)
	}
}

// Category, fee band and issuer apply together, and every link keeps the others.
func TestCardsIndex_FiltersCombine(t *testing.T) {
	ix := buildCardsIndex(indexFixture(), q("cat=travel&fee=under-100&issuer=chase"))
	if ix.Grouped {
		t.Error("a category filter shows a flat list")
	}
	if got := strings.Join(shownSlugs(ix), ","); got != "csp" {
		t.Errorf("travel + under $100 + Chase: got %s", got)
	}
	// Chip counts are what each click would show with the other filters kept.
	if c := chip(t, ix.Categories, "Hotel"); c.Count != 0 {
		t.Errorf("Hotel with under-100 + Chase should count 0, got %d", c.Count)
	}
	if c := chip(t, ix.FeeBands, "$100–$400"); c.Count != 1 || c.URL != "/cards/?cat=travel&fee=100-400&issuer=chase" {
		t.Errorf("fee band keeps category and issuer: %+v", c)
	}
	// The active chip removes just its own filter.
	if c := chip(t, ix.Categories, "Travel & Premium"); !c.Active || c.URL != "/cards/?fee=under-100&issuer=chase" {
		t.Errorf("active category chip: %+v", c)
	}
	var labels, removes []string
	for _, a := range ix.Active {
		labels = append(labels, a.Label)
		removes = append(removes, a.RemoveURL)
	}
	if got := strings.Join(labels, ","); got != "Travel & Premium,Under $100,Chase" {
		t.Errorf("applied filters: %s", got)
	}
	if removes[2] != "/cards/?cat=travel&fee=under-100" {
		t.Errorf("removing the issuer keeps the rest: %s", removes[2])
	}
}

// Bands meet without gaps or overlaps, and a card with no data is in none.
func TestCardsIndex_FeeBands(t *testing.T) {
	for band, want := range map[string]string{
		"none":      "ac,dc",
		"under-100": "one,csp",
		"100-400":   "edge-400,edge-100,gold",
		"over-400":  "edge-401,plat",
	} {
		ix := buildCardsIndex(indexFixture(), q("fee="+band+"&sort=name"))
		got := ix.Cards
		names := map[string]bool{}
		for _, s := range slugsOf(got) {
			names[s] = true
		}
		for _, s := range strings.Split(want, ",") {
			if !names[s] {
				t.Errorf("fee=%s should include %s, got %v", band, s, slugsOf(got))
			}
		}
		if len(got) != len(strings.Split(want, ",")) {
			t.Errorf("fee=%s: got %v, want %s", band, slugsOf(got), want)
		}
		if names["blank"] {
			t.Errorf("fee=%s includes a card with no fee data", band)
		}
	}
}

func TestCardsIndex_Sort(t *testing.T) {
	asc := shownSlugs(buildCardsIndex(indexFixture(), q("sort=fee-asc")))
	if got := strings.Join(asc, ","); got != "ac,dc,one,csp,edge-100,gold,edge-400,edge-401,plat,blank" {
		t.Errorf("fee low to high, unknown fee last: %s", got)
	}
	desc := shownSlugs(buildCardsIndex(indexFixture(), q("sort=fee-desc")))
	if desc[0] != "plat" || desc[len(desc)-1] != "blank" {
		t.Errorf("fee high to low, unknown fee last: %v", desc)
	}
	name := shownSlugs(buildCardsIndex(indexFixture(), q("sort=name")))
	if name[0] != "ac" || name[1] != "blank" {
		t.Errorf("by name: %v", name)
	}
}

// Values the index doesn't offer are ignored: a stale link shows every card.
func TestCardsIndex_UnknownValuesIgnored(t *testing.T) {
	ix := buildCardsIndex(indexFixture(), q("cat=nope&fee=cheap&issuer=acme&sort=random"))
	if ix.Query != (cardsQuery{}) || ix.Count != 10 || !ix.Grouped {
		t.Errorf("unknown values should be dropped, got %+v (count %d)", ix.Query, ix.Count)
	}
}

func TestCardsIndex_TileFacts(t *testing.T) {
	ix := buildCardsIndex(indexFixture(), q("sort=name"))
	tiles := map[string]cardTile{}
	for _, c := range ix.Cards {
		tiles[c.Slug] = c
	}
	plat := tiles["plat"]
	if plat.Fee != "$895" || plat.Earn.String() != "5x flights" || plat.Offer.Short != "Up to 175K points" {
		t.Errorf("platinum tile: fee %q, earn %q, offer %q", plat.Fee, plat.Earn.String(), plat.Offer.Short)
	}
	if dc := tiles["dc"]; dc.Earn.String() != "2% everything" || dc.Offer.Short != "$200 cash back" {
		t.Errorf("double cash tile: earn %q, offer %q", dc.Earn.String(), dc.Offer.Short)
	}
	if one := tiles["one"]; one.Offer.Available || one.Offer.Short != "No current offer" {
		t.Errorf("no-bonus card: %+v", one.Offer)
	}
	if blank := tiles["blank"]; blank.HasFacts || blank.Fee != "" {
		t.Errorf("a card without data shows no facts: %+v", blank)
	}
	// Only the first rows load their art straight away.
	for i, c := range ix.Cards {
		if c.Eager != (i < cardTilesEager) {
			t.Errorf("tile %d (%s): eager=%v", i, c.Slug, c.Eager)
		}
	}
}

func TestCardsURL(t *testing.T) {
	for _, c := range []struct {
		q    cardsQuery
		want string
	}{
		{cardsQuery{}, "/cards/"},
		{cardsQuery{Cat: "hotel"}, "/cards/?cat=hotel"},
		{cardsQuery{Cat: "cash-back", Fee: "none", Issuer: "bank-of-america", Sort: "fee-asc"}, "/cards/?cat=cash-back&fee=none&issuer=bank-of-america&sort=fee-asc"},
	} {
		if got := c.q.url(); got != c.want {
			t.Errorf("%+v.url() = %q, want %q", c.q, got, c.want)
		}
	}
	if got := issuerKey("Bank of America"); got != "bank-of-america" {
		t.Errorf("issuerKey: %q", got)
	}
}

// seedAllCards loads the 52 seed cards with their structured data, as
// cmd/seed-cards and cmd/seed-structured do.
func seedAllCards(t *testing.T, app *App) {
	t.Helper()
	read := func(name string, v interface{}) {
		raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	var cards []struct {
		Slug, Title, Category, Issuer, Status string
		ScoutsVerdict                         string `json:"scouts_verdict"`
		ImageURL                              string `json:"image_url"`
		ApplyURL                              string `json:"apply_url"`
		Sections                              json.RawMessage
	}
	var facts []struct {
		Slug  string
		Fee   int             `json:"annual_fee_amount"`
		Note  string          `json:"annual_fee_note"`
		Bonus string          `json:"welcome_bonus_text"`
		Rates json.RawMessage `json:"earning_rates"`
	}
	read("seed-cards/cards.json", &cards)
	read("seed-structured/structured-data.json", &facts)
	byslug := map[string]int{}
	for i, f := range facts {
		byslug[f.Slug] = i
	}
	for _, c := range cards {
		p := db.ReferencePage{
			Slug: c.Slug, Title: c.Title, Category: c.Category, Issuer: c.Issuer, Status: c.Status,
			ScoutsVerdict: c.ScoutsVerdict, ImageURL: c.ImageURL, ApplyURL: c.ApplyURL, SectionsJSON: string(c.Sections),
		}
		if i, ok := byslug[c.Slug]; ok {
			f := facts[i]
			p.AnnualFeeAmount, p.AnnualFeeNote, p.WelcomeBonusText, p.EarningRatesJSON = f.Fee, f.Note, f.Bonus, string(f.Rates)
		}
		if _, err := app.db.CreateReferencePage(&p); err != nil {
			t.Fatalf("seeding %s: %v", c.Slug, err)
		}
	}
}

func getPath(t *testing.T, app *App, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d", path, rec.Code)
	}
	return rec.Body.String()
}

// Integration: real templates and the 52 seed cards.
func TestHandleCardsList_SeedCards(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)

	body := getPath(t, app, "/cards/")
	if got := strings.Count(body, `class="card-tile"`); got != 52 {
		t.Fatalf("expected 52 tiles, got %d", got)
	}
	for _, want := range []string{
		// The filter bar is a GET form whose controls are labelled.
		`<form class="cards-filters" method="get" action="/cards/"`,
		`<span class="cards-filter-label">Issuer</span>`,
		`<select name="issuer">`,
		`<select name="sort">`,
		`<button type="submit" class="cards-filter-apply">Apply</button>`,
		// Fee bands as they fall in the seed data: 12 / 15 / 13 / 12.
		`<a href="/cards/?fee=none" class="filter-switch-item">No fee</a>`,
		`href="/cards/?cat=cash-back" class="filter-chip">Cash Back <span class="filter-count">9</span>`,
		`Showing 52 cards`,
		// Facts on the tiles.
		`<b>$895</b> annual fee`,
		`<b>5x</b> flights`,
		`Offer <b>Up to 175K points</b>`,
		`<b>No</b> annual fee`,
		// A condition on the rate stays with it.
		`<b>5%</b> dining, groceries &amp; more (top category only)`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/cards/ should contain %q", want)
		}
	}
	// The cut-off quote is gone; the facts replace it.
	if strings.Contains(body, "card-grid-excerpt") {
		t.Error("tiles should no longer carry the verdict excerpt")
	}
	// Art: fixed size on every tile, lazy after the first rows.
	imgs := regexp.MustCompile(`<span class="card-tile-art"><img [^>]*>`).FindAllString(body, -1)
	if len(imgs) != 52 {
		t.Fatalf("expected 52 tile images, got %d", len(imgs))
	}
	for i, img := range imgs {
		if !strings.Contains(img, `width="96" height="61"`) {
			t.Errorf("tile image %d has no fixed size: %s", i, img)
		}
		if lazy := strings.Contains(img, `loading="lazy"`); lazy != (i >= cardTilesEager) {
			t.Errorf("tile image %d: lazy=%v", i, lazy)
		}
	}

	counts := map[string]int{}
	for _, band := range feeBands {
		b := getPath(t, app, "/cards/?fee="+band.Key)
		counts[band.Key] = strings.Count(b, `class="card-tile"`)
	}
	if counts["none"] != 12 || counts["under-100"] != 15 || counts["100-400"] != 13 || counts["over-400"] != 12 {
		t.Errorf("fee band counts: %v", counts)
	}
}

func TestHandleCardsList_FilteredView(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)

	body := getPath(t, app, "/cards/?cat=cash-back&fee=none&sort=fee-asc")
	n := strings.Count(body, `class="card-tile"`)
	if n == 0 || n >= 9 {
		t.Fatalf("cash back with no fee should be some but not all of the 9 cash back cards, got %d", n)
	}
	for _, want := range []string{
		// The selects submit with the chips' filters kept.
		`<input type="hidden" name="cat" value="cash-back">`,
		`<input type="hidden" name="fee" value="none">`,
		`<option value="fee-asc" selected>`,
		`aria-label="Remove filter: Cash Back"`,
		`aria-label="Remove filter: No annual fee"`,
		`<a href="/cards/" class="filter-clear">Clear all</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("filtered /cards/ should contain %q", want)
		}
	}
	if strings.Contains(body, `class="category-heading"`) {
		t.Error("a filtered view is one list without category headings")
	}
	if strings.Contains(body, `$95</b>`) || strings.Contains(body, `$39</b>`) {
		t.Error("a no-fee filter must not show cards with a fee")
	}

	none := getPath(t, app, "/cards/?cat=hotel&fee=none&issuer=discover")
	if !strings.Contains(none, "No cards match these filters.") {
		t.Error("an empty result should say so and offer to clear")
	}
}
