package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

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

	var articles []db.GeneratedArticle
	var err error

	if typeFilter != "" {
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

	data := map[string]interface{}{
		"Title":      "Home",
		"Featured":   featured,
		"Sidebar":    sidebar,
		"Grid":       grid,
		"Articles":   rows,
		"TypeFilter": typeFilter,
		"Types":      []string{"deal", "roundup", "alert", "summary"},
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

	data := map[string]interface{}{
		"Title":        article.Title,
		"Article":      article,
		"Sources":      sources,
		"Upvotes":      upvotes,
		"Downvotes":    downvotes,
		"PublishedAt":  publishedAt,
		"UnderReview":  false,
		"TLDR":         article.TLDR,
		"KeyDetails":   keyDetails,
		"Links":        articleLinks,
		"RelatedCards": relatedCards,
		"ScoutsTake":   article.ScoutsTake,
		"PersonaName":  app.cfg.Site.PersonaName,
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

	data := map[string]interface{}{
		"Title":       "Cards",
		"PersonaName": personaName,
		"Groups":      groups,
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

	data := map[string]interface{}{
		"Title":         page.Title,
		"Card":          page,
		"Sections":      sections,
		"ScoutsVerdict": page.ScoutsVerdict,
		"RecentChanges": recentChanges,
		"PersonaName":   personaName,
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
		dbErr = err.Error()
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

	data := map[string]interface{}{
		"Title": "Search",
		"Query": query,
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
