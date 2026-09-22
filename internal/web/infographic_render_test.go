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
// templates rather than the stubs the other public tests use.
//
// The stubs cannot answer the question these tests ask. Whether an inline SVG
// survives to the page unescaped is a property of the shipped markup -- of
// home.html writing {{.Infographic}} and not {{.Infographic | printf "%s"}} --
// so a stub that hardcodes its own <div> would pass no matter what the real
// template did.
func setupRealTemplateApp(t *testing.T) *App {
	t.Helper()

	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cfg := &config.Config{
		Admin:  config.AdminConfig{PasswordHash: string(hash)},
		Server: config.ServerConfig{Port: 8080, Domain: "localhost"},
		Votes:  config.VotesConfig{AlertThreshold: 5},
	}

	app, err := NewAppWithTemplateDir(cfg, database, article.NewManager(database, nil),
		vote.NewService(database, cfg.Votes, nil), nil, nil, context.Background(), "../../templates")
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	t.Cleanup(app.WaitBackground)
	return app
}

// publishInfographicArticle creates an approved article carrying svgMarkup.
func publishInfographicArticle(t *testing.T, app *App, slug, svgMarkup string) {
	t.Helper()
	id, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Amex Platinum fee math", Slug: slug, Content: "<p>Body.</p>",
		Type: "analysis", Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	now := time.Now()
	if _, err := app.db.DB().Exec(
		`UPDATE generated_articles SET infographic_svg = ?, published_at = ? WHERE id = ?`,
		svgMarkup, now, id); err != nil {
		t.Fatalf("set infographic: %v", err)
	}
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

// The whole point of an inline SVG is that it is not escaped, so this asserts
// the one thing a template mistake would silently break: real tags, not
// &lt;svg&gt; printed as text.
func TestInfographic_RendersInlineOnArticleAndHome(t *testing.T) {
	app := setupRealTemplateApp(t)
	// The featured slot is a photographic hero with white text overlaid on a
	// CSS background-image, so an inline chart cannot go there and is not
	// meant to. Publishing several articles puts one in that slot and leaves
	// the rest in the sidebar and grid cards, which are the surfaces under
	// test.
	publishInfographicArticle(t, app, "featured-filler", sanitized)
	publishInfographicArticle(t, app, "sidebar-filler", sanitized)
	publishInfographicArticle(t, app, "amex-fee-math", sanitized)

	for _, path := range []string{"/", "/article/amex-fee-math"} {
		t.Run(path, func(t *testing.T) {
			body := get(t, app, path)
			if strings.Contains(body, "&lt;svg") {
				t.Fatalf("SVG was escaped into visible text on %s", path)
			}
			if !strings.Contains(body, `class="infographic-svg"`) {
				t.Fatalf("no inline SVG on %s", path)
			}
			if !strings.Contains(body, "Fee vs value") {
				t.Errorf("accessible name missing on %s", path)
			}
		})
	}
}

// A row that did not come from the sanitiser must not reach the page even
// though it is stored in the same column.
func TestInfographic_UnsanitizedRowIsNotRendered(t *testing.T) {
	app := setupRealTemplateApp(t)
	tampered := `<svg onload="alert(1)"><script>alert(1)</script><text>x</text></svg>`
	publishInfographicArticle(t, app, "featured-filler", tampered)
	publishInfographicArticle(t, app, "sidebar-filler", tampered)
	publishInfographicArticle(t, app, "tampered", tampered)

	for _, path := range []string{"/", "/article/tampered"} {
		body := get(t, app, path)
		if strings.Contains(body, "onload=") || strings.Contains(body, "<script>alert") {
			t.Fatalf("unsanitised markup was inlined on %s", path)
		}
	}
}

// The sentinel means "asked, nothing to chart" -- it must render as no
// infographic at all, not as the literal word.
func TestInfographic_SentinelRendersNothing(t *testing.T) {
	app := setupRealTemplateApp(t)
	publishInfographicArticle(t, app, "no-numbers", db.NoInfographic)

	body := get(t, app, "/article/no-numbers")
	if strings.Contains(body, `class="infographic"`) {
		t.Error("sentinel rendered an empty infographic figure")
	}
}
