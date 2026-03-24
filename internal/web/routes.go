package web

import (
	"fmt"
	"net/http"
	"time"
)

func (app *App) setupRoutes() {
	// Static files.
	app.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Public routes (placeholders).
	app.mux.HandleFunc("GET /{$}", app.handleHome)
	app.mux.HandleFunc("GET /article/{slug}", app.handleArticle)
	app.mux.HandleFunc("GET /feed.xml", app.handleFeed)
	app.mux.HandleFunc("POST /subscribe", app.handleSubscribe)
	app.mux.HandleFunc("GET /subscribe/confirm", app.handleSubscribeConfirm)

	// Auth routes (with rate limiting on login POST).
	app.mux.HandleFunc("GET /admin/login", app.handleLoginPage)
	loginHandler := rateLimitMiddleware(
		http.HandlerFunc(app.handleLoginSubmit), 10, 15*time.Minute,
	)
	app.mux.Handle("POST /admin/login", loginHandler)
	app.mux.HandleFunc("POST /admin/logout", app.handleLogout)

	// Admin routes (behind auth middleware).
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /admin/dashboard", app.handleAdminDashboard)
	adminMux.HandleFunc("GET /admin/published", app.handleAdminPublished)
	adminMux.HandleFunc("GET /admin/sources", app.handleAdminSources)
	adminMux.HandleFunc("GET /admin/subscribers", app.handleAdminSubscribers)
	adminMux.HandleFunc("GET /admin/newsletter", app.handleAdminNewsletter)
	adminMux.HandleFunc("GET /admin/settings", app.handleAdminSettings)
	app.mux.Handle("/admin/", app.adminAuthMiddleware(adminMux))

	// API routes.
	app.mux.HandleFunc("POST /api/vote", app.handleVote)
}

// --- Placeholder handlers ---

func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Miles Ahead - Home")
}

func (app *App) handleArticle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	fmt.Fprintf(w, "Article: %s", slug)
}

func (app *App) handleFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"></rss>`)
}

func (app *App) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Subscribed")
}

func (app *App) handleSubscribeConfirm(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Confirmed")
}

func (app *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Dashboard")
}

func (app *App) handleAdminPublished(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Published")
}

func (app *App) handleAdminSources(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Sources")
}

func (app *App) handleAdminSubscribers(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Subscribers")
}

func (app *App) handleAdminNewsletter(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Newsletter")
}

func (app *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "Admin Settings")
}

func (app *App) handleVote(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}
