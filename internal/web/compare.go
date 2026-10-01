package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/kumaranik/milesahead/internal/db"
)

// --- Compare page ---
//
// /compare?cards=a,b[,c] shows two or three cards side by side. Every note
// is printed under its value (none is hover-only), sections are real
// buttons, and the picker is a plain GET form that works without JavaScript.

// compareMaxCards is how many cards one comparison holds.
const compareMaxCards = 3

// compareItem is one entry of a list cell ("Other credits"), with its note.
type compareItem struct {
	Text string
	Note string
}

// compareCell holds a single cell value in the comparison table.
type compareCell struct {
	Value    string // "" when the card has nothing here (shown as a dash)
	Note     string
	Items    []compareItem // list cells; Value is "" when set
	IsWinner bool
}

// Empty reports whether the card has nothing in this row.
func (c compareCell) Empty() bool { return c.Value == "" && len(c.Items) == 0 }

// key identifies the cell's content, to find rows where every card is the
// same.
func (c compareCell) key() string {
	var b strings.Builder
	b.WriteString(c.Value + "\x00" + c.Note)
	for _, it := range c.Items {
		b.WriteString("\x00" + it.Text + "\x00" + it.Note)
	}
	return b.String()
}

// compareRow holds a label and one cell per card.
type compareRow struct {
	Label string
	Cells []compareCell
	// Same is set when every card has the same value: the row the "Only
	// differences" switch hides.
	Same bool
}

// compareSection is a titled, collapsible group of rows. Hidden counts the
// rows left out because none of the cards has them.
type compareSection struct {
	Key    string
	Title  string
	Rows   []compareRow
	Hidden int
}

// compareCardView holds parsed card data ready for the template.
type compareCardView struct {
	ID                 int64
	Slug               string
	Title              string
	Issuer             string
	ImageURL           string
	Fee                string
	ScoutsVerdictShort string
}

// earningRateCategories defines the display order and labels for earning
// rate keys. formatTopEarning (article card embeds) uses it too.
var earningRateCategories = []struct {
	Key   string
	Label string
}{
	{"dining", "Dining"},
	{"groceries", "Groceries"},
	{"gas", "Gas"},
	{"airlines", "Airlines"},
	{"hotels", "Hotels"},
	{"car_rental", "Car Rental"},
	{"other_travel", "Other Travel"},
	{"streaming", "Streaming"},
	{"base", "Everything Else"},
}

// directPortalPairs maps base keys that have direct/portal variants.
var directPortalPairs = map[string]bool{
	"airlines":     true,
	"hotels":       true,
	"car_rental":   true,
	"other_travel": true,
}

// genericCategories defines known keys and labels for credits, benefits,
// protections, in display order.
var genericCategories = map[string][]struct {
	Key   string
	Label string
}{
	"credits": {
		{"airline", "Airline credit"},
		{"hotel", "Hotel credit"},
		{"travel_other", "Other travel credit"},
		{"dining", "Dining credit"},
		{"streaming", "Streaming credit"},
	},
	"benefits": {
		{"lounge_access", "Lounge access"},
		{"hotel_status", "Hotel status"},
		{"global_entry_tsa", "Global Entry / TSA PreCheck"},
		{"companion_cert", "Companion certificate"},
		{"free_checked_bags", "Free checked bags"},
		{"no_ftf", "No foreign transaction fee"},
	},
	"protections": {
		{"trip_delay", "Trip delay"},
		{"baggage_delay_loss", "Baggage delay / loss"},
		{"car_rental", "Car rental insurance"},
		{"purchase_protection", "Purchase protection"},
		{"extended_warranty", "Extended warranty"},
	},
}

// nicheLabels names each section's catch-all row ("niche" in the data).
var nicheLabels = map[string]string{
	"earning":     "Other bonus categories",
	"credits":     "Other credits",
	"benefits":    "Other benefits",
	"protections": "Other protections",
}

// rateEntry is the JSON shape for a single earning rate value.
type rateEntry struct {
	Rate string `json:"rate"`
	Note string `json:"note"`
}

// transferPartnersEntry is the JSON shape for the transfer_partners key.
type transferPartnersEntry struct {
	Program     string   `json:"program"`
	Count       string   `json:"count"`
	KeyPartners []string `json:"key_partners"`
	Note        string   `json:"note"`
}

// genericEntry is the JSON shape for a credit/benefit/protection value.
type genericEntry struct {
	Amount string `json:"amount"`
	Value  string `json:"value"`
	Note   string `json:"note"`
}

// nicheEntry is the JSON shape for items in the "niche" array.
type nicheEntry struct {
	Label string `json:"label"`
	Note  string `json:"note"`
	Value string `json:"value"`
}

// popularComparisons are offered on the empty compare page. URLs are
// template.URL so the commas aren't escaped.
var popularComparisons = []struct {
	Label string
	URL   template.URL
}{
	{"Sapphire Reserve vs Platinum vs Venture X", "/compare?cards=chase-sapphire-reserve,amex-platinum,capital-one-venture-x"},
	{"Sapphire Preferred vs Venture", "/compare?cards=chase-sapphire-preferred,capital-one-venture"},
	{"Amex Gold vs Sapphire Preferred", "/compare?cards=amex-gold,chase-sapphire-preferred"},
	{"Freedom Flex vs Custom Cash vs Discover it", "/compare?cards=chase-freedom-flex,citi-custom-cash,discover-it-cash-back"},
	{"Venture X vs Bilt Palladium", "/compare?cards=capital-one-venture-x,bilt-palladium"},
}

// pickerOption is one card in the picker.
type pickerOption struct {
	Slug  string
	Title string
}

// pickerGroup is one issuer's cards in the picker.
type pickerGroup struct {
	Issuer string
	Cards  []pickerOption
}

// pickerSlot is one of the picker's three selects.
type pickerSlot struct {
	N        int
	Name     string
	Selected string
	Optional bool
}

// issuerNames spells out issuers stored by their short name.
var issuerNames = map[string]string{
	"Amex": "American Express",
	"BofA": "Bank of America",
}

// pickerGroups groups the cards by issuer, issuers and cards alphabetical.
func pickerGroups(pages []db.ReferencePage) []pickerGroup {
	byIssuer := map[string][]pickerOption{}
	for _, p := range pages {
		issuer := p.Issuer
		if full, ok := issuerNames[issuer]; ok {
			issuer = full
		}
		if issuer == "" {
			issuer = "Other"
		}
		byIssuer[issuer] = append(byIssuer[issuer], pickerOption{Slug: p.Slug, Title: p.Title})
	}
	var groups []pickerGroup
	for issuer, cards := range byIssuer {
		sort.Slice(cards, func(i, j int) bool { return cards[i].Title < cards[j].Title })
		groups = append(groups, pickerGroup{Issuer: issuer, Cards: cards})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Issuer < groups[j].Issuer })
	return groups
}

// compareSlugs reads the cards to compare: ?cards=a,b,c, or the picker's
// card1..card3 fields. It keeps up to three, in order, without blanks.
func compareSlugs(r *http.Request) (slugs []string, duplicate bool) {
	q := r.URL.Query()
	var raw []string
	if c := strings.TrimSpace(q.Get("cards")); c != "" {
		raw = strings.Split(c, ",")
	} else {
		for i := 1; i <= compareMaxCards; i++ {
			raw = append(raw, q.Get("card"+strconv.Itoa(i)))
		}
	}
	seen := map[string]bool{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 100 {
			continue
		}
		if seen[s] {
			duplicate = true
			continue
		}
		seen[s] = true
		slugs = append(slugs, s)
		if len(slugs) == compareMaxCards {
			break
		}
	}
	return slugs, duplicate
}

func (app *App) handleCompare(w http.ResponseWriter, r *http.Request) {
	allPages, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		http.Error(w, "Failed to load cards", http.StatusInternalServerError)
		return
	}

	slugs, duplicate := compareSlugs(r)
	// The picker submits card1..card3: send it to the canonical URL.
	if r.URL.Query().Get("cards") == "" && len(slugs) >= 2 && !duplicate {
		http.Redirect(w, r, "/compare?cards="+strings.Join(slugs, ","), http.StatusSeeOther)
		return
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	data := map[string]interface{}{
		"Title":           "Compare Cards",
		"MetaDescription": "Compare credit cards side by side.",
		"CanonicalURL":    "https://" + domain + "/compare",
		"OGType":          "website",
		"PickerGroups":    pickerGroups(allPages),
	}

	var cards []db.ComparisonCard
	if len(slugs) >= 2 {
		if cards, err = app.db.GetComparisonData(slugs); err != nil {
			http.Error(w, "Failed to load comparison data", http.StatusInternalServerError)
			return
		}
	}

	if len(cards) < 2 {
		// The picker, keeping what the reader chose, with a note on why
		// there is no comparison when they chose something.
		known := map[string]bool{}
		for _, p := range allPages {
			known[p.Slug] = true
		}
		var kept []string
		unknown := 0
		for _, s := range slugs {
			if known[s] {
				kept = append(kept, s)
			} else {
				unknown++
			}
		}
		switch {
		case duplicate && len(kept) < 2:
			data["Notice"] = "You picked the same card twice. Choose two different cards."
		case unknown > 0:
			data["Notice"] = "We couldn't find one of those cards. Choose it again below."
		case len(slugs) == 1:
			data["Notice"] = "Choose at least two cards to compare."
		}
		data["SelectorMode"] = true
		data["PickerSlots"] = pickerSlots(kept)
		data["Popular"] = popularComparisons
		app.renderTemplate(w, "compare.html", data)
		return
	}

	views := make([]compareCardView, len(cards))
	current := make([]string, len(cards))
	for i, c := range cards {
		views[i] = compareCardView{
			ID: c.ID, Slug: c.Slug, Title: c.Title, Issuer: c.Issuer, ImageURL: c.ImageURL,
			Fee:                feeLabel(c.AnnualFeeAmount),
			ScoutsVerdictShort: truncateSentence(c.ScoutsVerdict, 250),
		}
		current[i] = c.Slug
	}

	data["SelectorMode"] = false
	data["Cards"] = views
	data["ColSpan"] = len(cards) + 1
	data["Overview"] = overviewRows(cards)
	data["Sections"] = buildCompareSections(cards)
	data["PickerSlots"] = pickerSlots(current)
	app.renderTemplate(w, "compare.html", data)
}

// pickerSlots fills the picker's three selects with the chosen cards.
func pickerSlots(chosen []string) []pickerSlot {
	slots := make([]pickerSlot, compareMaxCards)
	for i := range slots {
		slots[i] = pickerSlot{N: i + 1, Name: "card" + strconv.Itoa(i+1), Optional: i == compareMaxCards-1}
		if i < len(chosen) {
			slots[i].Selected = chosen[i]
		}
	}
	return slots
}

// overviewRows are the rows above the sections: the annual fee (lowest
// wins) and the welcome bonus.
func overviewRows(cards []db.ComparisonCard) []compareRow {
	fee := make([]compareCell, len(cards))
	bonus := make([]compareCell, len(cards))
	for i, c := range cards {
		fee[i] = compareCell{Value: "$" + strconv.Itoa(c.AnnualFeeAmount), Note: c.AnnualFeeNote}
		bonus[i] = compareCell{Value: strings.TrimSpace(c.WelcomeBonusText)}
	}
	markWinners(fee, true)
	return finishRows([]compareRow{
		{Label: "Annual fee", Cells: fee},
		{Label: "Welcome bonus", Cells: bonus},
	})
}

// buildCompareSections builds the earning, credit, benefit and protection
// sections from each card's JSON.
func buildCompareSections(cards []db.ComparisonCard) []compareSection {
	parse := func(get func(db.ComparisonCard) string) []map[string]json.RawMessage {
		maps := make([]map[string]json.RawMessage, len(cards))
		for i, c := range cards {
			json.Unmarshal([]byte(get(c)), &maps[i])
			if maps[i] == nil {
				maps[i] = map[string]json.RawMessage{}
			}
		}
		return maps
	}
	sections := []compareSection{
		hideEmptyRows(compareSection{Key: "earning", Title: "Earning rates",
			Rows: earningRows(parse(func(c db.ComparisonCard) string { return c.EarningRatesJSON }))}),
	}
	for _, s := range []struct {
		key, title string
		get        func(db.ComparisonCard) string
	}{
		{"credits", "Credits", func(c db.ComparisonCard) string { return c.CreditsJSON }},
		{"benefits", "Benefits", func(c db.ComparisonCard) string { return c.BenefitsJSON }},
		{"protections", "Protections", func(c db.ComparisonCard) string { return c.ProtectionsJSON }},
	} {
		sections = append(sections, hideEmptyRows(compareSection{Key: s.key, Title: s.title,
			Rows: genericRows(parse(s.get), s.key)}))
	}
	return sections
}

// hideEmptyRows drops rows where no card has anything, counting them.
func hideEmptyRows(s compareSection) compareSection {
	var kept []compareRow
	for _, r := range s.Rows {
		empty := true
		for _, c := range r.Cells {
			if !c.Empty() {
				empty = false
				break
			}
		}
		if empty {
			s.Hidden++
			continue
		}
		kept = append(kept, r)
	}
	s.Rows = finishRows(kept)
	return s
}

// finishRows marks rows where every card shows the same thing.
func finishRows(rows []compareRow) []compareRow {
	for i := range rows {
		same := true
		for _, c := range rows[i].Cells[1:] {
			if c.key() != rows[i].Cells[0].key() {
				same = false
				break
			}
		}
		rows[i].Same = same
	}
	return rows
}

// cleanValue maps the data's placeholders for "nothing" to "".
func cleanValue(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToUpper(s) {
	case "", "-", "—", "N/A", "NONE":
		return ""
	}
	return s
}

// rateCell reads one earning rate.
func rateCell(m map[string]json.RawMessage, key string) compareCell {
	raw, ok := m[key]
	if !ok {
		return compareCell{}
	}
	var e rateEntry
	json.Unmarshal(raw, &e)
	return compareCell{Value: cleanValue(e.Rate), Note: strings.TrimSpace(e.Note)}
}

// hasValue reports whether any card has a value under key.
func hasValue(maps []map[string]json.RawMessage, key string) bool {
	for _, m := range maps {
		if !rateCell(m, key).Empty() {
			return true
		}
	}
	return false
}

// earningRows builds the earning rate rows. A category booked directly or
// through a travel portal gets one row for each, with its own winner, when
// any card earns extra through a portal.
func earningRows(maps []map[string]json.RawMessage) []compareRow {
	var rows []compareRow
	row := func(label, key string) {
		cells := make([]compareCell, len(maps))
		for i, m := range maps {
			cells[i] = rateCell(m, key)
		}
		markWinners(cells, false)
		rows = append(rows, compareRow{Label: label, Cells: cells})
	}
	for _, cat := range earningRateCategories {
		label := sentenceCase(cat.Label)
		if !directPortalPairs[cat.Key] {
			row(label, cat.Key)
			continue
		}
		if hasValue(maps, cat.Key+"_portal") {
			row(label+", direct", cat.Key+"_direct")
			row(label+", travel portal", cat.Key+"_portal")
		} else {
			row(label, cat.Key+"_direct")
		}
	}

	// The points or miles each card earns, and where they transfer.
	cells := make([]compareCell, len(maps))
	for i, m := range maps {
		var tp transferPartnersEntry
		if raw, ok := m["transfer_partners"]; ok {
			json.Unmarshal(raw, &tp)
		}
		cells[i] = compareCell{Value: cleanValue(tp.Program), Note: partnersNote(tp)}
	}
	rows = append(rows, compareRow{Label: "Rewards currency", Cells: cells})

	rows = append(rows, nicheRow(maps, nicheLabels["earning"]))
	return rows
}

// partnersNote describes a currency's transfer partners: "20 transfer
// partners, including ANA, Delta, Hilton and Marriott".
func partnersNote(tp transferPartnersEntry) string {
	count := strings.TrimSpace(tp.Count)
	if count == "" || cleanValue(count) == "" || count == "0" || !unicode.IsDigit(rune(count[0])) {
		return strings.TrimSpace(tp.Note)
	}
	note := count + " transfer partners"
	if strings.ContainsAny(count, " ,") {
		note = count // "38 airlines, mostly 3:1" already says what it counts
	}
	if n := len(tp.KeyPartners); n > 0 {
		shown := tp.KeyPartners
		if n > 4 {
			shown = shown[:4]
		}
		note += ", including " + joinAnd(shown)
	}
	return note
}

// joinAnd joins words as "a, b and c".
func joinAnd(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

// nicheRow lists each card's "niche" entries: the extras with no row of
// their own.
func nicheRow(maps []map[string]json.RawMessage, label string) compareRow {
	cells := make([]compareCell, len(maps))
	for i, m := range maps {
		var niches []nicheEntry
		if raw, ok := m["niche"]; ok {
			json.Unmarshal(raw, &niches)
		}
		for _, n := range niches {
			text := strings.TrimSpace(n.Label)
			if v := cleanValue(n.Value); v != "" && !strings.EqualFold(v, "yes") {
				text += ": " + v
			}
			if text != "" {
				cells[i].Items = append(cells[i].Items, compareItem{Text: text, Note: strings.TrimSpace(n.Note)})
			}
		}
	}
	return compareRow{Label: label, Cells: cells}
}

// genericRows builds the credit, benefit or protection rows: the known
// keys in order, then any others alphabetically, then the catch-all list.
func genericRows(maps []map[string]json.RawMessage, section string) []compareRow {
	var rows []compareRow
	row := func(label, key string) {
		cells := make([]compareCell, len(maps))
		for i, m := range maps {
			raw, ok := m[key]
			if !ok {
				continue
			}
			var e genericEntry
			json.Unmarshal(raw, &e)
			v := cleanValue(e.Amount)
			if v == "" {
				v = cleanValue(e.Value)
			}
			cells[i] = compareCell{Value: v, Note: strings.TrimSpace(e.Note)}
			if v == "" {
				cells[i].Note = "" // a note on nothing explains nothing
			}
		}
		markWinners(cells, false)
		rows = append(rows, compareRow{Label: label, Cells: cells})
	}

	known := map[string]bool{"niche": true}
	for _, cat := range genericCategories[section] {
		known[cat.Key] = true
		row(cat.Label, cat.Key)
	}
	var others []string
	seen := map[string]bool{}
	for _, m := range maps {
		for key := range m {
			if !known[key] && !seen[key] {
				seen[key] = true
				others = append(others, key)
			}
		}
	}
	sort.Strings(others) // map order is random: keep rows stable between loads
	for _, key := range others {
		row(keyLabel(key), key)
	}
	rows = append(rows, nicheRow(maps, nicheLabels[section]))
	return rows
}

// sentenceCase lower-cases all but the first letter: "Car Rental" ->
// "Car rental", matching the other row labels.
func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return s[:1] + strings.ToLower(s[1:])
}

// keyLabel turns a data key into a row label: "cell_phone" -> "Cell phone".
func keyLabel(key string) string {
	label := strings.TrimSpace(strings.ReplaceAll(key, "_", " "))
	if label == "" {
		return key
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

// --- Winners ---

// quantity is a value the winner rule can compare: a number and its unit
// ("x", "%", or "$" plus any suffix such as "/yr").
type quantity struct {
	n    float64
	unit string
}

var (
	rateValue   = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*(x|%)$`)
	dollarValue = regexp.MustCompile(`^\$(\d[\d,]*(?:\.\d+)?)\s*([kK])?\s*(/\s*[a-z]+)?$`)
	// conditionalNote marks a rate that only applies sometimes: a top
	// category, a rotating one, one you must choose or activate.
	conditionalNote = regexp.MustCompile(`(?i)\bif\b[^.;]*\btop\b|\btop[- ]?\d\b|\btop (eligible )?categor|rotating|\bactivat|\bchoose\b`)
)

// parseQuantity reads "3x", "5%", "$300/yr" or "$3K/trip".
func parseQuantity(s string) (quantity, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if m := rateValue.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(m[1], 64)
		return quantity{n, m[2]}, err == nil
	}
	if m := dollarValue.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		if m[2] != "" {
			n *= 1000
		}
		return quantity{n, "$" + strings.ReplaceAll(m[3], " ", "")}, err == nil
	}
	return quantity{}, false
}

// markWinners stars the best value in a row, but only when the values are
// comparable: every card that has one gives a number in the same unit
// ("3x" and "2x"; "$300/yr" and "$200/yr"). A card with nothing counts as
// zero. There is no star when the values are text ("Yes", "1 year"), mixed
// units, all equal, or when the best one is conditional (a top or rotating
// category, per its note). lowerWins is for fees.
func markWinners(cells []compareCell, lowerWins bool) {
	if len(cells) < 2 {
		return
	}
	qs := make([]float64, len(cells))
	unit := ""
	for i, c := range cells {
		if c.Empty() {
			if lowerWins {
				return // a missing fee is unknown, not free
			}
			continue
		}
		q, ok := parseQuantity(c.Value)
		if !ok || (unit != "" && q.unit != unit) {
			return
		}
		unit, qs[i] = q.unit, q.n
	}
	if unit == "" {
		return
	}
	best := qs[0]
	for _, n := range qs[1:] {
		if (lowerWins && n < best) || (!lowerWins && n > best) {
			best = n
		}
	}
	if !lowerWins && best <= 0 {
		return
	}
	winners := 0
	for i, n := range qs {
		if n == best {
			winners++
			if conditionalNote.MatchString(cells[i].Note) {
				return
			}
		}
	}
	if winners == len(cells) {
		return
	}
	for i, n := range qs {
		cells[i].IsWinner = n == best
	}
}
