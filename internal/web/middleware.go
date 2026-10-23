package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// csrfMiddleware generates CSRF tokens on GET and validates them on POST.
// Tokens are stored in a session-like cookie called "csrf_token".
func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			// Only set a CSRF cookie if one doesn't already exist.
			if _, err := r.Cookie("csrf_token"); err != nil {
				token := generateToken()
				http.SetCookie(w, &http.Cookie{
					Name:     "csrf_token",
					Value:    token,
					Path:     "/",
					HttpOnly: false,
					SameSite: http.SameSiteStrictMode,
				})
			}
			next.ServeHTTP(w, r)
			return
		}

		if r.Method == http.MethodPost {
			cookie, err := r.Cookie("csrf_token")
			if err != nil {
				http.Error(w, "CSRF token missing", http.StatusForbidden)
				return
			}
			formToken := r.FormValue("csrf_token")
			if formToken == "" || formToken != cookie.Value {
				http.Error(w, "CSRF token invalid", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// adminAuthMiddleware checks for a valid session cookie and redirects to login if missing.
// The login page is told where to return (see loginRedirect), so an expired
// session does not drop the admin back on the dashboard.
func (app *App) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil {
			http.Redirect(w, r, loginRedirect(r), http.StatusSeeOther)
			return
		}

		session, err := app.db.GetSession(cookie.Value)
		if err != nil || session == nil {
			http.Redirect(w, r, loginRedirect(r), http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimiter tracks login attempts by IP.
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

func newRateLimiter(maxAttempts int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		attempts: make(map[string][]time.Time),
		max:      maxAttempts,
		window:   window,
	}
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Remove expired attempts.
	valid := rl.attempts[ip][:0]
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	rl.attempts[ip] = valid
	if len(valid) == 0 {
		// Forget addresses with nothing in the window, so the map does not
		// grow with every visitor who ever tried.
		delete(rl.attempts, ip)
	}

	if len(rl.attempts[ip]) >= rl.max {
		return false
	}

	rl.attempts[ip] = append(rl.attempts[ip], now)
	return true
}

// rateLimitMiddleware wraps a handler with IP-based rate limiting.
func rateLimitMiddleware(next http.Handler, maxAttempts int, window time.Duration) http.Handler {
	rl := newRateLimiter(maxAttempts, window)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if !rl.allow(ip) {
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP returns the client IP from the request, stripping the port.
func extractIP(r *http.Request) string {
	// Check X-Forwarded-For first (use leftmost IP).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i != -1 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// normalizePath maps dynamic URL segments to static metric labels to prevent
// cardinality explosion in Prometheus.
func normalizePath(p string) string {
	// Static files collapsed.
	if strings.HasPrefix(p, "/static") {
		return "/static"
	}

	// Known static paths — pass through as-is.
	switch p {
	case "/", "/cards/", "/search", "/healthz", "/metrics",
		"/subscribe", "/subscribe/confirm", "/subscribe/check-email", "/unsubscribe", "/resubscribe",
		"/api/vote", "/feed.xml", "/robots.txt", "/sitemap.xml",
		"/admin/login", "/admin/logout",
		"/admin/dashboard", "/admin/published", "/admin/sources",
		"/admin/subscribers", "/admin/subscribers/export",
		"/admin/newsletter", "/admin/newsletter/send", "/admin/newsletter/history",
		"/admin/settings", "/admin/models", "/admin/models/test-alert", "/admin/cards", "/admin/cards/new",
		"/admin/sources/add":
		return p
	}

	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	// parts[0] is always "" for paths starting with /

	// /article/{slug}
	if len(parts) == 3 && parts[1] == "article" {
		return "/article/:slug"
	}

	// /cards/{slug} (but not /cards/ which is handled above)
	if len(parts) == 3 && parts[1] == "cards" && parts[2] != "" {
		return "/cards/:slug"
	}

	// /admin/cards/{id}/updates/{uid}/{action}
	if len(parts) == 7 && parts[1] == "admin" && parts[2] == "cards" && parts[4] == "updates" {
		return "/admin/cards/:id/updates/:uid/" + parts[6]
	}

	// /admin/cards/{id}/updates
	if len(parts) == 5 && parts[1] == "admin" && parts[2] == "cards" && parts[4] == "updates" {
		return "/admin/cards/:id/updates"
	}

	// /admin/articles/{id}/{action}
	if len(parts) == 5 && parts[1] == "admin" && parts[2] == "articles" {
		return "/admin/articles/:id/" + parts[4]
	}

	// /admin/cards/{id}/{action}
	if len(parts) == 5 && parts[1] == "admin" && parts[2] == "cards" {
		return "/admin/cards/:id/" + parts[4]
	}

	// /admin/sources/{id}/{action}
	if len(parts) == 5 && parts[1] == "admin" && parts[2] == "sources" {
		return "/admin/sources/:id/" + parts[4]
	}

	return "/_other"
}

// metricsMiddleware records HTTP request counts and durations.
func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		wrapped := &responseRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(wrapped, r)
		duration := time.Since(start).Seconds()
		path := normalizePath(r.URL.Path)
		metrics.HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(wrapped.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(r.Method, path).Observe(duration)
		if path != "/static" {
			metrics.ResponseBytes.WithLabelValues(r.Method, path).Observe(float64(wrapped.bytes))
		}
		trackUniqueVisitor(extractIP(r), path)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

var seenIPs sync.Map

func init() {
	go func() {
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			time.Sleep(time.Until(next))
			seenIPs = sync.Map{}
		}
	}()
}

func trackUniqueVisitor(ip, normalizedPath string) {
	if strings.HasPrefix(normalizedPath, "/admin") || normalizedPath == "/metrics" || normalizedPath == "/healthz" || normalizedPath == "/static" {
		return
	}
	h := sha256.Sum256([]byte(ip))
	key := hex.EncodeToString(h[:8])
	if _, loaded := seenIPs.LoadOrStore(key, struct{}{}); !loaded {
		metrics.DailyUniqueVisitors.Inc()
	}
}

// generateToken creates a cryptographically random hex token.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random token: " + err.Error())
	}
	return hex.EncodeToString(b)
}
