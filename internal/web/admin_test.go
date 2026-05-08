package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// mockNewsletterSvc implements NewsletterService for testing.
type mockNewsletterSvc struct {
	previewHTML string
	previewErr  error
	sendErr     error
	sendCalled  bool
}

func (m *mockNewsletterSvc) PreviewNext() (string, error) {
	return m.previewHTML, m.previewErr
}

func (m *mockNewsletterSvc) SendDaily() error {
	m.sendCalled = true
	return m.sendErr
}

// setupAdminTestApp creates a test App with article manager support and admin templates.
func setupAdminTestApp(t *testing.T) (*App, *db.Database) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	cfg := &config.Config{
		Admin: config.AdminConfig{
			PasswordHash: string(hash),
		},
		Server: config.ServerConfig{
			Port:   8080,
			Domain: "localhost",
		},
		Votes: config.VotesConfig{
			AlertThreshold: 5,
		},
		AI: config.AIConfig{
			HFAPIToken: "hf_test_token_12345",
		},
	}

	mgr := article.NewManager(database, nil)

	templateDir := filepath.Join(tmpDir, "templates", "admin")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	// Write layout template.
	layoutContent := `{{define "layout"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}
{{define "csrf_field"}}<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">{{end}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "templates", "layout.html"), []byte(layoutContent), 0644); err != nil {
		t.Fatalf("failed to write layout template: %v", err)
	}

	// Write login template.
	loginContent := `{{template "layout" .}}
{{define "content"}}<form method="POST" action="/admin/login">{{template "csrf_field" .}}<input type="password" name="password"><button type="submit">Login</button></form>{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "login.html"), []byte(loginContent), 0644); err != nil {
		t.Fatalf("failed to write login template: %v", err)
	}

	// Write dashboard template.
	dashboardContent := `{{template "layout" .}}
{{define "content"}}<h1>Dashboard</h1>{{range .Articles}}<div class="article">{{.Title}}<a href="/admin/articles/{{.ID}}/cards">Linked cards</a></div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "dashboard.html"), []byte(dashboardContent), 0644); err != nil {
		t.Fatalf("failed to write dashboard template: %v", err)
	}

	// Write published template.
	publishedContent := `{{template "layout" .}}
{{define "content"}}<h1>Published</h1>{{range .Articles}}<div class="article">{{.Title}}</div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "published.html"), []byte(publishedContent), 0644); err != nil {
		t.Fatalf("failed to write published template: %v", err)
	}

	// Write sources template.
	sourcesContent := `{{template "layout" .}}
{{define "content"}}<h1>Sources</h1>{{range .Sources}}<div class="source">{{.Name}}</div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "sources.html"), []byte(sourcesContent), 0644); err != nil {
		t.Fatalf("failed to write sources template: %v", err)
	}

	// Write subscribers template.
	subscribersContent := `{{template "layout" .}}
{{define "content"}}<h1>Subscribers</h1>{{range .Subscribers}}<div class="subscriber">{{.Email}}</div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "subscribers.html"), []byte(subscribersContent), 0644); err != nil {
		t.Fatalf("failed to write subscribers template: %v", err)
	}

	// Write settings template.
	settingsContent := `{{template "layout" .}}
{{define "content"}}<h1>Settings</h1>{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "settings.html"), []byte(settingsContent), 0644); err != nil {
		t.Fatalf("failed to write settings template: %v", err)
	}

	// Write newsletter template.
	newsletterContent := `{{template "layout" .}}
{{define "content"}}<h1>Newsletter</h1>{{if .NoContent}}<p>No articles to include</p>{{else}}<div class="preview">{{.PreviewHTML}}</div>{{end}}{{if .Success}}<div class="success">{{.Success}}</div>{{end}}{{if .Error}}<div class="error">{{.Error}}</div>{{end}}{{if .History}}{{range .History}}<div class="newsletter-row">{{.Subject}} {{.ArticleCount}}</div>{{end}}{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "newsletter.html"), []byte(newsletterContent), 0644); err != nil {
		t.Fatalf("failed to write newsletter template: %v", err)
	}

	// Write article-cards template.
	articleCardsContent := `{{template "layout" .}}
{{define "content"}}
<form method="POST">
<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
{{range .Linked}}<div class="admin-row linked"><input type="hidden" name="linked_slugs[]" value="{{.Slug}}"><b>{{.Title}}</b>{{if .Count}}<span>{{.Count}}× in body</span>{{end}}</div>{{end}}
{{range .Suggested}}<div class="admin-row suggested"><b>{{.Title}}</b><span>{{.Count}}× in body</span></div>{{end}}
</form>
{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "article-cards.html"), []byte(articleCardsContent), 0644); err != nil {
		t.Fatalf("failed to write article-cards template: %v", err)
	}

	// Write public article template (needed for XSS and render tests).
	publicDir := filepath.Join(tmpDir, "templates", "public")
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatalf("failed to create public template dir: %v", err)
	}
	articleContent := `{{template "layout" .}}
{{define "content"}}<h1>{{.Article.Title}}</h1><div class="content">{{.Article.Content}}</div>{{if .LinkedCards}}<div class="card-embeds"><h3 class="card-embeds-heading">{{if .LinkedCardsHeading}}{{.LinkedCardsHeading}}{{else}}Cards mentioned in this article{{end}}</h3>{{range .LinkedCards}}<div class="card-embed">{{.Title}}</div>{{end}}</div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "article.html"), []byte(articleContent), 0644); err != nil {
		t.Fatalf("failed to write article template: %v", err)
	}

	// Write minimal public home template to satisfy route setup.
	homeContent := `{{template "layout" .}}
{{define "content"}}<h1>Home</h1>{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "home.html"), []byte(homeContent), 0644); err != nil {
		t.Fatalf("failed to write home template: %v", err)
	}

	app, err := NewAppWithTemplateDir(cfg, database, mgr, nil, nil, nil, context.Background(), filepath.Join(tmpDir, "templates"))
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}

	return app, database
}

// loginAndGetSession logs in and returns session + CSRF cookies for admin requests.
func loginAndGetSession(t *testing.T, app *App) []*http.Cookie {
	t.Helper()

	// GET /admin/login to get CSRF token.
	getReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	getRec := httptest.NewRecorder()
	app.ServeHTTP(getRec, getReq)

	var csrfToken string
	for _, cookie := range getRec.Result().Cookies() {
		if cookie.Name == "csrf_token" {
			csrfToken = cookie.Value
			break
		}
	}

	// POST /admin/login.
	form := url.Values{}
	form.Set("password", "testpassword123")
	form.Set("csrf_token", csrfToken)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrfToken})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	return rec.Result().Cookies()
}

// getSessionAndCSRF extracts session and CSRF cookies from a cookie slice.
func getSessionAndCSRF(cookies []*http.Cookie) (session string, csrf string) {
	for _, c := range cookies {
		switch c.Name {
		case "session":
			session = c.Value
		case "csrf_token":
			csrf = c.Value
		}
	}
	return
}

// doAdminGET makes an authenticated GET request to the given path.
func doAdminGET(t *testing.T, app *App, path string, sessionToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionToken})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// doAdminPOST makes an authenticated POST request with CSRF.
// It first does a GET to /admin/dashboard to obtain a fresh CSRF token.
func doAdminPOST(t *testing.T, app *App, path string, sessionToken string, _ string, formData url.Values) *httptest.ResponseRecorder {
	t.Helper()

	// GET an admin page to get a fresh CSRF token from the middleware.
	getRec := doAdminGET(t, app, "/admin/dashboard", sessionToken)
	var freshCSRF string
	for _, c := range getRec.Result().Cookies() {
		if c.Name == "csrf_token" {
			freshCSRF = c.Value
			break
		}
	}
	if freshCSRF == "" {
		t.Fatal("no csrf_token from GET /admin/dashboard")
	}

	if formData == nil {
		formData = url.Values{}
	}
	formData.Set("csrf_token", freshCSRF)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session", Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: freshCSRF})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

// createPendingArticle creates a pending article and returns its ID.
func createPendingArticle(t *testing.T, database *db.Database, title string) int64 {
	t.Helper()
	slug := article.GenerateSlug(title)
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          title,
		Slug:           slug,
		Content:        "Test content for " + title,
		Type:           "analysis",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}
	return id
}

func TestAdminDashboard_Returns200WithPendingArticles(t *testing.T) {
	app, database := setupAdminTestApp(t)

	// Create some pending articles.
	createPendingArticle(t, database, "Article One")
	createPendingArticle(t, database, "Article Two")

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/dashboard", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Article One") {
		t.Error("expected body to contain 'Article One'")
	}
	if !strings.Contains(body, "Article Two") {
		t.Error("expected body to contain 'Article Two'")
	}
	if !strings.Contains(body, "Linked cards") {
		t.Error("expected body to contain 'Linked cards'")
	}
	if !strings.Contains(body, `href="/admin/articles/`) {
		t.Error("expected body to contain article links")
	}
}

func TestApproveArticle_TransitionsToApproved(t *testing.T) {
	app, database := setupAdminTestApp(t)
	articleID := createPendingArticle(t, database, "Approve Me")

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/articles/1/approve", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusApproved {
		t.Errorf("expected status %q, got %q", db.StatusApproved, article.Status)
	}
}

func TestRejectArticle_TransitionsToRejected(t *testing.T) {
	app, database := setupAdminTestApp(t)
	articleID := createPendingArticle(t, database, "Reject Me")

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/articles/1/reject", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusRejected {
		t.Errorf("expected status %q, got %q", db.StatusRejected, article.Status)
	}
}

func TestRequestRevision_TransitionsToRevisionRequested(t *testing.T) {
	app, database := setupAdminTestApp(t)
	articleID := createPendingArticle(t, database, "Revise Me")

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	form := url.Values{}
	form.Set("feedback", "Please add more sources")

	rec := doAdminPOST(t, app, "/admin/articles/1/revise", session, csrf, form)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusRevisionRequested {
		t.Errorf("expected status %q, got %q", db.StatusRevisionRequested, article.Status)
	}

	// Check feedback was stored.
	feedbacks, err := database.ListFeedbackForArticle(articleID)
	if err != nil {
		t.Fatalf("failed to list feedback: %v", err)
	}
	if len(feedbacks) != 1 {
		t.Fatalf("expected 1 feedback, got %d", len(feedbacks))
	}
	if feedbacks[0].FeedbackText != "Please add more sources" {
		t.Errorf("unexpected feedback text: %q", feedbacks[0].FeedbackText)
	}
}

func TestRequestRevision_RequiresFeedback(t *testing.T) {
	app, _ := setupAdminTestApp(t)

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	// No feedback provided.
	rec := doAdminPOST(t, app, "/admin/articles/1/revise", session, csrf, nil)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
}

func TestAdminPublished_Returns200(t *testing.T) {
	app, database := setupAdminTestApp(t)

	// Create and approve an article.
	id := createPendingArticle(t, database, "Published Article")
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/published", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Published Article") {
		t.Error("expected body to contain 'Published Article'")
	}
}

func TestUnpublishArticle_TransitionsToUnpublished(t *testing.T) {
	app, database := setupAdminTestApp(t)

	id := createPendingArticle(t, database, "Unpublish Me")
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("failed to approve: %v", err)
	}

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/articles/1/unpublish", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	article, err := database.GetGeneratedArticle(id)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusUnpublished {
		t.Errorf("expected status %q, got %q", db.StatusUnpublished, article.Status)
	}
}

func TestAdminSources_Returns200(t *testing.T) {
	app, database := setupAdminTestApp(t)

	// Add a source.
	database.CreateSource(&db.Source{
		Name:   "Test Source",
		URL:    "https://example.com",
		Active: true,
	})

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/sources", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Test Source") {
		t.Error("expected body to contain 'Test Source'")
	}
}

func TestAddSource(t *testing.T) {
	app, database := setupAdminTestApp(t)

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	form := url.Values{}
	form.Set("name", "New Source")
	form.Set("url", "https://newsource.com")

	rec := doAdminPOST(t, app, "/admin/sources/add", session, csrf, form)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	sources, err := database.ListAllSources()
	if err != nil {
		t.Fatalf("failed to list sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	if sources[0].Name != "New Source" {
		t.Errorf("expected source name 'New Source', got %q", sources[0].Name)
	}
}

func TestToggleSource(t *testing.T) {
	app, database := setupAdminTestApp(t)

	id, err := database.CreateSource(&db.Source{
		Name:   "Toggle Me",
		URL:    "https://example.com",
		Active: true,
	})
	if err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/sources/1/toggle", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}

	source, err := database.GetSource(id)
	if err != nil {
		t.Fatalf("failed to get source: %v", err)
	}
	if source.Active {
		t.Error("expected source to be inactive after toggle")
	}
}

func TestAdminSubscribers_Returns200(t *testing.T) {
	app, database := setupAdminTestApp(t)

	database.CreateSubscriber(&db.Subscriber{
		Email: "test@example.com",
		Token: "abc123",
	})

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/subscribers", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "test@example.com") {
		t.Error("expected body to contain 'test@example.com'")
	}
}

func TestExportSubscribers_ReturnsCSV(t *testing.T) {
	app, database := setupAdminTestApp(t)

	database.CreateSubscriber(&db.Subscriber{
		Email: "export@example.com",
		Token: "xyz789",
	})

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/subscribers/export", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if contentType != "text/csv" {
		t.Errorf("expected Content-Type text/csv, got %q", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "export@example.com") {
		t.Error("expected CSV to contain 'export@example.com'")
	}
	if !strings.Contains(body, "Email,Confirmed,Subscribed At,Confirmed At") {
		t.Error("expected CSV header row")
	}
}

func TestAdminSettings_Returns200(t *testing.T) {
	app, _ := setupAdminTestApp(t)

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/settings", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
}

func TestMaskString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"ab", "********"},
		{"abcd", "********"},
		{"abcde", "********"},
		{"hf_test_token_12345", "********"},
	}

	for _, tt := range tests {
		got := maskString(tt.input)
		if got != tt.expected {
			t.Errorf("maskString(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestEditSource(t *testing.T) {
	app, database := setupAdminTestApp(t)

	id, err := database.CreateSource(&db.Source{
		Name:   "Old Name",
		URL:    "https://old.com",
		Active: true,
	})
	if err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	form := url.Values{}
	form.Set("name", "New Name")
	form.Set("url", "https://new.com")

	rec := doAdminPOST(t, app, "/admin/sources/1/edit", session, csrf, form)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	source, err := database.GetSource(id)
	if err != nil {
		t.Fatalf("failed to get source: %v", err)
	}
	if source.Name != "New Name" {
		t.Errorf("expected name 'New Name', got %q", source.Name)
	}
	if source.URL != "https://new.com" {
		t.Errorf("expected URL 'https://new.com', got %q", source.URL)
	}
}

// setupNewsletterTestApp creates a test App with a mock newsletter service.
func setupNewsletterTestApp(t *testing.T, mock *mockNewsletterSvc) (*App, *db.Database) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	cfg := &config.Config{
		Admin: config.AdminConfig{
			PasswordHash: string(hash),
		},
		Server: config.ServerConfig{
			Port:   8080,
			Domain: "localhost",
		},
		Votes: config.VotesConfig{
			AlertThreshold: 5,
		},
		AI: config.AIConfig{
			HFAPIToken: "hf_test_token_12345",
		},
	}

	mgr := article.NewManager(database, nil)

	templateDir := filepath.Join(tmpDir, "templates", "admin")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	layoutContent := `{{define "layout"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}
{{define "csrf_field"}}<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">{{end}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "templates", "layout.html"), []byte(layoutContent), 0644); err != nil {
		t.Fatalf("failed to write layout template: %v", err)
	}

	loginContent := `{{template "layout" .}}
{{define "content"}}<form method="POST" action="/admin/login">{{template "csrf_field" .}}<input type="password" name="password"><button type="submit">Login</button></form>{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "login.html"), []byte(loginContent), 0644); err != nil {
		t.Fatalf("failed to write login template: %v", err)
	}

	dashboardContent := `{{template "layout" .}}
{{define "content"}}<h1>Dashboard</h1>{{range .Articles}}<div class="article">{{.Title}}<a href="/admin/articles/{{.ID}}/cards">Linked cards</a></div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "dashboard.html"), []byte(dashboardContent), 0644); err != nil {
		t.Fatalf("failed to write dashboard template: %v", err)
	}

	newsletterContent := `{{template "layout" .}}
{{define "content"}}<h1>Newsletter</h1>{{if .NoContent}}<p>No articles to include</p>{{else}}<div class="preview">{{.PreviewHTML}}</div>{{end}}{{if .Success}}<div class="success">{{.Success}}</div>{{end}}{{if .Error}}<div class="error">{{.Error}}</div>{{end}}{{if .History}}{{range .History}}<div class="newsletter-row">{{.Subject}} {{.ArticleCount}}</div>{{end}}{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "newsletter.html"), []byte(newsletterContent), 0644); err != nil {
		t.Fatalf("failed to write newsletter template: %v", err)
	}

	app, err := NewAppWithTemplateDir(cfg, database, mgr, nil, mock, nil, context.Background(), filepath.Join(tmpDir, "templates"))
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}

	return app, database
}

func TestAdminNewsletter_Returns200WithPreview(t *testing.T) {
	mock := &mockNewsletterSvc{
		previewHTML: "<h1>Test Newsletter</h1><p>Hello world</p>",
	}
	app, _ := setupNewsletterTestApp(t, mock)

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/newsletter", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Newsletter") {
		t.Error("expected body to contain 'Newsletter'")
	}
	if !strings.Contains(body, "Test Newsletter") {
		t.Error("expected body to contain preview HTML")
	}
	if strings.Contains(body, "No articles to include") {
		t.Error("should not show 'No articles to include' when preview available")
	}
}

func TestAdminNewsletter_NoContent(t *testing.T) {
	mock := &mockNewsletterSvc{
		previewErr: fmt.Errorf("no approved articles in the last 24 hours"),
	}
	app, _ := setupNewsletterTestApp(t, mock)

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/newsletter", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "No articles to include") {
		t.Error("expected body to contain 'No articles to include'")
	}
}

func TestNewsletterSend_TriggersAndRedirects(t *testing.T) {
	mock := &mockNewsletterSvc{
		previewHTML: "<h1>Newsletter</h1>",
	}
	app, _ := setupNewsletterTestApp(t, mock)

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/newsletter/send", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d; body: %s", rec.Code, rec.Body.String())
	}

	if !mock.sendCalled {
		t.Error("expected SendDaily to be called")
	}

	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/admin/newsletter") {
		t.Errorf("expected redirect to /admin/newsletter, got %q", loc)
	}
	if !strings.Contains(loc, "success=") {
		t.Errorf("expected success parameter in redirect URL, got %q", loc)
	}
}

func TestNewsletterSend_Error(t *testing.T) {
	mock := &mockNewsletterSvc{
		sendErr: fmt.Errorf("smtp connection failed"),
	}
	app, _ := setupNewsletterTestApp(t, mock)

	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	rec := doAdminPOST(t, app, "/admin/newsletter/send", session, csrf, nil)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}

	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("expected error parameter in redirect URL, got %q", loc)
	}
}

func TestAdminArticleCardsGET(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-a", Title: "Card A",
		Category: "credit-card", Issuer: "X", Status: "published", KeywordsJSON: `[]`})
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-b", Title: "Card B",
		Category: "credit-card", Issuer: "Y", Status: "published", KeywordsJSON: `[]`})

	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Test", Slug: "test-cards-get", Content: "Card A and Card A again. Also Card B.",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"card-a","title":"Card A"}]`,
	})

	rec := doAdminGET(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Card A",
		"Card B",
		`name="linked_slugs[]"`,
		"in body",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q from response", want)
		}
	}
}

func TestNewsletterHistory_ListsSentNewsletters(t *testing.T) {
	mock := &mockNewsletterSvc{
		previewErr: fmt.Errorf("no articles"),
	}
	app, database := setupNewsletterTestApp(t, mock)

	// Insert some newsletter history.
	database.CreateNewsletter(&db.Newsletter{
		Date:         "2026-03-23",
		Subject:      "Daily Digest March 23",
		HTMLContent:  "<p>test</p>",
		Status:       "sent",
		ArticleCount: 3,
	})
	database.CreateNewsletter(&db.Newsletter{
		Date:         "2026-03-24",
		Subject:      "Daily Digest March 24",
		HTMLContent:  "<p>test2</p>",
		Status:       "draft",
		ArticleCount: 2,
	})

	cookies := loginAndGetSession(t, app)
	session, _ := getSessionAndCSRF(cookies)

	rec := doAdminGET(t, app, "/admin/newsletter/history", session)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Daily Digest March 23") {
		t.Error("expected body to contain 'Daily Digest March 23'")
	}
	if !strings.Contains(body, "Daily Digest March 24") {
		t.Error("expected body to contain 'Daily Digest March 24'")
	}
}

func TestAdminArticleCardsPOST_Save(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-a", Title: "Card A",
		Category: "credit-card", Issuer: "X", Status: "published"})
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-b", Title: "Card B",
		Category: "credit-card", Issuer: "Y", Status: "published"})

	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "post-test", Content: "C", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"card-a","title":"Card A"}]`,
	})

	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("action", "save")
	form.Add("linked_slugs[]", "card-a")
	form.Add("linked_slugs[]", "card-b")
	form.Add("heading_override", "Cards earning Membership Rewards")

	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d, want 303; body: %s", rec.Code, rec.Body.String())
	}

	updated, err := app.db.GetGeneratedArticle(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(updated.RelatedCardsJSON, `"slug":"card-a"`) ||
		!strings.Contains(updated.RelatedCardsJSON, `"slug":"card-b"`) {
		t.Errorf("RelatedCardsJSON missing one of the slugs: %s", updated.RelatedCardsJSON)
	}
	if updated.LinkedCardsHeading != "Cards earning Membership Rewards" {
		t.Errorf("heading: got %q", updated.LinkedCardsHeading)
	}
}

func TestAdminArticleCardsPOST_RemoveSlug(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-a", Title: "Card A",
		Category: "credit-card", Issuer: "X", Status: "published"})
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-b", Title: "Card B",
		Category: "credit-card", Issuer: "Y", Status: "published"})
	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "post-remove", Content: "C", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"card-a","title":"Card A"},{"slug":"card-b","title":"Card B"}]`,
	})

	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("remove_slug", "card-a")
	form.Add("linked_slugs[]", "card-a")
	form.Add("linked_slugs[]", "card-b")

	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d", rec.Code)
	}
	updated, _ := app.db.GetGeneratedArticle(id)
	if strings.Contains(updated.RelatedCardsJSON, `"slug":"card-a"`) {
		t.Errorf("card-a should have been removed: %s", updated.RelatedCardsJSON)
	}
	if !strings.Contains(updated.RelatedCardsJSON, `"slug":"card-b"`) {
		t.Errorf("card-b should remain: %s", updated.RelatedCardsJSON)
	}
}

func TestAdminArticleCardsPOST_AddSlug(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-a", Title: "Card A",
		Category: "credit-card", Issuer: "X", Status: "published"})
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-b", Title: "Card B",
		Category: "credit-card", Issuer: "Y", Status: "published"})
	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "post-add", Content: "C", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"card-a","title":"Card A"}]`,
	})

	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("add_slug", "card-b")
	form.Add("linked_slugs[]", "card-a")

	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status: got %d", rec.Code)
	}
	updated, _ := app.db.GetGeneratedArticle(id)
	if !strings.Contains(updated.RelatedCardsJSON, `"slug":"card-b"`) {
		t.Errorf("card-b should have been added: %s", updated.RelatedCardsJSON)
	}
}

func TestAdminArticleCardsPOST_HeadingTooLong(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)
	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "heading-len", Content: "C", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("heading_override", strings.Repeat("a", 300)) // > 256 limit
	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized heading, got %d", rec.Code)
	}
}

func TestAdminArticleCardsPOST_HeadingXSSEscaped(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)
	app.db.CreateReferencePage(&db.ReferencePage{Slug: "card-a", Title: "Card A",
		Category: "credit-card", Issuer: "X", Status: "published"})
	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "heading-xss", Content: "C", Type: "deal",
		Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"card-a","title":"Card A"}]`,
	})
	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("linked_slugs[]", "card-a")
	form.Add("heading_override", `<script>alert(1)</script>`)
	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status: %d", rec.Code)
	}
	rec = doPublicGET(t, app, "/article/heading-xss")
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("script tag rendered unescaped — XSS exposure")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected escaped script tag in output")
	}
}

// TestCardCuration_EndToEnd exercises the full flow: seed cards, seed an
// article with auto-linked top-2 cards, verify public render shows the embeds,
// run admin POST to swap a card, verify updated public render.
func TestCardCuration_EndToEnd(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	cookies := loginAndGetSession(t, app)
	session, csrf := getSessionAndCSRF(cookies)

	// Seed 4 reference cards.
	for _, c := range []db.ReferencePage{
		{Slug: "csp", Title: "Chase Sapphire Preferred", Category: "credit-card", Issuer: "Chase",
			Status: "published", AnnualFeeAmount: 95, WelcomeBonusText: "80K after $4K/3mo",
			ImageURL: "https://example.com/csp.png", ApplyURL: "https://chase.com/csp"},
		{Slug: "amex-plat", Title: "American Express Platinum", Category: "credit-card", Issuer: "Amex",
			Status: "published", AnnualFeeAmount: 695, WelcomeBonusText: "150K after $20K/3mo",
			ImageURL: "https://example.com/amex-plat.png"},
		{Slug: "amex-gold", Title: "American Express Gold", Category: "credit-card", Issuer: "Amex",
			Status: "published", AnnualFeeAmount: 325, WelcomeBonusText: "60K after $6K/6mo",
			ImageURL: "https://example.com/amex-gold.png"},
		{Slug: "venture-x", Title: "Capital One Venture X", Category: "credit-card", Issuer: "Capital One",
			Status: "published", AnnualFeeAmount: 395, WelcomeBonusText: "75K after $4K/3mo",
			ImageURL: "https://example.com/venture-x.png"},
	} {
		if _, err := app.db.CreateReferencePage(&c); err != nil {
			t.Fatalf("seed %s: %v", c.Slug, err)
		}
	}

	// Article body with reference text. Seed pre-curated state with 2 cards
	// (CSP + amex-gold) — simulating what main.go's cap-to-2 logic would have done.
	id, _ := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Best Travel Cards Compared", Slug: "best-travel-cards-e2e",
		Content: `<p>In the premium travel card segment, two standouts offer different strengths. ` +
			`The first excels in point transfers and category flexibility, making it ideal for flexible redemptions. ` +
			`The second focuses on dining and airline benefits for those with specific spending patterns.</p>`,
		Type: "summary", Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
		RelatedCardsJSON: `[{"slug":"csp","title":"Chase Sapphire Preferred"},{"slug":"amex-gold","title":"American Express Gold"}]`,
	})

	// 1. Public render shows the 2 currently-linked cards.
	rec := doPublicGET(t, app, "/article/best-travel-cards-e2e")
	if rec.Code != http.StatusOK {
		t.Fatalf("public article status: %d body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Cards mentioned in this article") {
		t.Error("public render missing default heading")
	}
	if !strings.Contains(body, "Chase Sapphire Preferred") || !strings.Contains(body, "American Express Gold") {
		t.Error("public render missing initially-linked cards")
	}
	if strings.Contains(body, "American Express Platinum") || strings.Contains(body, "Capital One Venture X") {
		t.Error("public render shouldn't include suggested-but-not-linked cards")
	}

	// 2. Admin curation: swap amex-gold OUT, amex-plat IN, set custom heading.
	form := url.Values{}
	form.Add("csrf_token", csrf)
	form.Add("action", "save")
	form.Add("linked_slugs[]", "csp")
	form.Add("linked_slugs[]", "amex-plat")
	form.Add("heading_override", "Cards in this comparison")

	rec = doAdminPOST(t, app, fmt.Sprintf("/admin/articles/%d/cards", id), session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin POST status: %d body: %s", rec.Code, rec.Body.String())
	}

	// 3. Public render again: heading is custom, cards reflect curation.
	rec = doPublicGET(t, app, "/article/best-travel-cards-e2e")
	body = rec.Body.String()
	if !strings.Contains(body, "Cards in this comparison") {
		t.Error("custom heading missing")
	}
	if !strings.Contains(body, "Chase Sapphire Preferred") || !strings.Contains(body, "American Express Platinum") {
		t.Error("public render missing post-curation cards")
	}
	if strings.Contains(body, "American Express Gold") {
		t.Error("public render still showing removed card")
	}
}
