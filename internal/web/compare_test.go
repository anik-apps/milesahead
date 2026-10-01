package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/seeddata"
)

// compareApp is the real-template app holding the repository's 52 cards
// with their comparison data.
func compareApp(t *testing.T) *App {
	t.Helper()
	app := setupRealTemplateCardApp(t)
	if _, err := seeddata.Cards(app.db); err != nil {
		t.Fatal(err)
	}
	return app
}

// compareRowCells returns the cells of the row labelled label, or nil.
func compareRowCells(t *testing.T, body, label string) []string {
	t.Helper()
	row := regexp.MustCompile(`(?s)<tr role="row"[^>]*>\s*<th scope="row" role="rowheader" class="compare-label-col">` +
		regexp.QuoteMeta(label) + `</th>(.*?)</tr>`).FindStringSubmatch(body)
	if row == nil {
		return nil
	}
	var cells []string
	for _, m := range regexp.MustCompile(`(?s)<td role="cell">(.*?)</td>`).FindAllStringSubmatch(row[1], -1) {
		cells = append(cells, m[1])
	}
	return cells
}

// winners returns which cells of a row carry the star.
func winners(cells []string) []bool {
	out := make([]bool, len(cells))
	for i, c := range cells {
		out[i] = strings.Contains(c, "is-winner")
	}
	return out
}

func boolsEqual(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const premiumTrio = "/compare?cards=chase-sapphire-reserve,amex-platinum,capital-one-venture-x"

// Every caveat is printed under its value; nothing is a hover-only title.
func TestCompare_NotesAreVisible(t *testing.T) {
	app := compareApp(t)
	body := getPage(t, app, premiumTrio)
	table := regexp.MustCompile(`(?s)<table class="compare-table.*?</table>`).FindString(body)
	if table == "" {
		t.Fatal("no comparison table")
	}
	if strings.Contains(table, `title="`) || strings.Contains(table, "&#9432;") || strings.Contains(table, "ⓘ") {
		t.Error("notes must not be hover-only titles")
	}
	lounge := compareRowCells(t, body, "Lounge access")
	if len(lounge) != 3 || !strings.Contains(lounge[1], `<span class="compare-note">Centurion Lounges`) {
		t.Errorf("lounge access should show which lounges under the value: %v", lounge)
	}
	fee := compareRowCells(t, body, "Annual fee")
	if !strings.Contains(fee[0], `<span class="compare-note">Increased from $550 in June 2025</span>`) {
		t.Errorf("the fee note should be visible: %v", fee)
	}
	if n := strings.Count(table, `<span class="compare-note">`); n < 40 {
		t.Errorf("only %d visible notes", n)
	}
}

// Direct and portal rates are separate rows, each with its own winner; text
// values never get a star; the lowest fee wins.
func TestCompare_Winners(t *testing.T) {
	app := compareApp(t)
	body := getPage(t, app, premiumTrio)
	for _, c := range []struct {
		label string
		want  []bool
	}{
		{"Annual fee", []bool{false, false, true}},              // $795, $895, $395
		{"Airlines, direct", []bool{false, true, false}},        // 4x, 5x, 2x
		{"Airlines, travel portal", []bool{true, false, false}}, // 8x, 5x, 5x
		{"Hotels, travel portal", []bool{false, false, true}},   // 8x, 5x, 10x
		{"Extended warranty", []bool{false, false, false}},      // "1 year", "1 year", "Yes"
		{"Purchase protection", []bool{false, false, false}},    // per item vs per incident vs Yes
		{"Hotel credit", []bool{false, true, false}},            // $500/yr, $600/yr, none
		{"Lounge access", []bool{false, false, false}},          // Yes, Yes, Yes
	} {
		cells := compareRowCells(t, body, c.label)
		if got := winners(cells); !boolsEqual(got, c.want) {
			t.Errorf("%s: stars %v, want %v\n%v", c.label, got, c.want, cells)
		}
	}
	if compareRowCells(t, body, "Airlines (D|P)") != nil || strings.Contains(body, " D | ") {
		t.Error("the combined D|P rows should be gone")
	}
	if !strings.Contains(body, `<strong>Travel portal:</strong>`) {
		t.Error("the legend should explain direct and portal")
	}
}

// A rate that only applies to a top or rotating category doesn't win its
// row, and its condition is shown.
func TestCompare_ConditionalRatesDoNotWin(t *testing.T) {
	app := compareApp(t)
	body := getPage(t, app, "/compare?cards=chase-freedom-flex,citi-custom-cash,discover-it-cash-back")
	dining := compareRowCells(t, body, "Dining")
	if got := winners(dining); !boolsEqual(got, []bool{false, false, false}) {
		t.Errorf("Custom Cash's 5%% is only for the top category: stars %v", got)
	}
	if !strings.Contains(dining[1], "If top category") {
		t.Errorf("the condition should be visible: %v", dining[1])
	}
	if got := winners(compareRowCells(t, body, "Airlines, travel portal")); !boolsEqual(got, []bool{true, false, false}) {
		t.Errorf("only the Flex earns through a portal: %v", got)
	}
	// Rows none of the three has are left out, and the section says so.
	if compareRowCells(t, body, "Companion certificate") != nil {
		t.Error("a row no card has should be hidden")
	}
	if !strings.Contains(body, `rows hidden: none of these cards has them`) {
		t.Error("the section should count the hidden rows")
	}
}

// Section headers are buttons that start open; rows link to card pages;
// catch-all rows are named for their section; rows where every card is the
// same are marked for "Only differences".
func TestCompare_Structure(t *testing.T) {
	app := compareApp(t)
	body := getPage(t, app, premiumTrio)
	for _, key := range []string{"earning", "credits", "benefits", "protections"} {
		btn := `<button type="button" class="compare-section-toggle" aria-expanded="true" aria-controls="compare-sec-` + key + `">`
		if !strings.Contains(body, btn) || !strings.Contains(body, `id="compare-sec-`+key+`"`) {
			t.Errorf("section %s needs an open toggle button and its rows", key)
		}
	}
	if strings.Contains(body, `style="display:none`) {
		t.Error("no section should start hidden")
	}
	for _, slug := range []string{"chase-sapphire-reserve", "amex-platinum", "capital-one-venture-x"} {
		if strings.Count(body, `href="/cards/`+slug+`"`) < 2 {
			t.Errorf("%s should link to its card page from the header and the end", slug)
		}
	}
	if strings.Contains(body, "Niche Credits") || strings.Contains(body, "Niche Rates") {
		t.Error(`"Niche" labels should be gone`)
	}
	for _, label := range []string{"Other bonus categories", "Other credits", "Other benefits", "Other protections", "Rewards currency"} {
		if compareRowCells(t, body, label) == nil {
			t.Errorf("want a %q row", label)
		}
	}
	if strings.Contains(body, ": Yes") {
		t.Error(`list items should not end in ": Yes"`)
	}
	if !regexp.MustCompile(`<tr role="row" data-same>\s*<th scope="row" role="rowheader" class="compare-label-col">Global Entry / TSA PreCheck</th>`).MatchString(body) {
		t.Error("a row where every card is the same should be marked data-same")
	}
	if regexp.MustCompile(`<tr role="row" data-same>\s*<th scope="row" role="rowheader" class="compare-label-col">Dining</th>`).MatchString(body) {
		t.Error("a row that differs must not be marked")
	}
	if !strings.Contains(body, `<label class="compare-diff-toggle" hidden>`) {
		t.Error("the differences switch needs JavaScript, so it starts hidden")
	}
	// The change-cards form has the current cards selected.
	change := regexp.MustCompile(`(?s)<details class="compare-change">.*?</details>`).FindString(body)
	for _, sel := range []string{`<option value="chase-sapphire-reserve" selected>`, `<option value="amex-platinum" selected>`, `<option value="capital-one-venture-x" selected>`} {
		if !strings.Contains(change, sel) {
			t.Errorf("change form should preselect %s", sel)
		}
	}
}

// The picker is a GET form that works without JavaScript: its fields
// redirect to the canonical URL.
func TestCompare_PickerWithoutJavaScript(t *testing.T) {
	app := compareApp(t)
	req := httptest.NewRequest(http.MethodGet, "/compare?card1=amex-gold&card2=chase-sapphire-preferred&card3=", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/compare?cards=amex-gold,chase-sapphire-preferred" {
		t.Fatalf("got %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	body := getPage(t, app, "/compare")
	form := regexp.MustCompile(`(?s)<form class="compare-picker" action="/compare" method="GET">.*?</form>`).FindString(body)
	for _, want := range []string{`name="card1"`, `name="card2"`, `name="card3"`, `<optgroup label="American Express">`, `<optgroup label="Chase">`, `<button type="submit" class="compare-submit">Compare</button>`} {
		if !strings.Contains(form, want) {
			t.Errorf("picker should contain %s", want)
		}
	}
	if !strings.Contains(body, `href="/compare?cards=chase-sapphire-reserve,amex-platinum,capital-one-venture-x"`) {
		t.Error("the empty page should offer popular comparisons")
	}
	if strings.Contains(body, `class="compare-notice"`) {
		t.Error("no notice before the reader picks anything")
	}
}

// A choice that can't be compared says why and keeps what was picked.
func TestCompare_InvalidChoices(t *testing.T) {
	app := compareApp(t)
	for _, c := range []struct {
		path, notice, kept string
	}{
		{"/compare?cards=amex-gold,amex-gold", "You picked the same card twice", "amex-gold"},
		{"/compare?card1=amex-gold&card2=amex-gold", "You picked the same card twice", "amex-gold"},
		{"/compare?cards=amex-gold,no-such-card", "We couldn&#39;t find one of those cards", "amex-gold"},
		{"/compare?cards=amex-gold", "Choose at least two cards", "amex-gold"},
	} {
		body := getPage(t, app, c.path)
		if !strings.Contains(body, `<p class="compare-notice" role="alert">`+c.notice) {
			t.Errorf("%s: want notice %q", c.path, c.notice)
		}
		card1 := regexp.MustCompile(`(?s)<select name="card1".*?</select>`).FindString(body)
		if !strings.Contains(card1, `<option value="`+c.kept+`" selected>`) {
			t.Errorf("%s: card 1 should keep %s", c.path, c.kept)
		}
		if strings.Contains(body, `<table class="compare-table`) {
			t.Errorf("%s: no table without two cards", c.path)
		}
	}
}

// Extra keys come in a fixed order (they used to follow Go's random map
// order), after the known ones.
func TestGenericRows_StableOrder(t *testing.T) {
	maps := []map[string]json.RawMessage{
		{"zeta_perk": json.RawMessage(`{"value":"Yes"}`), "lounge_access": json.RawMessage(`{"value":"Yes"}`)},
		{"alpha_perk": json.RawMessage(`{"value":"Yes"}`), "mid_perk": json.RawMessage(`{"value":"No"}`)},
	}
	for i := 0; i < 20; i++ {
		var labels []string
		for _, r := range genericRows(maps, "benefits") {
			labels = append(labels, r.Label)
		}
		got := strings.Join(labels[len(labels)-4:], ",")
		if got != "Alpha perk,Mid perk,Zeta perk,Other benefits" {
			t.Fatalf("row order = %v", labels)
		}
	}
}

func TestMarkWinners(t *testing.T) {
	cell := func(v, note string) compareCell { return compareCell{Value: v, Note: note} }
	cases := []struct {
		name  string
		cells []compareCell
		lower bool
		want  []bool
	}{
		{"highest rate", []compareCell{cell("3x", ""), cell("2x", "")}, false, []bool{true, false}},
		{"tie for best", []compareCell{cell("3x", ""), cell("3x", ""), cell("1x", "")}, false, []bool{true, true, false}},
		{"all equal", []compareCell{cell("2x", ""), cell("2x", "")}, false, []bool{false, false}},
		{"none beats zero", []compareCell{cell("$200/yr", ""), {}}, false, []bool{true, false}},
		{"mixed units", []compareCell{cell("3x", ""), cell("5%", "")}, false, []bool{false, false}},
		{"text", []compareCell{cell("1 year", ""), cell("Yes", "")}, false, []bool{false, false}},
		{"per item vs per trip", []compareCell{cell("$500/item", ""), cell("$3K/trip", "")}, false, []bool{false, false}},
		{"thousands", []compareCell{cell("$3K/trip", ""), cell("$500/trip", "")}, false, []bool{true, false}},
		{"conditional best", []compareCell{cell("3%", ""), cell("5%", "If top category; $500/billing cycle cap")}, false, []bool{false, false}},
		{"conditional loser", []compareCell{cell("5%", ""), cell("1%", "Base rate (may be 5% if rotating quarterly category)")}, false, []bool{true, false}},
		{"cap is not a condition", []compareCell{cell("5x", "Booked directly with airlines, up to $500K/yr"), cell("4x", "")}, false, []bool{true, false}},
		{"lowest fee", []compareCell{cell("$795", ""), cell("$395", "")}, true, []bool{false, true}},
		{"no fee is lowest", []compareCell{cell("$0", ""), cell("$95", "")}, true, []bool{true, false}},
		{"one card", []compareCell{cell("3x", "")}, false, []bool{false}},
	}
	for _, c := range cases {
		markWinners(c.cells, c.lower)
		got := make([]bool, len(c.cells))
		for i, cl := range c.cells {
			got[i] = cl.IsWinner
		}
		if !boolsEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPartnersNote(t *testing.T) {
	for _, c := range []struct {
		tp   transferPartnersEntry
		want string
	}{
		{transferPartnersEntry{Program: "Amex Membership Rewards", Count: "20", KeyPartners: []string{"ANA", "Delta", "Hilton", "Marriott", "British Airways"}},
			"20 transfer partners, including ANA, Delta, Hilton and Marriott"},
		{transferPartnersEntry{Program: "Capital One Miles", Count: "15+", KeyPartners: []string{"Air Canada"}}, "15+ transfer partners, including Air Canada"},
		{transferPartnersEntry{Program: "IHG One Rewards", Count: "N/A", Note: "Earns IHG One Rewards directly, no transfer partners"}, "Earns IHG One Rewards directly, no transfer partners"},
		{transferPartnersEntry{Program: "Marriott Bonvoy", Count: "38 airlines, mostly 3:1"}, "38 airlines, mostly 3:1"},
	} {
		if got := partnersNote(c.tp); got != c.want {
			t.Errorf("partnersNote(%+v) = %q, want %q", c.tp, got, c.want)
		}
	}
}
