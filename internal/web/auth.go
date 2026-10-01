package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// handleLoginPage renders the admin login form.
func (app *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title":     "Login",
		"CSRFToken": getCSRFToken(w, r),
		"Next":      safeAdminNext(r.URL.Query().Get("next")),
		"Expired":   r.URL.Query().Get("expired") == "1",
		"Noindex":   true,
	}
	app.renderTemplate(w, "login.html", data)
}

// loginRedirect is where an unauthenticated admin request is sent: the login
// page, told to come back to the page the admin was on. For a GET that is
// the page itself. A POST (a form sent after the session expired) cannot be
// replayed, so it returns to the page the form was on, taken from the
// Referer when that is this site's admin, and the page says the session
// expired; text typed into the form is kept by the browser (admin.js).
func loginRedirect(r *http.Request) string {
	var next string
	expired := false
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		next = safeAdminNext(r.URL.RequestURI())
	} else {
		expired = true
		if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host {
			next = safeAdminNext(ref.RequestURI())
		}
	}
	q := url.Values{}
	if next != "" && next != "/admin/dashboard" {
		q.Set("next", next)
	}
	if expired {
		q.Set("expired", "1")
	}
	if len(q) == 0 {
		return "/admin/login"
	}
	return "/admin/login?" + q.Encode()
}

// safeAdminNext returns raw if it is a path within this site's admin, safe to
// redirect to after login, and "" otherwise. Only a plain "/admin/..." path
// (with an optional query) passes: no scheme or host, no "//" or backslash
// that a browser could read as another host, no "..", no control
// characters, and not the login or logout pages.
func safeAdminNext(raw string) string {
	if raw == "" || len(raw) > 2048 || !strings.HasPrefix(raw, "/admin/") {
		return ""
	}
	if strings.ContainsAny(raw, "\\\r\n\t") || strings.Contains(raw, "//") || strings.Contains(raw, "..") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	switch strings.TrimSuffix(u.Path, "/") {
	case "/admin/login", "/admin/logout":
		return ""
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.Path, "//") {
		return ""
	}
	u.Fragment = ""
	return u.RequestURI()
}

// handleLoginSubmit processes the admin login form submission.
func (app *App) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")

	err := bcrypt.CompareHashAndPassword([]byte(app.cfg.Admin.PasswordHash), []byte(password))
	if err != nil {
		data := map[string]interface{}{
			"Title":     "Login",
			"Error":     "Invalid password",
			"CSRFToken": getCSRFToken(w, r),
			"Next":      safeAdminNext(r.FormValue("next")),
			"Noindex":   true,
		}
		app.renderStatus(w, http.StatusUnauthorized, "login.html", data)
		return
	}

	// Create session.
	token := generateToken()
	session := &db.AdminSession{
		Token:     token,
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}
	if err := app.db.CreateSession(session); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		Expires:  session.ExpiresAt,
	})

	dest := safeAdminNext(r.FormValue("next"))
	if dest == "" {
		dest = "/admin/dashboard"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handleLogout clears the session cookie and deletes the session from the DB.
func (app *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")
	if err == nil {
		// Delete session from DB by inserting an expired record is not ideal;
		// we'll delete via token. We need a DeleteSession method or use expired sessions cleanup.
		// For now, we just expire the cookie and rely on expiry checks.
		_ = app.db.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// getCSRFToken retrieves the CSRF token from the cookie or generates a new one.
func getCSRFToken(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("csrf_token")
	if err == nil {
		return cookie.Value
	}
	// If no cookie set, check response cookies (set by middleware).
	// This is a fallback; normally the middleware sets it.
	token := generateToken()
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}
