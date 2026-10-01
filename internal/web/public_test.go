package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/vote"
	"golang.org/x/crypto/bcrypt"
)

// setupPublicTestApp creates a test App with public + admin templates for public handler tests.
func setupPublicTestApp(t *testing.T) (*App, *db.Database) {
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
	}

	mgr := article.NewManager(database, nil)
	voteSvc := vote.NewService(database, cfg.Votes, nil)

	// Create template directories.
	templateDir := filepath.Join(tmpDir, "templates")
	adminDir := filepath.Join(templateDir, "admin")
	publicDir := filepath.Join(templateDir, "public")
	if err := os.MkdirAll(adminDir, 0755); err != nil {
		t.Fatalf("failed to create admin template dir: %v", err)
	}
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatalf("failed to create public template dir: %v", err)
	}

	// Write layout template.
	layoutContent := `{{define "layout"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}
{{define "csrf_field"}}<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "layout.html"), []byte(layoutContent), 0644); err != nil {
		t.Fatalf("failed to write layout template: %v", err)
	}

	// Write minimal admin templates to satisfy route setup.
	for _, name := range []string{"login.html", "dashboard.html", "published.html", "sources.html", "subscribers.html", "settings.html"} {
		content := `{{template "layout" .}}
{{define "content"}}<h1>` + name + `</h1>{{end}}`
		if err := os.WriteFile(filepath.Join(adminDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	// Write public templates.
	homeContent := `{{template "layout" .}}
{{define "content"}}<h1>Home</h1>{{if .Featured}}<div class="featured">{{.Featured.Title}}</div>{{end}}{{range .Articles}}<div class="article-card" data-type="{{.Type}}">{{.Title}}</div>{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "home.html"), []byte(homeContent), 0644); err != nil {
		t.Fatalf("failed to write home template: %v", err)
	}

	articleContent := `{{template "layout" .}}
{{define "content"}}{{if .UnderReview}}<div class="under-review">This article is currently under review.</div>{{else}}<h1>{{.Article.Title}}</h1><div class="content">{{.Article.Content}}</div>{{range .Sources}}<a class="source" href="{{.URL}}">{{.Title}}</a>{{end}}<div class="votes"><span class="upvotes">{{.Upvotes}}</span><span class="downvotes">{{.Downvotes}}</span></div>{{if .LinkedCards}}<div class="card-embeds"><h3 class="card-embeds-heading">{{if .LinkedCardsHeading}}{{.LinkedCardsHeading}}{{else}}Cards mentioned in this article{{end}}</h3>{{range .LinkedCards}}<div class="card-embed"><div class="card-embed-top">{{if .ImageURL}}<img class="card-embed-logo" src="{{.ImageURL}}" alt="">{{end}}<div><h4 class="card-embed-name"><a href="/cards/{{.Slug}}">{{.Title}}</a></h4><span class="card-embed-issuer">{{.Issuer}} · {{.Category}} · {{.AnnualFee}}</span></div></div>{{if .WelcomeBonus}}<div class="card-embed-grid"><div class="stat"><b>Welcome bonus</b><span>{{.WelcomeBonus}}</span></div>{{if .TopEarning}}<div class="stat"><b>Top earnings</b><span>{{.TopEarning}}</span></div>{{end}}<div class="stat"><b>Annual fee</b><span>{{.AnnualFee}}</span></div></div>{{end}}{{if .ScoutsVerdict}}<p class="card-embed-verdict">{{.ScoutsVerdict}}</p>{{end}}{{if .ApplyURL}}<a href="{{.ApplyURL}}" target="_blank" rel="noopener" class="card-embed-cta">View card</a>{{end}}</div>{{end}}</div>{{end}}{{end}}{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "article.html"), []byte(articleContent), 0644); err != nil {
		t.Fatalf("failed to write article template: %v", err)
	}

	subscribeContent := `{{template "layout" .}}
{{define "content"}}<div class="state">{{.State}}</div>{{if .Error}}<div class="error">{{.Error}}</div>{{end}}<form method="POST" action="/subscribe"><input name="email"><button type="submit">Subscribe</button></form>{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "subscribe.html"), []byte(subscribeContent), 0644); err != nil {
		t.Fatalf("failed to write subscribe template: %v", err)
	}

	app, err := NewAppWithTemplateDir(cfg, database, mgr, voteSvc, nil, nil, context.Background(), templateDir)
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}

	// Join handler-started goroutines before fixtures are torn down.
	// t.Cleanup is LIFO, so registering here runs this BEFORE the
	// database close and temp-dir removal registered earlier.
	t.Cleanup(app.WaitBackground)

	return app, database
}

// createApprovedArticle creates an approved article and returns its ID and slug.
func createApprovedArticle(t *testing.T, database *db.Database, title, articleType string, featured bool) (int64, string) {
	t.Helper()
	slug := article.GenerateSlug(title)
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          title,
		Slug:           slug,
		Content:        "Content for " + title + ". This is a longer piece of content that should be enough to test excerpts and truncation.",
		Type:           articleType,
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
		Featured:       featured,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}
	// Transition to approved.
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("failed to approve article: %v", err)
	}
	return id, slug
}

func TestHome_Returns200WithApprovedArticles(t *testing.T) {
	app, database := setupPublicTestApp(t)

	// Create approved articles.
	createApprovedArticle(t, database, "Approved Article One", "deal", false)
	createApprovedArticle(t, database, "Approved Article Two", "roundup", true)

	// Create a draft article (should NOT appear).
	_, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Draft Article",
		Slug:           "draft-article-xyz123",
		Content:        "Draft content",
		Type:           "deal",
		Status:         db.StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create draft article: %v", err)
	}

	// Create a pending article (should NOT appear).
	_, err = database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Pending Article",
		Slug:           "pending-article-xyz123",
		Content:        "Pending content",
		Type:           "alert",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create pending article: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Approved Article One") {
		t.Error("expected body to contain 'Approved Article One'")
	}
	if !strings.Contains(body, "Approved Article Two") {
		t.Error("expected body to contain 'Approved Article Two'")
	}
	if strings.Contains(body, "Draft Article") {
		t.Error("body should NOT contain 'Draft Article'")
	}
	if strings.Contains(body, "Pending Article") {
		t.Error("body should NOT contain 'Pending Article'")
	}
}

func TestHome_CategoryFilter(t *testing.T) {
	app, database := setupPublicTestApp(t)

	createApprovedArticle(t, database, "Deal Article", "deal", false)
	createApprovedArticle(t, database, "Roundup Article", "roundup", false)

	req := httptest.NewRequest(http.MethodGet, "/?type=deal", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Deal Article") {
		t.Error("expected body to contain 'Deal Article'")
	}
	if strings.Contains(body, "Roundup Article") {
		t.Error("body should NOT contain 'Roundup Article' when filtering by deal")
	}
}

func TestArticle_Returns200WithContent(t *testing.T) {
	app, database := setupPublicTestApp(t)

	id, slug := createApprovedArticle(t, database, "Test Article Detail", "deal", false)

	// Link a raw article source.
	srcID, err := database.CreateSource(&db.Source{
		Name:   "Test Source",
		URL:    "https://source.example.com",
		Active: true,
	})
	if err != nil {
		t.Fatalf("failed to create source: %v", err)
	}
	rawID, err := database.CreateRawArticle(&db.RawArticle{
		SourceID: srcID,
		Title:    "Original Source Article",
		URL:      "https://source.example.com/article",
		Content:  "Source content",
		Hash:     "abc123hash",
	})
	if err != nil {
		t.Fatalf("failed to create raw article: %v", err)
	}
	if err := database.LinkArticleSources(id, []int64{rawID}); err != nil {
		t.Fatalf("failed to link sources: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/article/"+slug, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Test Article Detail") {
		t.Error("expected body to contain article title")
	}
	if !strings.Contains(body, "Content for Test Article Detail") {
		t.Error("expected body to contain article content")
	}
	// Public pages do not name or link the news sites an article was written
	// from: the handler does not hand them to the template at all, so even
	// this test template, which would list .Sources, shows none.
	if strings.Contains(body, "Original Source Article") || strings.Contains(body, "https://source.example.com/article") {
		t.Error("the public article page should not cite its source stories")
	}
}

// An article that is not approved is not public: 404, and nothing of it on
// the page.
func TestArticle_UnpublishedIsNotFound(t *testing.T) {
	app, database := setupPublicTestApp(t)

	slug := "pending-test-article-abc123"
	_, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Pending Test Article",
		Slug:           slug,
		Content:        "Some pending content",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/article/"+slug, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Some pending content") || strings.Contains(body, "Pending Test Article") {
		t.Error("the page must not show the unpublished article's content or headline")
	}
}

func TestArticle_NotFound(t *testing.T) {
	app, _ := setupPublicTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/article/nonexistent-slug", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}
}

func TestFeed_ReturnsValidRSS(t *testing.T) {
	app, database := setupPublicTestApp(t)

	createApprovedArticle(t, database, "Feed Article One", "deal", false)
	createApprovedArticle(t, database, "Feed Article Two", "roundup", false)

	// Draft article should not appear.
	database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Draft Feed Article",
		Slug:           "draft-feed-xyz123",
		Content:        "Draft feed content",
		Type:           "deal",
		Status:         db.StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})

	req := httptest.NewRequest(http.MethodGet, "/feed.xml", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/xml") {
		t.Errorf("expected Content-Type to contain application/xml, got %q", contentType)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<?xml") {
		t.Error("expected XML declaration")
	}
	if !strings.Contains(body, "<rss") {
		t.Error("expected RSS tag")
	}
	if !strings.Contains(body, "Feed Article One") {
		t.Error("expected feed to contain 'Feed Article One'")
	}
	if !strings.Contains(body, "Feed Article Two") {
		t.Error("expected feed to contain 'Feed Article Two'")
	}
	if strings.Contains(body, "Draft Feed Article") {
		t.Error("feed should NOT contain draft articles")
	}
	if !strings.Contains(body, "<item>") {
		t.Error("expected RSS items")
	}
	if !strings.Contains(body, "<title>") {
		t.Error("expected title elements in RSS")
	}
}

func TestSubscribe_Success(t *testing.T) {
	app, database := setupPublicTestApp(t)

	form := strings.NewReader("email=test@example.com")
	req := httptest.NewRequest(http.MethodPost, "/subscribe", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	// Post/redirect/get: a refresh must not post the form again.
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/subscribe/check-email" {
		t.Errorf("expected a redirect to /subscribe/check-email, got %q", loc)
	}

	// Verify subscriber was created.
	subs, err := database.ListAllSubscribers()
	if err != nil {
		t.Fatalf("failed to list subscribers: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscriber, got %d", len(subs))
	}
	if subs[0].Email != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got %q", subs[0].Email)
	}
	if subs[0].Token == "" {
		t.Error("expected subscriber to have a token")
	}
}

func TestSubscribe_InvalidEmail(t *testing.T) {
	app, _ := setupPublicTestApp(t)

	form := strings.NewReader("email=invalid")
	req := httptest.NewRequest(http.MethodPost, "/subscribe", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "valid email") {
		t.Error("expected error message about valid email")
	}
}

func TestSubscribeConfirm_Success(t *testing.T) {
	app, database := setupPublicTestApp(t)

	// Create unconfirmed subscriber.
	database.CreateSubscriber(&db.Subscriber{
		Email: "confirm@example.com",
		Token: "confirm-token-123",
	})

	req := httptest.NewRequest(http.MethodGet, "/subscribe/confirm?token=confirm-token-123", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `<div class="state">confirmed</div>`) {
		t.Error("expected the confirmed page")
	}
}

// The Unsubscribe link only asks; the button's POST unsubscribes.
func TestUnsubscribe_Success(t *testing.T) {
	app, database := setupPublicTestApp(t)

	database.CreateSubscriber(&db.Subscriber{
		Email: "unsub@example.com",
		Token: "unsub-token-456",
	})
	database.ConfirmSubscriber("unsub-token-456")

	req := httptest.NewRequest(http.MethodGet, "/unsubscribe?token=unsub-token-456", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<div class="state">unsub-confirm</div>`) {
		t.Error("expected the page asking to confirm")
	}
	if subs, _ := database.ListConfirmedSubscribers(); len(subs) != 1 {
		t.Fatalf("opening the link must not unsubscribe; %d confirmed", len(subs))
	}

	req = httptest.NewRequest(http.MethodPost, "/unsubscribe", strings.NewReader("token=unsub-token-456"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", rec.Code)
	}
	if subs, _ := database.ListConfirmedSubscribers(); len(subs) != 0 {
		t.Errorf("expected 0 confirmed subscribers after unsubscribe, got %d", len(subs))
	}
}

func TestVote_Success(t *testing.T) {
	app, database := setupPublicTestApp(t)

	id, _ := createApprovedArticle(t, database, "Vote Article", "deal", false)

	payload := `{"article_id":` + jsonInt64(id) + `,"vote":"up"}`
	req := httptest.NewRequest(http.MethodPost, "/api/vote", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"status":"ok"`) {
		t.Error("expected ok status in response")
	}
	if !strings.Contains(body, `"upvotes":1`) {
		t.Error("expected upvotes to be 1")
	}
}

func TestVote_Duplicate(t *testing.T) {
	app, database := setupPublicTestApp(t)

	id, _ := createApprovedArticle(t, database, "Vote Dup Article", "deal", false)

	body := `{"article_id":` + jsonInt64(id) + `,"vote":"up"}`

	// First vote.
	req := httptest.NewRequest(http.MethodPost, "/api/vote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first vote failed: %d", rec.Code)
	}

	// Second vote (duplicate).
	req = httptest.NewRequest(http.MethodPost, "/api/vote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.1:12345"
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected status 409 for duplicate vote, got %d", rec.Code)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		n        int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 10, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.expected)
		}
	}
}

func doPublicGET(t *testing.T, app *App, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

func TestHandleArticle_RendersCardEmbed(t *testing.T) {
	app, _ := setupPublicTestApp(t)
	cardID, err := app.db.CreateReferencePage(&db.ReferencePage{
		Slug:             "chase-sapphire-preferred",
		Title:            "Chase Sapphire Preferred",
		Category:         "credit-card",
		Issuer:           "Chase",
		Status:           "published",
		AnnualFeeAmount:  95,
		WelcomeBonusText: "80,000 points after $4K spend",
		ScoutsVerdict:    "Best $95 you'll spend.",
		ImageURL:         "https://example.com/csp.png",
		ApplyURL:         "https://chase.com/csp",
	})
	if err != nil {
		t.Fatalf("create ref page: %v", err)
	}
	_ = cardID

	_, err = app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:            "Chase Bumps the CSP Bonus to 80K",
		Slug:             "csp-80k",
		Content:          "<p>The Sapphire Preferred bonus is back to 80K.</p>",
		Type:             "deal",
		Status:           db.StatusApproved,
		SourcesJSON:      "[]",
		RelatedCardsJSON: `[{"slug":"chase-sapphire-preferred","title":"Chase Sapphire Preferred"}]`,
		RevisionNumber:   1,
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}

	rec := doPublicGET(t, app, "/article/csp-80k")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rec.Code, body)
	}
	for _, want := range []string{
		"Cards mentioned in this article",
		`href="/cards/chase-sapphire-preferred"`,
		"80,000 points after $4K spend",
		"Best $95 you&#39;ll spend.",
		`https://example.com/csp.png`,
		`href="https://chase.com/csp"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q", want)
		}
	}
}

func TestHandleArticle_NoEmbedWhenNoLinkedCards(t *testing.T) {
	app, _ := setupPublicTestApp(t)
	_, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "no-cards", Content: "<p>body</p>", Type: "deal",
		Status: db.StatusApproved, SourcesJSON: "[]", RelatedCardsJSON: "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := doPublicGET(t, app, "/article/no-cards")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `class="card-embeds"`) {
		t.Error("embed section rendered despite empty related_cards_json")
	}
}

func TestHandleArticle_MalformedRelatedCardsJSON(t *testing.T) {
	app, _ := setupPublicTestApp(t)
	_, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "bad-json", Content: "<p>body</p>", Type: "deal",
		Status: db.StatusApproved, SourcesJSON: "[]",
		RelatedCardsJSON: `not valid json`, RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := doPublicGET(t, app, "/article/bad-json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `class="card-embeds"`) {
		t.Error("embed section rendered despite malformed JSON")
	}
}

func TestHandleArticle_LinkedSlugMissingOrUnpublished(t *testing.T) {
	app, _ := setupPublicTestApp(t)
	_, _ = app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "real-card", Title: "Real Card", Category: "credit-card",
		Issuer: "X", Status: "published", ImageURL: "x.png",
	})
	_, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "missing-slug", Content: "<p>body</p>", Type: "deal",
		Status: db.StatusApproved, SourcesJSON: "[]",
		RelatedCardsJSON: `[{"slug":"real-card","title":"Real Card"},{"slug":"deleted-card","title":"Gone"}]`,
		RevisionNumber:   1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := doPublicGET(t, app, "/article/missing-slug")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Real Card") {
		t.Error("expected Real Card embed to render")
	}
	if strings.Contains(body, "Gone") {
		t.Error("missing-slug card should not render at all")
	}
}

// TestHandleArticle_LinkedSlugDraftCard ensures cards with status != "published"
// are skipped from public render even if linked via related_cards_json.
func TestHandleArticle_LinkedSlugDraftCard(t *testing.T) {
	app, _ := setupPublicTestApp(t)
	// Published card.
	_, _ = app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "pub-card", Title: "Published Card", Category: "credit-card",
		Issuer: "X", Status: "published", ImageURL: "p.png",
	})
	// Draft card.
	_, _ = app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "draft-card", Title: "Draft Card", Category: "credit-card",
		Issuer: "Y", Status: "draft", ImageURL: "d.png",
	})
	_, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T", Slug: "draft-card-test", Content: "<p>body</p>", Type: "deal",
		Status: db.StatusApproved, SourcesJSON: "[]",
		RelatedCardsJSON: `[{"slug":"pub-card","title":"Published Card"},{"slug":"draft-card","title":"Draft Card"}]`,
		RevisionNumber:   1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := doPublicGET(t, app, "/article/draft-card-test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Published Card") {
		t.Error("published card should render")
	}
	if strings.Contains(body, "Draft Card") {
		t.Error("draft card should NOT render publicly")
	}
}

func jsonInt64(id int64) string {
	b, _ := json.Marshal(id)
	return string(b)
}
