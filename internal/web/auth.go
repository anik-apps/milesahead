package web

import (
	"net/http"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// handleLoginPage renders the admin login form.
func (app *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title":     "Login",
		"CSRFToken": getCSRFToken(w, r),
	}
	app.renderTemplate(w, "login.html", data)
}

// handleLoginSubmit processes the admin login form submission.
func (app *App) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")

	err := bcrypt.CompareHashAndPassword([]byte(app.cfg.Admin.PasswordHash), []byte(password))
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		data := map[string]interface{}{
			"Title":     "Login",
			"Error":     "Invalid password",
			"CSRFToken": getCSRFToken(w, r),
		}
		app.renderTemplate(w, "login.html", data)
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

	http.Redirect(w, r, "/admin/dashboard", http.StatusSeeOther)
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
