package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/offer"
)

const (
	// searchPerPage is how many articles a results page lists.
	searchPerPage = 10
	// searchTopCards caps the cards shown above the articles: those whose
	// title holds every word of the query.
	searchTopCards = 6
	// searchOtherCards is how many other matching cards are listed before
	// the rest fold under "Show more".
	searchOtherCards = 5
	// searchMaxCards bounds the card query; there are ~50 cards in all.
	searchMaxCards = 100
	// searchMaxQuery caps the query length, in bytes.
	searchMaxQuery = 200
)

// searchArticleRow is an article search result plus what the list shows
// about it.
type searchArticleRow struct {
	db.SearchResult
	Ended bool
	Date  string
}

// searchCardRow is a card search result plus its fee as the list shows it.
type searchCardRow struct {
	db.SearchResult
	Fee string
}

// searchPageLink is one link in the results pager; N == 0 is a gap ("…").
type searchPageLink struct {
	N       int
	URL     string
	Current bool
}

func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > searchMaxQuery {
		query = strings.ToValidUTF8(query[:searchMaxQuery], "")
	}
	page := 1
	for _, key := range []string{"page", "pa"} { // pa: the old articles pager
		if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v > 0 {
			page = v
			break
		}
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	data := map[string]interface{}{
		"Title":           "Search",
		"Query":           query,
		"MetaDescription": "Search MilesAhead for credit card deals, travel rewards, and points strategies.",
		"CanonicalURL":    "https://" + domain + "/search",
		"OGType":          "website",
	}
	if query != "" {
		for k, v := range app.searchResults(query, page) {
			data[k] = v
		}
	}
	app.renderTemplate(w, "search.html", data)
}

// searchResults runs a reader's query and returns the template data for the
// results: cards whose title matches (above the articles), one page of
// articles, the other matching cards (on page 1), counts and the pager.
func (app *App) searchResults(query string, page int) map[string]interface{} {
	out := map[string]interface{}{"Searched": true}
	q := db.ParseSearchQuery(query)
	if q.Empty() {
		return out
	}

	today := app.offerToday()
	match := q.Match(false)
	articleTotal, cardMatches := app.countSearch(match, q.Phrase())
	if articleTotal == 0 && len(cardMatches) == 0 && len(q.Words()) > 1 {
		// Nothing has every word: show what has any of them, and say so.
		match = q.Match(true)
		articleTotal, cardMatches = app.countSearch(match, q.Phrase())
		out["AnyWord"] = articleTotal > 0 || len(cardMatches) > 0
	}
	out["MultiWord"] = len(q.Words()) > 1

	// Cards: those whose title holds every word go first, above the
	// articles; the rest follow the articles.
	var top, others []searchCardRow
	topIDs := map[int64]bool{}
	if len(cardMatches) > 0 {
		titled, err := app.db.SearchCardTitles(q, searchTopCards)
		if err != nil {
			slog.Warn("card title search failed", "query", query, "error", err)
		}
		for _, c := range titled {
			topIDs[c.ID] = true
			top = append(top, searchCardRow{SearchResult: c, Fee: feeLabel(c.AnnualFeeAmount)})
		}
		for _, c := range cardMatches {
			if !topIDs[c.ID] {
				others = append(others, searchCardRow{SearchResult: c, Fee: feeLabel(c.AnnualFeeAmount)})
			}
		}
	}

	pages := (articleTotal + searchPerPage - 1) / searchPerPage
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	var rows []searchArticleRow
	if articleTotal > 0 {
		articles, err := app.db.SearchArticles(match, q.Phrase(), today, searchPerPage, (page-1)*searchPerPage)
		if err != nil {
			slog.Warn("article search failed", "query", query, "error", err)
		}
		for _, a := range articles {
			rows = append(rows, searchArticleRow{
				SearchResult: a,
				Ended:        offer.Ended(a.ExpiresOn, today),
				Date:         a.PublishedAt.Format("Jan 2, 2006"),
			})
		}
	}

	out["Articles"] = rows
	out["ArticleTotal"] = articleTotal
	out["CardTotal"] = len(cardMatches)
	out["Page"] = page
	out["Pages"] = pages
	if page == 1 {
		out["TopCards"] = top
		if len(others) > searchOtherCards {
			out["OtherCards"], out["MoreCards"] = others[:searchOtherCards], others[searchOtherCards:]
		} else {
			out["OtherCards"] = others
		}
	}
	if pages > 1 {
		pageURL := func(n int) string {
			v := url.Values{"q": {query}}
			if n > 1 {
				v.Set("page", strconv.Itoa(n))
			}
			return "/search?" + v.Encode()
		}
		out["PageLinks"] = pageLinks(page, pages, pageURL)
		if page > 1 {
			out["PrevURL"] = pageURL(page - 1)
			if len(cardMatches) > 0 {
				out["CardsURL"] = pageURL(1) // cards are listed on page 1 only
			}
		}
		if page < pages {
			out["NextURL"] = pageURL(page + 1)
		}
	}
	return out
}

// countSearch returns how many articles match and every matching card (the
// card list is small enough to fetch whole).
func (app *App) countSearch(match, phrase string) (int, []db.SearchResult) {
	n, err := app.db.CountSearchArticles(match)
	if err != nil {
		slog.Warn("article search count failed", "match", match, "error", err)
		n = 0
	}
	cards, err := app.db.SearchCards(match, phrase, searchMaxCards, 0)
	if err != nil {
		slog.Warn("card search failed", "match", match, "error", err)
	}
	return n, cards
}

// pageLinks lists the pager's page links: all of them when there are few,
// otherwise the first, the last and two either side of the current page,
// with "…" for the gaps (a gap of one page shows that page instead).
func pageLinks(page, pages int, pageURL func(int) string) []searchPageLink {
	show := func(n int) bool {
		return pages <= 7 || n == 1 || n == pages || (n >= page-2 && n <= page+2)
	}
	var links []searchPageLink
	for n := 1; n <= pages; n++ {
		if !show(n) && !(show(n-1) && show(n+1)) {
			if len(links) > 0 && links[len(links)-1].N != 0 {
				links = append(links, searchPageLink{})
			}
			continue
		}
		links = append(links, searchPageLink{N: n, URL: pageURL(n), Current: n == page})
	}
	return links
}

// feeLabel is a card's annual fee as the search list shows it.
func feeLabel(amount int) string {
	if amount <= 0 {
		return "No annual fee"
	}
	return fmt.Sprintf("$%d a year", amount)
}
