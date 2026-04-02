package newsletter

import (
	"bytes"
	"database/sql"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// MailSender is the interface for sending newsletter emails.
type MailSender interface {
	SendNewsletter(subject, htmlContent string, recipients []string) error
}

// templateDir is the base directory for email templates. Override for testing.
var templateDir = "templates/email"

// Service handles newsletter generation and delivery.
type Service struct {
	db       *db.Database
	sender   MailSender
	timezone string
	sendTime string
}

// NewService creates a new newsletter Service.
func NewService(database *db.Database, sender MailSender, cfg config.NewsletterConfig) *Service {
	tz := cfg.Timezone
	if tz == "" {
		tz = "UTC"
	}
	st := cfg.SendTime
	if st == "" {
		st = "08:00"
	}
	return &Service{
		db:       database,
		sender:   sender,
		timezone: tz,
		sendTime: st,
	}
}

// articleItem represents a single article for the newsletter template.
type articleItem struct {
	Title   string
	Slug    string
	Excerpt string
	Summary string
}

// newsletterData is the template data for rendering a newsletter.
type newsletterData struct {
	Date           string
	Domain         string
	Lead           *articleItem
	Sidebar        []articleItem
	UnsubscribeURL string
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

// listRecentApproved fetches approved articles published in the last 24 hours.
func (s *Service) listRecentApproved() ([]db.GeneratedArticle, error) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	return s.db.ListApprovedArticlesSince(since)
}

// todayDate returns today's date string in YYYY-MM-DD format for the configured timezone.
func (s *Service) todayDate() string {
	loc, err := time.LoadLocation(s.timezone)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02")
}

// renderNewsletter renders the newsletter HTML for the given articles and date.
func renderNewsletter(articles []db.GeneratedArticle, date, domain string) (string, error) {
	tmplPath := filepath.Join(templateDir, "newsletter.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading newsletter template: %w", err)
	}

	tmpl, err := template.New("newsletter").Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("parsing newsletter template: %w", err)
	}

	data := buildTemplateData(articles, date, domain)

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

	htmlContent, err := renderNewsletter(articles, date, "")
	if err != nil {
		return nil, fmt.Errorf("rendering newsletter: %w", err)
	}

	subject := fmt.Sprintf("Miles Ahead Daily Digest — %s", date)

	nl := &db.Newsletter{
		Date:         date,
		Subject:      subject,
		HTMLContent:  htmlContent,
		Status:       "draft",
		ArticleCount: len(articles),
	}

	id, err := s.db.CreateNewsletter(nl)
	if err != nil {
		return nil, fmt.Errorf("saving newsletter: %w", err)
	}
	nl.ID = id

	slog.Info("newsletter generated", "date", date, "article_count", len(articles))

	return nl, nil
}

// SendDaily generates (if needed) and sends the daily newsletter to all confirmed subscribers.
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
			// No articles to send.
			return nil
		}
	}

	// Already sent?
	if nl.Status == "sent" {
		return nil
	}

	// Fetch confirmed subscribers.
	subscribers, err := s.db.ListConfirmedSubscribers()
	if err != nil {
		return fmt.Errorf("listing subscribers: %w", err)
	}
	if len(subscribers) == 0 {
		return nil
	}

	recipients := make([]string, len(subscribers))
	for i, sub := range subscribers {
		recipients[i] = sub.Email
	}

	if err := s.sender.SendNewsletter(nl.Subject, nl.HTMLContent, recipients); err != nil {
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
		return "", fmt.Errorf("no approved articles in the last 24 hours")
	}

	date := s.todayDate()
	return renderNewsletter(articles, date, "")
}
