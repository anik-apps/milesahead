package web

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/vote"
)

// templateFuncs provides custom template functions.
var templateFuncs = template.FuncMap{
	"safeHTML": func(s string) template.HTML {
		return template.HTML(s)
	},
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

// App is the main web application struct that holds dependencies and serves HTTP.
type App struct {
	db             *db.Database
	cfg            *config.Config
	articleManager *article.Manager
	voteSvc        *vote.Service
	newsletterSvc  NewsletterService
	templates      map[string]*template.Template
	mux            *http.ServeMux
	startedAt      time.Time
}

// NewApp creates a new App, loads templates, and sets up routes.
func NewApp(cfg *config.Config, database *db.Database, mgr *article.Manager, voteSvc *vote.Service, newsletterSvc NewsletterService) (*App, error) {
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
	}

	if err := app.loadTemplates("templates"); err != nil {
		return nil, err
	}

	app.setupRoutes()
	return app, nil
}

// NewAppWithTemplateDir creates a new App loading templates from the specified directory.
func NewAppWithTemplateDir(cfg *config.Config, database *db.Database, mgr *article.Manager, voteSvc *vote.Service, newsletterSvc NewsletterService, templateDir string) (*App, error) {
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
	}

	if err := app.loadTemplates(templateDir); err != nil {
		return nil, err
	}

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
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// ServeHTTP delegates to the internal mux, wrapped with metrics middleware.
func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	metricsMiddleware(app.mux).ServeHTTP(w, r)
}
