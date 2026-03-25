package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"golang.org/x/crypto/bcrypt"
)

// setupTestApp creates a test App with a temp database and known password hash.
func setupTestApp(t *testing.T) *App {
	t.Helper()

	// Create a temp directory for the DB.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Generate a bcrypt hash for "testpassword123".
	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("failed to generate bcrypt hash: %v", err)
	}

	cfg := &config.Config{
		Admin: config.AdminConfig{
			PasswordHash: string(hash),
		},
		Server: config.ServerConfig{
			Port:   8080,
			Domain: "localhost",
		},
	}

	// Create template directory with test templates.
	templateDir := filepath.Join(tmpDir, "templates", "admin")
	if err := os.MkdirAll(templateDir, 0755); err != nil {
		t.Fatalf("failed to create template dir: %v", err)
	}

	// Write layout template.
	layoutContent := `{{define "layout"}}<!DOCTYPE html><html><body>{{template "content" .}}</body></html>{{end}}
{{define "csrf_field"}}<input type="hidden" name="csrf_token" value="{{.CSRFToken}}">{{end}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "templates", "layout.html"), []byte(layoutContent), 0644); err != nil {
		t.Fatalf("failed to write layout template: %v", err)
	}

	// Write login template.
	loginContent := `{{template "layout" .}}
{{define "content"}}<form method="POST" action="/admin/login">{{template "csrf_field" .}}{{if .Error}}<p class="error">{{.Error}}</p>{{end}}<input type="password" name="password"><button type="submit">Login</button></form>{{end}}`
	if err := os.WriteFile(filepath.Join(templateDir, "login.html"), []byte(loginContent), 0644); err != nil {
		t.Fatalf("failed to write login template: %v", err)
	}

	app, err := NewAppWithTemplateDir(cfg, database, nil, nil, nil, filepath.Join(tmpDir, "templates"))
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}

	return app
}

func TestLogin_Success(t *testing.T) {
	app := setupTestApp(t)

	// First, do a GET to /admin/login to obtain a CSRF token cookie.
	getReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	getRec := httptest.NewRecorder()
	app.ServeHTTP(getRec, getReq)

	// Extract the CSRF token from the Set-Cookie header.
	var csrfToken string
	for _, cookie := range getRec.Result().Cookies() {
		if cookie.Name == "csrf_token" {
			csrfToken = cookie.Value
			break
		}
	}
	if csrfToken == "" {
		t.Fatal("no csrf_token cookie set on GET /admin/login")
	}

	// POST /admin/login with correct password.
	form := url.Values{}
	form.Set("password", "testpassword123")
	form.Set("csrf_token", csrfToken)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrfToken})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	resp := rec.Result()

	// Should redirect with 303.
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location != "/admin/dashboard" {
		t.Errorf("expected redirect to /admin/dashboard, got %s", location)
	}

	// Should have a session cookie.
	var sessionCookie *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "session" {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie set after successful login")
	}
	if !sessionCookie.HttpOnly {
		t.Error("session cookie should be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie should have SameSite=Strict")
	}
}

func TestLogin_BadPassword(t *testing.T) {
	app := setupTestApp(t)

	// GET to obtain CSRF token.
	getReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	getRec := httptest.NewRecorder()
	app.ServeHTTP(getRec, getReq)

	var csrfToken string
	for _, cookie := range getRec.Result().Cookies() {
		if cookie.Name == "csrf_token" {
			csrfToken = cookie.Value
			break
		}
	}

	// POST with wrong password.
	form := url.Values{}
	form.Set("password", "wrongpassword")
	form.Set("csrf_token", csrfToken)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrfToken})

	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	resp := rec.Result()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", resp.StatusCode)
	}
}

func TestAdminMiddleware_NoSession(t *testing.T) {
	app := setupTestApp(t)

	// GET /admin/dashboard without a session cookie.
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	resp := rec.Result()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("expected status 303, got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location != "/admin/login" {
		t.Errorf("expected redirect to /admin/login, got %s", location)
	}
}
