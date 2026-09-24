package newsletter

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// mockSender records calls to SendNewsletter.
type mockSender struct {
	calls []sendCall
}

type sendCall struct {
	subject    string
	html       string
	recipients []string
}

func (m *mockSender) SendNewsletter(subject, htmlContent string, recipients []string) error {
	m.calls = append(m.calls, sendCall{subject: subject, html: htmlContent, recipients: recipients})
	return nil
}

// setupTestDB creates a temporary SQLite database and returns it along with a cleanup function.
func setupTestDB(t *testing.T) (*db.Database, func()) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	return database, func() { database.Close() }
}

// createApprovedArticle inserts a generated article with status=approved and published_at=now.
func createApprovedArticle(t *testing.T, database *db.Database, title, slug, content string, featured bool) int64 {
	t.Helper()
	a := &db.GeneratedArticle{
		Title:       title,
		Slug:        slug,
		Content:     content,
		Type:        "article",
		Status:      db.StatusDraft,
		SourcesJSON: "[]",
		Featured:    featured,
	}
	id, err := database.CreateGeneratedArticle(a)
	if err != nil {
		t.Fatalf("creating article: %v", err)
	}
	// Transition: draft -> pending -> approved
	if err := database.UpdateArticleStatus(id, db.StatusPending); err != nil {
		t.Fatalf("setting pending: %v", err)
	}
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("setting approved: %v", err)
	}
	return id
}

// setTemplateDir points the template directory to the project root templates.
func setTemplateDir(t *testing.T) {
	t.Helper()
	// Find the project root by looking for go.mod.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (go.mod)")
		}
		dir = parent
	}
	templateDir = filepath.Join(dir, "templates", "email")
}

func TestGenerateDaily_ThreeArticles(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	// Create 3 approved articles.
	createApprovedArticle(t, database, "Breaking: New Amex Platinum Bonus", "amex-platinum-bonus", "American Express has launched a record-breaking sign-up bonus of 150,000 points on the Platinum Card, marking the highest offer in the card's history.", false)
	createApprovedArticle(t, database, "Chase Sapphire Reserve Gets Lounge Access", "chase-sapphire-lounge", "Chase announced today that Sapphire Reserve cardholders will gain complimentary access to over 1,300 airport lounges worldwide starting next month.", false)
	createApprovedArticle(t, database, "Southwest Companion Pass Strategy 2026", "southwest-companion-pass", "A comprehensive guide to earning the Southwest Companion Pass this year, including the best credit card combinations and point-earning strategies.", true)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{
		Timezone: "UTC",
		SendTime: "08:00",
	})

	nl, err := svc.GenerateDaily()
	if err != nil {
		t.Fatalf("GenerateDaily() error: %v", err)
	}
	if nl == nil {
		t.Fatal("GenerateDaily() returned nil newsletter")
	}

	// Verify all 3 titles appear in the HTML content.
	titles := []string{
		"Breaking: New Amex Platinum Bonus",
		"Chase Sapphire Reserve Gets Lounge Access",
		"Southwest Companion Pass Strategy 2026",
	}
	for _, title := range titles {
		if !strings.Contains(nl.HTMLContent, title) {
			t.Errorf("newsletter HTML missing title %q", title)
		}
	}

	// Verify newspaper-style layout markers.
	if !strings.Contains(nl.HTMLContent, "/static/img/brand/email-lockup.png") {
		t.Error("newsletter HTML missing the MilesAhead masthead image")
	}
	if !strings.Contains(nl.HTMLContent, "WEEKLY") {
		t.Error("newsletter HTML missing subtitle")
	}
	// Verify at least the lead article title appears in the rendered HTML.
	if !strings.Contains(nl.HTMLContent, "Breaking: New Amex Platinum Bonus") {
		t.Error("newsletter HTML missing lead article title")
	}
	if !strings.Contains(nl.HTMLContent, "Unsubscribe") {
		t.Error("newsletter HTML missing unsubscribe link")
	}

	// Verify article count.
	if nl.ArticleCount != 3 {
		t.Errorf("article count = %d, want 3", nl.ArticleCount)
	}

	// Verify it was stored in the database.
	stored, err := database.GetNewsletterByDate(nl.Date)
	if err != nil {
		t.Fatalf("GetNewsletterByDate() error: %v", err)
	}
	if stored.ID != nl.ID {
		t.Errorf("stored newsletter ID = %d, want %d", stored.ID, nl.ID)
	}
}

func TestGenerateDaily_NoArticles(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{
		Timezone: "UTC",
		SendTime: "08:00",
	})

	nl, err := svc.GenerateDaily()
	if err != nil {
		t.Fatalf("GenerateDaily() error: %v", err)
	}
	if nl != nil {
		t.Fatal("expected nil newsletter when no articles")
	}
}

func TestGenerateDaily_Idempotent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	createApprovedArticle(t, database, "Test Article", "test-article", "Test content for idempotency check.", false)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{
		Timezone: "UTC",
	})

	nl1, err := svc.GenerateDaily()
	if err != nil {
		t.Fatalf("first GenerateDaily() error: %v", err)
	}

	nl2, err := svc.GenerateDaily()
	if err != nil {
		t.Fatalf("second GenerateDaily() error: %v", err)
	}

	if nl1.ID != nl2.ID {
		t.Errorf("second call created new newsletter: ID %d vs %d", nl1.ID, nl2.ID)
	}
}

func TestSendDaily(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	createApprovedArticle(t, database, "Send Test Article", "send-test", "Content for send test.", false)

	// Create confirmed subscribers.
	for _, email := range []string{"alice@example.com", "bob@example.com"} {
		id, err := database.CreateSubscriber(&db.Subscriber{Email: email, Token: "tok-" + email})
		if err != nil {
			t.Fatalf("creating subscriber: %v", err)
		}
		_ = id
		if err := database.ConfirmSubscriber("tok-" + email); err != nil {
			t.Fatalf("confirming subscriber: %v", err)
		}
	}

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})

	if err := svc.SendDaily(); err != nil {
		t.Fatalf("SendDaily() error: %v", err)
	}

	// Verify sender was called.
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}
	call := sender.calls[0]
	if len(call.recipients) != 2 {
		t.Errorf("expected 2 recipients, got %d", len(call.recipients))
	}

	// Verify newsletter was marked as sent.
	date := time.Now().UTC().Format("2006-01-02")
	nl, err := database.GetNewsletterByDate(date)
	if err != nil {
		t.Fatalf("GetNewsletterByDate() error: %v", err)
	}
	if nl.Status != "sent" {
		t.Errorf("newsletter status = %q, want 'sent'", nl.Status)
	}
}

func TestSendDaily_NoSubscribers(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	createApprovedArticle(t, database, "No Sub Article", "no-sub", "Content.", false)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})

	// Nothing goes out, and the admin is told so rather than "sent".
	if err := svc.SendDaily(); !errors.Is(err, ErrNoSubscribers) {
		t.Fatalf("SendDaily() = %v, want ErrNoSubscribers", err)
	}

	// Sender should not have been called.
	if len(sender.calls) != 0 {
		t.Errorf("expected 0 send calls, got %d", len(sender.calls))
	}
}

func TestSendDaily_AlreadySent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	createApprovedArticle(t, database, "Already Sent", "already-sent", "Content.", false)

	// Create a confirmed subscriber.
	if _, err := database.CreateSubscriber(&db.Subscriber{Email: "test@example.com", Token: "tok-test"}); err != nil {
		t.Fatalf("creating subscriber: %v", err)
	}
	if err := database.ConfirmSubscriber("tok-test"); err != nil {
		t.Fatalf("confirming subscriber: %v", err)
	}

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})

	// Send once.
	if err := svc.SendDaily(); err != nil {
		t.Fatalf("first SendDaily() error: %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("expected 1 send call, got %d", len(sender.calls))
	}

	// Send again — nothing goes out, and the admin is told so rather than
	// "sent".
	if err := svc.SendDaily(); !errors.Is(err, ErrAlreadySent) {
		t.Fatalf("second SendDaily() = %v, want ErrAlreadySent", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("expected still 1 send call after resend, got %d", len(sender.calls))
	}
}

func TestPreviewNext(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	createApprovedArticle(t, database, "Preview Article", "preview-article", "Preview content here for the admin to review before sending.", false)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})

	html, err := svc.PreviewNext()
	if err != nil {
		t.Fatalf("PreviewNext() error: %v", err)
	}

	if !strings.Contains(html, "Preview Article") {
		t.Error("preview HTML missing article title")
	}
	if !strings.Contains(html, "/static/img/brand/email-lockup.png") {
		t.Error("preview HTML missing masthead")
	}

	// Verify nothing was saved to the database.
	date := time.Now().UTC().Format("2006-01-02")
	_, err = database.GetNewsletterByDate(date)
	if err == nil {
		t.Error("PreviewNext should not save to database")
	}
}

func TestPreviewNext_NoArticles(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})

	_, err := svc.PreviewNext()
	if err == nil {
		t.Fatal("expected error when no articles")
	}
}

func TestBuildTemplateData_FeaturedLeadStory(t *testing.T) {
	articles := []db.GeneratedArticle{
		{Title: "Featured Story", Slug: "featured", Content: "Featured content.", Featured: true},
		{Title: "Story Two", Slug: "two", Content: "Second content."},
		{Title: "Story Three", Slug: "three", Content: "Third content."},
	}

	data := buildTemplateData(articles, "2026-03-24", "https://example.com")

	if data.Lead == nil {
		t.Fatal("expected a lead story")
	}
	if data.Lead.Title != "Featured Story" {
		t.Errorf("lead title = %q, want 'Featured Story'", data.Lead.Title)
	}
	if len(data.Sidebar) != 2 {
		t.Errorf("sidebar count = %d, want 2", len(data.Sidebar))
	}
	if data.UnsubscribeURL != "https://example.com/unsubscribe" {
		t.Errorf("unsubscribe URL = %q", data.UnsubscribeURL)
	}
}

func TestExcerpt(t *testing.T) {
	short := "Short text."
	if got := excerpt(short, 300); got != short {
		t.Errorf("excerpt(%q) = %q", short, got)
	}

	long := strings.Repeat("Word ", 100)
	got := excerpt(long, 50)
	if len(got) > 55 { // 50 + "..."
		t.Errorf("excerpt too long: %d chars", len(got))
	}
}

// The schedule's timezone is resolved once, when the service is made: an
// unknown name warns once and falls back to UTC, however often the date is
// looked up.
func TestNewService_ResolvesTimezoneOnce(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	svc := NewService(nil, &mockSender{}, config.NewsletterConfig{Timezone: "Not/AZone"})
	for i := 0; i < 3; i++ {
		svc.todayDate()
		svc.thisSaturday()
		svc.nextSaturday()
	}
	if svc.loc != time.UTC {
		t.Errorf("loc = %v, want the UTC fallback", svc.loc)
	}
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 || !strings.Contains(logged.String(), "Not/AZone") {
		t.Errorf("want one warning about the unknown zone, got %d:\n%s", n, logged.String())
	}

	ny := NewService(nil, &mockSender{}, config.NewsletterConfig{Timezone: "America/New_York"})
	if ny.loc.String() != "America/New_York" {
		t.Errorf("loc = %v, want America/New_York", ny.loc)
	}
	if got, want := ny.todayDate(), time.Now().In(ny.loc).Format("2006-01-02"); got != want {
		t.Errorf("todayDate = %s, want %s", got, want)
	}
}
