package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// indexPage is a published card with just the fields the index reads.
// Cards with structured data get welcome_bonus_verified_at, as the seeder
// writes it with the fee; that is what makes a $0 fee "no annual fee".
func indexPage(slug, title, cat, issuer string, fee int, rates, bonus string) db.ReferencePage {
	p := db.ReferencePage{
		Slug: slug, Title: title, Category: cat, Issuer: issuer,
		ImageURL: "/static/img/cards/" + slug + ".png", Status: "published",
		AnnualFeeAmount: fee, EarningRatesJSON: rates, WelcomeBonusText: bonus,
	}
	if rates != "" || bonus != "" {
		p.WelcomeBonusVerifiedAt = &seedVerifiedAt
	}
	return p
}

var seedVerifiedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

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
	if !ix.Grouped || ix.Count != 10 || ix.CountText != "10 cards" || ix.Filtered || ix.ClearURL != "/cards/" {
		t.Fatalf("unfiltered: grouped=%v count=%d filtered=%v", ix.Grouped, ix.Count, ix.Filtered)
	}
	if !ix.Sorts[0].Selected || ix.Sorts[0].Label != "By category" || ix.Sorts[0].URL != "/cards/" {
		t.Errorf("default sort: %+v", ix.Sorts[0])
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

// The "No annual fee" toggle is fee=none: only cards whose fee is recorded
// as $0. Every chip keeps the toggle and the sort, and the toggle keeps the
// category and the sort.
func TestCardsIndex_NoFeeToggle(t *testing.T) {
	off := buildCardsIndex(indexFixture(), q("sort=name"))
	if off.NoFee.Active || off.NoFee.Count != 2 || off.NoFee.URL != "/cards/?fee=none&sort=name" {
		t.Errorf("toggle off: %+v", off.NoFee)
	}
	if off.Filtered || len(off.Applied) != 0 || len(off.Legacy) != 0 {
		t.Errorf("a sort alone is not a filter: filtered=%v applied=%v legacy=%v", off.Filtered, off.Applied, off.Legacy)
	}

	on := buildCardsIndex(indexFixture(), q("fee=none&sort=name"))
	// Recorded $0 fees only: "blank" has $0 in the column but no fee record.
	if got := strings.Join(shownSlugs(on), ","); got != "ac,dc" {
		t.Errorf("no annual fee: got %s", got)
	}
	if !on.NoFee.Active || on.NoFee.URL != "/cards/?sort=name" {
		t.Errorf("toggle on removes just itself: %+v", on.NoFee)
	}
	if c := chip(t, on.Categories, "Cash Back"); c.Count != 2 || c.URL != "/cards/?cat=cash-back&fee=none&sort=name" {
		t.Errorf("category chip keeps the toggle and the sort: %+v", c)
	}
	if c := chip(t, on.Categories, "Hotel"); c.Count != 0 {
		t.Errorf("no hotel card is free: %+v", c)
	}

	both := buildCardsIndex(indexFixture(), q("cat=cash-back&fee=none&sort=fee-desc"))
	if got := strings.Join(shownSlugs(both), ","); got != "ac,dc" {
		t.Errorf("cash back + no annual fee: got %s", got)
	}
	if both.NoFee.URL != "/cards/?cat=cash-back&sort=fee-desc" {
		t.Errorf("toggling off keeps the category and sort: %s", both.NoFee.URL)
	}
	if c := chip(t, both.Categories, "Cash Back"); !c.Active || c.URL != "/cards/?fee=none&sort=fee-desc" {
		t.Errorf("active category chip removes just the category: %+v", c)
	}
	if got := strings.Join(both.Applied, ","); got != "Cash Back,No annual fee" || len(both.Legacy) != 0 {
		t.Errorf("results line: applied %v, legacy %v", both.Applied, both.Legacy)
	}
	if !both.Filtered || both.ClearURL != "/cards/?sort=fee-desc" {
		t.Errorf("Clear removes the filters and keeps the sort: filtered=%v %s", both.Filtered, both.ClearURL)
	}

	// The toggle is greyed out where it would show nothing.
	if hotel := buildCardsIndex(indexFixture(), q("cat=hotel")); hotel.NoFee.Count != 0 {
		t.Errorf("no free hotel card, toggle count %d", hotel.NoFee.Count)
	}
}

// Older links with a fee band or an issuer still filter. They have no control
// on the page, so the results line offers a link that removes each one.
func TestCardsIndex_LegacyFilters(t *testing.T) {
	ix := buildCardsIndex(indexFixture(), q("cat=travel&fee=under-100&issuer=chase&sort=name"))
	if got := strings.Join(shownSlugs(ix), ","); got != "csp" {
		t.Errorf("travel + under $100 + Chase: got %s", got)
	}
	if c := chip(t, ix.Categories, "Hotel"); c.Count != 0 || c.URL != "/cards/?cat=hotel&fee=under-100&issuer=chase&sort=name" {
		t.Errorf("chips keep the old filters too: %+v", c)
	}
	if got := strings.Join(ix.Applied, ","); got != "Travel & Premium" {
		t.Errorf("applied: %s", got)
	}
	var labels, removes []string
	for _, a := range ix.Legacy {
		labels = append(labels, a.Label)
		removes = append(removes, a.RemoveURL)
	}
	if got := strings.Join(labels, ","); got != "Under $100,Chase" {
		t.Errorf("old filters on the results line: %s", got)
	}
	if got := strings.Join(removes, " "); got != "/cards/?cat=travel&issuer=chase&sort=name /cards/?cat=travel&fee=under-100&sort=name" {
		t.Errorf("removing one keeps the rest: %s", got)
	}
	// Turning the toggle on replaces the band, which it could only contradict.
	if ix.NoFee.Active || ix.NoFee.URL != "/cards/?cat=travel&fee=none&issuer=chase&sort=name" {
		t.Errorf("toggle with a fee band: %+v", ix.NoFee)
	}
	if !ix.Filtered || ix.ClearURL != "/cards/?sort=name" {
		t.Errorf("Clear: %v %s", ix.Filtered, ix.ClearURL)
	}

	issuer := buildCardsIndex(indexFixture(), q("issuer=chase"))
	if got := strings.Join(shownSlugs(issuer), ","); got != "edge-100,csp,edge-400" {
		t.Errorf("issuer=chase: got %s", got)
	}
	if !issuer.Grouped || len(issuer.Legacy) != 1 || issuer.Legacy[0].Label != "Chase" || issuer.Legacy[0].RemoveURL != "/cards/" {
		t.Errorf("issuer alone: grouped=%v legacy=%+v", issuer.Grouped, issuer.Legacy)
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
	// Every sort but the default is one list without category headings.
	for _, s := range []string{"name", "fee-asc", "fee-desc"} {
		if ix := buildCardsIndex(indexFixture(), q("sort="+s)); ix.Grouped || ix.Count != 10 {
			t.Errorf("sort=%s: grouped=%v count=%d", s, ix.Grouped, ix.Count)
		}
	}
	// A sort never changes which cards show.
	if got := buildCardsIndex(indexFixture(), q("fee=none&sort=fee-desc")).Count; got != 2 {
		t.Errorf("sorted no-fee view: %d cards", got)
	}
	// The menu: the default first, each option a URL that keeps the filters.
	ix := buildCardsIndex(indexFixture(), q("cat=cash-back&sort=fee-asc"))
	var opts []string
	for _, o := range ix.Sorts {
		sel := ""
		if o.Selected {
			sel = "*"
		}
		opts = append(opts, sel+o.Label+"="+o.URL)
	}
	want := "By category=/cards/?cat=cash-back,Name (A–Z)=/cards/?cat=cash-back&sort=name," +
		"*Lowest fee=/cards/?cat=cash-back&sort=fee-asc,Highest fee=/cards/?cat=cash-back&sort=fee-desc"
	if got := strings.Join(opts, ","); got != want {
		t.Errorf("sort menu:\n got %s\nwant %s", got, want)
	}
	// An unknown sort is the default.
	if bad := buildCardsIndex(indexFixture(), q("sort=random")); !bad.Grouped || bad.Query.Sort != "" || !bad.Sorts[0].Selected {
		t.Errorf("unknown sort: %+v", bad.Query)
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
			p.WelcomeBonusVerifiedAt = &seedVerifiedAt // cmd/seed-structured sets it with the fee
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
		// One row of filters: category chips with counts, then the toggle.
		`<div class="filter-bar cards-cat-bar" role="group" aria-label="Category">`,
		`<a href="/cards/" class="filter-chip active" aria-current="true">All <span class="filter-count">52</span></a>`,
		`href="/cards/?cat=cash-back" class="filter-chip">Cash Back <span class="filter-count">9</span>`,
		`<a href="/cards/?fee=none" class="filter-chip filter-toggle" role="button" aria-pressed="false">No annual fee</a>`,
		// The results line: the count, then a labelled sort menu that
		// defaults to the grouped view, with Go for browsers without JS.
		`<span class="cards-results-count">52 cards</span>`,
		`<label for="cards-sort-select">Sort</label>`,
		`<select id="cards-sort-select" name="sort" data-sort-menu>`,
		`<option value="" data-url="/cards/" selected>By category</option>`,
		`<option value="name" data-url="/cards/?sort=name">Name (A–Z)</option>`,
		`<option value="fee-asc" data-url="/cards/?sort=fee-asc">Lowest fee</option>`,
		`<option value="fee-desc" data-url="/cards/?sort=fee-desc">Highest fee</option>`,
		`<button type="submit" class="cards-sort-go">Go</button>`,
		`<h2 class="category-heading">Travel &amp; Premium</h2>`,
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
	// The fee bands, issuer menu and Apply button are gone, and with nothing
	// filtered there is nothing to clear.
	for _, gone := range []string{
		"filter-switch", `name="issuer"`, ">Apply<", `class="filter-clear"`, `class="filter-remove"`, "Under $100",
	} {
		if strings.Contains(body, gone) {
			t.Errorf("/cards/ should no longer contain %q", gone)
		}
	}
	if got := strings.Count(body, `class="category-heading"`); got != 6 {
		t.Errorf("the default sort groups by category: %d headings", got)
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
		// The toggle is pressed, and pressing it again keeps the category
		// and the sort.
		`<a href="/cards/?cat=cash-back&amp;sort=fee-asc" class="filter-chip filter-toggle active" role="button" aria-pressed="true">No annual fee</a>`,
		// Chips keep the toggle and the sort; the active one removes itself.
		`<a href="/cards/?cat=hotel&amp;fee=none&amp;sort=fee-asc" class="filter-chip`,
		`<a href="/cards/?fee=none&amp;sort=fee-asc" class="filter-chip active" aria-current="true">Cash Back`,
		// The results line names the filters and clears them, keeping the sort.
		`<span class="cards-results-applied">Cash Back</span>`,
		`<span class="cards-results-applied">No annual fee</span>`,
		`<a href="/cards/?sort=fee-asc" class="filter-clear">Clear</a>`,
		// The sort menu keeps the filters, with and without JavaScript.
		`<input type="hidden" name="cat" value="cash-back">`,
		`<input type="hidden" name="fee" value="none">`,
		`<option value="fee-asc" data-url="/cards/?cat=cash-back&amp;fee=none&amp;sort=fee-asc" selected>Lowest fee</option>`,
		`<option value="" data-url="/cards/?cat=cash-back&amp;fee=none">By category</option>`,
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
	if strings.Contains(body, `class="filter-remove"`) {
		t.Error("filters with a chip are not repeated as removable items")
	}

	none := getPath(t, app, "/cards/?cat=hotel&fee=none&issuer=discover&sort=name")
	if !strings.Contains(none, "No cards match these filters.") || !strings.Contains(none, `<a href="/cards/?sort=name">Show all cards</a>`) {
		t.Error("an empty result should say so and offer to clear")
	}
}

// The sorts reorder the same cards as one list; the default groups them.
func TestHandleCardsList_Sorts(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)
	titles := func(body string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`<h3 class="card-tile-title">([^<]*)</h3>`).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
		return out
	}
	fees := func(body string) []int {
		var out []int
		for _, m := range regexp.MustCompile(`(?s)class="card-tile-facts">\s*<span class="card-tile-fact">(?:<b>\$([\d,]+)</b>|<b>No</b>) annual fee`).FindAllStringSubmatch(body, -1) {
			n, _ := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
			out = append(out, n)
		}
		return out
	}
	for _, sortBy := range []string{"name", "fee-asc", "fee-desc"} {
		body := getPath(t, app, "/cards/?sort="+sortBy)
		if strings.Count(body, `class="card-tile"`) != 52 || strings.Contains(body, `class="category-heading"`) {
			t.Errorf("sort=%s: every card, one list without headings", sortBy)
		}
		if !strings.Contains(body, `<option value="`+sortBy+`" data-url="/cards/?sort=`+sortBy+`" selected>`) {
			t.Errorf("sort=%s is selected in the menu", sortBy)
		}
		if !strings.Contains(body, `<a href="/cards/?cat=hotel&amp;sort=`+sortBy+`" class="filter-chip">`) ||
			!strings.Contains(body, `<a href="/cards/?fee=none&amp;sort=`+sortBy+`" class="filter-chip filter-toggle"`) {
			t.Errorf("sort=%s: the chips and the toggle keep the sort", sortBy)
		}
		if strings.Contains(body, `class="filter-clear"`) {
			t.Errorf("sort=%s: a sort is not a filter, nothing to clear", sortBy)
		}
	}
	names := titles(getPath(t, app, "/cards/?sort=name"))
	if !sort.SliceIsSorted(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) }) {
		t.Errorf("by name: %v", names)
	}
	asc := fees(getPath(t, app, "/cards/?sort=fee-asc"))
	if len(asc) != 52 || !sort.IntsAreSorted(asc) {
		t.Errorf("lowest fee first: %v", asc)
	}
	desc := fees(getPath(t, app, "/cards/?sort=fee-desc"))
	if len(desc) != 52 || !sort.SliceIsSorted(desc, func(i, j int) bool { return desc[i] > desc[j] }) {
		t.Errorf("highest fee first: %v", desc)
	}
	// An unknown sort is the default grouped view.
	bad := getPath(t, app, "/cards/?sort=random")
	if strings.Count(bad, `class="category-heading"`) != 6 || !strings.Contains(bad, `<option value="" data-url="/cards/" selected>By category</option>`) {
		t.Error("an unknown sort falls back to By category")
	}
}

// Links from before the simpler filters keep working: a fee band or an issuer
// still filters and shows on the results line with a link that removes it.
func TestHandleCardsList_LegacyLinks(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)

	band := getPath(t, app, "/cards/?cat=travel&fee=under-100&sort=name")
	if n := strings.Count(band, `class="card-tile"`); n == 0 || n >= 10 {
		t.Errorf("travel under $100: %d of the 10 travel cards", n)
	}
	for _, want := range []string{
		`<a href="/cards/?cat=travel&amp;sort=name" class="filter-remove" aria-label="Remove filter: Under $100">Under $100<span class="filter-remove-x" aria-hidden="true">✕</span></a>`,
		`<input type="hidden" name="fee" value="under-100">`,
		// The toggle replaces the band and is not pressed by it.
		`<a href="/cards/?cat=travel&amp;fee=none&amp;sort=name" class="filter-chip filter-toggle" role="button" aria-pressed="false">`,
		`<a href="/cards/?sort=name" class="filter-clear">Clear</a>`,
	} {
		if !strings.Contains(band, want) {
			t.Errorf("?fee=under-100 should contain %q", want)
		}
	}

	issuer := getPath(t, app, "/cards/?issuer=chase&fee=none")
	n := strings.Count(issuer, `class="card-tile"`)
	if n == 0 || n != strings.Count(issuer, `<span class="issuer-badge">Chase</span>`) {
		t.Errorf("issuer=chase&fee=none: %d tiles, not all Chase", n)
	}
	for _, want := range []string{
		`<a href="/cards/?fee=none" class="filter-remove" aria-label="Remove filter: Chase">Chase`,
		`<span class="cards-results-applied">No annual fee</span>`,
		`<input type="hidden" name="issuer" value="chase">`,
		`<a href="/cards/?cat=cash-back&amp;fee=none&amp;issuer=chase" class="filter-chip">`,
	} {
		if !strings.Contains(issuer, want) {
			t.Errorf("?issuer=chase should contain %q", want)
		}
	}
	// Removing the issuer keeps the toggle.
	if after := getPath(t, app, "/cards/?fee=none"); strings.Count(after, `class="card-tile"`) != 12 {
		t.Error("no annual fee without the issuer: the 12 free cards")
	}
}

// A $0 fee that was recorded is "No annual fee"; a fee that was never
// recorded (the column's default 0) shows nothing and stays out of the "No
// annual fee" toggle and the fee bands, though the card is still listed.
func TestCardsIndex_UnknownFeeIsNotNoFee(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	known := indexPage("zero-fee", "Zero Fee Card", "cash-back", "Citi", 0, `{"base":{"rate":"2%"}}`, "$200 bonus after $500 spend")
	unknown := indexPage("unknown-fee", "Unknown Fee Card", "cash-back", "Citi", 0, `{"base":{"rate":"1.5%"}}`, "$100 bonus after $500 spend")
	unknown.WelcomeBonusVerifiedAt = nil // rates and offer entered, the fee never was
	for _, p := range []db.ReferencePage{known, unknown} {
		p.SectionsJSON = `[{"key":"overview","title":"Overview","content":"<p>Body.</p>"}]`
		if _, err := app.db.CreateReferencePage(&p); err != nil {
			t.Fatal(err)
		}
	}
	tile := func(body, slug string) string {
		m := regexp.MustCompile(`(?s)<a href="/cards/` + slug + `" class="card-tile">.*?</a>`).FindString(body)
		if m == "" {
			t.Fatalf("no tile for %s", slug)
		}
		return m
	}

	all := getPath(t, app, "/cards/")
	if !strings.Contains(tile(all, "zero-fee"), "<b>No</b> annual fee") {
		t.Error("a recorded $0 fee reads 'No annual fee'")
	}
	u := tile(all, "unknown-fee")
	if strings.Contains(u, "annual fee") || strings.Contains(u, "$0") {
		t.Errorf("an unknown fee must show nothing: %s", u)
	}
	if !strings.Contains(u, "<b>1.5%</b> everything") {
		t.Error("the card's other facts still show")
	}

	noFee := getPath(t, app, "/cards/?fee=none")
	if !strings.Contains(noFee, `href="/cards/zero-fee"`) || strings.Contains(noFee, `href="/cards/unknown-fee"`) {
		t.Error("No annual fee holds the recorded $0 fee only")
	}
	if !strings.Contains(getPath(t, app, "/cards/?fee=any"), `href="/cards/unknown-fee"`) {
		t.Error("Any still lists the card with an unknown fee")
	}
	for _, by := range []string{"fee-asc", "fee-desc"} {
		b := getPath(t, app, "/cards/?sort="+by)
		if strings.Index(b, `href="/cards/unknown-fee"`) < strings.Index(b, `href="/cards/zero-fee"`) {
			t.Errorf("sort=%s: an unknown fee sorts last, after $0", by)
		}
	}

	// The card page's summary: no fee cell for the unknown fee.
	if body := getCardPage(t, app, "unknown-fee"); strings.Contains(body, "<dt>Annual fee</dt>") || !strings.Contains(body, "<dt>Earns</dt>") {
		t.Error("unknown fee: no Annual fee cell, the other facts stay")
	}
	if body := getCardPage(t, app, "zero-fee"); !strings.Contains(body, `<dt>Annual fee</dt><dd><span class="card-fact-value">$0</span><span class="card-fact-detail">No annual fee</span>`) {
		t.Error("recorded $0 fee: the summary says $0, no annual fee")
	}
}

func TestAnnualFeeKnown(t *testing.T) {
	for _, c := range []struct {
		name string
		p    db.ReferencePage
		want bool
	}{
		{"fee above 0", db.ReferencePage{AnnualFeeAmount: 95}, true},
		{"$0 with a note", db.ReferencePage{AnnualFeeNote: "No annual fee"}, true},
		{"$0 with structured data written", db.ReferencePage{WelcomeBonusVerifiedAt: &seedVerifiedAt}, true},
		{"never filled in", db.ReferencePage{}, false},
		{"rates but no fee record", db.ReferencePage{EarningRatesJSON: `{"base":{"rate":"1x"}}`}, false},
	} {
		if got := annualFeeKnown(&c.p); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
