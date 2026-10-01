package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// The /cards/ index: every published card as a compact tile (fee, best
// earning rate, welcome offer), with category, fee and issuer filters and a
// sort. The filters are plain links and a GET form, so they work without
// JavaScript and every view has its own URL: /cards/?cat=cash-back&fee=none.

// cardTile is one card on the index.
type cardTile struct {
	Slug, Title, Issuer, ImageURL string
	Category                      string
	FeeAmount                     int
	HasFacts                      bool
	Fee                           string   // "$895"; empty without facts
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

// indexOption is an <option> in the issuer or sort select.
type indexOption struct {
	Value, Label string
	Selected     bool
}

// cardsQuery is the index's filter state, read from the URL.
type cardsQuery struct {
	Cat, Fee, Issuer, Sort string
}

// cardSorts are the sort choices; the first is the default and is left out of
// URLs.
var cardSorts = []indexOption{
	{Value: "", Label: "Category"},
	{Value: "fee-asc", Label: "Annual fee: low to high"},
	{Value: "fee-desc", Label: "Annual fee: high to low"},
	{Value: "name", Label: "Name"},
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
		if !t.HasFacts || !b.contains(t.FeeAmount) {
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
		Category: p.Category, FeeAmount: p.AnnualFeeAmount, HasFacts: hasCardFacts(p),
	}
	if t.HasFacts {
		t.Fee = feeText(p.AnnualFeeAmount)
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

// categoriesInOrder lists the categories the tiles use: the known ones in
// categoryOrder, then any others alphabetically.
func categoriesInOrder(tiles []cardTile) []string {
	present := map[string]bool{}
	for _, t := range tiles {
		present[t.Category] = true
	}
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
	FeeBands   []indexChip // "Any", then each band
	Issuers    []indexOption
	Sorts      []indexOption
	Active     []activeFilter
	Count      int
	CountText  string
	Grouped    bool
	Groups     []cardTileGroup // when Grouped
	Cards      []cardTile      // otherwise
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
	cats := categoriesInOrder(tiles)
	for _, c := range cats {
		cq := q
		cq.Cat = c
		chip := indexChip{Label: categoryName(c), URL: cq.url(), Count: countMatching(tiles, cq), Active: q.Cat == c}
		if chip.Active {
			chip.URL = all.url()
		}
		ix.Categories = append(ix.Categories, chip)
	}

	anyFee := q
	anyFee.Fee = ""
	ix.FeeBands = append(ix.FeeBands, indexChip{Label: "Any", URL: anyFee.url(), Count: countMatching(tiles, anyFee), Active: q.Fee == ""})
	for _, b := range feeBands {
		fq := q
		fq.Fee = b.Key
		chip := indexChip{Label: b.Label, URL: fq.url(), Count: countMatching(tiles, fq), Active: q.Fee == b.Key}
		if chip.Active {
			chip.URL = anyFee.url()
		}
		ix.FeeBands = append(ix.FeeBands, chip)
	}

	issuers := map[string]string{}
	for _, t := range tiles {
		issuers[issuerKey(t.Issuer)] = t.Issuer
	}
	keys := make([]string, 0, len(issuers))
	for k := range issuers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ix.Issuers = append(ix.Issuers, indexOption{Value: "", Label: "All issuers", Selected: q.Issuer == ""})
	for _, k := range keys {
		ix.Issuers = append(ix.Issuers, indexOption{Value: k, Label: issuers[k], Selected: q.Issuer == k})
	}
	for _, s := range cardSorts {
		s.Selected = s.Value == q.Sort
		ix.Sorts = append(ix.Sorts, s)
	}

	// Applied filters, each with a link that removes just it.
	if q.Cat != "" {
		r := q
		r.Cat = ""
		ix.Active = append(ix.Active, activeFilter{Label: categoryName(q.Cat), RemoveURL: r.url()})
	}
	if q.Fee != "" {
		r := q
		r.Fee = ""
		b, _ := feeBandByKey(q.Fee)
		label := b.Label
		if b.Key == "none" {
			label = "No annual fee"
		}
		ix.Active = append(ix.Active, activeFilter{Label: label, RemoveURL: r.url()})
	}
	if q.Issuer != "" {
		r := q
		r.Issuer = ""
		ix.Active = append(ix.Active, activeFilter{Label: issuers[q.Issuer], RemoveURL: r.url()})
	}

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
			if a.HasFacts != b.HasFacts { // unknown fees go last either way
				return a.HasFacts
			}
			if a.FeeAmount != b.FeeAmount {
				return (a.FeeAmount < b.FeeAmount) != desc
			}
			return byName(a, b)
		})
	default:
		sort.SliceStable(shown, func(i, j int) bool { return byName(shown[i], shown[j]) })
	}

	// Category headings only make sense across categories, in the default sort.
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
		http.Error(w, "Failed to load cards", http.StatusInternalServerError)
		return
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	ix := buildCardsIndex(pages, r.URL.Query())
	data := map[string]interface{}{
		"Title":           "Credit Card Guide",
		"PersonaName":     app.cfg.Site.PersonaName,
		"Index":           ix,
		"Total":           len(pages),
		"MetaDescription": "Compare the best credit cards for travel, cash back, hotels, and more.",
		"CanonicalURL":    "https://" + domain + "/cards/",
		"OGType":          "website",
	}
	app.renderTemplate(w, "cards.html", data)
}
