package newsletter

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/offer"
)

// MailSender is the interface for sending newsletter emails.
type MailSender interface {
	SendNewsletter(subject, htmlContent string, recipients []string) error
}

// templateDir is the base directory for email templates. Override for testing.
var templateDir = "templates/email"

// weeklySubject opens every weekly issue's subject.
const weeklySubject = "MilesAhead Weekly"

// Service handles newsletter generation and delivery.
type Service struct {
	db       *db.Database
	sender   MailSender
	ai       ai.HFClientInterface
	aiModel  string
	sendTime string
	domain   string
	// loc is the schedule's timezone, resolved once: an unknown name falls
	// back to UTC with one warning, not one per lookup.
	loc *time.Location
	// now is the clock: time.Now, except in tests that need a given time
	// of day.
	now func() time.Time
}

// NewService creates a new newsletter Service.
func NewService(database *db.Database, sender MailSender, cfg config.NewsletterConfig) *Service {
	st := cfg.SendTime
	if st == "" {
		st = "08:00"
	}
	return &Service{
		db:       database,
		sender:   sender,
		loc:      offer.Location(cfg.Timezone),
		sendTime: st,
		now:      time.Now,
	}
}

// NewServiceWithAI creates a newsletter Service with AI ranking support.
func NewServiceWithAI(database *db.Database, sender MailSender, cfg config.NewsletterConfig, aiClient ai.HFClientInterface, aiModel, domain string) *Service {
	svc := NewService(database, sender, cfg)
	svc.ai = aiClient
	svc.aiModel = aiModel
	svc.domain = domain
	return svc
}

// articleItem represents a single article for the newsletter template.
type articleItem struct {
	Title   string
	Slug    string
	Excerpt string
	Summary string
	Tags    []string
}

// newsletterData is the template data for rendering a newsletter.
type newsletterData struct {
	Date           string
	Domain         string
	Lead           *articleItem
	Sidebar        []articleItem
	TagGroups      map[string][]articleItem
	CardOfWeek     *cardOfWeekData
	ScoutsPick     *scoutsPickData
	UnsubscribeURL string
}

// cardOfWeekData holds card-of-the-week info for the template.
type cardOfWeekData struct {
	ID       int64
	Title    string
	Slug     string
	ImageURL string
	Verdict  string
}

// scoutsPickData holds the Scout's Pick article info for the template.
type scoutsPickData struct {
	ID         int64
	Title      string
	Slug       string
	ScoutsTake string
	ImageURL   string
}

// excerpt returns the first ~300 characters of content, breaking at a sentence or word boundary.
func excerpt(content string, maxLen int) string {
	if len(content) <= maxLen {
		return content
	}
	cut := content[:maxLen]
	// Try to break at a sentence boundary.
	if idx := strings.LastIndexAny(cut, ".!?"); idx > maxLen/2 {
		return cut[:idx+1]
	}
	// Fall back to word boundary.
	if idx := strings.LastIndex(cut, " "); idx > 0 {
		return cut[:idx] + "..."
	}
	return cut + "..."
}

// summary returns the first ~120 characters for sidebar items.
func summary(content string) string {
	return excerpt(content, 120)
}

// listRecentApproved fetches approved articles published in the last 24
// hours whose offer has not ended.
func (s *Service) listRecentApproved() ([]db.GeneratedArticle, error) {
	return s.listLiveSince(s.now().UTC().Add(-24 * time.Hour))
}

// listLiveSince fetches approved articles published since since whose offer
// has not ended.
func (s *Service) listLiveSince(since time.Time) ([]db.GeneratedArticle, error) {
	articles, err := s.db.ListApprovedArticlesSince(since)
	if err != nil {
		return nil, err
	}
	return s.liveOffers(articles), nil
}

// liveOffers drops articles whose offer has ended, judged on the same date
// as the site (offer.Today). An offer is live through its last day. A reader
// acting on an ended transfer bonus makes a transfer that cannot be undone,
// so an ended offer is never sent, featured or made Scout's Pick.
func (s *Service) liveOffers(articles []db.GeneratedArticle) []db.GeneratedArticle {
	today := offer.Today(s.now())
	live := make([]db.GeneratedArticle, 0, len(articles))
	for _, a := range articles {
		if offer.Ended(a.ExpiresOn, today) {
			slog.Info("newsletter: leaving out an ended offer", "article_id", a.ID, "ended", a.ExpiresOn)
			continue
		}
		live = append(live, a)
	}
	return live
}

// containsArticle reports whether id is among articles.
func containsArticle(articles []db.GeneratedArticle, id int64) bool {
	for _, a := range articles {
		if a.ID == id {
			return true
		}
	}
	return false
}

// todayDate returns today's date string in YYYY-MM-DD format for the configured timezone.
func (s *Service) todayDate() string {
	return s.now().In(s.loc).Format("2006-01-02")
}

// nextSaturday returns the date string for the next Saturday (or today if Saturday).
func (s *Service) nextSaturday() string {
	now := s.now().In(s.loc)
	daysUntilSat := (6 - int(now.Weekday()) + 7) % 7
	if daysUntilSat == 0 {
		// Today is Saturday.
		return now.Format("2006-01-02")
	}
	return now.AddDate(0, 0, daysUntilSat).Format("2006-01-02")
}

// thisSaturday returns the most recent Saturday date (today if Saturday, else last Saturday).
func (s *Service) thisSaturday() string {
	now := s.now().In(s.loc)
	daysSinceSat := (int(now.Weekday()) - 6 + 7) % 7
	return now.AddDate(0, 0, -daysSinceSat).Format("2006-01-02")
}

// parseTags parses a JSON array of tag strings.
func parseTags(tagsJSON string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		return nil
	}
	return tags
}

// parseRelatedCards parses related_cards_json and returns card slugs.
func parseRelatedCards(relatedJSON string) []string {
	var cards []string
	if err := json.Unmarshal([]byte(relatedJSON), &cards); err != nil {
		return nil
	}
	return cards
}

// groupArticlesByTag groups articles by their primary (first) tag.
// Each article appears in exactly one group to avoid duplicates in the newsletter.
func groupArticlesByTag(articles []db.GeneratedArticle) map[string][]articleItem {
	groups := make(map[string][]articleItem)
	for _, a := range articles {
		tags := parseTags(a.TagsJSON)
		item := articleItem{
			Title:   a.Title,
			Slug:    a.Slug,
			Summary: summary(a.TLDR),
			Tags:    tags,
		}
		if len(tags) > 0 {
			groups[tags[0]] = append(groups[tags[0]], item)
		} else {
			groups["other"] = append(groups["other"], item)
		}
	}
	return groups
}

// baseURL is the absolute site root that email links and images are built on.
// An email client has no base URL, so a relative link or image in a newsletter
// is simply broken. Empty only when no domain is configured (tests).
func (s *Service) baseURL() string {
	if s.domain == "" {
		return ""
	}
	return "https://" + s.domain
}

// renderNewsletter renders the newsletter HTML for the given articles and date.
func renderNewsletter(articles []db.GeneratedArticle, date, domain string) (string, error) {
	tmplPath := filepath.Join(templateDir, "newsletter.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading newsletter template: %w", err)
	}

	tmpl, err := template.New("newsletter").Funcs(template.FuncMap{
		"safeHTML":       func(s string) template.HTML { return template.HTML(s) },
		"tagDisplayName": tagDisplayName,
	}).Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("parsing newsletter template: %w", err)
	}

	base := buildTemplateData(articles, date, domain)
	data := newsletterWeeklyData{
		newsletterData:   base,
		OrderedTagGroups: orderedTagGroups(base.TagGroups),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing newsletter template: %w", err)
	}

	return buf.String(), nil
}

// tagDisplayName converts a tag slug like "credit-cards" to "Credit Cards".
func tagDisplayName(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// orderedTagGroups returns tag groups as an ordered slice of tag/articles pairs.
type tagGroup struct {
	Tag         string
	DisplayName string
	Articles    []articleItem
}

func orderedTagGroups(groups map[string][]articleItem) []tagGroup {
	result := make([]tagGroup, 0, len(groups))
	for tag, articles := range groups {
		result = append(result, tagGroup{Tag: tag, DisplayName: tagDisplayName(tag), Articles: articles})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Tag < result[j].Tag
	})
	return result
}

// newsletterWeeklyData extends newsletterData with ordered tag groups for templates.
type newsletterWeeklyData struct {
	newsletterData
	OrderedTagGroups []tagGroup
}

// renderWeeklyNewsletter renders a weekly newsletter with enriched data.
func renderWeeklyNewsletter(articles []db.GeneratedArticle, date, domain string, cow *cardOfWeekData, sp *scoutsPickData) (string, error) {
	tmplPath := filepath.Join(templateDir, "newsletter.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading newsletter template: %w", err)
	}

	tmpl, err := template.New("newsletter").Funcs(template.FuncMap{
		"safeHTML":       func(s string) template.HTML { return template.HTML(s) },
		"tagDisplayName": tagDisplayName,
	}).Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("parsing newsletter template: %w", err)
	}

	base := buildWeeklyTemplateData(articles, date, domain, cow, sp)
	data := newsletterWeeklyData{
		newsletterData:   base,
		OrderedTagGroups: orderedTagGroups(base.TagGroups),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing newsletter template: %w", err)
	}

	return buf.String(), nil
}

// buildTemplateData converts a list of articles into template data.
func buildTemplateData(articles []db.GeneratedArticle, date, domain string) newsletterData {
	data := newsletterData{
		Date:           date,
		Domain:         domain,
		UnsubscribeURL: domain + "/unsubscribe",
	}

	if len(articles) == 0 {
		return data
	}

	// First article is the lead (sorted by featured DESC, published_at DESC).
	lead := articles[0]
	data.Lead = &articleItem{
		Title:   lead.Title,
		Slug:    lead.Slug,
		Excerpt: excerpt(lead.Content, 300),
	}

	// Remaining articles go in the sidebar.
	for _, a := range articles[1:] {
		data.Sidebar = append(data.Sidebar, articleItem{
			Title:   a.Title,
			Slug:    a.Slug,
			Summary: summary(a.Content),
		})
	}

	return data
}

// buildWeeklyTemplateData builds template data for a weekly newsletter with tag groups.
// The Scout's Pick article is excluded from tag groups to avoid duplication.
func buildWeeklyTemplateData(articles []db.GeneratedArticle, date, domain string, cow *cardOfWeekData, sp *scoutsPickData) newsletterData {
	data := buildTemplateData(articles, date, domain)

	// Exclude Scout's Pick from tag-grouped articles to avoid showing it twice
	var tagArticles []db.GeneratedArticle
	for _, a := range articles {
		if sp != nil && a.ID == sp.ID {
			continue
		}
		tagArticles = append(tagArticles, a)
	}
	data.TagGroups = groupArticlesByTag(tagArticles)
	data.CardOfWeek = cow
	data.ScoutsPick = sp
	return data
}

// BuildWeekly creates a weekly newsletter with AI-ranked candidates, Scout's Pick,
// and Card of the Week. It is idempotent: if a newsletter already exists for the
// target Saturday date, it returns the existing one.
func (s *Service) BuildWeekly() (*db.Newsletter, error) {
	sendDate := s.nextSaturday()

	// Check if newsletter already exists for this date.
	existing, err := s.db.GetNewsletterByDate(sendDate)
	if err == nil && existing != nil {
		slog.Info("weekly newsletter already exists", "date", sendDate)
		return existing, nil
	}

	// Query the week's articles (Saturday minus 7 days, same window as SendWeekly).
	satDate, _ := time.Parse("2006-01-02", sendDate)
	since := satDate.AddDate(0, 0, -7)
	articles, err := s.db.ListApprovedArticlesSince(since)
	if err != nil {
		return nil, fmt.Errorf("listing approved articles: %w", err)
	}
	// Ended offers are dropped before ranking, so neither the candidates nor
	// Scout's Pick can be one.
	articles = s.liveOffers(articles)
	if len(articles) == 0 {
		slog.Info("no articles for weekly newsletter", "date", sendDate)
		return nil, nil
	}

	// AI rank articles if client is available.
	rankedIDs := s.rankArticles(articles)

	// Reorder articles by ranked IDs.
	articles = reorderArticles(articles, rankedIDs)

	// Create newsletter record with status draft.
	nl := &db.Newsletter{
		Date:         sendDate,
		Subject:      fmt.Sprintf("%s — %s", weeklySubject, sendDate),
		HTMLContent:  "", // Will be rendered at send time.
		Status:       "draft",
		ArticleCount: len(articles),
	}

	nlID, err := s.db.CreateNewsletter(nl)
	if err != nil {
		return nil, fmt.Errorf("creating newsletter: %w", err)
	}
	nl.ID = nlID

	// Create top 3 candidates (or fewer if less articles).
	top := 3
	if len(articles) < top {
		top = len(articles)
	}
	for i := 0; i < top; i++ {
		c := &db.NewsletterCandidate{
			NewsletterID: nlID,
			ArticleID:    articles[i].ID,
			Rank:         i + 1,
			Featured:     i == 0, // Rank #1 is featured.
		}
		if err := s.db.CreateNewsletterCandidate(c); err != nil {
			slog.Error("failed to create newsletter candidate", "error", err, "article_id", articles[i].ID)
		}
	}

	// Scout's Pick: rank #1 article.
	scoutsPickID := articles[0].ID
	if err := s.db.UpdateNewsletterScoutsPick(nlID, scoutsPickID); err != nil {
		slog.Error("failed to set scout's pick", "error", err)
	}
	nl.ScoutsPickArticleID = &scoutsPickID

	// Card of the Week: most-mentioned published card from this week's articles.
	cardID := s.findCardOfWeek(articles)
	if cardID > 0 {
		if err := s.db.UpdateNewsletterCardOfWeek(nlID, cardID); err != nil {
			slog.Error("failed to set card of week", "error", err)
		}
		nl.CardOfWeekID = &cardID
	}

	// Render HTML content for the newsletter.
	var cow *cardOfWeekData
	if cardID > 0 {
		cow = s.loadCardOfWeekData(cardID)
	}
	var sp *scoutsPickData
	sp = s.loadScoutsPickData(scoutsPickID)

	htmlContent, err := renderWeeklyNewsletter(articles, sendDate, s.baseURL(), cow, sp)
	if err != nil {
		slog.Error("failed to render weekly newsletter", "error", err)
	} else {
		nl.HTMLContent = htmlContent
		// Persist rendered HTML so the admin preview shows content.
		if err := s.db.UpdateNewsletterHTML(nlID, htmlContent); err != nil {
			slog.Error("failed to persist newsletter html", "error", err)
		}
	}

	// Set status to auto_approved.
	if err := s.db.UpdateNewsletterStatus(nlID, "auto_approved"); err != nil {
		slog.Error("failed to set newsletter status", "error", err)
	}
	nl.Status = "auto_approved"

	slog.Info("weekly newsletter built", "date", sendDate, "articles", len(articles), "card_of_week", cardID, "scouts_pick", scoutsPickID)

	return nl, nil
}

// rankArticles uses AI to rank articles by reader value. Falls back to original order.
func (s *Service) rankArticles(articles []db.GeneratedArticle) []int64 {
	ids := make([]int64, len(articles))
	for i, a := range articles {
		ids[i] = a.ID
	}

	if s.ai == nil || s.aiModel == "" {
		return ids
	}

	summaries := make([]ai.ArticleSummary, len(articles))
	for i, a := range articles {
		summaries[i] = ai.ArticleSummary{
			ID:    a.ID,
			Title: a.Title,
			TLDR:  a.TLDR,
		}
	}

	prompt := ai.NewsletterPickPrompt(summaries)
	resp, err := s.ai.Generate(s.aiModel, prompt, 512)
	if err != nil {
		slog.Error("AI ranking failed, using default order", "error", err)
		return ids
	}

	// Parse response as JSON array of int64.
	var ranked []int64
	// Try to extract JSON array from response.
	resp = strings.TrimSpace(resp)
	start := strings.Index(resp, "[")
	end := strings.LastIndex(resp, "]")
	if start >= 0 && end > start {
		resp = resp[start : end+1]
	}
	if err := json.Unmarshal([]byte(resp), &ranked); err != nil {
		slog.Error("failed to parse AI ranking response", "error", err, "response", resp)
		return ids
	}

	if len(ranked) == 0 {
		return ids
	}
	return ranked
}

// reorderArticles reorders articles according to the ranked ID list.
func reorderArticles(articles []db.GeneratedArticle, rankedIDs []int64) []db.GeneratedArticle {
	byID := make(map[int64]db.GeneratedArticle, len(articles))
	for _, a := range articles {
		byID[a.ID] = a
	}

	result := make([]db.GeneratedArticle, 0, len(articles))
	seen := make(map[int64]bool)
	for _, id := range rankedIDs {
		if a, ok := byID[id]; ok && !seen[id] {
			result = append(result, a)
			seen[id] = true
		}
	}
	// Append any articles not in the ranked list.
	for _, a := range articles {
		if !seen[a.ID] {
			result = append(result, a)
		}
	}
	return result
}

// findCardOfWeek counts card mentions across articles' related_cards_json and
// returns the ID of the most-mentioned published card. Returns 0 if none found.
func (s *Service) findCardOfWeek(articles []db.GeneratedArticle) int64 {
	slugCounts := make(map[string]int)
	for _, a := range articles {
		slugs := parseRelatedCards(a.RelatedCardsJSON)
		for _, slug := range slugs {
			slugCounts[slug]++
		}
	}

	if len(slugCounts) == 0 {
		return 0
	}

	// Find the most-mentioned slug among published reference pages.
	var bestID int64
	var bestCount int
	for slug, count := range slugCounts {
		page, err := s.db.GetReferencePageBySlug(slug)
		if err != nil {
			continue
		}
		if page.Status != "published" {
			continue
		}
		if count > bestCount {
			bestCount = count
			bestID = page.ID
		}
	}
	return bestID
}

// loadCardOfWeekData loads reference page data for the card of the week.
func (s *Service) loadCardOfWeekData(cardID int64) *cardOfWeekData {
	page, err := s.db.GetReferencePage(cardID)
	if err != nil {
		slog.Error("failed to load card of week", "error", err, "id", cardID)
		return nil
	}
	return &cardOfWeekData{
		ID:       page.ID,
		Title:    page.Title,
		Slug:     page.Slug,
		ImageURL: s.absoluteURL(page.ImageURL),
		Verdict:  page.ScoutsVerdict,
	}
}

// absoluteURL makes a site-relative image path absolute. Card art is served from
// the site's own /static/ directory, and a relative src does not resolve inside
// an email client.
func (s *Service) absoluteURL(u string) string {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return s.baseURL() + u
	}
	return u
}

// loadScoutsPickData loads article data for the Scout's Pick.
func (s *Service) loadScoutsPickData(articleID int64) *scoutsPickData {
	article, err := s.db.GetGeneratedArticle(articleID)
	if err != nil {
		slog.Error("failed to load scout's pick article", "error", err, "id", articleID)
		return nil
	}
	return &scoutsPickData{
		ID:         article.ID,
		Title:      article.Title,
		Slug:       article.Slug,
		ScoutsTake: article.ScoutsTake,
		ImageURL:   article.ImageURL,
	}
}

// SendWeekly sends the weekly newsletter for this Saturday to all confirmed subscribers.
func (s *Service) SendWeekly() error {
	sendDate := s.thisSaturday()

	nl, err := s.db.GetNewsletterByDate(sendDate)
	if err != nil {
		if err == sql.ErrNoRows {
			slog.Info("no weekly newsletter found for send date", "date", sendDate)
			return nil
		}
		return fmt.Errorf("fetching newsletter: %w", err)
	}

	// Only send if auto_approved or approved.
	switch nl.Status {
	case "auto_approved", "approved":
		// OK to send.
	case "sent":
		slog.Info("weekly newsletter already sent", "date", sendDate)
		return nil
	default:
		slog.Info("weekly newsletter not ready to send", "date", sendDate, "status", nl.Status)
		return nil
	}

	// Fetch confirmed subscribers.
	subscribers, err := s.db.ListConfirmedSubscribers()
	if err != nil {
		return fmt.Errorf("listing subscribers: %w", err)
	}
	if len(subscribers) == 0 {
		slog.Info("no confirmed subscribers, skipping send")
		return nil
	}

	// Re-render template at send time to reflect any admin overrides, and
	// because the newsletter was built the evening before: an offer in it
	// may have ended overnight. The stored HTML is never sent in place of
	// the re-rendered issue -- it cannot be checked, and may hold an offer
	// that has ended or an article that has been unpublished.
	since, _ := time.Parse("2006-01-02", sendDate)
	since = since.AddDate(0, 0, -7)
	articles, err := s.db.ListApprovedArticlesSince(since)
	if err != nil {
		return fmt.Errorf("reloading articles for send: %w", err)
	}
	articles = s.liveOffers(articles)
	if len(articles) == 0 {
		slog.Warn("weekly newsletter not sent: no published article in it is still live", "date", sendDate)
		return nil
	}

	// Reload card of week and scout's pick data.
	var cow *cardOfWeekData
	if nl.CardOfWeekID != nil {
		cow = s.loadCardOfWeekData(*nl.CardOfWeekID)
	}

	// Re-rank articles using candidates order.
	candidates, _ := s.db.ListCandidatesForNewsletter(nl.ID)
	if len(candidates) > 0 {
		candidateIDs := make([]int64, len(candidates))
		for i, c := range candidates {
			candidateIDs[i] = c.ArticleID
		}
		articles = reorderArticles(articles, candidateIDs)
	}

	var sp *scoutsPickData
	if nl.ScoutsPickArticleID != nil {
		pickID := *nl.ScoutsPickArticleID
		if !containsArticle(articles, pickID) {
			// Scout's Pick has ended (or been unpublished) since it was
			// chosen. Hand the slot to the top article still being sent.
			slog.Info("weekly newsletter: replacing Scout's Pick that is no longer live",
				"was", pickID, "now", articles[0].ID)
			pickID = articles[0].ID
			if err := s.db.UpdateNewsletterScoutsPick(nl.ID, pickID); err != nil {
				slog.Error("failed to update scout's pick", "error", err)
			}
		}
		sp = s.loadScoutsPickData(pickID)
	}

	htmlContent, err := renderWeeklyNewsletter(articles, sendDate, s.baseURL(), cow, sp)
	if err != nil {
		return fmt.Errorf("re-rendering newsletter at send time: %w", err)
	}
	nl.HTMLContent = htmlContent

	recipients := make([]string, len(subscribers))
	for i, sub := range subscribers {
		recipients[i] = sub.Email
	}

	if err := s.sender.SendNewsletter(nl.Subject, nl.HTMLContent, recipients); err != nil {
		slog.Error("weekly newsletter send failed", "error", err)
		return fmt.Errorf("sending newsletter: %w", err)
	}

	if err := s.db.MarkNewsletterSent(nl.ID); err != nil {
		return fmt.Errorf("marking newsletter sent: %w", err)
	}

	slog.Info("weekly newsletter sent", "date", sendDate, "recipients", len(recipients))
	return nil
}

// GenerateDaily collects approved articles from the last 24 hours and generates a newsletter.
// Returns nil, nil if there are no articles.
func (s *Service) GenerateDaily() (*db.Newsletter, error) {
	articles, err := s.listRecentApproved()
	if err != nil {
		return nil, err
	}
	if len(articles) == 0 {
		return nil, nil
	}

	date := s.todayDate()

	// Check if already generated for today.
	existing, err := s.db.GetNewsletterByDate(date)
	if err == nil && existing != nil {
		return existing, nil
	}

	htmlContent, err := renderNewsletter(articles, date, s.baseURL())
	if err != nil {
		return nil, fmt.Errorf("rendering newsletter: %w", err)
	}

	subject := fmt.Sprintf("MilesAhead Daily Digest — %s", date)

	created := s.now().UTC()
	nl := &db.Newsletter{
		Date:         date,
		Subject:      subject,
		HTMLContent:  htmlContent,
		Status:       "draft",
		ArticleCount: len(articles),
		CreatedAt:    &created,
	}

	id, err := s.db.CreateNewsletter(nl)
	if err != nil {
		return nil, fmt.Errorf("saving newsletter: %w", err)
	}
	nl.ID = id

	slog.Info("newsletter generated", "date", date, "article_count", len(articles))

	return nl, nil
}

// SendDaily's answers when it sends nothing, so the admin is never told a
// newsletter went out when none did.
var (
	// ErrNothingLive: no article for today's issue is still live.
	ErrNothingLive = errors.New("nothing sent: no article for today's newsletter is still live")
	// ErrAlreadySent: today's issue has gone out already.
	ErrAlreadySent = errors.New("nothing sent: today's newsletter was already sent")
	// ErrNoSubscribers: there is no one to send it to.
	ErrNoSubscribers = errors.New("nothing sent: there are no confirmed subscribers")
)

// dailyWindow is when the articles in today's daily nl may have been
// published: from 24 hours before it was generated, or before now if that
// is earlier, up to when it was generated. An issue generated in the morning
// and sent in the afternoon keeps its morning articles, and one sent late,
// after a failed try, does not take in articles published since: those
// belong to the next day's issue, and would go out twice. An issue stored
// before generation times were recorded counts from 24 hours before the
// start of its date to the end of it, which covers whenever that day it was
// generated.
func (s *Service) dailyWindow(nl *db.Newsletter, now time.Time) (since, until time.Time) {
	since = now.Add(-24 * time.Hour)
	var from time.Time
	if nl.CreatedAt != nil {
		from = nl.CreatedAt.Add(-24 * time.Hour)
		until = *nl.CreatedAt
	} else if day, err := time.ParseInLocation("2006-01-02", nl.Date, s.loc); err == nil {
		from = day.Add(-24 * time.Hour)
		until = day.AddDate(0, 0, 1)
	}
	if !from.IsZero() && from.Before(since) {
		since = from
	}
	return since.UTC(), until
}

// publishedBy keeps the articles published no later than until; a zero
// until keeps them all.
func publishedBy(articles []db.GeneratedArticle, until time.Time) []db.GeneratedArticle {
	if until.IsZero() {
		return articles
	}
	kept := articles[:0]
	for _, a := range articles {
		if a.PublishedAt == nil || !a.PublishedAt.After(until) {
			kept = append(kept, a)
		}
	}
	return kept
}

// isWeekly reports whether nl is a weekly issue. A weekly is dated the
// Saturday it goes out, so on a Saturday it holds the date a daily would.
// Only BuildWeekly writes the weekly subject or candidates. (A Scout's Pick
// or Card of the Week is no sign: the admin page can set either on whichever
// issue is current, a daily included.)
func (s *Service) isWeekly(nl *db.Newsletter) (bool, error) {
	if strings.HasPrefix(nl.Subject, weeklySubject) {
		return true, nil
	}
	candidates, err := s.db.ListCandidatesForNewsletter(nl.ID)
	if err != nil {
		return false, err
	}
	return len(candidates) > 0, nil
}

// SendDaily sends today's daily newsletter to all confirmed subscribers,
// generating it first if there is none. The admin's "send" button calls it.
//
// The issue is rendered again as it is sent, from the live articles of the
// 24 hours before it was generated (dailyWindow).
// Its stored HTML was rendered when it was generated, hours earlier perhaps,
// and may hold an offer that has ended since; it is never sent. Today's
// issue is not sent at all when it is the weekly one (SendWeekly sends that,
// on its own checks) or an admin has held or skipped it. Whenever nothing
// goes out, SendDaily returns an error saying why.
func (s *Service) SendDaily() error {
	date := s.todayDate()

	// Try to get an existing newsletter for today.
	nl, err := s.db.GetNewsletterByDate(date)
	if err != nil {
		if err != sql.ErrNoRows {
			return fmt.Errorf("checking for existing newsletter: %w", err)
		}
		// Not yet generated — generate now.
		nl, err = s.GenerateDaily()
		if err != nil {
			return fmt.Errorf("generating newsletter: %w", err)
		}
		if nl == nil {
			return ErrNothingLive
		}
	}

	switch nl.Status {
	case "sent":
		return ErrAlreadySent
	case "draft", "approved", "auto_approved":
		// Sendable.
	case "hold":
		return fmt.Errorf("today's newsletter is on hold, so it was not sent")
	default:
		return fmt.Errorf("today's newsletter is %s, so it was not sent", nl.Status)
	}
	weekly, err := s.isWeekly(nl)
	if err != nil {
		return fmt.Errorf("checking whether today's newsletter is the weekly: %w", err)
	}
	if weekly {
		return fmt.Errorf("today's newsletter is the weekly issue, which goes out on its own schedule; it was not sent")
	}

	// Fetch confirmed subscribers.
	subscribers, err := s.db.ListConfirmedSubscribers()
	if err != nil {
		return fmt.Errorf("listing subscribers: %w", err)
	}
	if len(subscribers) == 0 {
		return ErrNoSubscribers
	}

	since, until := s.dailyWindow(nl, s.now())
	articles, err := s.listLiveSince(since)
	if err != nil {
		return fmt.Errorf("reloading articles for send: %w", err)
	}
	articles = publishedBy(articles, until)
	if len(articles) == 0 {
		slog.Warn("daily newsletter not sent: no article in it is still live", "date", date)
		return ErrNothingLive
	}
	htmlContent, err := renderNewsletter(articles, date, s.baseURL())
	if err != nil {
		return fmt.Errorf("re-rendering newsletter at send time: %w", err)
	}
	// Keep the stored copy as sent, for the admin's history.
	if err := s.db.UpdateNewsletterHTML(nl.ID, htmlContent); err != nil {
		slog.Warn("failed to store the daily newsletter as sent", "error", err, "id", nl.ID)
	}

	recipients := make([]string, len(subscribers))
	for i, sub := range subscribers {
		recipients[i] = sub.Email
	}

	if err := s.sender.SendNewsletter(nl.Subject, htmlContent, recipients); err != nil {
		slog.Error("newsletter send failed", "error", err)
		return fmt.Errorf("sending newsletter: %w", err)
	}

	if err := s.db.MarkNewsletterSent(nl.ID); err != nil {
		return fmt.Errorf("marking newsletter sent: %w", err)
	}

	slog.Info("newsletter sent", "recipients", len(recipients))

	return nil
}

// PreviewNext generates newsletter HTML without saving, for admin preview.
func (s *Service) PreviewNext() (string, error) {
	articles, err := s.listRecentApproved()
	if err != nil {
		return "", err
	}
	if len(articles) == 0 {
		return "", fmt.Errorf("no live approved articles in the last 24 hours")
	}

	date := s.todayDate()
	return renderNewsletter(articles, date, s.baseURL())
}
