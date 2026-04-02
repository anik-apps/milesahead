package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
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
}

// sourceInfo holds source data for article detail pages.
type sourceInfo struct {
	Title string
	URL   string
}

func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	typeFilter := r.URL.Query().Get("type")
	tagFilter := r.URL.Query().Get("tag")

	var articles []db.GeneratedArticle
	var err error

	if tagFilter != "" {
		articles, err = app.db.ListPublishedArticlesByTag(tagFilter, 20, 0)
	} else if typeFilter != "" {
		articles, err = app.db.ListPublishedArticlesByType(typeFilter, 20, 0)
	} else {
		articles, err = app.db.ListPublishedArticles(20, 0)
	}
	if err != nil {
		http.Error(w, "Failed to load articles", http.StatusInternalServerError)
		return
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
		// Try to find a featured article.
		featuredIdx := 0
		for i, r := range rows {
			if r.Featured {
				featuredIdx = i
				break
			}
		}
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
		"Types":           []string{"deal", "roundup", "alert", "summary"},
		"ActiveTag":       tagFilter,
		"Tags":            predefinedTags,
		"MetaDescription": "The latest miles, points, and credit card deals — curated daily.",
		"CanonicalURL":    "https://" + domain + "/",
		"OGType":          "website",
		"JSONLD": buildJSONLD(websiteLDData{
			Context: "https://schema.org",
			Type:    "WebSite",
			Name:    "Miles Ahead",
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

	data := map[string]interface{}{
		"Title":           article.Title,
		"Article":         article,
		"Sources":         sources,
		"Upvotes":         upvotes,
		"Downvotes":       downvotes,
		"PublishedAt":     publishedAt,
		"UnderReview":     false,
		"TLDR":            article.TLDR,
		"KeyDetails":      keyDetails,
		"Links":           articleLinks,
		"RelatedCards":    relatedCards,
		"ArticleTags":     articleTags,
		"ScoutsTake":      article.ScoutsTake,
		"PersonaName":     app.cfg.Site.PersonaName,
		"MetaDescription": metaDesc,
		"CanonicalURL":    canonicalURL,
		"OGType":          "article",
		"OGImage":         article.ImageURL,
		"JSONLD": buildJSONLD(articleLDData{
			Context:       "https://schema.org",
			Type:          "Article",
			Headline:      article.Title,
			DatePublished: datePublished,
			Description:   metaDesc,
			Image:         article.ImageURL,
			Author:        jsonLDOrg{Type: "Organization", Name: "Miles Ahead"},
			Publisher:     jsonLDOrg{Type: "Organization", Name: "Miles Ahead"},
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
	fmt.Fprintf(w, `<title>%s</title>`, html.EscapeString("Miles Ahead"))
	fmt.Fprintf(w, `<link>https://%s</link>`, html.EscapeString(domain))
	fmt.Fprint(w, `<description>Curated news and analysis</description>`)

	for _, a := range articles {
		excerpt := truncate(a.Content, 200)
		link := fmt.Sprintf("https://%s/article/%s", domain, a.Slug)
		pubDate := ""
		if a.PublishedAt != nil {
			pubDate = a.PublishedAt.Format("Mon, 02 Jan 2006 15:04:05 -0700")
		}

		fmt.Fprint(w, `<item>`)
		fmt.Fprintf(w, `<title>%s</title>`, html.EscapeString(a.Title))
		fmt.Fprintf(w, `<link>%s</link>`, html.EscapeString(link))
		fmt.Fprintf(w, `<description>%s</description>`, html.EscapeString(excerpt))
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

	domain := app.cfg.Server.Domain
	if domain == "" {
		domain = "localhost"
	}
	canonicalURL := "https://" + domain + "/cards/" + page.Slug
	metaDesc := truncate(page.ScoutsVerdict, 160)

	data := map[string]interface{}{
		"Title":           page.Title,
		"Card":            page,
		"Sections":        sections,
		"ScoutsVerdict":   page.ScoutsVerdict,
		"RecentChanges":   recentChanges,
		"PersonaName":     personaName,
		"MetaDescription": metaDesc,
		"CanonicalURL":    canonicalURL,
		"OGType":          "website",
		"OGImage":         page.ImageURL,
		"JSONLD": buildJSONLD(cardLDData{
			Context:     "https://schema.org",
			Type:        "Product",
			Name:        page.Title,
			Description: metaDesc,
			Image:       page.ImageURL,
			Brand:       jsonLDBrand{Type: "Brand", Name: page.Issuer},
		}),
	}
	app.renderTemplate(w, "card.html", data)
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
		"MetaDescription": "Search Miles Ahead for credit card deals, travel rewards, and points strategies.",
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

		data["Articles"] = articles
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

// truncate strips HTML tags and returns the first n runes, appending "..." if truncated.
func truncate(s string, n int) string {
	s = stripHTML(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
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
	Type        string `json:"@type"`
	Target      string `json:"target"`
	QueryInput  string `json:"query-input"`
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
