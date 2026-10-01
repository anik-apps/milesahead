package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- Header, skip link, footer ---

func TestLayout_SkipLinkComesFirst(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	skip := strings.Index(body, `<a class="skip-link" href="#main">Skip to content</a>`)
	nav := strings.Index(body, `<nav class="site-nav"`)
	if skip < 0 || nav < 0 || skip > nav {
		t.Errorf("the skip link should be the first thing in <body>, before the header (skip at %d, nav at %d)", skip, nav)
	}
	if !strings.Contains(body, `<main id="main" tabindex="-1">`) {
		t.Error("the skip link's target, <main id=\"main\">, is missing")
	}
}

// The header has flat links (the two-item Cards menu is gone), a search
// toggle that reports its state, and no Subscribe button yet.
func TestLayout_HeaderLinks(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	for _, want := range []string{
		`<a href="/cards/" class="nav-link">Cards</a>`,
		`<a href="/compare" class="nav-link">Compare</a>`,
		`aria-expanded="false" aria-controls="nav-search-form"`,
		`<input type="search" id="nav-search-input" name="q"`,
		`<label for="nav-search-input" class="visually-hidden">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("header should contain %s", want)
		}
	}
	for _, gone := range []string{"nav-dropdown", "nav-subscribe"} {
		if strings.Contains(body, gone) {
			t.Errorf("header should not contain %q", gone)
		}
	}

	cards := getPage(t, app, "/cards/")
	if !strings.Contains(cards, `<a href="/cards/" class="nav-link" aria-current="page">Cards</a>`) {
		t.Error("on /cards/ the Cards link should be marked as the current section")
	}
	compare := getPage(t, app, "/compare")
	if !strings.Contains(compare, `<a href="/compare" class="nav-link" aria-current="page">Compare</a>`) {
		t.Error("on /compare the Compare link should be marked as the current section")
	}
}

func TestLayout_FooterYearAndThemeToggle(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	if want := "&copy; " + strconv.Itoa(time.Now().Year()) + " MilesAhead"; !strings.Contains(body, want) {
		t.Errorf("footer should say %s", want)
	}
	if n := strings.Count(body, "data-theme-toggle"); n != 2 {
		t.Errorf("want a theme toggle in the header and one in the footer for phones, got %d", n)
	}
}

// The script wires every theme toggle, and the search toggle's
// aria-expanded and Escape.
func TestMainJS_TogglesAreAccessible(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "static", "js", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`querySelectorAll('[data-theme-toggle]')`,
		`searchToggle.setAttribute('aria-expanded'`,
		`e.key === 'Escape'`,
		`searchToggle.focus()`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("main.js should contain %s", want)
		}
	}
	if strings.Contains(js, "nav-dropdown") {
		t.Error("main.js still has the dropdown code")
	}
}

// The home hero says what the site is, in the page's h1, instead of
// repeating the logo.
func TestHome_HeroHeading(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	if n := strings.Count(body, "<h1"); n != 1 {
		t.Errorf("want one h1, got %d", n)
	}
	if !strings.Contains(body, `<section class="hero-strip">`) || strings.Contains(body, "hero-brand") {
		t.Error("the home page should use the compact hero strip")
	}
	if strings.Contains(body, `<h1 class="brand-word">`) {
		t.Error("the h1 should describe the site, not be the logo word")
	}
}

// --- Static files and caching ---

func TestStaticHandler_CacheControl(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.css"), []byte("a{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := staticHandler(http.FileServer(http.Dir(dir)))
	for _, c := range []struct {
		path, want string
		status     int
	}{
		{"/a.css?v=0123456789ab", "public, max-age=31536000, immutable", 200},
		{"/a.css", "public, max-age=3600", 200},
		{"/missing.css?v=0123456789ab", "", 404},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.want)
		}
	}
}

// The script is versioned by content like the stylesheet, so both can be
// cached for a year; pages themselves are always revalidated.
func TestPages_VersionedScriptAndNoCacheHTML(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	rec := do(t, app, http.MethodGet, "/", "")
	if !regexp.MustCompile(`<script src="/static/js/main\.js\?v=[0-9a-f]{12}"></script>`).MatchString(rec.Body.String()) {
		t.Error("main.js should be linked with a content version")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("HTML Cache-Control = %q, want no-cache", got)
	}
}

// --- Contrast ---

// The colours changed for contrast meet WCAG AA (4.5:1) and are in the CSS.
func TestStyles_ContrastFixes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, c := range []struct{ name, fg, bg, decl string }{
		{"deal badge", "#ffffff", "#c81e3a", ".badge-deal { background: #c81e3a; }"},
		{"error message", "#b91c1c", "#fef2f2", "color: #b91c1c;"},
		{"success message", "#15803d", "#f0fdf4", "color: #15803d;"},
		{"dark accent button", "#0d1117", "#58a6ff", "--on-accent: #0d1117;"},
		{"light nav link", "#334155", "#e6f1fb", "--nav-link: #334155;"},
	} {
		if r := contrast(c.fg, c.bg); r < 4.5 {
			t.Errorf("%s: %s on %s is %.2f:1", c.name, c.fg, c.bg, r)
		}
		if !strings.Contains(css, c.decl) {
			t.Errorf("%s: style.css should contain %q", c.name, c.decl)
		}
	}
	for _, sel := range []string{".search-form button", ".status-search button, .subscribe-form button", ".vote-btn.active"} {
		i := strings.Index(css, sel+" {")
		if i < 0 {
			t.Errorf("missing rule %s", sel)
			continue
		}
		rule := css[i : i+strings.Index(css[i:], "}")]
		if !strings.Contains(rule, "color: var(--on-accent)") {
			t.Errorf("%s should use var(--on-accent) for its text", sel)
		}
	}
}
