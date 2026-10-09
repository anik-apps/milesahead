package web

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/seeddata"
)

// searchApp is the real-template app holding the repository's cards and
// the made-up articles.
func searchApp(t *testing.T) *App {
	t.Helper()
	app := setupRealTemplateCardApp(t)
	if _, err := seeddata.Cards(app.db); err != nil {
		t.Fatal(err)
	}
	if err := seeddata.SeedArticles(app.db, time.Now()); err != nil {
		t.Fatal(err)
	}
	return app
}

// searchPage is what a results page shows.
type searchPage struct {
	body       string
	topCards   []string // the "Cards" tiles above the articles
	articles   []string
	otherCards []string // cards after the articles, including "Show more"
	ended      []bool   // per article
	summary    string
	empty      bool
}

var (
	tileRe       = regexp.MustCompile(`<a href="/cards/([^"]+)" class="search-card-tile">`)
	articleRe    = regexp.MustCompile(`<li class="search-item( is-ended)?">\s*<a href="/article/([^"]+)"`)
	otherCardRe  = regexp.MustCompile(`<li class="search-item">\s*<a href="/cards/([^"]+)"`)
	summaryRe    = regexp.MustCompile(`(?s)<p class="search-summary" role="status">(.*?)</p>`)
	tagRe        = regexp.MustCompile(`<[^>]+>`)
	spaceRe      = regexp.MustCompile(`\s+`)
	articleCount = regexp.MustCompile(`(\d+) articles?`)
)

func search(t *testing.T, app *App, query string) searchPage {
	t.Helper()
	body := getPage(t, app, "/search?q="+url.QueryEscape(query))
	p := searchPage{body: body, empty: strings.Contains(body, `<div class="search-empty"`)}
	for _, m := range tileRe.FindAllStringSubmatch(body, -1) {
		p.topCards = append(p.topCards, m[1])
	}
	for _, m := range articleRe.FindAllStringSubmatch(body, -1) {
		p.articles = append(p.articles, m[2])
		p.ended = append(p.ended, m[1] != "")
	}
	for _, m := range otherCardRe.FindAllStringSubmatch(body, -1) {
		p.otherCards = append(p.otherCards, m[1])
	}
	if m := summaryRe.FindStringSubmatch(body); m != nil {
		p.summary = strings.TrimSpace(spaceRe.ReplaceAllString(tagRe.ReplaceAllString(m[1], ""), " "))
	}
	return p
}

func (p searchPage) articleTotal(t *testing.T) int {
	t.Helper()
	m := articleCount.FindStringSubmatch(p.summary)
	if m == nil {
		t.Fatalf("no article count in %q", p.summary)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// The queries from the search review, against the repository's cards and
// the made-up articles: each finds what a reader typing it wants, with the
// card they named first.
func TestSearch_ReviewQueries(t *testing.T) {
	app := searchApp(t)
	cases := []struct {
		query        string
		topCards     []string // exactly, in order
		wantArticles []string // among the articles
		minArticles  int
	}{
		{"sapph", []string{"chase-sapphire-reserve", "chase-sapphire-preferred", "chase-sapphire-reserve-business"}, []string{"csr-150k-offer", "csp-100k", "hyatt-ratio-cut"}, 5},
		{"reserve sapphire", []string{"chase-sapphire-reserve", "chase-sapphire-reserve-business"}, []string{"csr-150k-offer", "csr-vs-plat"}, 3},
		{"hyatt transfer", nil, []string{"hyatt-ratio-cut", "bilt-hyatt"}, 2},
		{"amex plat", []string{"amex-platinum", "amex-business-platinum"}, []string{"plat-saks", "amex-airline-discounts", "plat-lounge-guest"}, 3},
		{"amex platinum", []string{"amex-platinum", "amex-business-platinum"}, []string{"plat-saks"}, 3},
		{"american express platinum", []string{"amex-platinum", "amex-business-platinum"}, []string{"plat-saks"}, 3},
		{"csr", []string{"chase-sapphire-reserve", "chase-sapphire-reserve-business"}, nil, 0},
		{"csp", []string{"chase-sapphire-preferred"}, nil, 0},
		{"lounges", nil, []string{"lounge-network-2026", "sapphire-lounge-dfw", "plat-lounge-guest"}, 3},
		{"transfer bonus", nil, []string{"marriott-65", "rove-aeroplan", "bilt-rent-day", "c1-jal"}, 6},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			p := search(t, app, c.query)
			if p.empty {
				t.Fatalf("no results")
			}
			if strings.Join(p.topCards, ",") != strings.Join(c.topCards, ",") {
				t.Errorf("cards above the articles = %v, want %v", p.topCards, c.topCards)
			}
			for _, a := range c.wantArticles {
				if !has(p.articles, a) {
					t.Errorf("want article %s among %v", a, p.articles)
				}
			}
			if len(p.articles) < c.minArticles {
				t.Errorf("%d articles, want at least %d", len(p.articles), c.minArticles)
			}
			for _, top := range p.topCards {
				if has(p.otherCards, top) {
					t.Errorf("%s is listed twice", top)
				}
			}
			if strings.Contains(p.summary, "any of them") {
				t.Errorf("every word matched, but the summary says %q", p.summary)
			}
		})
	}
}

// Word order, plurals and partial words find the same results.
func TestSearch_EquivalentQueries(t *testing.T) {
	app := searchApp(t)
	for _, pair := range [][2]string{
		{"reserve sapphire", "sapphire reserve"},
		{"lounge", "lounges"},
		{"transfer bonus", "transfer bonuses"},
		{"sapph", "sapphire"},
	} {
		a, b := search(t, app, pair[0]), search(t, app, pair[1])
		if a.summary == "" || a.summary != b.summary {
			t.Errorf("%q: %q but %q: %q", pair[0], a.summary, pair[1], b.summary)
		}
	}
}

// Ended offers are listed after live ones, still marked.
func TestSearch_EndedOffersAfterLive(t *testing.T) {
	app := searchApp(t)
	p := search(t, app, "transfer bonus")
	sawEnded, ended := false, 0
	for i, e := range p.ended {
		if e {
			sawEnded, ended = true, ended+1
		} else if sawEnded {
			t.Fatalf("live %s after an ended offer: %v %v", p.articles[i], p.articles, p.ended)
		}
	}
	if ended != 3 {
		t.Errorf("want the 3 ended transfer bonuses marked, got %d of %v", ended, p.articles)
	}
	if !strings.Contains(p.body, `<span class="ended-badge">Ended</span> <span class="ended-title">`) {
		t.Error("ended results keep the badge")
	}
}

// Each article shows its type and date; the summary gives the counts.
func TestSearch_ResultDetails(t *testing.T) {
	app := searchApp(t)
	p := search(t, app, "reserve sapphire")
	if !strings.HasSuffix(p.summary, "match every word") || p.articleTotal(t) != len(p.articles) {
		t.Errorf("summary = %q for %d articles", p.summary, len(p.articles))
	}
	item := regexp.MustCompile(`(?s)<a href="/article/csr-150k-offer".*?</li>`).FindString(p.body)
	date := time.Now().AddDate(0, 0, -3).UTC().Format("Jan 2, 2006")
	for _, want := range []string{`<span class="type-badge badge-deal">deal</span>`, `>` + date + `</time>`, `<mark>`} {
		if !strings.Contains(item, want) {
			t.Errorf("result should contain %q:\n%s", want, item)
		}
	}
	tile := regexp.MustCompile(`(?s)<a href="/cards/chase-sapphire-reserve" class="search-card-tile">.*?</a>`).FindString(p.body)
	if !strings.Contains(tile, "$795 a year") || !strings.Contains(tile, `<img src="/static/img/cards/`) {
		t.Errorf("card tile should show art and fee:\n%s", tile)
	}
}

// When no page has every word, pages with any of them are shown, and the
// page says so.
func TestSearch_FallsBackToAnyWord(t *testing.T) {
	app := searchApp(t)
	p := search(t, app, "sapphire xyzzy")
	if p.empty || len(p.articles) == 0 {
		t.Fatal("want results for the word that matched")
	}
	if !strings.HasPrefix(p.summary, "No results have every word, so these have any of them") {
		t.Errorf("summary = %q", p.summary)
	}
	if len(p.topCards) != 0 {
		t.Errorf("no card title has every word: %v", p.topCards)
	}
}

// No results: a heading, tips and ways forward instead of one grey line.
func TestSearch_NoResults(t *testing.T) {
	app := searchApp(t)
	if b := search(t, app, `"><script>alert(1)</script>`).body; strings.Contains(b, "<script>alert") {
		t.Fatal("the query must be escaped")
	}
	for _, q := range []string{"xyzzy", "!!!", `"><qqq>`} {
		p := search(t, app, q)
		if !p.empty {
			t.Fatalf("%q: want the empty state", q)
		}
		for _, want := range []string{`<h2>No results for &ldquo;`, `href="/cards/"`, `href="/compare"`} {
			if !strings.Contains(p.body, want) {
				t.Errorf("%q: empty state should contain %q", q, want)
			}
		}
		if strings.Contains(p.body, "<qqq>") {
			t.Fatalf("%q: query must be escaped", q)
		}
	}
}

// The field has a label, is a search field, and doesn't grab focus (which
// opened the keyboard over the results on phones).
func TestSearch_FieldIsLabelled(t *testing.T) {
	app := searchApp(t)
	body := getPage(t, app, "/search?q=sapphire")
	for _, want := range []string{`<label for="search-q" class="visually-hidden">`, `<input type="search" id="search-q" name="q" value="sapphire"`} {
		if !strings.Contains(body, want) {
			t.Errorf("want %q", want)
		}
	}
	form := regexp.MustCompile(`(?s)<form action="/search".*?</form>`).FindString(body)
	if strings.Contains(form, "autofocus") {
		t.Error("the search field should not autofocus")
	}
}

// One pager for the articles, with page numbers; cards are on page 1.
func TestSearch_Pager(t *testing.T) {
	app := searchApp(t)
	for i := 0; i < 23; i++ {
		if _, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
			Title: fmt.Sprintf("Zebra Miles Story %d", i), Slug: fmt.Sprintf("zebra-%d", i), Type: "summary",
			Content: "<p>Zebra miles content.</p>", TLDR: "Zebra", Status: db.StatusApproved,
			SourcesJSON: "[]", RevisionNumber: 1, KeyDetails: "[]", Links: "[]", RelatedCardsJSON: "[]",
		}); err != nil {
			t.Fatal(err)
		}
	}
	p := search(t, app, "zebra")
	if len(p.articles) != 10 || !strings.HasSuffix(p.summary, "page 1 of 3") {
		t.Fatalf("page 1: %d articles, summary %q", len(p.articles), p.summary)
	}
	for _, want := range []string{
		`<span class="btn-page is-current" aria-current="page">1</span>`,
		`<a href="/search?page=2&amp;q=zebra" class="btn-page">2</a>`,
		`<a href="/search?page=3&amp;q=zebra" class="btn-page">3</a>`,
		`<a href="/search?page=2&amp;q=zebra" class="btn-page" rel="next">`,
	} {
		if !strings.Contains(p.body, want) {
			t.Errorf("pager should contain %s", want)
		}
	}
	body := getPage(t, app, "/search?q=zebra&page=3")
	if n := len(articleRe.FindAllString(body, -1)); n != 3 {
		t.Errorf("page 3 has %d articles, want 3", n)
	}
	if strings.Contains(body, `cards are on page 1`) {
		t.Error("no card matches zebra, so page 3 shouldn't point to cards")
	}
	if !strings.Contains(body, `rel="prev"`) || strings.Contains(body, `rel="next"`) {
		t.Error("the last page links back, not forward")
	}
	// The old articles pager parameter still works, and a page past the end
	// shows the last page.
	for _, path := range []string{"/search?q=zebra&pa=3", "/search?q=zebra&page=99"} {
		if b := getPage(t, app, path); !strings.Contains(b, "page 3 of 3") {
			t.Errorf("%s should show page 3", path)
		}
	}
}

func TestPageLinks(t *testing.T) {
	render := func(page, pages int) string {
		var parts []string
		for _, l := range pageLinks(page, pages, func(n int) string { return "" }) {
			switch {
			case l.N == 0:
				parts = append(parts, "…")
			case l.Current:
				parts = append(parts, "["+strconv.Itoa(l.N)+"]")
			default:
				parts = append(parts, strconv.Itoa(l.N))
			}
		}
		return strings.Join(parts, " ")
	}
	for _, c := range []struct {
		page, pages int
		want        string
	}{
		{1, 3, "[1] 2 3"},
		{4, 7, "1 2 3 [4] 5 6 7"},
		{1, 12, "[1] 2 3 … 12"},
		{6, 12, "1 … 4 5 [6] 7 8 … 12"},
		{5, 12, "1 2 3 4 [5] 6 7 … 12"},
		{12, 12, "1 … 10 11 [12]"},
	} {
		if got := render(c.page, c.pages); got != c.want {
			t.Errorf("pageLinks(%d, %d) = %s, want %s", c.page, c.pages, got, c.want)
		}
	}
}
