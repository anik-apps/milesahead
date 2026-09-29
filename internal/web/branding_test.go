package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func getPage(t *testing.T, app *App, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d", path, rec.Code)
	}
	return rec.Body.String()
}

// Integration: the real home route and templates render the brand header,
// hero and footer band.
func TestLayout_RendersBrand(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")

	for _, want := range []string{
		`<nav class="site-nav">`,
		`class="logo brand-logo" aria-label="MilesAhead home"`,
		`src="/static/img/brand/mark.svg"`,
		`src="/static/img/brand/mark-dark.svg"`,
		`<span class="brand-word">MilesAhead</span>`,
		`<section class="hero-brand">`,
		`<h1 class="brand-word">MilesAhead</h1>`,
		`<footer class="site-footer">`,
		`<nav class="footer-links" aria-label="Footer">`,
		`<a href="/cards/">Cards</a>`,
		`<a href="/compare">Compare</a>`,
		`<a href="/feed.xml">RSS feed</a>`,
		`<a href="/cards/">Guides</a>`,
		`rel="icon" href="/static/img/brand/favicon.svg"`,
		`rel="apple-touch-icon" href="/static/img/brand/apple-touch-icon.png"`,
		`<meta property="og:site_name" content="MilesAhead">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home page should contain %s", want)
		}
	}
	if strings.Contains(body, "Miles Ahead") {
		t.Error(`the name is now one word, "MilesAhead"; found "Miles Ahead"`)
	}
	if strings.Contains(body, "Toggle Theme") {
		t.Error("the theme toggle should be an icon button with an accessible name, not text that wraps on phones")
	}
	if !strings.Contains(body, `id="theme-toggle" type="button" class="theme-icon-btn" aria-label="Dark theme" aria-pressed="false"`) {
		t.Error("the theme toggle should be a labelled toggle button")
	}
}

// The saved or OS theme must be applied before the stylesheet loads, or
// dark-mode readers see a white flash on every page.
func TestLayout_AppliesThemeBeforeStylesheet(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	script := strings.Index(body, "prefers-color-scheme: dark")
	css := strings.Index(body, `href="/static/css/style.css`)
	if script < 0 || css < 0 || script > css {
		t.Errorf("the theme script should come before the stylesheet (script at %d, css at %d)", script, css)
	}
}

// Pages without an image of their own share a branded preview; pages with one
// keep it.
func TestLayout_DefaultShareImage(t *testing.T) {
	app := setupRealTemplateCardApp(t)

	home := getPage(t, app, "/")
	want := `<meta property="og:image" content="https://localhost/static/img/brand/og-default.png">`
	if !strings.Contains(home, want) {
		t.Errorf("home should fall back to the branded share image: %s", want)
	}
	if !strings.Contains(home, `<meta name="twitter:image" content="https://localhost/static/img/brand/og-default.png">`) {
		t.Error("home twitter:image should use the branded share image")
	}

	createPublishedCard(t, app, "amex-gold", "American Express Gold Card")
	card := getCardPage(t, app, "amex-gold")
	if strings.Contains(card, "og-default.png") {
		t.Error("a card page has its own art and should not use the default share image")
	}
}

// Every brand asset a template or the stylesheet points at must be committed.
func TestBrandAssets_Exist(t *testing.T) {
	root := filepath.Join("..", "..")
	var sources []string
	for _, glob := range []string{"templates/*.html", "templates/public/*.html", "templates/email/*.html", "static/css/style.css"} {
		m, err := filepath.Glob(filepath.Join(root, glob))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, m...)
	}
	re := regexp.MustCompile(`/static/img/brand/[a-z0-9-]+\.(?:svg|png)`)
	seen := map[string]bool{}
	for _, f := range sources {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range re.FindAllString(string(b), -1) {
			seen[ref] = true
		}
	}
	if len(seen) < 10 {
		t.Fatalf("expected the templates and CSS to reference the brand assets, found only %d", len(seen))
	}
	for ref := range seen {
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(ref, "/"))); err != nil {
			t.Errorf("%s is referenced but missing: %v", ref, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "static/img/brand/og-default.png")); err != nil {
		t.Errorf("the default share image is missing: %v", err)
	}
}
