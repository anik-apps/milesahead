package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/offer"
)

// templateKeyDetail holds a key detail for template rendering.
type templateKeyDetail struct {
	Label string
	Value string
}

// templateArticleLink holds a link for template rendering.
type templateArticleLink struct {
	Text string
	URL  string
}

// tagItem holds a tag slug and display name for the tag filter UI.
type tagItem struct {
	Slug string
	Name string
}

// linkedCardEmbed is the per-card data passed to the article template for the
// bottom card-embed section. Joined from generated_articles.related_cards_json
// and reference_pages.
type linkedCardEmbed struct {
	Slug          string
	Title         string
	Issuer        string
	Category      string
	AnnualFee     string // formatted: "$95" or "$0 (no annual fee)"
	WelcomeBonus  string
	TopEarning    string // first 2 entries from earning_rates_json, joined
	ScoutsVerdict string
	ImageURL      string
	ApplyURL      string
}

// predefinedTags is the list of tags shown as filter pills on the homepage,
// derived from ai.ValidTags so there is a single source of truth.
var predefinedTags = func() []tagItem {
	items := make([]tagItem, len(ai.ValidTags))
	for i, s := range ai.ValidTags {
		words := strings.Split(s, "-")
		for j, w := range words {
			if len(w) > 0 {
				words[j] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		items[i] = tagItem{Slug: s, Name: strings.Join(words, " ")}
	}
	return items
}()

// articleTagItem holds a tag slug and display name for article badge rendering.
type articleTagItem struct {
	Slug string
	Name string
}

// publicArticleRow holds data for rendering an article card on public pages.
type publicArticleRow struct {
	ID               int64
	Title            string
	Slug             string
	Content          string
	Excerpt          string
	Type             string
	Status           string
	Featured         bool
	PublishedAt      string
	SourceCount      int
	ImageURL         string
	ImageThumbnail   string
	ImageAttribution string
	// ThumbSrcset offers the display image as a sharper stand-in for the
	// thumbnail where a grid card shows it wide; see thumbSrcset.
	ThumbSrcset string
	// Ended marks an article whose offer has passed its end date. Listings
	// show it with an "Ended" badge and de-emphasised.
	Ended bool
}

// searchArticleRow is an article search result plus whether its offer ended.
type searchArticleRow struct {
	db.SearchResult
	Ended bool
}

// recentChangeRow is a card's recent change plus whether the offer in the
// article it links to has ended, and that article's type for the badge.
type recentChangeRow struct {
	db.RecentChange
	Ended       bool
	ArticleType string
}

// gridThumbSlotMax is the widest, in CSS px, a bottom-grid card shows its
// thumbnail where the srcset applies (769-1024 px, two columns): the source's
// sizes, (100vw - 92px) / 2, at 1024 px.
const gridThumbSlotMax = 466

// thumbSrcset returns a srcset of the thumbnail and the display image, each
// with the width its URL fixes, for the bottom grid's two-column layout
// (769-1024 px wide). There a card is up to 466 CSS px wide, so a 500px
// thumbnail is soft on a 2x screen. home.html offers it only in that range:
// at three columns a card is at most 361 px, and a 2x screen fetching the
// display image for all 16 cards more than doubled the page's image bytes.
//
// It returns "" unless:
//
//   - the URLs are two sizes of one picture (see images.SrcsetWidths). Only
//     the two URLs the row stores -- each confirmed to load before it was
//     written -- are ever referenced.
//   - the thumbnail alone fills the widest card on a 1x screen, so no 1x
//     screen fetches the display image.
//   - the display image is the wider of the two.
//   - neither URL has a comma or white space, which separate srcset entries.
//
// The page does not know how heavy the display image is, so the srcset does
// not check it. The pick path confirms both fields within their byte caps.
// The resize settles for a display image over its cap only when every step
// down to 500px is over 1 MiB and the copy it takes is either lighter than
// the stored file or, if the stored file no longer loads, at most 2 MiB; if
// it also chose the thumbnail, that thumbnail is the 330px step -- the 500px
// copy is over the 512 KiB thumbnail cap too -- and the row gets no srcset.
// That does not hold for a thumbnail the resize left as it was: a 500px
// thumbnail stored some other way beside an over-cap display image would
// still be offered it. Keeping those out would take the display image's
// size stored with the row, which nothing records.
func thumbSrcset(thumbnail, image string) string {
	tw, iw, ok := images.SrcsetWidths(thumbnail, image)
	if !ok || tw < gridThumbSlotMax || iw <= tw || strings.ContainsAny(thumbnail+image, ", \t\n\r\f") {
		return ""
	}
	return fmt.Sprintf("%s %dw, %s %dw", thumbnail, tw, image, iw)
}

// sourceInfo holds source data for article detail pages.
type sourceInfo struct {
	Title string
	URL   string
}

// absoluteURL turns a site-relative path such as "/static/img/cards/x.png" into
// an absolute URL for contexts that leave the site: og:image and JSON-LD, which
// crawlers resolve without a base. Absolute URLs are returned unchanged.
func absoluteURL(domain, u string) string {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return "https://" + domain + u
	}
	return u
}

func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	typeFilter := r.URL.Query().Get("type")
	tagFilter := r.URL.Query().Get("tag")

	var articles []db.GeneratedArticle
	var err error
	today := app.offerToday()

	if tagFilter != "" {
		articles, err = app.db.ListPublishedArticlesByTag(tagFilter, 20, 0)
	} else if typeFilter != "" {
		articles, err = app.db.ListPublishedArticlesByType(typeFilter, 20, 0)
	} else {
		// The default feed is what a reader takes as "current", so offers
		// that have ended are left out of it. Filtered views and search
		// still list them, marked as ended.
		articles, err = app.db.ListLivePublishedArticles(today, 20, 0)
	}
	if err != nil {
		http.Error(w, "Failed to load articles", http.StatusInternalServerError)
		return
	}
	// An empty live feed on a site that has published articles means every
	// one of them has ended, which the empty state should say rather than
	// "nothing published yet".
	allEnded := false
	if len(articles) == 0 && tagFilter == "" && typeFilter == "" {
		if published, perr := app.db.ListPublishedArticles(1, 0); perr == nil && len(published) > 0 {
			allEnded = true
		}
	}

	var rows []publicArticleRow
	for _, a := range articles {
		row := publicArticleRow{
			ID:               a.ID,
			Title:            a.Title,
			Slug:             a.Slug,
			Content:          a.Content,
			Excerpt:          truncate(a.Content, 200),
			Type:             a.Type,
			Status:           a.Status,
			Featured:         a.Featured,
			ImageURL:         a.ImageURL,
			ImageThumbnail:   a.ImageThumbnail,
			ImageAttribution: a.ImageAttribution,
			ThumbSrcset:      thumbSrcset(a.ImageThumbnail, a.ImageURL),
			Ended:            offer.Ended(a.ExpiresOn, today),
		}
		if a.PublishedAt != nil {
			row.PublishedAt = a.PublishedAt.Format("Jan 2, 2006")
		}
		if count, cerr := app.db.CountArticleSources(a.ID); cerr == nil {
			row.SourceCount = count
		}
		rows = append(rows, row)
	}

	// Determine featured article.
	var featured *publicArticleRow
	var sidebar []publicArticleRow
	var grid []publicArticleRow

	if len(rows) > 0 {
		featuredIdx := pickFeatured(rows)
		f := rows[featuredIdx]
		featured = &f

		// Remaining articles (skip the featured one).
		remaining := make([]publicArticleRow, 0, len(rows)-1)
		for i, r := range rows {
			if i != featuredIdx {
				remaining = append(remaining, r)
			}
		}

		// Sidebar: up to 3.
		if len(remaining) > 3 {
			sidebar = remaining[:3]
			grid = remaining[3:]
		} else {
			sidebar = remaining
		}
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	data := map[string]interface{}{
		"Title":           "Home",
		"Featured":        featured,
		"Sidebar":         sidebar,
		"Grid":            grid,
		"Articles":        rows,
		"TypeFilter":      typeFilter,
		"AllEnded":        allEnded,
		"Types":           []string{"deal", "roundup", "alert", "summary"},
		"ActiveTag":       tagFilter,
		"Tags":            predefinedTags,
		"MetaDescription": "The latest miles, points, and credit card deals — curated daily.",
		"CanonicalURL":    "https://" + domain + "/",
		"OGType":          "website",
		"JSONLD": buildJSONLD(websiteLDData{
			Context: "https://schema.org",
			Type:    "WebSite",
			Name:    "MilesAhead",
			URL:     "https://" + domain + "/",
			PotentialAction: jsonLDSearchAction{
				Type:       "SearchAction",
				Target:     "https://" + domain + "/search?q={search_term_string}",
				QueryInput: "required name=search_term_string",
			},
		}),
	}
	app.renderTemplate(w, "home.html", data)
}

// endedLabel is the badge on an article whose end date has passed: "Ended"
// for an offer, and "Deadline passed" for a news story ("summary"), which
// reports a deadline rather than offering a deal.
func endedLabel(articleType string) string {
	if articleType == "summary" {
		return "Deadline passed"
	}
	return "Ended"
}

// endedNote is the sentence telling a reader that an article's offer has
// ended ("This offer ended on July 29, 2026."), or "" without an end date. A
// news story ("summary") reports a deadline rather than offering a deal, so
// it says the deadline passed.
func endedNote(articleType, endsOn string) string {
	on := offer.Display(endsOn)
	if on == "" {
		return ""
	}
	if articleType == "summary" {
		return "The deadline in this story passed on " + on + "."
	}
	return "This offer ended on " + on + "."
}

// pickFeatured chooses the article for the large featured slot: the first one
// flagged as featured, else the newest. An ended offer is never put there
// while a live article is available -- in a filtered view it stays in the
// list, marked, but does not headline it.
func pickFeatured(rows []publicArticleRow) int {
	for i, r := range rows {
		if r.Featured && !r.Ended {
			return i
		}
	}
	for i, r := range rows {
		if !r.Ended {
			return i
		}
	}
	// Everything has ended: fall back to the usual rule.
	for i, r := range rows {
		if r.Featured {
			return i
		}
	}
	return 0
}

// buildLinkedCardEmbeds joins related_cards_json (slug list) to reference_pages
// rows and shapes the result for the article template.
func (app *App) buildLinkedCardEmbeds(relatedCardsJSON string) []linkedCardEmbed {
	if relatedCardsJSON == "" || relatedCardsJSON == "[]" {
		return nil
	}
	var refs []ai.RelatedCard
	if err := json.Unmarshal([]byte(relatedCardsJSON), &refs); err != nil {
		slog.Warn("buildLinkedCardEmbeds: unmarshal", "err", err)
		return nil
	}
	out := make([]linkedCardEmbed, 0, len(refs))
	for _, r := range refs {
		page, err := app.db.GetReferencePageBySlug(r.Slug)
		if err != nil || page == nil {
			continue
		}
		if page.Status != "published" {
			continue
		}
		fee := fmt.Sprintf("$%d", page.AnnualFeeAmount)
		if page.AnnualFeeAmount == 0 {
			fee = "$0 (no annual fee)"
		}
		out = append(out, linkedCardEmbed{
			Slug:          page.Slug,
			Title:         page.Title,
			Issuer:        page.Issuer,
			Category:      page.Category,
			AnnualFee:     fee,
			WelcomeBonus:  page.WelcomeBonusText,
			TopEarning:    formatTopEarning(page.EarningRatesJSON, 2),
			ScoutsVerdict: page.ScoutsVerdict,
			ImageURL:      page.ImageURL,
			ApplyURL:      page.ApplyURL,
		})
	}
	return out
}

// formatTopEarning returns up to n earning categories formatted as
// "5x Dining · 4x Travel". Reuses the existing rateEntry type and
// earningRateCategories slice defined elsewhere in this package.
func formatTopEarning(earningRatesJSON string, n int) string {
	if earningRatesJSON == "" || earningRatesJSON == "{}" {
		return ""
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(earningRatesJSON), &raw); err != nil {
		return ""
	}
	parts := make([]string, 0, n)
	for _, meta := range earningRateCategories {
		if len(parts) >= n {
			break
		}
		val, ok := raw[meta.Key]
		if !ok {
			continue
		}
		var entry rateEntry
		if err := json.Unmarshal(val, &entry); err != nil {
			continue
		}
		if entry.Rate == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s", entry.Rate, meta.Label))
	}
	return strings.Join(parts, " · ")
}

// minVotesToShowCounts is how many votes an article needs before its helpful /
// not helpful counts are shown. Below that, a count says more about traffic than
// about the article.
const minVotesToShowCounts = 10

func (app *App) handleArticle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	article, err := app.db.GetGeneratedArticleBySlug(slug)
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Failed to load article", http.StatusInternalServerError)
		return
	}

	// If not approved, show "under review" message.
	if article.Status != db.StatusApproved {
		data := map[string]interface{}{
			"Title":       article.Title,
			"UnderReview": true,
		}
		app.renderTemplate(w, "article.html", data)
		return
	}

	// Load linked sources.
	rawArticles, err := app.db.GetLinkedRawArticles(article.ID)
	if err != nil {
		rawArticles = nil // non-fatal
	}

	var sources []sourceInfo
	for _, ra := range rawArticles {
		sources = append(sources, sourceInfo{
			Title: ra.Title,
			URL:   ra.URL,
		})
	}

	// Load vote counts.
	var upvotes, downvotes int
	if vc, err := app.db.GetVoteCounts(article.ID); err == nil {
		upvotes = vc.Upvotes
		downvotes = vc.Downvotes
	}

	publishedAt := ""
	if article.PublishedAt != nil {
		publishedAt = article.PublishedAt.Format("Jan 2, 2006")
	}

	// Parse structured content from DB.
	var keyDetails []templateKeyDetail
	if article.KeyDetails != "" && article.KeyDetails != "[]" {
		json.Unmarshal([]byte(article.KeyDetails), &keyDetails)
	}
	var articleLinks []templateArticleLink
	if article.Links != "" && article.Links != "[]" {
		json.Unmarshal([]byte(article.Links), &articleLinks)
	}

	type relatedCard struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	}
	var relatedCards []relatedCard
	if article.RelatedCardsJSON != "" && article.RelatedCardsJSON != "[]" {
		json.Unmarshal([]byte(article.RelatedCardsJSON), &relatedCards)
	}

	var articleTags []articleTagItem
	if article.TagsJSON != "" && article.TagsJSON != "[]" {
		var slugs []string
		if err := json.Unmarshal([]byte(article.TagsJSON), &slugs); err == nil {
			for _, s := range slugs {
				words := strings.Split(s, "-")
				for j, w := range words {
					if len(w) > 0 {
						words[j] = strings.ToUpper(w[:1]) + w[1:]
					}
				}
				articleTags = append(articleTags, articleTagItem{
					Slug: s,
					Name: strings.Join(words, " "),
				})
			}
		}
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	canonicalURL := "https://" + domain + "/article/" + article.Slug
	metaDesc := truncate(article.TLDR, 160)
	if metaDesc == "" {
		metaDesc = truncate(article.Content, 160)
	}

	datePublished := ""
	if article.PublishedAt != nil {
		datePublished = article.PublishedAt.Format("2006-01-02")
	}

	// An ended offer keeps its page, indexable and intact; a banner says it
	// has ended so nobody acts on it.
	offerEnded := offer.Ended(article.ExpiresOn, app.offerToday())

	data := map[string]interface{}{
		"Title":              article.Title,
		"Article":            article,
		"Sources":            sources,
		"Upvotes":            upvotes,
		"Downvotes":          downvotes,
		"ShowVoteCounts":     upvotes+downvotes >= minVotesToShowCounts,
		"PublishedAt":        publishedAt,
		"UnderReview":        false,
		"TLDR":               article.TLDR,
		"KeyDetails":         keyDetails,
		"Links":              articleLinks,
		"RelatedCards":       relatedCards,
		"ArticleTags":        articleTags,
		"LinkedCards":        app.buildLinkedCardEmbeds(article.RelatedCardsJSON),
		"LinkedCardsHeading": article.LinkedCardsHeading,
		"ScoutsTake":         article.ScoutsTake,
		"OfferEnded":         offerEnded,
		"OfferEndedNote":     endedNote(article.Type, article.ExpiresOn),
		"PersonaName":        app.cfg.Site.PersonaName,
		"MetaDescription":    metaDesc,
		"CanonicalURL":       canonicalURL,
		"OGType":             "article",
		"OGImage":            article.ImageURL,
		"JSONLD": buildJSONLD(articleLDData{
			Context:       "https://schema.org",
			Type:          "Article",
			Headline:      article.Title,
			DatePublished: datePublished,
			Description:   metaDesc,
			Image:         article.ImageURL,
			Author:        jsonLDOrg{Type: "Organization", Name: "MilesAhead"},
			Publisher:     jsonLDOrg{Type: "Organization", Name: "MilesAhead"},
		}),
	}
	app.renderTemplate(w, "article.html", data)
}

func (app *App) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	if email == "" || !strings.Contains(email, "@") {
		data := map[string]interface{}{
			"Title": "Subscribe",
			"Error": "Please provide a valid email address.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	token := generateToken()
	_, err := app.db.CreateSubscriber(&db.Subscriber{
		Email: email,
		Token: token,
	})
	if err != nil {
		// Likely duplicate email.
		data := map[string]interface{}{
			"Title": "Subscribe",
			"Error": "This email is already subscribed.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	data := map[string]interface{}{
		"Title":   "Subscribe",
		"Success": "Please check your email to confirm your subscription.",
	}
	app.renderTemplate(w, "subscribe.html", data)
}

func (app *App) handleSubscribeConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		data := map[string]interface{}{
			"Title": "Confirm Subscription",
			"Error": "Invalid confirmation link.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	if err := app.db.ConfirmSubscriber(token); err != nil {
		data := map[string]interface{}{
			"Title": "Confirm Subscription",
			"Error": "Invalid or expired confirmation link.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	data := map[string]interface{}{
		"Title":   "Subscription Confirmed",
		"Success": "Your subscription has been confirmed!",
	}
	app.renderTemplate(w, "subscribe.html", data)
}

func (app *App) handleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		data := map[string]interface{}{
			"Title": "Unsubscribe",
			"Error": "Invalid unsubscribe link.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	if err := app.db.Unsubscribe(token); err != nil {
		data := map[string]interface{}{
			"Title": "Unsubscribe",
			"Error": "Invalid or expired unsubscribe link.",
		}
		app.renderTemplate(w, "subscribe.html", data)
		return
	}

	data := map[string]interface{}{
		"Title":   "Unsubscribed",
		"Success": "You have been unsubscribed successfully.",
	}
	app.renderTemplate(w, "subscribe.html", data)
}

func (app *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	articles, err := app.db.ListPublishedArticles(20, 0)
	if err != nil {
		http.Error(w, "Failed to generate feed", http.StatusInternalServerError)
		return
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, `<rss version="2.0">`)
	fmt.Fprint(w, `<channel>`)
	fmt.Fprintf(w, `<title>%s</title>`, html.EscapeString("MilesAhead"))
	fmt.Fprintf(w, `<link>https://%s</link>`, html.EscapeString(domain))
	fmt.Fprint(w, `<description>Curated news and analysis</description>`)

	today := app.offerToday()
	for _, a := range articles {
		excerpt := truncate(a.Content, 200)
		// An ended offer is marked, not dropped: a category, and the ended
		// note leading the description. Its title is left alone because
		// feed readers have already stored it.
		ended := offer.Ended(a.ExpiresOn, today)
		if ended {
			excerpt = endedNote(a.Type, a.ExpiresOn) + " " + excerpt
		}
		link := fmt.Sprintf("https://%s/article/%s", domain, a.Slug)
		pubDate := ""
		if a.PublishedAt != nil {
			pubDate = a.PublishedAt.Format("Mon, 02 Jan 2006 15:04:05 -0700")
		}

		fmt.Fprint(w, `<item>`)
		fmt.Fprintf(w, `<title>%s</title>`, html.EscapeString(a.Title))
		fmt.Fprintf(w, `<link>%s</link>`, html.EscapeString(link))
		fmt.Fprintf(w, `<description>%s</description>`, html.EscapeString(excerpt))
		if ended {
			fmt.Fprintf(w, `<category>%s</category>`, endedLabel(a.Type))
		}
		if pubDate != "" {
			fmt.Fprintf(w, `<pubDate>%s</pubDate>`, pubDate)
		}
		fmt.Fprintf(w, `<guid>%s</guid>`, html.EscapeString(link))
		fmt.Fprint(w, `</item>`)
	}

	fmt.Fprint(w, `</channel>`)
	fmt.Fprint(w, `</rss>`)
}

// --- Public Reference Card Handlers ---

// publicCardRow holds data for a card in the public listing.
type publicCardRow struct {
	ID             int64
	Title          string
	Slug           string
	Issuer         string
	ImageURL       string
	VerdictPreview string
}

// publicCardGroup groups cards by category for the public listing.
type publicCardGroup struct {
	Name  string
	Cards []publicCardRow
}

// compareGroup groups cards by category for the "Compare with..." dropdown.
type compareGroup struct {
	Category string
	Cards    []struct{ Slug, Title string }
}

// categoryDisplayNames maps DB category slugs to display names.
var categoryDisplayNames = map[string]string{
	"travel":    "Travel & Premium",
	"cash-back": "Cash Back",
	"business":  "Business",
	"hotel":     "Hotel",
	"airline":   "Airline",
	"rewards":   "Rewards",
}

// categoryOrder defines the display order of categories.
var categoryOrder = []string{"travel", "cash-back", "hotel", "airline", "business", "rewards"}

func (app *App) handleCardsList(w http.ResponseWriter, r *http.Request) {
	pages, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		http.Error(w, "Failed to load cards", http.StatusInternalServerError)
		return
	}

	personaName := app.cfg.Site.PersonaName

	// Group by category.
	catMap := make(map[string][]publicCardRow)
	for _, p := range pages {
		verdict := truncate(p.ScoutsVerdict, 120)
		row := publicCardRow{
			ID:             p.ID,
			Title:          p.Title,
			Slug:           p.Slug,
			Issuer:         p.Issuer,
			ImageURL:       p.ImageURL,
			VerdictPreview: verdict,
		}
		catMap[p.Category] = append(catMap[p.Category], row)
	}

	var groups []publicCardGroup
	for _, cat := range categoryOrder {
		if cards, ok := catMap[cat]; ok {
			name := categoryDisplayNames[cat]
			if name == "" {
				name = cat
			}
			groups = append(groups, publicCardGroup{Name: name, Cards: cards})
		}
	}
	// Add any categories not in the predefined order.
	for cat, cards := range catMap {
		found := false
		for _, c := range categoryOrder {
			if c == cat {
				found = true
				break
			}
		}
		if !found {
			name := categoryDisplayNames[cat]
			if name == "" {
				name = cat
			}
			groups = append(groups, publicCardGroup{Name: name, Cards: cards})
		}
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	data := map[string]interface{}{
		"Title":           "Cards",
		"PersonaName":     personaName,
		"Groups":          groups,
		"MetaDescription": "Compare the best credit cards for travel, cash back, hotels, and more.",
		"CanonicalURL":    "https://" + domain + "/cards/",
		"OGType":          "website",
	}
	app.renderTemplate(w, "cards.html", data)
}

func (app *App) handleCardDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	page, err := app.db.GetReferencePageBySlug(slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Only show published cards to the public.
	if page.Status != "published" {
		http.NotFound(w, r)
		return
	}

	personaName := app.cfg.Site.PersonaName

	var sections []db.ReferenceSection
	if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
		json.Unmarshal([]byte(page.SectionsJSON), &sections)
	}

	var recentChanges []db.RecentChange
	if page.RecentChangesJSON != "" && page.RecentChangesJSON != "[]" {
		json.Unmarshal([]byte(page.RecentChangesJSON), &recentChanges)
	}
	changeRows := app.markEndedChanges(recentChanges)

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	canonicalURL := "https://" + domain + "/cards/" + page.Slug
	metaDesc := truncate(page.ScoutsVerdict, 160)

	// Build "Compare with..." groups: all published cards except the current one.
	var compareGroups []compareGroup
	allCards, err := app.db.ListPublishedReferencePages("")
	if err == nil {
		groupMap := map[string]*compareGroup{}
		var groupOrder []string
		for _, c := range allCards {
			if c.Slug == page.Slug {
				continue
			}
			cat := c.Category
			if display, ok := categoryDisplayNames[cat]; ok {
				cat = display
			}
			if _, exists := groupMap[cat]; !exists {
				groupMap[cat] = &compareGroup{Category: cat}
				groupOrder = append(groupOrder, cat)
			}
			groupMap[cat].Cards = append(groupMap[cat].Cards, struct{ Slug, Title string }{c.Slug, c.Title})
		}
		for _, cat := range groupOrder {
			compareGroups = append(compareGroups, *groupMap[cat])
		}
	}

	data := map[string]interface{}{
		"Title":           page.Title,
		"Card":            page,
		"Sections":        sections,
		"ScoutsVerdict":   page.ScoutsVerdict,
		"RecentChanges":   changeRows,
		"PersonaName":     personaName,
		"MetaDescription": metaDesc,
		"CanonicalURL":    canonicalURL,
		"OGType":          "website",
		"OGImage":         absoluteURL(domain, page.ImageURL),
		"CompareGroups":   compareGroups,
		"JSONLD": buildJSONLD(cardLDData{
			Context:     "https://schema.org",
			Type:        "Product",
			Name:        page.Title,
			Description: metaDesc,
			Image:       absoluteURL(domain, page.ImageURL),
			Brand:       jsonLDBrand{Type: "Brand", Name: page.Issuer},
		}),
	}

	// Video embed is beta-gated: it renders only with ?beta=1 so it can be
	// reviewed on the live site before going public. Noindex keeps the beta
	// variant out of search results, since canonical alone is only a hint.
	if r.URL.Query().Get("beta") == "1" {
		if v, ok := cardVideos[page.Slug]; ok && v.ID != "" {
			data["Video"] = v
			data["VideoPoster"] = "/static/img/video-poster-" + page.Slug + ".jpg"
			data["Noindex"] = true
		}
	}

	// Infographics are static first-party images with no third-party embed, so
	// unlike the video they render for everyone rather than behind ?beta=1.
	if ig, ok := cardInfographics[page.Slug]; ok {
		version := ""
		if v := app.infographicVersions[page.Slug]; v != "" {
			version = "?v=" + v
		}
		base := "/static/img/infographic-" + page.Slug
		data["Infographic"] = ig
		data["InfographicImage"] = base + ".jpg" + version
		data["InfographicTeaser"] = base + "-teaser.jpg" + version
		// 1024 wide always; the height follows the image, so a design taller than
		// 2:3 is shown whole rather than squeezed into the original layout's box.
		height := app.infographicHeights[page.Slug]
		if height == 0 {
			height = infographicDefaultHeight
		}
		data["InfographicWidth"] = infographicWidth
		data["InfographicHeight"] = height
	}

	app.renderTemplate(w, "card.html", data)
}

// markEndedChanges flags the recent changes whose linked article describes an
// offer that has ended. A card page lists these as its related articles.
func (app *App) markEndedChanges(changes []db.RecentChange) []recentChangeRow {
	if len(changes) == 0 {
		return nil
	}
	var slugs []string
	for _, c := range changes {
		if c.ArticleSlug != "" {
			slugs = append(slugs, c.ArticleSlug)
		}
	}
	ends, err := app.db.OfferEndsBySlug(slugs)
	if err != nil {
		slog.Warn("loading offer end dates for recent changes", "error", err)
	}
	today := app.offerToday()
	rows := make([]recentChangeRow, len(changes))
	for i, c := range changes {
		end := ends[c.ArticleSlug]
		rows[i] = recentChangeRow{RecentChange: c, Ended: offer.Ended(end.EndsOn, today), ArticleType: end.ArticleType}
	}
	return rows
}

func (app *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(app.startedAt).Round(time.Second).String()

	var dbStatus string
	var dbErr string
	err := app.db.DB().QueryRow("SELECT 1").Err()
	if err != nil {
		dbStatus = "error"
		dbErr = "database unreachable"
	} else {
		dbStatus = "ok"
	}

	status := "ok"
	httpCode := http.StatusOK
	if dbStatus != "ok" {
		status = "degraded"
		httpCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.WriteHeader(httpCode)

	resp := map[string]string{
		"status": status,
		"db":     dbStatus,
		"uptime": uptime,
	}
	if dbErr != "" {
		resp["error"] = dbErr
	}

	json.NewEncoder(w).Encode(resp)
}

func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		query = query[:200]
	}
	pageArticles := 1
	pageCards := 1
	if p := r.URL.Query().Get("pa"); p != "" {
		if v, err := fmt.Sscanf(p, "%d", &pageArticles); err != nil || v < 1 {
			pageArticles = 1
		}
	}
	if p := r.URL.Query().Get("pc"); p != "" {
		if v, err := fmt.Sscanf(p, "%d", &pageCards); err != nil || v < 1 {
			pageCards = 1
		}
	}

	const perPage = 10

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
		// Sanitize for FTS5: wrap in double quotes to treat as phrase.
		safeQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`

		articleOffset := (pageArticles - 1) * perPage
		articles, err := app.db.SearchArticles(safeQuery, perPage+1, articleOffset)
		if err != nil {
			articles = nil
		}
		hasMoreArticles := len(articles) > perPage
		if hasMoreArticles {
			articles = articles[:perPage]
		}

		cardOffset := (pageCards - 1) * perPage
		cards, err := app.db.SearchCards(safeQuery, perPage+1, cardOffset)
		if err != nil {
			cards = nil
		}
		hasMoreCards := len(cards) > perPage
		if hasMoreCards {
			cards = cards[:perPage]
		}

		today := app.offerToday()
		articleRows := make([]searchArticleRow, len(articles))
		for i, a := range articles {
			articleRows[i] = searchArticleRow{SearchResult: a, Ended: offer.Ended(a.ExpiresOn, today)}
		}

		data["Articles"] = articleRows
		data["Cards"] = cards
		data["PageArticles"] = pageArticles
		data["PageCards"] = pageCards
		data["HasMoreArticles"] = hasMoreArticles
		data["HasMoreCards"] = hasMoreCards
		data["HasPrevArticles"] = pageArticles > 1
		data["HasPrevCards"] = pageCards > 1
		data["PrevPageArticles"] = pageArticles - 1
		data["NextPageArticles"] = pageArticles + 1
		data["PrevPageCards"] = pageCards - 1
		data["NextPageCards"] = pageCards + 1
		data["TotalResults"] = len(articles) + len(cards)
	}

	app.renderTemplate(w, "search.html", data)
}

// --- Compare Handler ---

// compareCell holds a single cell value in the comparison table.
type compareCell struct {
	Value    string
	Note     string
	IsWinner bool
}

// compareRow holds a label and one cell per card.
type compareRow struct {
	Label string
	Cells []compareCell
}

// compareCardView holds parsed card data ready for the template.
type compareCardView struct {
	ID                 int64
	Slug               string
	Title              string
	Issuer             string
	ImageURL           string
	AnnualFeeAmount    int
	AnnualFeeNote      string
	WelcomeBonusText   string
	ScoutsVerdictShort string
}

// earningRate represents one row in the earning_rates_json array.
// earningRate holds parsed earning rate data for a single category.
type earningRate struct {
	Category   string
	DirectRate string
	PortalRate string
	Note       string
}

// genericItem holds a parsed credit/benefit/protection row.
type genericItem struct {
	Name  string
	Value string
	Note  string
}

// earningRateCategories defines the display order and labels for earning rate keys.
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

// genericCategories defines known keys and labels for credits, benefits, protections.
var genericCategories = map[string][]struct {
	Key   string
	Label string
}{
	"credits": {
		{"airline", "Airline Credit"},
		{"hotel", "Hotel Credit"},
		{"travel_other", "Other Travel Credit"},
		{"dining", "Dining Credit"},
		{"streaming", "Streaming Credit"},
	},
	"benefits": {
		{"lounge_access", "Lounge Access"},
		{"hotel_status", "Hotel Status"},
		{"global_entry_tsa", "Global Entry / TSA PreCheck"},
		{"companion_cert", "Companion Certificate"},
		{"free_checked_bags", "Free Checked Bags"},
		{"no_ftf", "No Foreign Transaction Fee"},
	},
	"protections": {
		{"trip_delay", "Trip Delay"},
		{"baggage_delay_loss", "Baggage Delay / Loss"},
		{"car_rental", "Car Rental Insurance"},
		{"purchase_protection", "Purchase Protection"},
		{"extended_warranty", "Extended Warranty"},
	},
}

// extractNumber extracts the first numeric value from strings like "3x", "5x", "2%", "$300".
func extractNumber(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "N/A" {
		return 0
	}
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	var num float64
	fmt.Sscanf(s, "%f", &num)
	return num
}

// computeWinners sets IsWinner=true on the cell with the highest numeric value.
func computeWinners(cells []compareCell) {
	if len(cells) < 2 {
		return
	}
	maxVal := -1.0
	for _, c := range cells {
		v := extractNumber(c.Value)
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal <= 0 {
		return
	}
	// Check for ties.
	winners := 0
	for _, c := range cells {
		if extractNumber(c.Value) == maxVal {
			winners++
		}
	}
	if winners == len(cells) {
		return // all tied, no winner
	}
	for i := range cells {
		if extractNumber(cells[i].Value) == maxVal {
			cells[i].IsWinner = true
		}
	}
}

func (app *App) handleCompare(w http.ResponseWriter, r *http.Request) {
	cardsParam := strings.TrimSpace(r.URL.Query().Get("cards"))

	// Load all published cards for dropdowns.
	allPages, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		http.Error(w, "Failed to load cards", http.StatusInternalServerError)
		return
	}
	type selectorCard struct {
		Slug  string
		Title string
	}
	var allCards []selectorCard
	for _, p := range allPages {
		allCards = append(allCards, selectorCard{Slug: p.Slug, Title: p.Title})
	}

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	// Parse and validate slugs.
	var slugs []string
	if cardsParam != "" {
		parts := strings.Split(cardsParam, ",")
		seen := make(map[string]bool)
		for _, s := range parts {
			s = strings.TrimSpace(s)
			if s == "" || len(s) > 100 || seen[s] {
				continue
			}
			seen[s] = true
			slugs = append(slugs, s)
			if len(slugs) >= 3 {
				break
			}
		}
	}

	// If fewer than 2 valid slugs, show selector.
	if len(slugs) < 2 {
		data := map[string]interface{}{
			"Title":           "Compare Cards",
			"SelectorMode":    true,
			"AllCards":        allCards,
			"MetaDescription": "Compare credit cards side by side.",
			"CanonicalURL":    "https://" + domain + "/compare",
			"OGType":          "website",
		}
		app.renderTemplate(w, "compare.html", data)
		return
	}

	// Load comparison data.
	cards, err := app.db.GetComparisonData(slugs)
	if err != nil {
		http.Error(w, "Failed to load comparison data", http.StatusInternalServerError)
		return
	}

	// If fewer than 2 valid cards returned, show selector.
	if len(cards) < 2 {
		data := map[string]interface{}{
			"Title":           "Compare Cards",
			"SelectorMode":    true,
			"AllCards":        allCards,
			"MetaDescription": "Compare credit cards side by side.",
			"CanonicalURL":    "https://" + domain + "/compare",
			"OGType":          "website",
		}
		app.renderTemplate(w, "compare.html", data)
		return
	}

	// Build card views.
	var cardViews []compareCardView
	for _, c := range cards {
		cv := compareCardView{
			ID:                 c.ID,
			Slug:               c.Slug,
			Title:              c.Title,
			Issuer:             c.Issuer,
			ImageURL:           c.ImageURL,
			AnnualFeeAmount:    c.AnnualFeeAmount,
			AnnualFeeNote:      c.AnnualFeeNote,
			WelcomeBonusText:   c.WelcomeBonusText,
			ScoutsVerdictShort: truncateSentence(c.ScoutsVerdict, 250),
		}
		cardViews = append(cardViews, cv)
	}

	colSpan := len(cards) + 1

	// Parse earning rates from map-format JSON and build rows.
	allEarningMaps := make([]map[string]json.RawMessage, len(cards))
	for i, c := range cards {
		if c.EarningRatesJSON != "" && c.EarningRatesJSON != "[]" && c.EarningRatesJSON != "{}" {
			json.Unmarshal([]byte(c.EarningRatesJSON), &allEarningMaps[i])
		}
		if allEarningMaps[i] == nil {
			allEarningMaps[i] = make(map[string]json.RawMessage)
		}
	}
	earningRateRows := buildEarningRateRowsFromMaps(allEarningMaps, len(cards))

	// Parse credits from map-format JSON.
	allCreditMaps := make([]map[string]json.RawMessage, len(cards))
	for i, c := range cards {
		if c.CreditsJSON != "" && c.CreditsJSON != "[]" && c.CreditsJSON != "{}" {
			json.Unmarshal([]byte(c.CreditsJSON), &allCreditMaps[i])
		}
		if allCreditMaps[i] == nil {
			allCreditMaps[i] = make(map[string]json.RawMessage)
		}
	}
	creditRows := buildGenericRowsFromMaps(allCreditMaps, len(cards), "credits")

	// Parse benefits from map-format JSON.
	allBenefitMaps := make([]map[string]json.RawMessage, len(cards))
	for i, c := range cards {
		if c.BenefitsJSON != "" && c.BenefitsJSON != "[]" && c.BenefitsJSON != "{}" {
			json.Unmarshal([]byte(c.BenefitsJSON), &allBenefitMaps[i])
		}
		if allBenefitMaps[i] == nil {
			allBenefitMaps[i] = make(map[string]json.RawMessage)
		}
	}
	benefitRows := buildGenericRowsFromMaps(allBenefitMaps, len(cards), "benefits")

	// Parse protections from map-format JSON.
	allProtectionMaps := make([]map[string]json.RawMessage, len(cards))
	for i, c := range cards {
		if c.ProtectionsJSON != "" && c.ProtectionsJSON != "[]" && c.ProtectionsJSON != "{}" {
			json.Unmarshal([]byte(c.ProtectionsJSON), &allProtectionMaps[i])
		}
		if allProtectionMaps[i] == nil {
			allProtectionMaps[i] = make(map[string]json.RawMessage)
		}
	}
	protectionRows := buildGenericRowsFromMaps(allProtectionMaps, len(cards), "protections")

	data := map[string]interface{}{
		"Title":           "Compare Cards",
		"SelectorMode":    false,
		"AllCards":        allCards,
		"Cards":           cardViews,
		"ColSpan":         colSpan,
		"EarningRateRows": earningRateRows,
		"CreditRows":      creditRows,
		"BenefitRows":     benefitRows,
		"ProtectionRows":  protectionRows,
		"MetaDescription": "Compare credit cards side by side.",
		"CanonicalURL":    "https://" + domain + "/compare",
		"OGType":          "website",
	}

	// Set CurrentSlug for template option matching.
	// Each header select needs to know its own card's slug, done via data-slot in template.

	app.renderTemplate(w, "compare.html", data)
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

// buildEarningRateRowsFromMaps parses map-format earning rate JSON into comparison rows.
func buildEarningRateRowsFromMaps(allMaps []map[string]json.RawMessage, numCards int) []compareRow {
	var rows []compareRow

	for _, cat := range earningRateCategories {
		if directPortalPairs[cat.Key] {
			// Build combined Direct|Portal row.
			directKey := cat.Key + "_direct"
			portalKey := cat.Key + "_portal"

			// Check if any card has this category.
			hasAny := false
			for i := 0; i < numCards; i++ {
				if _, ok := allMaps[i][directKey]; ok {
					hasAny = true
					break
				}
				if _, ok := allMaps[i][portalKey]; ok {
					hasAny = true
					break
				}
			}
			if !hasAny {
				continue
			}

			cells := make([]compareCell, numCards)
			hasPortal := false
			for i := 0; i < numCards; i++ {
				if raw, ok := allMaps[i][portalKey]; ok {
					var e rateEntry
					json.Unmarshal(raw, &e)
					if e.Rate != "" && e.Rate != "-" {
						hasPortal = true
						break
					}
				}
			}

			for i := 0; i < numCards; i++ {
				var directRate, portalRate, note string
				if raw, ok := allMaps[i][directKey]; ok {
					var e rateEntry
					json.Unmarshal(raw, &e)
					directRate = e.Rate
					note = e.Note
				}
				if raw, ok := allMaps[i][portalKey]; ok {
					var e rateEntry
					json.Unmarshal(raw, &e)
					portalRate = e.Rate
					if note == "" {
						note = e.Note
					}
				}

				if directRate == "" && portalRate == "" {
					cells[i] = compareCell{Value: "-"}
					continue
				}

				if hasPortal {
					d := directRate
					if d == "" || d == "N/A" {
						d = "—"
					}
					p := portalRate
					if p == "" || p == "N/A" || p == "—" {
						// No portal — show direct only with "no portal" note
						val := d + " D"
						if note != "" {
							note = note + "; no portal"
						} else {
							note = "No portal booking bonus"
						}
						cells[i] = compareCell{Value: val, Note: note}
					} else {
						cells[i] = compareCell{Value: d + " D | " + p + " P", Note: note}
					}
				} else {
					v := directRate
					if v == "" {
						v = "-"
					}
					cells[i] = compareCell{Value: v, Note: note}
				}
			}

			if hasPortal {
				portalCells := make([]compareCell, numCards)
				for i := 0; i < numCards; i++ {
					if raw, ok := allMaps[i][portalKey]; ok {
						var e rateEntry
						json.Unmarshal(raw, &e)
						portalCells[i] = compareCell{Value: e.Rate}
					}
				}
				computeWinners(portalCells)
				for i, pc := range portalCells {
					cells[i].IsWinner = pc.IsWinner
				}
			} else {
				computeWinners(cells)
			}

			label := cat.Label
			if hasPortal {
				label = cat.Label + " (D|P)"
			}
			rows = append(rows, compareRow{Label: label, Cells: cells})
		} else {
			// Simple category (dining, groceries, gas, streaming, base).
			hasAny := false
			for i := 0; i < numCards; i++ {
				if _, ok := allMaps[i][cat.Key]; ok {
					hasAny = true
					break
				}
			}
			if !hasAny {
				continue
			}

			cells := make([]compareCell, numCards)
			for i := 0; i < numCards; i++ {
				raw, ok := allMaps[i][cat.Key]
				if !ok {
					cells[i] = compareCell{Value: "-"}
					continue
				}
				var e rateEntry
				json.Unmarshal(raw, &e)
				v := e.Rate
				if v == "" {
					v = "-"
				}
				cells[i] = compareCell{Value: v, Note: e.Note}
			}
			computeWinners(cells)
			rows = append(rows, compareRow{Label: cat.Label, Cells: cells})
		}
	}

	// Transfer partners row.
	hasTP := false
	for i := 0; i < numCards; i++ {
		if _, ok := allMaps[i]["transfer_partners"]; ok {
			hasTP = true
			break
		}
	}
	if hasTP {
		cells := make([]compareCell, numCards)
		for i := 0; i < numCards; i++ {
			raw, ok := allMaps[i]["transfer_partners"]
			if !ok {
				cells[i] = compareCell{Value: "-"}
				continue
			}
			var tp transferPartnersEntry
			json.Unmarshal(raw, &tp)
			val := tp.Program
			if tp.Count != "" {
				val += " (" + tp.Count + ")"
			}
			if len(tp.KeyPartners) > 0 {
				val += ": " + strings.Join(tp.KeyPartners, ", ")
			}
			cells[i] = compareCell{Value: val}
		}
		rows = append(rows, compareRow{Label: "Earned Currency", Cells: cells})
	}

	// Niche earning items — consolidated into a single row.
	hasNiche := false
	for i := 0; i < numCards; i++ {
		if _, ok := allMaps[i]["niche"]; ok {
			hasNiche = true
			break
		}
	}
	if hasNiche {
		cells := make([]compareCell, numCards)
		for i := 0; i < numCards; i++ {
			raw, ok := allMaps[i]["niche"]
			if !ok {
				cells[i] = compareCell{Value: "-"}
				continue
			}
			var niches []nicheEntry
			json.Unmarshal(raw, &niches)
			var parts []string
			for _, n := range niches {
				v := n.Value
				if v == "" {
					v = "Yes"
				}
				parts = append(parts, n.Label+": "+v)
			}
			cells[i] = compareCell{Value: strings.Join(parts, ", ")}
		}
		rows = append(rows, compareRow{Label: "Niche Rates", Cells: cells})
	}

	return rows
}

// buildGenericRowsFromMaps parses map-format credit/benefit/protection JSON into comparison rows.
func buildGenericRowsFromMaps(allMaps []map[string]json.RawMessage, numCards int, section string) []compareRow {
	var rows []compareRow

	knownKeys := genericCategories[section]

	// Process known keys in order.
	for _, cat := range knownKeys {
		hasAny := false
		for i := 0; i < numCards; i++ {
			if _, ok := allMaps[i][cat.Key]; ok {
				hasAny = true
				break
			}
		}
		if !hasAny {
			continue
		}

		cells := make([]compareCell, numCards)
		for i := 0; i < numCards; i++ {
			raw, ok := allMaps[i][cat.Key]
			if !ok {
				cells[i] = compareCell{Value: "-"}
				continue
			}
			var e genericEntry
			json.Unmarshal(raw, &e)
			v := e.Amount
			if v == "" {
				v = e.Value
			}
			if v == "" {
				v = "-"
			}
			cells[i] = compareCell{Value: v, Note: e.Note}
		}
		computeWinners(cells)
		rows = append(rows, compareRow{Label: cat.Label, Cells: cells})
	}

	// Process unknown keys (not in knownKeys) that appear in any card.
	knownSet := make(map[string]bool)
	for _, cat := range knownKeys {
		knownSet[cat.Key] = true
	}
	knownSet["niche"] = true // handled separately

	// Collect unknown keys in order of first appearance.
	var unknownKeys []string
	unknownSeen := make(map[string]bool)
	for i := 0; i < numCards; i++ {
		for key := range allMaps[i] {
			if !knownSet[key] && !unknownSeen[key] {
				unknownSeen[key] = true
				unknownKeys = append(unknownKeys, key)
			}
		}
	}

	for _, key := range unknownKeys {
		cells := make([]compareCell, numCards)
		for i := 0; i < numCards; i++ {
			raw, ok := allMaps[i][key]
			if !ok {
				cells[i] = compareCell{Value: "-"}
				continue
			}
			var e genericEntry
			json.Unmarshal(raw, &e)
			v := e.Amount
			if v == "" {
				v = e.Value
			}
			if v == "" {
				v = "-"
			}
			cells[i] = compareCell{Value: v, Note: e.Note}
		}
		// Format key as label: replace underscores, title case words.
		label := strings.ReplaceAll(key, "_", " ")
		words := strings.Fields(label)
		for wi, w := range words {
			if len(w) > 0 {
				words[wi] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		label = strings.Join(words, " ")
		computeWinners(cells)
		rows = append(rows, compareRow{Label: label, Cells: cells})
	}

	// Niche items — consolidated into a single row.
	hasNiche := false
	for i := 0; i < numCards; i++ {
		if _, ok := allMaps[i]["niche"]; ok {
			hasNiche = true
			break
		}
	}
	if hasNiche {
		cells := make([]compareCell, numCards)
		for i := 0; i < numCards; i++ {
			raw, ok := allMaps[i]["niche"]
			if !ok {
				cells[i] = compareCell{Value: "-"}
				continue
			}
			var niches []nicheEntry
			json.Unmarshal(raw, &niches)
			var parts []string
			for _, n := range niches {
				v := n.Value
				if v == "" {
					v = "Yes"
				}
				parts = append(parts, n.Label+": "+v)
			}
			cells[i] = compareCell{Value: strings.Join(parts, ", ")}
		}
		rows = append(rows, compareRow{Label: "Niche Credits", Cells: cells})
	}

	return rows
}

// truncate strips HTML tags and returns the first n runes, appending "..." if truncated.
func truncate(s string, n int) string {
	s = stripHTML(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// truncateSentence strips HTML tags and truncates s at the end of the last
// complete sentence (ending in '.', '!', or '?') that fits within maxRunes.
// If no sentence boundary is found, it falls back to a hard cut at maxRunes.
func truncateSentence(s string, maxRunes int) string {
	s = stripHTML(s)
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	// Walk backwards from maxRunes looking for a sentence-ending punctuation.
	sub := runes[:maxRunes]
	for i := len(sub) - 1; i >= 0; i-- {
		if sub[i] == '.' || sub[i] == '!' || sub[i] == '?' {
			return string(sub[:i+1])
		}
	}
	// Fallback: hard cut with ellipsis.
	return string(sub) + "..."
}

// stripHTML removes HTML tags from a string.
func stripHTML(s string) string {
	var result []byte
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>':
			inTag = false
		case !inTag:
			result = append(result, s[i])
		}
	}
	return strings.TrimSpace(string(result))
}

// --- JSON-LD structured data ---

type jsonLDOrg struct {
	Type string `json:"@type"`
	Name string `json:"name"`
}

type jsonLDSearchAction struct {
	Type       string `json:"@type"`
	Target     string `json:"target"`
	QueryInput string `json:"query-input"`
}

type websiteLDData struct {
	Context         string             `json:"@context"`
	Type            string             `json:"@type"`
	Name            string             `json:"name"`
	URL             string             `json:"url"`
	PotentialAction jsonLDSearchAction `json:"potentialAction"`
}

type articleLDData struct {
	Context       string    `json:"@context"`
	Type          string    `json:"@type"`
	Headline      string    `json:"headline"`
	DatePublished string    `json:"datePublished,omitempty"`
	Description   string    `json:"description,omitempty"`
	Image         string    `json:"image,omitempty"`
	Author        jsonLDOrg `json:"author"`
	Publisher     jsonLDOrg `json:"publisher"`
}

type jsonLDBrand struct {
	Type string `json:"@type"`
	Name string `json:"name"`
}

type cardLDData struct {
	Context     string      `json:"@context"`
	Type        string      `json:"@type"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Image       string      `json:"image,omitempty"`
	Brand       jsonLDBrand `json:"brand"`
}

func buildJSONLD(data interface{}) template.HTML {
	b, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	// Escape </script> to prevent XSS when embedding JSON in script tags
	s := strings.ReplaceAll(string(b), "</", `<\/`)
	return template.HTML(`<script type="application/ld+json">` + s + `</script>`)
}

// --- Sitemap & Robots ---

func (app *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	baseURL := "https://" + domain

	articles, err := app.db.ListPublishedArticles(10000, 0)
	if err != nil {
		http.Error(w, "Failed to generate sitemap", http.StatusInternalServerError)
		return
	}

	cards, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		http.Error(w, "Failed to generate sitemap", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")

	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprint(w, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)

	// Homepage
	fmt.Fprintf(w, `<url><loc>%s/</loc><priority>1.0</priority></url>`, html.EscapeString(baseURL))

	// Cards listing
	fmt.Fprintf(w, `<url><loc>%s/cards/</loc><priority>0.9</priority></url>`, html.EscapeString(baseURL))

	// Search
	fmt.Fprintf(w, `<url><loc>%s/search</loc><priority>0.3</priority></url>`, html.EscapeString(baseURL))

	// Articles
	for _, a := range articles {
		loc := fmt.Sprintf("%s/article/%s", baseURL, a.Slug)
		lastmod := ""
		if a.PublishedAt != nil {
			lastmod = a.PublishedAt.Format("2006-01-02")
		}
		fmt.Fprintf(w, `<url><loc>%s</loc><priority>0.8</priority>`, html.EscapeString(loc))
		if lastmod != "" {
			fmt.Fprintf(w, `<lastmod>%s</lastmod>`, lastmod)
		}
		fmt.Fprint(w, `</url>`)
	}

	// Cards
	for _, c := range cards {
		loc := fmt.Sprintf("%s/cards/%s", baseURL, c.Slug)
		lastmod := c.UpdatedAt.Format("2006-01-02")
		fmt.Fprintf(w, `<url><loc>%s</loc><priority>0.7</priority><lastmod>%s</lastmod></url>`, html.EscapeString(loc), lastmod)
	}

	fmt.Fprint(w, `</urlset>`)
}

func (app *App) handleRobots(w http.ResponseWriter, r *http.Request) {
	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "User-agent: *\n")
	fmt.Fprint(w, "Allow: /\n")
	fmt.Fprint(w, "Disallow: /admin/\n")
	fmt.Fprint(w, "Disallow: /metrics\n")
	fmt.Fprint(w, "Disallow: /healthz\n")
	fmt.Fprintf(w, "\nSitemap: https://%s/sitemap.xml\n", domain)
}
