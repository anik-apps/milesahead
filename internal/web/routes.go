package web

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func (app *App) setupRoutes() {
	// Health check endpoint.
	app.mux.HandleFunc("GET /healthz", app.handleHealthz)

	// Metrics endpoint (no auth, scraped by Alloy).
	app.mux.Handle("GET /metrics", promhttp.Handler())

	// Static files.
	app.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// SEO routes.
	app.mux.HandleFunc("GET /sitemap.xml", app.handleSitemap)
	app.mux.HandleFunc("GET /robots.txt", app.handleRobots)

	// Public routes (placeholders).
	app.mux.HandleFunc("GET /{$}", app.handleHome)
	app.mux.HandleFunc("GET /article/{slug}", app.handleArticle)
	app.mux.HandleFunc("GET /feed.xml", app.handleFeed)
	app.mux.HandleFunc("POST /subscribe", app.handleSubscribe)
	app.mux.HandleFunc("GET /subscribe/confirm", app.handleSubscribeConfirm)
	app.mux.HandleFunc("GET /unsubscribe", app.handleUnsubscribe)

	// Search route.
	app.mux.HandleFunc("GET /search", app.handleSearch)

	// Public card routes.
	app.mux.HandleFunc("GET /cards/", app.handleCardsList)
	app.mux.HandleFunc("GET /cards/{slug}", app.handleCardDetail)

	// Compare route.
	app.mux.HandleFunc("GET /compare", app.handleCompare)

	// Auth routes (with rate limiting on login POST).
	app.mux.HandleFunc("GET /admin/login", app.handleLoginPage)
	loginHandler := csrfMiddleware(rateLimitMiddleware(
		http.HandlerFunc(app.handleLoginSubmit), 10, 15*time.Minute,
	))
	app.mux.Handle("POST /admin/login", loginHandler)
	app.mux.HandleFunc("POST /admin/logout", app.handleLogout)

	// Admin routes (behind auth + CSRF middleware).
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /admin/dashboard", app.handleAdminDashboard)
	adminMux.HandleFunc("GET /admin/published", app.handleAdminPublished)
	adminMux.HandleFunc("GET /admin/sources", app.handleAdminSources)
	adminMux.HandleFunc("GET /admin/subscribers", app.handleAdminSubscribers)
	adminMux.HandleFunc("GET /admin/subscribers/export", app.handleExportSubscribers)
	adminMux.HandleFunc("GET /admin/newsletter", app.handleAdminNewsletter)
	adminMux.HandleFunc("GET /admin/settings", app.handleAdminSettings)

	// Article action routes.
	adminMux.HandleFunc("POST /admin/articles/{id}/approve", app.handleApproveArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/reject", app.handleRejectArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/revise", app.handleRequestRevision)
	adminMux.HandleFunc("POST /admin/articles/{id}/unpublish", app.handleUnpublishArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/unpublish-revise", app.handleUnpublishAndRevise)

	// Newsletter action routes.
	adminMux.HandleFunc("POST /admin/newsletter/send", app.handleNewsletterSend)
	adminMux.HandleFunc("POST /admin/newsletter/feature", app.handleNewsletterFeature)
	adminMux.HandleFunc("POST /admin/newsletter/card-override", app.handleNewsletterCardOverride)
	adminMux.HandleFunc("POST /admin/newsletter/status", app.handleNewsletterStatus)
	adminMux.HandleFunc("GET /admin/newsletter/history", app.handleNewsletterHistory)

	// Card admin routes.
	adminMux.HandleFunc("GET /admin/cards", app.handleAdminCards)
	adminMux.HandleFunc("GET /admin/cards/new", app.handleAdminCardNew)
	adminMux.HandleFunc("POST /admin/cards", app.handleAdminCardCreate)
	adminMux.HandleFunc("GET /admin/cards/{id}/edit", app.handleAdminCardEdit)
	adminMux.HandleFunc("POST /admin/cards/{id}/edit", app.handleAdminCardUpdate)
	adminMux.HandleFunc("POST /admin/cards/{id}/delete", app.handleAdminCardDelete)
	adminMux.HandleFunc("GET /admin/cards/{id}/updates", app.handleAdminCardUpdates)
	adminMux.HandleFunc("POST /admin/cards/{id}/updates/{uid}/approve", app.handleApproveCardUpdate)
	adminMux.HandleFunc("POST /admin/cards/{id}/updates/{uid}/reject", app.handleRejectCardUpdate)

	// Source action routes.
	adminMux.HandleFunc("POST /admin/sources/add", app.handleAddSource)
	adminMux.HandleFunc("POST /admin/sources/{id}/edit", app.handleEditSource)
	adminMux.HandleFunc("POST /admin/sources/{id}/toggle", app.handleToggleSource)

	app.mux.Handle("/admin/", csrfMiddleware(app.adminAuthMiddleware(adminMux)))

	// API routes.
	app.mux.HandleFunc("POST /api/vote", app.handleVoteAPI)
}

