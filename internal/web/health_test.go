package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

func newHealthTestApp(t *testing.T, database *db.Database) *App {
	t.Helper()
	app := &App{
		db:        database,
		mux:       http.NewServeMux(),
		startedAt: time.Now(),
		templates: make(map[string]*template.Template),
	}
	app.mux.HandleFunc("GET /healthz", app.handleHealthz)
	return app
}

func TestHandleHealthz_Healthy(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	app := newHealthTestApp(t, database)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	app.handleHealthz(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	contentType := rr.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", contentType)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", resp["status"])
	}
	if resp["db"] != "ok" {
		t.Errorf("expected db=ok, got %q", resp["db"])
	}
	if resp["uptime"] == "" {
		t.Error("expected non-empty uptime")
	}
}

func TestHandleHealthz_Degraded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	// Close the DB before making the request so the query fails.
	database.Close()

	app := newHealthTestApp(t, database)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	app.handleHealthz(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rr.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["status"] != "degraded" {
		t.Errorf("expected status=degraded, got %q", resp["status"])
	}
	if resp["db"] != "error" {
		t.Errorf("expected db=error, got %q", resp["db"])
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}
