package web

import (
	"html/template"
	"net/http"
	"os"
	"path/filepath"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// App is the main web application struct that holds dependencies and serves HTTP.
type App struct {
	db        *db.Database
	cfg       *config.Config
	templates *template.Template
	mux       *http.ServeMux
}

// NewApp creates a new App, loads templates, and sets up routes.
func NewApp(cfg *config.Config, database *db.Database) (*App, error) {
	app := &App{
		db:  database,
		cfg: cfg,
		mux: http.NewServeMux(),
	}

	if err := app.loadTemplates("templates"); err != nil {
		return nil, err
	}

	app.setupRoutes()
	return app, nil
}

// newAppWithTemplateDir creates a new App loading templates from the specified directory.
func newAppWithTemplateDir(cfg *config.Config, database *db.Database, templateDir string) (*App, error) {
	app := &App{
		db:  database,
		cfg: cfg,
		mux: http.NewServeMux(),
	}

	if err := app.loadTemplates(templateDir); err != nil {
		return nil, err
	}

	app.setupRoutes()
	return app, nil
}

func (app *App) loadTemplates(dir string) error {
	// Collect all template files.
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".html" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(files) == 0 {
		// No templates found; create an empty template set.
		app.templates = template.New("")
		return nil
	}

	t, err := template.ParseFiles(files...)
	if err != nil {
		return err
	}
	app.templates = t
	return nil
}

// ServeHTTP delegates to the internal mux.
func (app *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	app.mux.ServeHTTP(w, r)
}
