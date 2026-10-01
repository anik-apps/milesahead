package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// renderStatus renders a page template with the given status code. The page
// is rendered into a buffer first, so a template error becomes the 500 page
// rather than half a page followed by a Go error message.
func (app *App) renderStatus(w http.ResponseWriter, status int, name string, data interface{}) {
	t, ok := app.templates[name]
	if !ok {
		slog.Error("render: template not found", "template", name)
		plainError(w, http.StatusInternalServerError)
		return
	}
	if m, ok := data.(map[string]interface{}); ok {
		// Signed-in admin pages get the admin bar's tab and counts.
		if t.Lookup("admin_layout") != nil && name != "login.html" {
			m["Nav"] = app.adminNavFor(name, m)
		}
		// Pages without their own image (home, compare, search) still get a
		// branded preview when shared. Crawlers need an absolute URL.
		if img, _ := m["OGImage"].(string); img == "" {
			m["OGImage"] = "https://" + app.siteDomain() + defaultOGImage
		}
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		slog.Error("render: template failed", "template", name, "error", err)
		if name != errorTemplate {
			app.renderError(w, http.StatusInternalServerError, nil)
			return
		}
		plainError(w, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	if h.Get("Cache-Control") == "" {
		// Pages change whenever an article is published: always revalidate.
		h.Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// plainError is the last resort when even the error page cannot render.
func plainError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

// errorTemplate renders 404, 410, 500 and similar pages.
const errorTemplate = "error.html"

// Error page kinds, which pick its heading and copy.
const (
	errKindPage    = "page"
	errKindArticle = "article"
	errKindCard    = "card"
	errKindServer  = "server"
)

// errorPage is what error.html renders.
type errorPage struct {
	Status int
	Kind   string
	// Query is the words taken from the requested slug; Articles and Cards
	// are what a search for them found.
	Query    string
	Articles []db.SearchResult
	Cards    []db.SearchResult
}

// renderError renders the error page. page may be nil for a plain one.
func (app *App) renderError(w http.ResponseWriter, status int, page *errorPage) {
	if page == nil {
		page = &errorPage{Kind: errKindPage}
	}
	page.Status = status
	if status >= 500 {
		page.Kind = errKindServer
	}
	title := "Page not found"
	switch {
	case status >= 500:
		title = "Something went wrong"
	case status == http.StatusGone:
		title = "No longer available"
	}
	if _, ok := app.templates[errorTemplate]; !ok {
		// Test fixtures with a minimal template set.
		plainError(w, status)
		return
	}
	app.renderStatus(w, status, errorTemplate, map[string]interface{}{
		"Title":   title,
		"Noindex": true,
		"Error":   page,
	})
}

// serverError logs err and shows readers the 500 page, never the error.
func (app *App) serverError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Error(msg, "path", r.URL.Path, "error", err)
	app.renderError(w, http.StatusInternalServerError, nil)
}

// handleNotFound serves every path no other route matches.
func (app *App) handleNotFound(w http.ResponseWriter, r *http.Request) {
	app.renderError(w, http.StatusNotFound, nil)
}

// notFound renders the 404 (or, for content that was published and has been
// taken down, 410) page for a missing article or card, with suggestions
// from the words in its slug.
func (app *App) notFound(w http.ResponseWriter, status int, kind, slug string) {
	page := &errorPage{Kind: kind}
	words := slugWords(slug, kind == errKindArticle)
	if len(words) > 0 {
		page.Query = strings.Join(words, " ")
		q := ftsAnyPrefix(words)
		if arts, err := app.db.SearchArticles(q, "", app.offerToday(), 3, 0); err == nil {
			page.Articles = arts
		} else {
			slog.Warn("not found: suggesting articles", "error", err)
		}
		if cards, err := app.db.SearchCards(q, "", 3, 0); err == nil {
			page.Cards = cards
		} else {
			slog.Warn("not found: suggesting cards", "error", err)
		}
	}
	app.renderError(w, status, page)
}

// slugStopWords are left out of suggestion searches: they match everything.
var slugStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "you": true, "your": true,
	"from": true, "this": true, "that": true, "are": true, "now": true, "new": true,
	"how": true, "what": true, "why": true, "its": true, "into": true, "off": true,
	"card": true, "cards": true, "credit": true,
}

// slugWords turns a slug into up to six search words. Article slugs always
// end in a six-character random suffix (article.GenerateSlug), which is
// dropped.
func slugWords(slug string, dropSuffix bool) []string {
	parts := strings.Split(strings.ToLower(slug), "-")
	if dropSuffix && len(parts) > 1 && len(parts[len(parts)-1]) == 6 {
		parts = parts[:len(parts)-1]
	}
	var words []string
	seen := map[string]bool{}
	for _, p := range parts {
		w := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, p)
		if len(w) < 3 || slugStopWords[w] || seen[w] {
			continue
		}
		seen[w] = true
		words = append(words, w)
		if len(words) == 6 {
			break
		}
	}
	return words
}

// ftsAnyPrefix builds an FTS5 query matching any of the words as a prefix.
// The words are [a-z0-9] only (slugWords), so quoting is enough.
func ftsAnyPrefix(words []string) string {
	terms := make([]string, len(words))
	for i, w := range words {
		terms[i] = `"` + w + `"*`
	}
	return strings.Join(terms, " OR ")
}
