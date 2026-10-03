package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/vote"
)

// setupRealTemplateApp builds an App against the repository's actual
// templates, with ai.enrichment_infographics set to mode.
//
// Stubs cannot answer what these tests ask -- where the chart lands on the
// page and in what form -- because that is a property of the shipped markup.
func setupRealTemplateApp(t *testing.T, mode string) *App {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfg := &config.Config{
		Admin:  config.AdminConfig{PasswordHash: string(hash)},
		Server: config.ServerConfig{Port: 8080, Domain: "localhost"},
		Votes:  config.VotesConfig{AlertThreshold: 5},
		AI:     config.AIConfig{EnrichmentInfographics: mode},
		Site:   config.SiteConfig{PersonaName: "Scout"},
	}

	app, err := NewAppWithTemplateDir(cfg, database, article.NewManager(database, nil),
		vote.NewService(database, cfg.Votes, nil), nil, nil, context.Background(), "../../templates")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	t.Cleanup(app.WaitBackground)
	return app
}

// createInfographicArticle creates an article in status carrying svgMarkup,
// with a summary, key details and a photo so the page has its full layout.
func createInfographicArticle(t *testing.T, app *App, slug, status, svgMarkup string) int64 {
	t.Helper()
	id, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Amex Platinum fee math " + slug, Slug: slug, Content: "<p>Body.</p>",
		Type: "analysis", Status: status, SourcesJSON: "[]", RevisionNumber: 1,
		TLDR:       "The credits cover the fee if you use them.",
		KeyDetails: `[{"Label":"Annual fee","Value":"$895"}]`,
		ImageURL:   "https://images.pexels.com/photos/1/p.jpeg",
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	if _, err := app.db.DB().Exec(
		`UPDATE generated_articles SET infographic_svg = ?, published_at = ? WHERE id = ?`,
		svgMarkup, time.Now(), id); err != nil {
		t.Fatalf("set infographic: %v", err)
	}
	return id
}

func get(t *testing.T, app *App, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}

// With infographics on, the article page shows the chart as an image of a
// data: URL -- never as inline SVG -- at the top of the story: after the
// headline, summary and photo, before the body.
func TestInfographic_ArticlePageShowsAnImageAtTheTopOfTheStory(t *testing.T) {
	app := setupRealTemplateApp(t, config.InfographicsOn)
	createInfographicArticle(t, app, "amex-fee-math", db.StatusApproved, sanitized)

	body := get(t, app, "/article/amex-fee-math")
	fig := strings.Index(body, `<figure class="article-infographic">`)
	if fig == -1 {
		t.Fatal("no infographic figure on the article page")
	}
	// html/template writes the + of the MIME type as &#43;, which the browser
	// reads back as +.
	if !strings.Contains(body, `<img src="data:image/svg&#43;xml;base64,`) {
		t.Error("the chart is not an <img> of a base64 data: URL")
	}
	if !strings.Contains(body, `alt="Fee vs value"`) || !strings.Contains(body, `width="560" height="360"`) {
		t.Error("the chart image lost its alt text or size")
	}
	// The chart's own markup must not be on the page: inline, it would run
	// in the site's origin.
	if strings.Contains(body, "<title>Fee vs value</title>") || strings.Contains(body, `role="img"><title>`) {
		t.Error("the chart's SVG was inlined into the page")
	}
	for _, before := range []string{`class="article-standfirst"`, `class="article-hero"`, `class="article-sidebar"`} {
		if i := strings.Index(body, before); i == -1 || i > fig {
			t.Errorf("%s should come before the infographic", before)
		}
	}
	if i := strings.Index(body, `<div class="article-content">`); i == -1 || i < fig {
		t.Error("the infographic should come before the article body")
	}
}

// "off" and "preview" keep the chart off public pages, and the homepage never
// shows one: its cards are photo thumbnails, at which size a chart's labels
// are unreadable.
func TestInfographic_HiddenUnlessOnAndNeverOnTheHomepage(t *testing.T) {
	for _, mode := range []string{config.InfographicsOff, config.InfographicsPreview, config.InfographicsOn} {
		t.Run(mode, func(t *testing.T) {
			app := setupRealTemplateApp(t, mode)
			for _, slug := range []string{"featured-filler", "sidebar-filler", "amex-fee-math"} {
				createInfographicArticle(t, app, slug, db.StatusApproved, sanitized)
			}
			if strings.Contains(get(t, app, "/"), "article-infographic") {
				t.Error("the homepage shows an infographic")
			}
			shown := strings.Contains(get(t, app, "/article/amex-fee-math"), "article-infographic")
			if want := mode == config.InfographicsOn; shown != want {
				t.Errorf("article page shows the infographic = %v, want %v", shown, want)
			}
		})
	}
}

// A row that did not come from the sanitiser must not reach the page even
// though it is stored in the same column.
func TestInfographic_UnsanitizedRowIsNotRendered(t *testing.T) {
	app := setupRealTemplateApp(t, config.InfographicsOn)
	createInfographicArticle(t, app, "tampered", db.StatusApproved,
		`<svg onload="alert(1)"><script>alert(1)</script><text>x</text></svg>`)

	body := get(t, app, "/article/tampered")
	if strings.Contains(body, "onload=") || strings.Contains(body, "alert(1)") || strings.Contains(body, "article-infographic") {
		t.Fatal("unsanitised markup reached the article page")
	}
}

// The sentinel means "asked, nothing to chart": no figure, not the word.
func TestInfographic_SentinelRendersNothing(t *testing.T) {
	app := setupRealTemplateApp(t, config.InfographicsOn)
	createInfographicArticle(t, app, "no-numbers", db.StatusApproved, db.NoInfographic)

	if body := get(t, app, "/article/no-numbers"); strings.Contains(body, "article-infographic") {
		t.Error("the sentinel rendered an infographic figure")
	}
}

// The admin preview shows the chart before it is published -- that is what
// "preview" is for -- and says when the site will not show it, or why there
// is none.
func TestInfographic_AdminPreview(t *testing.T) {
	cases := []struct {
		mode, stored string
		want, absent []string
	}{
		{config.InfographicsPreview, sanitized,
			[]string{`<figure class="article-infographic">`, `<img src="data:image/svg&#43;xml;base64,`, `Not shown on the site: ai.enrichment_infographics is not &ldquo;on&rdquo;.`},
			[]string{"No infographic"}},
		{config.InfographicsOn, sanitized,
			[]string{`<figure class="article-infographic">`},
			[]string{"Not shown on the site", "No infographic"}},
		{config.InfographicsOn, db.NoInfographic,
			[]string{"No infographic: enrichment found nothing in the key details to chart."},
			[]string{"article-infographic"}},
		{config.InfographicsPreview, "",
			[]string{"No infographic yet."},
			[]string{"article-infographic"}},
		{config.InfographicsOff, "",
			nil,
			[]string{"article-infographic", "No infographic"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode+"/"+tc.stored[:min(len(tc.stored), 8)], func(t *testing.T) {
			app := setupRealTemplateApp(t, tc.mode)
			createInfographicArticle(t, app, "pending-chart", db.StatusPending, tc.stored)
			session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
			rec := doAdminGET(t, app, "/admin/dashboard", session)
			if rec.Code != http.StatusOK {
				t.Fatalf("dashboard status %d", rec.Code)
			}
			body := rec.Body.String()
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("preview missing %q", w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(body, a) {
					t.Errorf("preview should not contain %q", a)
				}
			}
		})
	}
}
