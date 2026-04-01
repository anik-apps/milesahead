package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestSitemap_ContainsURLs(t *testing.T) {
	app, database := setupPublicTestApp(t)

	// Create test articles.
	_, slug1 := createApprovedArticle(t, database, "Sitemap Article One", "deal", false)
	_, slug2 := createApprovedArticle(t, database, "Sitemap Article Two", "roundup", false)

	// Create test cards.
	_, err := database.CreateReferencePage(&db.ReferencePage{
		Slug:          "chase-sapphire",
		Title:         "Chase Sapphire Preferred",
		Category:      "travel",
		Issuer:        "Chase",
		SectionsJSON:  "[]",
		ScoutsVerdict: "Great card",
		ImageURL:      "",
		Status:        "published",
	})
	if err != nil {
		t.Fatalf("failed to create card: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/xml") {
		t.Errorf("expected Content-Type application/xml, got %s", contentType)
	}

	cacheControl := rec.Header().Get("Cache-Control")
	if !strings.Contains(cacheControl, "public") {
		t.Errorf("expected Cache-Control public, got %s", cacheControl)
	}

	body := rec.Body.String()

	// Check XML structure.
	if !strings.Contains(body, `<?xml version="1.0"`) {
		t.Error("expected XML declaration")
	}
	if !strings.Contains(body, `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`) {
		t.Error("expected urlset element")
	}

	// Check homepage.
	if !strings.Contains(body, "https://localhost/") {
		t.Error("expected homepage URL in sitemap")
	}

	// Check cards listing.
	if !strings.Contains(body, "https://localhost/cards/") {
		t.Error("expected /cards/ URL in sitemap")
	}

	// Check search.
	if !strings.Contains(body, "https://localhost/search") {
		t.Error("expected /search URL in sitemap")
	}

	// Check articles.
	if !strings.Contains(body, "/article/"+slug1) {
		t.Errorf("expected article slug %s in sitemap", slug1)
	}
	if !strings.Contains(body, "/article/"+slug2) {
		t.Errorf("expected article slug %s in sitemap", slug2)
	}

	// Check card.
	if !strings.Contains(body, "/cards/chase-sapphire") {
		t.Error("expected card slug chase-sapphire in sitemap")
	}

	// Check priorities.
	if !strings.Contains(body, "<priority>1.0</priority>") {
		t.Error("expected priority 1.0 for homepage")
	}
	if !strings.Contains(body, "<priority>0.8</priority>") {
		t.Error("expected priority 0.8 for articles")
	}
	if !strings.Contains(body, "<priority>0.7</priority>") {
		t.Error("expected priority 0.7 for cards")
	}
}

func TestRobots_Content(t *testing.T) {
	app, _ := setupPublicTestApp(t)

	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("expected Content-Type text/plain, got %s", contentType)
	}

	body := rec.Body.String()

	if !strings.Contains(body, "User-agent: *") {
		t.Error("expected User-agent directive")
	}
	if !strings.Contains(body, "Allow: /") {
		t.Error("expected Allow directive")
	}
	if !strings.Contains(body, "Disallow: /admin/") {
		t.Error("expected Disallow /admin/")
	}
	if !strings.Contains(body, "Disallow: /metrics") {
		t.Error("expected Disallow /metrics")
	}
	if !strings.Contains(body, "Disallow: /healthz") {
		t.Error("expected Disallow /healthz")
	}
	if !strings.Contains(body, "Sitemap: https://localhost/sitemap.xml") {
		t.Error("expected Sitemap URL")
	}
}
