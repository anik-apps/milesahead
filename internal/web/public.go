package web

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	// Ended marks the detail that gives the offer's end, once that has
	// passed: the article page strikes it through and labels it "Ended".
	Ended bool
}

// templateArticleLink holds a link for template rendering.
type templateArticleLink struct {
	Text string
	URL  string
}

// linkedCardEmbed is the per-card data passed to the article template for the
// bottom card-embed section. Joined from generated_articles.related_cards_json
// and reference_pages.
type linkedCardEmbed struct {
	Slug          string
	Title         string
	Issuer        string
	Category      string
	AnnualFee     string // formatted: "$95 annual fee" or "No annual fee"
	WelcomeBonus  string
	TopEarning    string // the 2 best earning lines, see formatTopEarning
	ScoutsVerdict string
	// VerdictLead is the verdict's opening, short enough for the card's row
	// on an article; the full verdict is on the card's page.
	VerdictLead string
	ImageURL    string
	ApplyURL    string
	ApplyLabel  string // "Apply at Chase"
}

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
	typeFilter := strings.TrimSpace(r.URL.Query().Get("type"))
	tagFilter := strings.TrimSpace(r.URL.Query().Get("tag"))

	var articles []db.GeneratedArticle
	var err error
	today := app.offerToday()

	if typeFilter != "" || tagFilter != "" {
		// Type and topic apply together. An unknown value matches nothing,
		// which shows the empty state.
		articles, err = app.db.ListPublishedArticlesFiltered(typeFilter, tagFilter, 20, 0)
	} else {
		// The default feed is what a reader takes as "current", so offers
		// that have ended are left out of it. Filtered views and search
		// still list them, marked as ended.
		articles, err = app.db.ListLivePublishedArticles(today, 20, 0)
	}
	if err != nil {
		app.serverError(w, r, "home: loading articles", err)
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

	// The type switch offers only types with something published, so a type
	// with no articles yet (or any more) does not lead to an empty page.
	publishedTypes, err := app.db.ListPublishedArticleTypes()
	if err != nil {
		slog.Warn("home: listing published article types", "error", err)
		publishedTypes = articleTypes
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
		"Filters":         buildHomeFilters(typeFilter, tagFilter, publishedTypes),
		"AllEnded":        allEnded,
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
		out = append(out, linkedCardEmbed{
			Slug:          page.Slug,
			Title:         page.Title,
			Issuer:        page.Issuer,
			Category:      page.Category,
			AnnualFee:     annualFeeLabel(page.AnnualFeeAmount),
			WelcomeBonus:  page.WelcomeBonusText,
			TopEarning:    formatTopEarning(page.EarningRatesJSON, 2),
			ScoutsVerdict: page.ScoutsVerdict,
			VerdictLead:   verdictLead(page.ScoutsVerdict, verdictLeadMax),
			ImageURL:      page.ImageURL,
			ApplyURL:      page.ApplyURL,
			ApplyLabel:    applyLabel(page.Issuer),
		})
	}
	return out
}

// formatTopEarning returns a card's n best earning lines, best first, as
// "5x flights · 5x hotels via Amex Travel". See rankEarnRates for the order.
func formatTopEarning(earningRatesJSON string, n int) string {
	rates := topEarnRates(earningRatesJSON, n)
	parts := make([]string, len(rates))
	for i, r := range rates {
		parts[i] = r.Detail()
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
		if errors.Is(err, sql.ErrNoRows) {
			app.notFound(w, http.StatusNotFound, errKindArticle, slug)
			return
		}
		app.serverError(w, r, "article: loading", err)
		return
	}

	// Anything not approved is not public. It gets the same page as a slug
	// that never existed, with nothing of the article in it (not even the
	// headline in <title>), so drafts and rejected stories stay private.
	// One that was published and has been taken down is 410 Gone, which
	// tells search engines to drop it.
	if article.Status != db.StatusApproved {
		status := http.StatusNotFound
		if article.Status == db.StatusUnpublished || article.PublishedAt != nil {
			status = http.StatusGone
		}
		app.notFound(w, status, errKindArticle, slug)
		return
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
	var tagSlugs []string
	if article.TagsJSON != "" && article.TagsJSON != "[]" {
		if err := json.Unmarshal([]byte(article.TagsJSON), &tagSlugs); err == nil {
			for _, s := range tagSlugs {
				articleTags = append(articleTags, articleTagItem{Slug: s, Name: tagName(s)})
			}
		} else {
			tagSlugs = nil
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

	// An ended offer keeps its page, indexable and intact; a banner under the
	// headline says it has ended so nobody acts on it, and the Key Detail that
	// gave the end date is marked.
	today := app.offerToday()
	offerEnded := offer.Ended(article.ExpiresOn, today)
	if offerEnded {
		for i := range keyDetails {
			keyDetails[i].Ended = offer.IsEndLabel(keyDetails[i].Label)
		}
	}

	// The topic the related stories and the "more in" link are about.
	topicSlug := articleTopic(tagSlugs)
	var topic *articleTagItem
	if topicSlug != "" {
		topic = &articleTagItem{Slug: topicSlug, Name: tagName(topicSlug)}
	}

	data := map[string]interface{}{
		"Title":              article.Title,
		"Article":            article,
		"Upvotes":            upvotes,
		"Downvotes":          downvotes,
		"ShowVoteCounts":     upvotes+downvotes >= minVotesToShowCounts,
		"PublishedAt":        publishedAt,
		"TLDR":               article.TLDR,
		"KeyDetails":         keyDetails,
		"Links":              articleLinks,
		"RelatedCards":       relatedCards,
		"ArticleTags":        articleTags,
		"LinkedCards":        storyCardsFirst(app.buildLinkedCardEmbeds(article.RelatedCardsJSON), article.Title),
		"LinkedCardsHeading": article.LinkedCardsHeading,
		"ScoutsTake":         article.ScoutsTake,
		"Infographic":        app.publicInfographic(article.InfographicJSON),
		"OfferEnded":         offerEnded,
		"OfferEndedNote":     endedNote(article.Type, article.ExpiresOn),
		"PersonaName":        app.cfg.Site.PersonaName,
		"PublishedISO":       datePublished,
		"Content":            scrollTables("Table", externalLinksNewTab(article.Content, domain)),
		"HeroSrcset":         heroSrcset(article.ImageThumbnail, article.ImageURL),
		"HeroSizes":          heroSizes,
		"HeroWidth":          heroWidth,
		"HeroHeight":         heroHeight,
		"Topic":              topic,
		"Related":            app.relatedStories(article, tagSlugs, topicSlug, today),
		"Share":              buildShareLinks(article.Title, canonicalURL),
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

func (app *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	articles, err := app.db.ListPublishedArticles(20, 0)
	if err != nil {
		slog.Error("feed: generating", "error", err)
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

func (app *App) handleCardDetail(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")

	page, err := app.db.GetReferencePageBySlug(slug)
	if err != nil {
		// Only a missing card is a 404; a failing database is a 500, not a
		// page telling the reader the card does not exist.
		if errors.Is(err, sql.ErrNoRows) {
			app.notFound(w, http.StatusNotFound, errKindCard, slug)
			return
		}
		app.serverError(w, r, "card: loading", err)
		return
	}

	// Only show published cards to the public.
	if page.Status != "published" {
		app.notFound(w, http.StatusNotFound, errKindCard, slug)
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

	// The other published cards, for the compare list and next steps.
	allCards, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		slog.Warn("card page: listing other cards", "slug", page.Slug, "err", err)
	}

	data := map[string]interface{}{
		"NavSection":      "cards",
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
		"JSONLD": buildJSONLD(cardLDData{
			Context:     "https://schema.org",
			Type:        "Product",
			Name:        page.Title,
			Description: metaDesc,
			Image:       absoluteURL(domain, page.ImageURL),
			Brand:       jsonLDBrand{Type: "Brand", Name: page.Issuer},
		}),
	}

	for k, v := range cardDetailData(page, sections, allCards) {
		data[k] = v
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
