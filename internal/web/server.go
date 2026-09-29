package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/offer"
	"github.com/kumaranik/milesahead/internal/vote"
)

// templateFuncs provides custom template functions.
var templateFuncs = template.FuncMap{
	"safeHTML": func(s string) template.HTML {
		return template.HTML(s)
	},
	"scrollTables": scrollTables,
	"endedLabel":   endedLabel,
	"deref": func(v interface{}) string {
		if v == nil {
			return ""
		}
		switch p := v.(type) {
		case *string:
			if p == nil {
				return ""
			}
			return *p
		case *int:
			if p == nil {
				return ""
			}
			return fmt.Sprintf("%d", *p)
		default:
			return fmt.Sprintf("%v", v)
		}
	},
}

// NewsletterService defines the interface for newsletter operations used by admin handlers.
type NewsletterService interface {
	PreviewNext() (string, error)
	SendDaily() error
}

// CardUpdateDetector detects whether a newly approved article affects any
// credit card reference pages and drafts pending updates.
type CardUpdateDetector interface {
	DetectAndDraft(ctx context.Context, articleID int64) error
}

// App is the main web application struct that holds dependencies and serves HTTP.
type App struct {
	db             *db.Database
	cfg            *config.Config
	articleManager *article.Manager
	voteSvc        *vote.Service
	newsletterSvc  NewsletterService
	cardDetector   CardUpdateDetector
	shutdownCtx    context.Context
	// bg tracks fire-and-forget background work started by handlers, so both
	// shutdown and tests can wait for it. Without it these goroutines outlive
	// the request AND their database: in tests they ran on after t.Cleanup had
	// closed the DB and removed the temp dir, producing "sql: database is
	// closed" and a flaky "directory not empty" cleanup failure.
	bg        sync.WaitGroup
	templates map[string]*template.Template
	mux       *http.ServeMux
	startedAt time.Time
	// infographicVersions is a content hash per card slug, appended to that
	// card's infographic URLs so a replaced image is not served from cache.
	infographicVersions map[string]string
	// infographicHeights is the pixel height of each card's full infographic,
	// read from the JPEG at startup, for the viewer's height attribute.
	infographicHeights map[string]int
	// now is the clock offers are judged against; tests replace it.
	now func() time.Time
}

// offerToday is the date offer end dates are compared against
// ("2006-01-02"); see offer.Today for the zone it is taken in.
func (app *App) offerToday() string {
	// Guarded so an App built without a constructor (a test fixture) still
	// works rather than panicking on a nil clock.
	now := time.Now
	if app.now != nil {
		now = app.now
	}
	return offer.Today(now())
}

// WaitBackground blocks until handler-started background work finishes.
// Used by graceful shutdown and by tests before their fixtures are torn down.
func (app *App) WaitBackground() {
	app.bg.Wait()
}

// NewApp creates a new App, loads templates, and sets up routes.
func NewApp(cfg *config.Config, database *db.Database, mgr *article.Manager, voteSvc *vote.Service, newsletterSvc NewsletterService, cardDetector CardUpdateDetector, shutdownCtx context.Context) (*App, error) {
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		cardDetector:   cardDetector,
		shutdownCtx:    shutdownCtx,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
		now:            time.Now,
	}

	if err := app.loadTemplates("templates"); err != nil {
		return nil, err
	}
	app.infographicVersions = infographicVersions("static")
	app.infographicHeights = infographicHeights("static")

	app.setupRoutes()
	return app, nil
}

// NewAppWithTemplateDir creates a new App loading templates from the specified directory.
func NewAppWithTemplateDir(cfg *config.Config, database *db.Database, mgr *article.Manager, voteSvc *vote.Service, newsletterSvc NewsletterService, cardDetector CardUpdateDetector, shutdownCtx context.Context, templateDir string) (*App, error) {
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		cardDetector:   cardDetector,
		shutdownCtx:    shutdownCtx,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
		now:            time.Now,
	}

	if err := app.loadTemplates(templateDir); err != nil {
		return nil, err
	}
	// static/ sits beside templates/, as it does in the repository and on the server.
	staticDir := filepath.Join(filepath.Dir(templateDir), "static")
	app.infographicVersions = infographicVersions(staticDir)
	app.infographicHeights = infographicHeights(staticDir)

	app.setupRoutes()
	return app, nil
}

func (app *App) loadTemplates(dir string) error {
	layoutFile := filepath.Join(dir, "layout.html")

	// Collect all page template files (excluding layout.html itself).
	var pageFiles []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".html" && filepath.Base(path) != "layout.html" {
			// Skip email templates — they are rendered by the newsletter service, not the web server.
			if strings.Contains(path, string(filepath.Separator)+"email"+string(filepath.Separator)) {
				return nil
			}
			pageFiles = append(pageFiles, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	app.templates = make(map[string]*template.Template)

	if len(pageFiles) == 0 {
		return nil
	}

	// Parse each page template separately with the layout so that each gets
	// its own "content" block definition without conflicts.
	for _, pf := range pageFiles {
		name := filepath.Base(pf)
		t, err := template.New(name).Funcs(templateFuncs).ParseFiles(layoutFile, pf)
		if err != nil {
			return fmt.Errorf("parsing template %s: %w", name, err)
		}
		app.templates[name] = t
	}
	return nil
}

// renderTemplate executes a named template with the given data.
func (app *App) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	t, ok := app.templates[name]
	if !ok {
		http.Error(w, "Template not found: "+name, http.StatusInternalServerError)
		return
	}
	if m, ok := data.(map[string]interface{}); ok {
		// Pages without their own image (home, compare, search) still get a
		// branded preview when shared. Crawlers need an absolute URL.
		if img, _ := m["OGImage"].(string); img == "" {
			m["OGImage"] = "https://" + app.siteDomain() + defaultOGImage
		}
	}
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// defaultOGImage is the share image for pages that have none of their own.
const defaultOGImage = "/static/img/brand/og-default.png"

// siteDomain is the public host name, falling back to localhost like the
// handlers do when none is configured.
func (app *App) siteDomain() string {
	if app.cfg.Server.Domain == "" {
		return "localhost"
	}
	return app.cfg.Server.Domain
}

// ServeHTTP delegates to the internal mux, wrapped with metrics middleware.
func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metricsMiddleware(app.mux).ServeHTTP(w, r)
}
