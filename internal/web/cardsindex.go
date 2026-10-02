package web

import (
	"maps"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// The /cards/ index: every published card as a compact tile (fee, best
// earning rate, welcome offer). The filters are one row of links: category
// chips and a "No annual fee" toggle (fee=none). The sort is a small GET form
// on the results line that changes the order, never which cards show. Every
// view has its own URL (/cards/?cat=cash-back&fee=none&sort=name) and nothing
// needs JavaScript.
//
// Older links may also carry a fee band (fee=under-100, 100-400, over-400) or
// an issuer (issuer=chase). Those still filter, and show on the results line
// with a link that removes them, but there are no controls to set them.

// cardTile is one card on the index.
type cardTile struct {
	Slug, Title, Issuer, ImageURL string
	Category                      string
	FeeAmount                     int
	FeeKnown                      bool // see annualFeeKnown; false keeps it out of "No annual fee" and the fee bands
	HasFacts                      bool
	Fee                           string   // "$895"; empty when the fee is unknown
	Earn                          earnRate // best earning line; zero without data
	Offer                         cardOffer
	Eager                         bool // in the first rows: load the art now
}

// cardTileGroup is a category heading and its tiles, for the default view.
type cardTileGroup struct {
	Name  string
	Cards []cardTile
}

// indexChip is a filter link with the number of cards it would show.
type indexChip struct {
	Label  string
	URL    string
	Count  int
	Active bool
}

// indexOption is an <option> in the sort menu. URL is the page in that
// order, which the menu opens when it changes.
type indexOption struct {
	Value, Label, URL string
	Selected          bool
}

// cardsQuery is the index's filter state, read from the URL. Fee is
// feeNone for the "No annual fee" toggle, or a fee band from an older link.
type cardsQuery struct {
	Cat, Fee, Issuer, Sort string
}

// feeNone is the fee value of the "No annual fee" toggle. It is the "none"
// fee band, so /cards/?fee=none links from before the toggle still work.
const feeNone = "none"

// cardSorts are the sort choices; the first is the default and is left out of
// URLs.
var cardSorts = []indexOption{
	{Value: "", Label: "By category"},
	{Value: "name", Label: "Name (A–Z)"},
	{Value: "fee-asc", Label: "Lowest fee"},
	{Value: "fee-desc", Label: "Highest fee"},
}

// cardTilesEager is how many tiles load their art straight away: the first
// two rows on a laptop, more than a phone's first screen.
const cardTilesEager = 6

func (q cardsQuery) url() string {
	v := url.Values{}
	if q.Cat != "" {
		v.Set("cat", q.Cat)
	}
	if q.Fee != "" {
		v.Set("fee", q.Fee)
	}
	if q.Issuer != "" {
		v.Set("issuer", q.Issuer)
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	if len(v) == 0 {
		return "/cards/"
	}
	return "/cards/?" + v.Encode()
}

// matches reports whether a tile passes the query's filters.
func (q cardsQuery) matches(t cardTile) bool {
	if q.Cat != "" && t.Category != q.Cat {
		return false
	}
	if q.Issuer != "" && issuerKey(t.Issuer) != q.Issuer {
		return false
	}
	if q.Fee != "" {
		b, _ := feeBandByKey(q.Fee)
		if !t.FeeKnown || !b.contains(t.FeeAmount) {
			return false
		}
	}
	return true
}

// readCardsQuery reads the filters, dropping values the index doesn't offer,
// so a stale or mistyped link shows every card rather than none.
func readCardsQuery(v url.Values, tiles []cardTile) cardsQuery {
	q := cardsQuery{
		Cat:    strings.TrimSpace(v.Get("cat")),
		Fee:    strings.TrimSpace(v.Get("fee")),
		Issuer: strings.TrimSpace(v.Get("issuer")),
		Sort:   strings.TrimSpace(v.Get("sort")),
	}
	knownCat, knownIssuer := false, false
	for _, t := range tiles {
		knownCat = knownCat || t.Category == q.Cat
		knownIssuer = knownIssuer || issuerKey(t.Issuer) == q.Issuer
	}
	if !knownCat {
		q.Cat = ""
	}
	if !knownIssuer {
		q.Issuer = ""
	}
	if _, ok := feeBandByKey(q.Fee); !ok {
		q.Fee = ""
	}
	knownSort := false
	for _, s := range cardSorts {
		knownSort = knownSort || s.Value == q.Sort
	}
	if !knownSort {
		q.Sort = ""
	}
	return q
}

// feeOff is q without its fee filter.
func feeOff(q cardsQuery) cardsQuery {
	q.Fee = ""
	return q
}

func countMatching(tiles []cardTile, q cardsQuery) int {
	n := 0
	for _, t := range tiles {
		if q.matches(t) {
			n++
		}
	}
	return n
}

func newCardTile(p *db.ReferencePage) cardTile {
	t := cardTile{
		Slug: p.Slug, Title: p.Title, Issuer: p.Issuer, ImageURL: p.ImageURL,
		Category: p.Category, FeeAmount: p.AnnualFeeAmount, FeeKnown: annualFeeKnown(p), HasFacts: hasCardFacts(p),
	}
	if t.FeeKnown {
		t.Fee = feeText(p.AnnualFeeAmount)
	}
	if t.HasFacts {
		if top := topEarnRates(p.EarningRatesJSON, 1); len(top) == 1 {
			t.Earn = top[0]
		}
		t.Offer = parseOffer(p.WelcomeBonusText)
	}
	return t
}

func categoryName(cat string) string {
	if name := categoryDisplayNames[cat]; name != "" {
		return name
	}
	return cat
}

// categoriesInOrder lists the given categories: the known ones in
// categoryOrder, then any others alphabetically.
func categoriesInOrder(present map[string]bool) []string {
	present = maps.Clone(present)
	var out []string
	for _, c := range categoryOrder {
		if present[c] {
			out = append(out, c)
			delete(present, c)
		}
	}
	var rest []string
	for c := range present {
		rest = append(rest, c)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// cardsIndex is everything cards.html renders.
type cardsIndex struct {
	Query      cardsQuery
	Categories []indexChip // "All", then each category
	NoFee      indexChip   // the "No annual fee" toggle
	Sorts      []indexOption
	// Applied names the filters set by the chips above, for the results
	// line: "Cash Back", "No annual fee".
	Applied []string
	// Legacy are filters from older links (a fee band, an issuer) that have
	// no control on the page, each with a link that removes just it.
	Legacy    []activeFilter
	Filtered  bool   // any filter applied: offer Clear
	ClearURL  string // every filter removed, the sort kept
	Count     int
	CountText string
	Grouped   bool
	Groups    []cardTileGroup // when Grouped
	Cards     []cardTile      // otherwise
}

func buildCardsIndex(pages []db.ReferencePage, v url.Values) cardsIndex {
	tiles := make([]cardTile, 0, len(pages))
	for i := range pages {
		tiles = append(tiles, newCardTile(&pages[i]))
	}
	q := readCardsQuery(v, tiles)
	ix := cardsIndex{Query: q}

	// Each chip's count is what the page would show after clicking it, with
	// the other filters kept.
	all := q
	all.Cat = ""
	ix.Categories = append(ix.Categories, indexChip{Label: "All", URL: all.url(), Count: countMatching(tiles, all), Active: q.Cat == ""})
	present := map[string]bool{}
	for _, t := range tiles {
		present[t.Category] = true
	}
	cats := categoriesInOrder(present)
	for _, c := range cats {
		cq := q
		cq.Cat = c
		chip := indexChip{Label: categoryName(c), URL: cq.url(), Count: countMatching(tiles, cq), Active: q.Cat == c}
		if chip.Active {
			chip.URL = all.url()
		}
		ix.Categories = append(ix.Categories, chip)
	}

	// The toggle keeps the category, issuer and sort. Turning it on replaces
	// a fee band from an older link, which it could only contradict.
	fq := q
	fq.Fee = feeNone
	ix.NoFee = indexChip{Label: "No annual fee", URL: fq.url(), Count: countMatching(tiles, fq), Active: q.Fee == feeNone}
	if ix.NoFee.Active {
		ix.NoFee.URL = feeOff(q).url()
	}

	issuers := map[string]string{}
	for _, t := range tiles {
		issuers[issuerKey(t.Issuer)] = t.Issuer
	}
	for _, s := range cardSorts {
		sq := q
		sq.Sort = s.Value
		s.URL = sq.url()
		s.Selected = s.Value == q.Sort
		ix.Sorts = append(ix.Sorts, s)
	}

	// The results line: what is applied, and links that remove the filters
	// with no control of their own.
	if q.Cat != "" {
		ix.Applied = append(ix.Applied, categoryName(q.Cat))
	}
	if q.Fee == feeNone {
		ix.Applied = append(ix.Applied, ix.NoFee.Label)
	} else if q.Fee != "" {
		b, _ := feeBandByKey(q.Fee)
		ix.Legacy = append(ix.Legacy, activeFilter{Label: b.Label, RemoveURL: feeOff(q).url()})
	}
	if q.Issuer != "" {
		r := q
		r.Issuer = ""
		ix.Legacy = append(ix.Legacy, activeFilter{Label: issuers[q.Issuer], RemoveURL: r.url()})
	}
	ix.Filtered = q.Cat != "" || q.Fee != "" || q.Issuer != ""
	ix.ClearURL = cardsQuery{Sort: q.Sort}.url()

	var shown []cardTile
	for _, t := range tiles {
		if q.matches(t) {
			shown = append(shown, t)
		}
	}
	ix.Count = len(shown)
	ix.CountText = cardCountText(len(shown))

	byName := func(a, b cardTile) bool { return strings.ToLower(a.Title) < strings.ToLower(b.Title) }
	switch q.Sort {
	case "fee-asc", "fee-desc":
		desc := q.Sort == "fee-desc"
		sort.SliceStable(shown, func(i, j int) bool {
			a, b := shown[i], shown[j]
			if a.FeeKnown != b.FeeKnown { // unknown fees go last either way
				return a.FeeKnown
			}
			if a.FeeAmount != b.FeeAmount {
				return (a.FeeAmount < b.FeeAmount) != desc
			}
			return byName(a, b)
		})
	default:
		sort.SliceStable(shown, func(i, j int) bool { return byName(shown[i], shown[j]) })
	}

	// Category headings only make sense across categories, in the default
	// sort; the other sorts are one list.
	ix.Grouped = q.Cat == "" && q.Sort == ""
	if ix.Grouped {
		byCat := map[string][]cardTile{}
		for _, t := range shown {
			byCat[t.Category] = append(byCat[t.Category], t)
		}
		n := 0
		for _, c := range cats {
			if len(byCat[c]) == 0 {
				continue
			}
			for i := range byCat[c] {
				byCat[c][i].Eager = n < cardTilesEager
				n++
			}
			ix.Groups = append(ix.Groups, cardTileGroup{Name: categoryName(c), Cards: byCat[c]})
		}
	} else {
		for i := range shown {
			shown[i].Eager = i < cardTilesEager
		}
		ix.Cards = shown
	}
	return ix
}

func (app *App) handleCardsList(w http.ResponseWriter, r *http.Request) {
	pages, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		app.serverError(w, r, "cards: loading", err)
		return
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	ix := buildCardsIndex(pages, r.URL.Query())
	data := map[string]interface{}{
		"Title":           "Credit Card Guide",
		"NavSection":      "cards",
		"PersonaName":     app.cfg.Site.PersonaName,
		"Index":           ix,
		"Total":           len(pages),
		"MetaDescription": "Compare the best credit cards for travel, cash back, hotels, and more.",
		"CanonicalURL":    "https://" + domain + "/cards/",
		"OGType":          "website",
	}
	app.renderTemplate(w, "cards.html", data)
}
