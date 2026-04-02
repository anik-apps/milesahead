package web

import (
	"context"
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

// setupCompareTestApp creates a test App with compare template included.
func setupCompareTestApp(t *testing.T) (*App, *db.Database) {
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

	templateDir := filepath.Join(tmpDir, "templates")
	adminDir := filepath.Join(templateDir, "admin")
	publicDir := filepath.Join(templateDir, "public")
	if err := os.MkdirAll(adminDir, 0755); err != nil {
		t.Fatalf("failed to create admin template dir: %v", err)
	}
	if err := os.MkdirAll(publicDir, 0755); err != nil {
		t.Fatalf("failed to create public template dir: %v", err)
	}

	layoutContent := `{{define "layout"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}
{{define "csrf_field"}}<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "layout.html"), []byte(layoutContent), 0644); err != nil {
		t.Fatalf("failed to write layout: %v", err)
	}

	// Minimal admin templates.
	for _, name := range []string{"login.html", "dashboard.html", "published.html", "sources.html", "subscribers.html", "settings.html"} {
		content := `{{template "layout" .}}
{{define "content"}}<h1>` + name + `</h1>{{end}}`
		if err := os.WriteFile(filepath.Join(adminDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	// Minimal public templates needed by routes.
	for _, name := range []string{"home.html", "article.html", "subscribe.html", "cards.html", "card.html", "search.html"} {
		content := `{{template "layout" .}}
{{define "content"}}<h1>` + name + `</h1>{{end}}`
		if err := os.WriteFile(filepath.Join(publicDir, name), []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	// Compare template.
	compareContent := `{{template "layout" .}}
{{define "content"}}<div class="compare-page">{{if .SelectorMode}}<div class="selector">{{range .AllCards}}<option>{{.Title}}</option>{{end}}</div>{{else}}{{range .Cards}}<div class="card-title">{{.Title}}</div>{{end}}{{range .EarningRateRows}}<div class="rate-row">{{.Label}}</div>{{end}}{{range .CreditRows}}<div class="credit-row">{{.Label}}</div>{{end}}{{range .BenefitRows}}<div class="benefit-row">{{.Label}}</div>{{end}}{{range .ProtectionRows}}<div class="protection-row">{{.Label}}</div>{{end}}{{end}}</div>{{end}}`
	if err := os.WriteFile(filepath.Join(publicDir, "compare.html"), []byte(compareContent), 0644); err != nil {
		t.Fatalf("failed to write compare template: %v", err)
	}

	app, err := NewAppWithTemplateDir(cfg, database, mgr, voteSvc, nil, nil, context.Background(), templateDir)
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}

	return app, database
}

// createTestCard creates a published reference page with comparison data and returns its slug.
func createTestCard(t *testing.T, database *db.Database, title, slug, issuer, category string) string {
	t.Helper()

	earningRates := []byte(`{
		"dining": {"rate": "3x", "note": ""},
		"other_travel_direct": {"rate": "2x", "note": ""},
		"other_travel_portal": {"rate": "4x", "note": ""}
	}`)
	credits := []byte(`{
		"dining": {"amount": "$120", "note": "Annual"}
	}`)
	benefits := []byte(`{
		"lounge_access": {"value": "Priority Pass", "note": ""}
	}`)
	protections := []byte(`{
		"purchase_protection": {"amount": "$10,000", "note": "120 days"}
	}`)

	_, err := database.CreateReferencePage(&db.ReferencePage{
		Slug:               slug,
		Title:              title,
		Category:           category,
		Issuer:             issuer,
		SectionsJSON:       "[]",
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		ScoutsVerdict:      "Great card for travel enthusiasts with excellent earning rates and perks.",
		KeywordsJSON:       "[]",
		ImageURL:           "",
		ApplyURL:           "",
		AnnualFeeAmount:    95,
		AnnualFeeNote:      "",
		WelcomeBonusText:   "75,000 points after $4k spend",
		EarningRatesJSON:   string(earningRates),
		CreditsJSON:        string(credits),
		BenefitsJSON:       string(benefits),
		ProtectionsJSON:    string(protections),
		FlaggedFieldsJSON:  "[]",
		Status:             "published",
	})
	if err != nil {
		t.Fatalf("failed to create reference page %s: %v", slug, err)
	}
	return slug
}

func TestCompare_TwoCards(t *testing.T) {
	app, database := setupCompareTestApp(t)

	slug1 := createTestCard(t, database, "Chase Sapphire Preferred", "chase-sapphire-preferred", "Chase", "travel")
	slug2 := createTestCard(t, database, "Amex Gold", "amex-gold", "American Express", "travel")

	req := httptest.NewRequest(http.MethodGet, "/compare?cards="+slug1+","+slug2, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Chase Sapphire Preferred") {
		t.Error("expected body to contain first card title")
	}
	if !strings.Contains(body, "Amex Gold") {
		t.Error("expected body to contain second card title")
	}
	if !strings.Contains(body, "Dining") {
		t.Error("expected body to contain earning rate category")
	}
	if !strings.Contains(body, "credit-row") {
		t.Error("expected body to contain credit rows")
	}
	if strings.Contains(body, "selector") {
		t.Error("should NOT show selector mode when 2 valid cards provided")
	}
}

func TestCompare_InvalidSlugs(t *testing.T) {
	app, _ := setupCompareTestApp(t)

	// Both slugs are invalid (no cards in DB).
	req := httptest.NewRequest(http.MethodGet, "/compare?cards=nonexistent-card-1,nonexistent-card-2", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	// Should fall back to selector mode.
	if !strings.Contains(body, "selector") {
		t.Error("expected selector mode when invalid slugs provided")
	}
}

func TestCompare_DuplicateSlugs(t *testing.T) {
	app, database := setupCompareTestApp(t)

	slug1 := createTestCard(t, database, "Chase Sapphire Preferred", "chase-sapphire-preferred", "Chase", "travel")

	// Duplicate slug should be deduped, resulting in <2 cards -> selector.
	req := httptest.NewRequest(http.MethodGet, "/compare?cards="+slug1+","+slug1, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	// Should show selector because after dedup only 1 card.
	if !strings.Contains(body, "selector") {
		t.Error("expected selector mode when duplicate slugs provided")
	}
}

func TestCompare_NoCardsParam(t *testing.T) {
	app, _ := setupCompareTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/compare", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "selector") {
		t.Error("expected selector mode when no cards param")
	}
}

func TestExtractNumber(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"3x", 3},
		{"5x", 5},
		{"2%", 2},
		{"$300", 300},
		{"$1,200", 1200},
		{"-", 0},
		{"N/A", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := extractNumber(tt.input)
		if got != tt.expected {
			t.Errorf("extractNumber(%q) = %f, want %f", tt.input, got, tt.expected)
		}
	}
}
