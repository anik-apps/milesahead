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
	app.mux.Handle("GET /static/", staticHandler(http.StripPrefix("/static/", http.FileServer(http.Dir("static")))))

	// SEO routes.
	app.mux.HandleFunc("GET /sitemap.xml", app.handleSitemap)
	app.mux.HandleFunc("GET /robots.txt", app.handleRobots)

	// Public routes (placeholders).
	app.mux.HandleFunc("GET /{$}", app.handleHome)
	app.mux.HandleFunc("GET /article/{slug}", app.handleArticle)
	app.mux.HandleFunc("GET /feed.xml", app.handleFeed)

	// Newsletter sign-up, confirmation and unsubscribe (subscribe.go).
	app.mux.HandleFunc("GET /subscribe", app.handleSubscribePage)
	app.mux.HandleFunc("POST /subscribe", app.handleSubscribe)
	app.mux.HandleFunc("GET /subscribe/check-email", app.handleCheckEmail)
	app.mux.HandleFunc("GET /subscribe/confirm", app.handleSubscribeConfirm)
	app.mux.HandleFunc("GET /unsubscribe", app.handleUnsubscribePage)
	app.mux.HandleFunc("POST /unsubscribe", app.handleUnsubscribe)
	app.mux.HandleFunc("POST /resubscribe", app.handleResubscribe)

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
	adminMux.HandleFunc("GET /admin/dashboard/state", app.handleDashboardState)
	adminMux.HandleFunc("GET /admin/published", app.handleAdminPublished)
	adminMux.HandleFunc("GET /admin/sources", app.handleAdminSources)
	adminMux.HandleFunc("GET /admin/subscribers", app.handleAdminSubscribers)
	adminMux.HandleFunc("GET /admin/subscribers/export", app.handleExportSubscribers)
	adminMux.HandleFunc("GET /admin/newsletter", app.handleAdminNewsletter)
	adminMux.HandleFunc("GET /admin/settings", app.handleAdminSettings)

	// Article action routes.
	adminMux.HandleFunc("GET /admin/articles/{id}/cards", app.handleAdminArticleCardsGET)
	adminMux.HandleFunc("POST /admin/articles/{id}/cards", app.handleAdminArticleCardsPOST)
	adminMux.HandleFunc("POST /admin/articles/{id}/approve", app.handleApproveArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/undo-approve", app.handleUndoApprove)
	adminMux.HandleFunc("POST /admin/articles/{id}/reject", app.handleRejectArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/revise", app.handleRequestRevision)
	adminMux.HandleFunc("POST /admin/articles/{id}/unpublish", app.handleUnpublishArticle)
	adminMux.HandleFunc("POST /admin/articles/{id}/unpublish-revise", app.handleUnpublishAndRevise)
	adminMux.HandleFunc("POST /admin/articles/{id}/new-image", app.handleRequestNewImage)

	// Vote alerts, listed on the Published page.
	adminMux.HandleFunc("POST /admin/vote-alerts/{id}/acknowledge", app.handleAcknowledgeVoteAlert)
	adminMux.HandleFunc("POST /admin/vote-alerts/acknowledge-all", app.handleAcknowledgeAllVoteAlerts)

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
	adminMux.HandleFunc("POST /admin/cards/{id}/flag", app.handleFlagField)
	adminMux.HandleFunc("POST /admin/cards/{id}/resolve-flag", app.handleResolveFlag)
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

	// Everything else: the branded 404 page.
	app.mux.HandleFunc("/", app.handleNotFound)
}

// staticHandler sets Cache-Control on static files. A URL carrying ?v= names
// its content (versionedAsset, the infographic versions), so it can be
// cached for a year; anything else may change under the same URL and is
// kept for an hour. Errors (a missing file) are not marked cacheable.
func staticHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := "public, max-age=3600"
		if r.URL.Query().Get("v") != "" {
			policy = "public, max-age=31536000, immutable"
		}
		next.ServeHTTP(&cacheControlWriter{ResponseWriter: w, policy: policy}, r)
	})
}

// cacheControlWriter adds a Cache-Control header to successful responses.
type cacheControlWriter struct {
	http.ResponseWriter
	policy      string
	wroteHeader bool
}

func (c *cacheControlWriter) WriteHeader(status int) {
	if !c.wroteHeader {
		c.wroteHeader = true
		if status == http.StatusOK || status == http.StatusNotModified || status == http.StatusPartialContent {
			c.Header().Set("Cache-Control", c.policy)
		}
	}
	c.ResponseWriter.WriteHeader(status)
}

func (c *cacheControlWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}
