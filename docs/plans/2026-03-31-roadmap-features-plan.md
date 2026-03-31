# Roadmap Features Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement six features — graceful shutdown, health check, auto-linking, Scout's Take backfill, full-text search, and auto-update detection — to improve infrastructure reliability, content quality, and user experience.

**Architecture:** The application is a Go monolith with SQLite (WAL mode), a cron-based scheduler, an AI pipeline (HuggingFace/NVIDIA API), and server-rendered HTML templates. New features integrate at the `cmd/milesahead/main.go` orchestration layer, the `internal/db` persistence layer, and the `internal/web` HTTP layer. Features 1-2 are sequential (shutdown context is used by health check and later features); features 3-6 are independent.

**Tech Stack:** Go 1.26, SQLite with FTS5, `html/template`, `net/http` (stdlib router), `modernc.org/sqlite`, `robfig/cron/v3`

**Spec:** `docs/specs/2026-03-31-roadmap-features-design.md`

---

## Task 1: Graceful Shutdown

**Files:**
- Modify: `cmd/milesahead/main.go`
- Test: Manual (send SIGTERM, verify clean exit)

### Step 1.1: Add signal-handling imports

- [ ] Add `context`, `net/http`, `os/signal`, and `syscall` imports to `main.go`.

In `cmd/milesahead/main.go`, update the import block:

```go
import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/metrics"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/scanner"
	"github.com/kumaranik/milesahead/internal/scheduler"
	"github.com/kumaranik/milesahead/internal/vote"
	"github.com/kumaranik/milesahead/internal/web"
)
```

### Step 1.2: Create shutdown context at top of main()

- [ ] Right after `flag.Parse()` and config load, create a cancellable context and signal channel.

Insert after the `cfg` error check (after line 47 in current `main.go`):

```go
	// Shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
```

### Step 1.3: Replace http.ListenAndServe with explicit http.Server

- [ ] Replace the final `http.ListenAndServe` block with an explicit `http.Server` and a goroutine for shutdown.

Replace the current server block (lines 247-251):

```go
	slog.Info("server starting", "port", cfg.Server.Port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.Server.Port), app); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
```

With:

```go
	// Web server with explicit Server struct for graceful shutdown
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: app,
	}

	// Start server in a goroutine
	go func() {
		slog.Info("server starting", "port", cfg.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	sig := <-sigCh
	slog.Info("shutting down...", "signal", sig.String())
	cancel()

	// Stop scheduler (waits for running jobs)
	sched.Stop()

	// Shutdown HTTP server with 30s timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
		os.Exit(1)
	}

	slog.Info("shutdown complete")
```

### Step 1.4: Remove defer sched.Stop()

- [ ] Remove the existing `defer sched.Stop()` line (currently line 238) since we now call `sched.Stop()` explicitly in the shutdown sequence.

Delete:
```go
	defer sched.Stop()
```

### Step 1.5: Pass shutdown context to scan job closure

- [ ] The scan job closure already captures variables from the outer `main()` scope. The `ctx` variable created in Step 1.2 is available inside the closure. No code change needed — the context is captured by closure. However, add a `ctx.Done()` check at the top of the scan job to bail out early if shutdown is in progress.

At the beginning of the `sched.AddScanJob` closure (after the `slog.Info("scheduled job started"...)` line), add:

```go
		select {
		case <-ctx.Done():
			slog.Info("scan job skipped, shutting down")
			return
		default:
		}
```

### Step 1.6: Build and verify compilation

- [ ] Run `go build ./cmd/milesahead/` to verify the changes compile.

```bash
go build ./cmd/milesahead/
```

Expected: no errors.

### Step 1.7: Manual shutdown test

- [ ] Start the server, send SIGTERM, verify clean exit with "shutdown complete" log.

```bash
# Terminal 1:
go run ./cmd/milesahead/ -config config.yaml &
PID=$!
sleep 2
kill -TERM $PID
# Expected output includes: "shutting down..." and "shutdown complete"
```

### Step 1.8: Commit

- [ ] Stage and commit with message: `feat: add graceful shutdown with signal handling and 30s timeout`

---

## Task 2: Health Check Endpoint

**Files:**
- Modify: `internal/web/public.go`
- Modify: `internal/web/routes.go`
- Create: `internal/web/health_test.go`

### Step 2.1: Add startup time tracking to App

- [ ] Add a `startedAt` field to the `App` struct in `server.go` and set it in `NewApp`.

In `internal/web/server.go`, add to the `App` struct:

```go
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
```

In `NewApp`, set it:

```go
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
	}
```

Also update `NewAppWithTemplateDir` the same way:

```go
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
	}
```

### Step 2.2: Add the health check handler

- [ ] Add `handleHealthz` to `internal/web/public.go`.

Add at the end of `internal/web/public.go`:

```go
func (app *App) handleHealthz(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(app.startedAt).Round(time.Second).String()

	// Check DB connectivity
	var dbStatus string
	var dbErr string
	err := app.db.DB().QueryRow("SELECT 1").Err()
	if err != nil {
		dbStatus = "error"
		dbErr = err.Error()
	} else {
		dbStatus = "ok"
	}

	status := "ok"
	httpCode := http.StatusOK
	if dbStatus != "ok" {
		status = "degraded"
		httpCode = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)

	resp := map[string]string{
		"status": status,
		"db":     dbStatus,
		"uptime": uptime,
	}
	if dbErr != "" {
		resp["error"] = dbErr
	}

	json.NewEncoder(w).Encode(resp)
}
```

Add `"encoding/json"` and `"time"` to the import block in `public.go` if not already present. (`"encoding/json"` is already imported; `"time"` is not — add it.)

### Step 2.3: Register the route

- [ ] Add `GET /healthz` route in `internal/web/routes.go`, before the static files handler.

In `internal/web/routes.go`, add after the metrics endpoint:

```go
	// Health check endpoint.
	app.mux.HandleFunc("GET /healthz", app.handleHealthz)
```

### Step 2.4: Write the health check test

- [ ] Create `internal/web/health_test.go`.

```go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

func TestHandleHealthz_Healthy(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	app := &App{
		db:        database,
		cfg:       &config.Config{},
		mux:       http.NewServeMux(),
		startedAt: time.Now().Add(-5 * time.Minute),
	}
	app.mux.HandleFunc("GET /healthz", app.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	app.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %s", resp["status"])
	}
	if resp["db"] != "ok" {
		t.Errorf("expected db ok, got %s", resp["db"])
	}
	if resp["uptime"] == "" {
		t.Error("expected uptime to be non-empty")
	}
}

func TestHandleHealthz_Degraded(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	// Close the DB to simulate a failure
	database.Close()

	app := &App{
		db:        database,
		cfg:       &config.Config{},
		mux:       http.NewServeMux(),
		startedAt: time.Now(),
	}
	app.mux.HandleFunc("GET /healthz", app.handleHealthz)

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	app.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	if resp["status"] != "degraded" {
		t.Errorf("expected status degraded, got %s", resp["status"])
	}
	if resp["db"] != "error" {
		t.Errorf("expected db error, got %s", resp["db"])
	}
}
```

### Step 2.5: Run the test

- [ ] Run `go test ./internal/web/ -run TestHandleHealthz -v` and verify both tests pass.

```bash
go test ./internal/web/ -run TestHandleHealthz -v
```

Expected:
```
--- PASS: TestHandleHealthz_Healthy
--- PASS: TestHandleHealthz_Degraded
PASS
```

### Step 2.6: Commit

- [ ] Stage and commit with message: `feat: add /healthz endpoint with DB check and uptime`

---

## Task 3: Auto-Linking Card Mentions in Articles

**Files:**
- Create: `internal/linker/linker.go`
- Create: `internal/linker/linker_test.go`
- Modify: `internal/db/migrations.go` — add `related_cards_json` column
- Modify: `internal/db/models.go` — add `RelatedCardsJSON` field
- Modify: `internal/db/db.go` — add column to all GeneratedArticle queries/scans
- Modify: `internal/ai/pipeline.go` — add `RelatedCards` to `PipelineResult`
- Modify: `cmd/milesahead/main.go` — call linker after pipeline
- Modify: `internal/article/manager.go` — persist `RelatedCardsJSON`
- Modify: `internal/web/public.go` — parse and pass related cards
- Modify: `templates/public/article.html` — sidebar section

### Step 3.1: Add migration for related_cards_json column

- [ ] Add `ALTER TABLE generated_articles ADD COLUMN related_cards_json TEXT DEFAULT '[]'` to the `alterStatements` slice in `internal/db/migrations.go`.

In `internal/db/migrations.go`, add to the `alterStatements` slice:

```go
		`ALTER TABLE generated_articles ADD COLUMN related_cards_json TEXT DEFAULT '[]'`,
```

### Step 3.2: Add RelatedCardsJSON field to GeneratedArticle model

- [ ] Add the field to the `GeneratedArticle` struct in `internal/db/models.go`.

In `internal/db/models.go`, add after `ScoutsTake`:

```go
	RelatedCardsJSON string     `json:"related_cards_json"`
```

### Step 3.3: Update all GeneratedArticle SELECT lists and Scan calls in db.go

- [ ] This is a cross-cutting change. Every query that selects from `generated_articles` and every `Scan` call for `GeneratedArticle` must include `related_cards_json`.

**`CreateGeneratedArticle`** — update INSERT to include `related_cards_json`:

```go
func (d *Database) CreateGeneratedArticle(a *GeneratedArticle) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Slug, a.Content, a.Type, a.Status, a.SourcesJSON, a.RevisionNumber, a.Featured, a.TLDR, a.KeyDetails, a.Links, a.ImageURL, a.ImageThumbnail, a.ImageAttribution, a.ScoutsTake, a.RelatedCardsJSON,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
```

**`GetGeneratedArticle`** — add `related_cards_json` to SELECT and Scan:

```go
func (d *Database) GetGeneratedArticle(id int64) (*GeneratedArticle, error) {
	a := &GeneratedArticle{}
	err := d.db.QueryRow(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, created_at, updated_at, published_at FROM generated_articles WHERE id = ?`, id,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}
```

**`GetGeneratedArticleBySlug`** — same pattern, add `related_cards_json` after `scouts_take` in both SELECT and Scan.

**`ListArticlesByStatus`** — update SELECT:

```go
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, created_at, updated_at, published_at FROM generated_articles WHERE status = ? ORDER BY created_at DESC`, status,
	)
```

**`ListPublishedArticles`** — update SELECT similarly.

**`ListPublishedArticlesByType`** — update SELECT similarly.

**`GetFeaturedArticle`** — update both SELECT queries (primary and fallback) similarly.

**`scanGeneratedArticles`** — update Scan to include `&a.RelatedCardsJSON`:

```go
func scanGeneratedArticles(rows *sql.Rows) ([]GeneratedArticle, error) {
	var articles []GeneratedArticle
	for rows.Next() {
		var a GeneratedArticle
		if err := rows.Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt); err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}
```

### Step 3.4: Add RelatedCards to PipelineResult

- [ ] Add a `RelatedCards` field to `PipelineResult` in `internal/ai/pipeline.go`.

Add a type for the related card data and the field:

```go
// RelatedCard holds a linked card reference for article sidebar display.
type RelatedCard struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}
```

Add to `PipelineResult`:

```go
	RelatedCards     []RelatedCard
```

### Step 3.5: Create the linker package

- [ ] Create `internal/linker/linker.go` with the `AutoLink` function.

```go
package linker

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// MatchedCard holds a matched reference page for sidebar display.
type MatchedCard struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// AutoLink scans article HTML for card keyword mentions and links the first
// occurrence of each card. It skips keywords that appear inside existing <a>
// or <code> tags. Keywords are matched longest-first to avoid partial overlaps.
// Returns the modified HTML and the list of matched cards.
func AutoLink(content string, cards []db.ReferencePage) (string, []MatchedCard) {
	if len(cards) == 0 {
		return content, nil
	}

	// Build keyword-to-card map, sorted by keyword length descending
	type kwEntry struct {
		keyword string
		card    db.ReferencePage
	}
	var entries []kwEntry

	for _, card := range cards {
		var keywords []string
		if card.KeywordsJSON != "" && card.KeywordsJSON != "[]" {
			json.Unmarshal([]byte(card.KeywordsJSON), &keywords)
		}
		// Also use the card title as a keyword
		keywords = append(keywords, card.Title)
		for _, kw := range keywords {
			kw = strings.TrimSpace(kw)
			if kw == "" {
				continue
			}
			entries = append(entries, kwEntry{keyword: kw, card: card})
		}
	}

	// Sort by keyword length descending (longer matches first)
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].keyword) > len(entries[j].keyword)
	})

	// Track which cards have been linked (max one link per card)
	linkedCards := make(map[int64]bool)
	var matched []MatchedCard

	for _, entry := range entries {
		if linkedCards[entry.card.ID] {
			continue
		}

		// Case-insensitive search for first occurrence not inside <a> or <code>
		linked, found := linkFirstMention(content, entry.keyword, entry.card.Slug)
		if found {
			content = linked
			linkedCards[entry.card.ID] = true
			matched = append(matched, MatchedCard{
				Slug:  entry.card.Slug,
				Title: entry.card.Title,
			})
		}
	}

	return content, matched
}

// linkFirstMention finds the first case-insensitive occurrence of keyword in
// content that is NOT inside an <a>...</a> or <code>...</code> block, and
// wraps it in a link. Returns the modified content and whether a match was found.
func linkFirstMention(content, keyword, slug string) (string, bool) {
	// Use word boundary regex for case-insensitive keyword matching
	pattern := `(?i)\b` + regexp.QuoteMeta(keyword) + `\b`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return content, false
	}

	locs := re.FindAllStringIndex(content, -1)
	if len(locs) == 0 {
		return content, false
	}

	// Pre-compute regions inside <a>...</a> and <code>...</code> tags
	lower := strings.ToLower(content)
	excludeRegions := findExcludeRegions(lower)

	for _, loc := range locs {
		start, end := loc[0], loc[1]

		// Check if this occurrence is inside an excluded region
		if isInRegion(start, end, excludeRegions) {
			continue
		}

		// Replace this occurrence with a link
		original := content[start:end]
		link := `<a href="/cards/` + slug + `">` + original + `</a>`
		content = content[:start] + link + content[end:]
		return content, true
	}

	return content, false
}

type region struct {
	start, end int
}

// findExcludeRegions returns byte ranges of <a>...</a> and <code>...</code> blocks.
func findExcludeRegions(lowerContent string) []region {
	var regions []region

	// Find <a ...>...</a> regions
	aOpen := regexp.MustCompile(`<a[\s>]`)
	aClose := regexp.MustCompile(`</a>`)
	addTagRegions(&regions, lowerContent, aOpen, aClose)

	// Find <code>...</code> regions
	codeOpen := regexp.MustCompile(`<code[>\s]`)
	codeClose := regexp.MustCompile(`</code>`)
	addTagRegions(&regions, lowerContent, codeOpen, codeClose)

	return regions
}

func addTagRegions(regions *[]region, content string, openRe, closeRe *regexp.Regexp) {
	opens := openRe.FindAllStringIndex(content, -1)
	for _, o := range opens {
		closeIdx := closeRe.FindStringIndex(content[o[0]:])
		if closeIdx != nil {
			*regions = append(*regions, region{start: o[0], end: o[0] + closeIdx[1]})
		}
	}
}

// isInRegion checks if the range [start, end) overlaps with any excluded region.
func isInRegion(start, end int, regions []region) bool {
	for _, r := range regions {
		if start >= r.start && end <= r.end {
			return true
		}
	}
	return false
}
```

### Step 3.6: Write linker tests

- [ ] Create `internal/linker/linker_test.go`.

```go
package linker

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestAutoLink_BasicMatch(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Chase Sapphire Preferred", Slug: "chase-sapphire-preferred", KeywordsJSON: `["Chase Sapphire Preferred", "CSP"]`},
	}

	content := `<p>The Chase Sapphire Preferred card is offering a new bonus.</p>`
	linked, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Slug != "chase-sapphire-preferred" {
		t.Errorf("expected slug chase-sapphire-preferred, got %s", matched[0].Slug)
	}
	expected := `<p>The <a href="/cards/chase-sapphire-preferred">Chase Sapphire Preferred</a> card is offering a new bonus.</p>`
	if linked != expected {
		t.Errorf("unexpected output:\n  got:  %s\n  want: %s", linked, expected)
	}
}

func TestAutoLink_SkipsExistingLinks(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Amex Platinum", Slug: "amex-platinum", KeywordsJSON: `["Amex Platinum"]`},
	}

	content := `<p>The <a href="/other">Amex Platinum</a> is great. Amex Platinum offers more.</p>`
	linked, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	// Should link the second mention, not the one already inside <a>
	if !contains(linked, `<a href="/cards/amex-platinum">Amex Platinum</a>`) {
		t.Errorf("expected second mention to be linked, got: %s", linked)
	}
}

func TestAutoLink_SkipsCodeBlocks(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Test Card", Slug: "test-card", KeywordsJSON: `["Test Card"]`},
	}

	content := `<code>Test Card</code>`
	_, matched := AutoLink(content, cards)

	if len(matched) != 0 {
		t.Errorf("expected 0 matches inside code block, got %d", len(matched))
	}
}

func TestAutoLink_OnlyFirstMention(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Delta SkyMiles", Slug: "delta-skymiles", KeywordsJSON: `["Delta SkyMiles"]`},
	}

	content := `<p>Delta SkyMiles is great.</p><p>Delta SkyMiles has many partners.</p>`
	linked, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}

	// Count occurrences of the link
	count := 0
	idx := 0
	for {
		i := indexOf(linked[idx:], `<a href="/cards/delta-skymiles">`)
		if i == -1 {
			break
		}
		count++
		idx += i + 1
	}
	if count != 1 {
		t.Errorf("expected exactly 1 link, found %d in: %s", count, linked)
	}
}

func TestAutoLink_LongerKeywordFirst(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Chase Sapphire", Slug: "chase-sapphire", KeywordsJSON: `["Chase Sapphire"]`},
		{ID: 2, Title: "Chase Sapphire Preferred", Slug: "chase-sapphire-preferred", KeywordsJSON: `["Chase Sapphire Preferred"]`},
	}

	content := `<p>The Chase Sapphire Preferred is the best card.</p>`
	_, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].Slug != "chase-sapphire-preferred" {
		t.Errorf("expected longer keyword to match first, got slug: %s", matched[0].Slug)
	}
}

func TestAutoLink_NoCards(t *testing.T) {
	content := `<p>Some article content.</p>`
	linked, matched := AutoLink(content, nil)

	if linked != content {
		t.Errorf("expected unchanged content")
	}
	if len(matched) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matched))
	}
}

func TestAutoLink_CaseInsensitive(t *testing.T) {
	cards := []db.ReferencePage{
		{ID: 1, Title: "Amex Gold", Slug: "amex-gold", KeywordsJSON: `["Amex Gold"]`},
	}

	content := `<p>The amex gold card is solid.</p>`
	_, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Errorf("expected case-insensitive match, got %d matches", len(matched))
	}
}

func contains(s, substr string) bool {
	return indexOf(s, substr) != -1
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
```

### Step 3.7: Run linker tests

- [ ] Run `go test ./internal/linker/ -v` and verify all tests pass.

```bash
go test ./internal/linker/ -v
```

Expected: all tests PASS.

### Step 3.8: Update article manager to persist RelatedCardsJSON

- [ ] In `internal/article/manager.go`, update `CreateFromPipeline` to marshal and set `RelatedCardsJSON`.

In the `CreateFromPipeline` method, after the `linksJSON` handling, add:

```go
	relatedCardsJSON, rcErr := json.Marshal(result.RelatedCards)
	if rcErr != nil {
		relatedCardsJSON = []byte("[]")
	}
```

And add to the `article` struct initialization:

```go
		RelatedCardsJSON: string(relatedCardsJSON),
```

Also add `"github.com/kumaranik/milesahead/internal/ai"` is already imported. No change needed there.

### Step 3.9: Call linker in main.go scan job

- [ ] In `cmd/milesahead/main.go`, import the linker package and call `AutoLink` after pipeline processing but before `CreateFromPipeline`.

Add import:
```go
	"github.com/kumaranik/milesahead/internal/linker"
```

After the image selection block and before `articleMgr.CreateFromPipeline`, add:

```go
			// Auto-link card mentions in article content
			refPages, _ := database.ListPublishedReferencePages("")
			if len(refPages) > 0 {
				linkedContent, matchedCards := linker.AutoLink(result.Content, refPages)
				result.Content = linkedContent
				for _, mc := range matchedCards {
					result.RelatedCards = append(result.RelatedCards, ai.RelatedCard{
						Slug:  mc.Slug,
						Title: mc.Title,
					})
				}
				if len(matchedCards) > 0 {
					slog.Info("auto-linked cards", "title", result.Title, "cards", len(matchedCards))
				}
			}
```

### Step 3.10: Update article handler to pass RelatedCards to template

- [ ] In `internal/web/public.go`, in `handleArticle`, parse `RelatedCardsJSON` and pass it to the template.

Add a type at the top of `public.go`:

```go
// relatedCardLink holds a related card for article sidebar display.
type relatedCardLink struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}
```

In `handleArticle`, after parsing `articleLinks`, add:

```go
	var relatedCards []relatedCardLink
	if article.RelatedCardsJSON != "" && article.RelatedCardsJSON != "[]" {
		json.Unmarshal([]byte(article.RelatedCardsJSON), &relatedCards)
	}
```

Add to the `data` map:

```go
		"RelatedCards": relatedCards,
```

### Step 3.11: Add Related Cards sidebar section to article template

- [ ] In `templates/public/article.html`, add a "Related Cards" section to the sidebar.

After the `{{if .Links}}...{{end}}` block in the sidebar, add:

```html
            {{if .RelatedCards}}
            <div class="sidebar-section">
                <h4>Related Cards</h4>
                <ul class="related-cards">
                    {{range .RelatedCards}}
                    <li><a href="/cards/{{.Slug}}">&rarr; {{.Title}}</a></li>
                    {{end}}
                </ul>
            </div>
            {{end}}
```

### Step 3.12: Build and verify

- [ ] Run `go build ./cmd/milesahead/` and `go test ./internal/linker/ -v` to verify everything compiles and tests pass.

```bash
go build ./cmd/milesahead/ && go test ./internal/linker/ -v
```

### Step 3.13: Commit

- [ ] Stage and commit with message: `feat: auto-link card mentions in articles with Related Cards sidebar`

---

## Task 4: Scout's Take Backfill Script

**Files:**
- Create: `cmd/backfill-scouts-take/main.go`

### Step 4.1: Create the backfill script

- [ ] Create `cmd/backfill-scouts-take/main.go`.

```go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	dbPath := flag.String("db", "./data/milesahead.db", "database path")
	apiKey := flag.String("api-key", "", "NVIDIA API key (required)")
	apiURL := flag.String("api-url", "https://integrate.api.nvidia.com/v1", "API base URL")
	persona := flag.String("persona", "Scout", "persona name for Scout's Take")
	dryRun := flag.Bool("dry-run", false, "show which articles would be updated without calling API")
	model := flag.String("model", "", "model name for generation (required)")
	flag.Parse()

	if *apiKey == "" && !*dryRun {
		fmt.Fprintln(os.Stderr, "Error: -api-key is required (or use -dry-run)")
		flag.Usage()
		os.Exit(1)
	}

	if *model == "" && !*dryRun {
		fmt.Fprintln(os.Stderr, "Error: -model is required (or use -dry-run)")
		flag.Usage()
		os.Exit(1)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// Get all approved articles without a Scout's Take
	articles, err := database.ListArticlesByStatus(db.StatusApproved)
	if err != nil {
		slog.Error("failed to list articles", "error", err)
		os.Exit(1)
	}

	var needsBackfill []db.GeneratedArticle
	for _, a := range articles {
		if strings.TrimSpace(a.ScoutsTake) == "" {
			needsBackfill = append(needsBackfill, a)
		}
	}

	slog.Info("backfill scan complete", "total_approved", len(articles), "needs_backfill", len(needsBackfill))

	if *dryRun {
		for _, a := range needsBackfill {
			fmt.Printf("[DRY RUN] Would backfill: ID=%d Title=%q\n", a.ID, a.Title)
		}
		fmt.Printf("\nTotal: %d articles would be updated\n", len(needsBackfill))
		return
	}

	// Create AI client
	hfClient := ai.NewHFClient(*apiKey, 3)
	hfClient.BaseURL = *apiURL

	succeeded := 0
	failed := 0
	skipped := 0

	for i, a := range needsBackfill {
		slog.Info("processing article", "index", i+1, "total", len(needsBackfill), "id", a.ID, "title", a.Title)

		// Build prompt
		prompt := ai.ScoutsTakePrompt(*persona, a.Title, a.TLDR, a.Content)

		// Call API
		scoutsTake, err := hfClient.Generate(*model, prompt, 256)
		if err != nil {
			slog.Error("API call failed, skipping", "id", a.ID, "error", err)
			failed++
			continue
		}

		scoutsTake = strings.TrimSpace(scoutsTake)
		if scoutsTake == "" {
			slog.Warn("empty response, skipping", "id", a.ID)
			skipped++
			continue
		}

		// Update DB
		_, updateErr := database.DB().Exec(
			`UPDATE generated_articles SET scouts_take = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			scoutsTake, a.ID,
		)
		if updateErr != nil {
			slog.Error("DB update failed", "id", a.ID, "error", updateErr)
			failed++
			continue
		}

		succeeded++
		slog.Info("backfill succeeded", "id", a.ID, "title", a.Title)

		// Rate limit: 1 second between API calls
		if i < len(needsBackfill)-1 {
			time.Sleep(1 * time.Second)
		}
	}

	slog.Info("backfill complete",
		"succeeded", succeeded,
		"failed", failed,
		"skipped", skipped,
		"total", len(needsBackfill),
	)
}
```

### Step 4.2: Build the backfill script

- [ ] Run `go build ./cmd/backfill-scouts-take/` to verify it compiles.

```bash
go build ./cmd/backfill-scouts-take/
```

### Step 4.3: Verify dry-run mode

- [ ] Run with `-dry-run` flag against the dev DB (or any DB) to verify it lists articles correctly.

```bash
go run ./cmd/backfill-scouts-take/ -db ./data/milesahead.db -dry-run
```

Expected: lists articles needing backfill or reports "needs_backfill: 0".

### Step 4.4: Commit

- [ ] Stage and commit with message: `feat: add Scout's Take backfill script for existing articles`

---

## Task 5: Full-Text Search

**Files:**
- Modify: `internal/db/migrations.go` — FTS5 tables + initial population
- Modify: `internal/db/db.go` — FTS sync + search methods
- Modify: `internal/web/routes.go` — search route
- Modify: `internal/web/public.go` — search handler
- Create: `templates/public/search.html` — results page
- Modify: `templates/layout.html` — nav search icon
- Modify: `static/css/style.css` — search styles
- Modify: `static/js/main.js` — search expand/collapse
- Create: `internal/db/fts_test.go` — FTS tests

### Step 5.1: Add FTS5 table creation to migrations

- [ ] In `internal/db/migrations.go`, add FTS5 virtual table creation after the existing ALTER statements.

After the `alterStatements` loop, add:

```go
	// FTS5 virtual tables for full-text search
	ftsStatements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS articles_fts USING fts5(title, tldr, content)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS cards_fts USING fts5(title, sections_text, scouts_verdict)`,
	}
	for _, stmt := range ftsStatements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	// Populate FTS tables from existing data
	if err := populateFTS(db); err != nil {
		return fmt.Errorf("populating FTS: %w", err)
	}
```

Add the `"fmt"` import to `migrations.go` if not already present.

### Step 5.2: Add FTS population function in migrations

- [ ] Add `populateFTS` function to `internal/db/migrations.go`.

```go
// populateFTS populates FTS tables from existing data. Runs only once —
// skips entirely if articles_fts already has data.
func populateFTS(conn *sql.DB) error {
	var count int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM articles_fts`).Scan(&count); err == nil && count > 0 {
		return nil // Already populated
	}

	// Populate articles_fts
	rows, err := conn.Query(`SELECT id, title, tldr, content FROM generated_articles`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var title, tldr, content string
		if err := rows.Scan(&id, &title, &tldr, &content); err != nil {
			return err
		}
		plainContent := stripHTMLForFTS(content)
		_, err := conn.Exec(`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`, id, title, tldr, plainContent)
		if err != nil {
			// Ignore duplicate rowid errors
			if !strings.Contains(err.Error(), "constraint") {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Populate cards_fts
	cardRows, err := conn.Query(`SELECT id, title, sections_json, scouts_verdict FROM reference_pages`)
	if err != nil {
		return err
	}
	defer cardRows.Close()

	for cardRows.Next() {
		var id int64
		var title, sectionsJSON, scoutsVerdict string
		if err := cardRows.Scan(&id, &title, &sectionsJSON, &scoutsVerdict); err != nil {
			return err
		}
		sectionsText := extractSectionsText(sectionsJSON)
		_, err := conn.Exec(`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (?, ?, ?, ?)`, id, title, sectionsText, scoutsVerdict)
		if err != nil {
			if !strings.Contains(err.Error(), "constraint") {
				return err
			}
		}
	}
	return cardRows.Err()
}

// stripHTMLForFTS removes HTML tags from content for FTS indexing.
func stripHTMLForFTS(s string) string {
	var result []byte
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>':
			inTag = false
			result = append(result, ' ')
		case !inTag:
			result = append(result, s[i])
		}
	}
	return strings.TrimSpace(string(result))
}

// extractSectionsText extracts plain text from sections_json for FTS indexing.
func extractSectionsText(sectionsJSON string) string {
	if sectionsJSON == "" || sectionsJSON == "[]" {
		return ""
	}
	type section struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	var sections []section
	if err := json.Unmarshal([]byte(sectionsJSON), &sections); err != nil {
		return ""
	}
	var parts []string
	for _, s := range sections {
		parts = append(parts, s.Title)
		parts = append(parts, stripHTMLForFTS(s.Content))
	}
	return strings.Join(parts, " ")
}
```

Add `"encoding/json"` to the imports in `migrations.go`.

### Step 5.3: Add FTS sync methods to db.go

- [ ] Add `SyncArticleFTS`, `SyncCardFTS`, `DeleteArticleFTS`, `DeleteCardFTS` to `internal/db/db.go`.

```go
// --- FTS Sync ---

// SyncArticleFTS upserts an article's content into the FTS index.
func (d *Database) SyncArticleFTS(id int64) error {
	var title, tldr, content string
	err := d.db.QueryRow(`SELECT title, tldr, content FROM generated_articles WHERE id = ?`, id).Scan(&title, &tldr, &content)
	if err != nil {
		return err
	}

	plainContent := stripHTMLForDB(content)

	// Delete existing entry, then insert
	d.db.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id)
	_, err = d.db.Exec(`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`, id, title, tldr, plainContent)
	return err
}

// SyncCardFTS upserts a reference page's content into the FTS index.
func (d *Database) SyncCardFTS(id int64) error {
	var title, sectionsJSON, scoutsVerdict string
	err := d.db.QueryRow(`SELECT title, sections_json, scouts_verdict FROM reference_pages WHERE id = ?`, id).Scan(&title, &sectionsJSON, &scoutsVerdict)
	if err != nil {
		return err
	}

	sectionsText := extractSectionsTextDB(sectionsJSON)

	d.db.Exec(`DELETE FROM cards_fts WHERE rowid = ?`, id)
	_, err = d.db.Exec(`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (?, ?, ?, ?)`, id, title, sectionsText, scoutsVerdict)
	return err
}

// DeleteArticleFTS removes an article from the FTS index.
func (d *Database) DeleteArticleFTS(id int64) error {
	_, err := d.db.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id)
	return err
}

// DeleteCardFTS removes a card from the FTS index.
func (d *Database) DeleteCardFTS(id int64) error {
	_, err := d.db.Exec(`DELETE FROM cards_fts WHERE rowid = ?`, id)
	return err
}

// stripHTMLForDB removes HTML tags from content for FTS indexing.
func stripHTMLForDB(s string) string {
	var result []byte
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>':
			inTag = false
			result = append(result, ' ')
		case !inTag:
			result = append(result, s[i])
		}
	}
	return strings.TrimSpace(string(result))
}

// extractSectionsTextDB extracts plain text from sections_json for FTS.
func extractSectionsTextDB(sectionsJSON string) string {
	if sectionsJSON == "" || sectionsJSON == "[]" {
		return ""
	}
	type section struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	var sections []section
	if err := json.Unmarshal([]byte(sectionsJSON), &sections); err != nil {
		return ""
	}
	var parts []string
	for _, s := range sections {
		parts = append(parts, s.Title)
		parts = append(parts, stripHTMLForDB(s.Content))
	}
	return strings.Join(parts, " ")
}
```

Add `"encoding/json"` to `db.go` imports if not already present.

### Step 5.4: Add FTS sync calls to content-modifying operations

- [ ] Call `SyncArticleFTS` in `CreateGeneratedArticle` and `SyncCardFTS` in `CreateReferencePage` / `UpdateReferencePage`.

In `CreateGeneratedArticle`, after `return res.LastInsertId()`, refactor to:

```go
func (d *Database) CreateGeneratedArticle(a *GeneratedArticle) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Slug, a.Content, a.Type, a.Status, a.SourcesJSON, a.RevisionNumber, a.Featured, a.TLDR, a.KeyDetails, a.Links, a.ImageURL, a.ImageThumbnail, a.ImageAttribution, a.ScoutsTake, a.RelatedCardsJSON,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// Sync FTS index (best-effort, don't fail the insert)
	d.SyncArticleFTS(id)
	return id, nil
}
```

In `CreateReferencePage`, after getting the ID:

```go
func (d *Database) CreateReferencePage(page *ReferencePage) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO reference_pages (slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		page.Slug, page.Title, page.Category, page.Issuer, page.SectionsJSON, page.RecentChangesJSON, page.PendingUpdatesJSON, page.ScoutsVerdict, page.KeywordsJSON, page.ImageURL, page.ApplyURL, page.Status,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	d.SyncCardFTS(id)
	return id, nil
}
```

In `UpdateReferencePage`, after the Exec:

```go
func (d *Database) UpdateReferencePage(page *ReferencePage) error {
	_, err := d.db.Exec(
		`UPDATE reference_pages SET slug = ?, title = ?, category = ?, issuer = ?, sections_json = ?, recent_changes_json = ?, pending_updates_json = ?, scouts_verdict = ?, keywords_json = ?, image_url = ?, apply_url = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		page.Slug, page.Title, page.Category, page.Issuer, page.SectionsJSON, page.RecentChangesJSON, page.PendingUpdatesJSON, page.ScoutsVerdict, page.KeywordsJSON, page.ImageURL, page.ApplyURL, page.Status, page.ID,
	)
	if err != nil {
		return err
	}
	d.SyncCardFTS(page.ID)
	return nil
}
```

### Step 5.5: Add search query methods to db.go

- [ ] Add `SearchArticles` and `SearchCards` methods.

```go
// SearchResult holds a single search result from the FTS index.
type SearchResult struct {
	ID      int64
	Title   string
	Snippet string
	Type    string  // article type or "card"
	Slug    string
	Date    string  // published_at for articles, issuer for cards
}

// SearchArticles searches the articles FTS index for approved articles matching the query.
func (d *Database) SearchArticles(query string, limit, offset int) ([]SearchResult, error) {
	rows, err := d.db.Query(`
		SELECT ga.id, ga.title, snippet(articles_fts, 2, '<mark>', '</mark>', '...', 32) as snip, ga.type, ga.slug, COALESCE(ga.published_at, '')
		FROM articles_fts
		JOIN generated_articles ga ON ga.id = articles_fts.rowid
		WHERE articles_fts MATCH ?
		AND ga.status = 'approved'
		ORDER BY rank
		LIMIT ? OFFSET ?
	`, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Title, &r.Snippet, &r.Type, &r.Slug, &r.Date); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchCards searches the cards FTS index for published cards matching the query.
func (d *Database) SearchCards(query string, limit, offset int) ([]SearchResult, error) {
	rows, err := d.db.Query(`
		SELECT rp.id, rp.title, snippet(cards_fts, 1, '<mark>', '</mark>', '...', 32) as snip, 'card' as type, rp.slug, rp.issuer
		FROM cards_fts
		JOIN reference_pages rp ON rp.id = cards_fts.rowid
		WHERE cards_fts MATCH ?
		AND rp.status = 'published'
		ORDER BY rank
		LIMIT ? OFFSET ?
	`, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ID, &r.Title, &r.Snippet, &r.Type, &r.Slug, &r.Date); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}
```

### Step 5.6: Write FTS tests

- [ ] Create `internal/db/fts_test.go`.

```go
package db

import (
	"testing"
)

func setupTestDB(t *testing.T) *Database {
	t.Helper()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestSyncArticleFTS(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create an article
	id, err := database.CreateGeneratedArticle(&GeneratedArticle{
		Title:          "Test Chase Sapphire Bonus",
		Slug:           "test-chase-bonus",
		Content:        "<p>Chase is offering a 90K bonus on the Sapphire Preferred.</p>",
		Type:           "deal",
		Status:         StatusApproved,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
		TLDR:           "Chase 90K bonus",
		KeyDetails:     "[]",
		Links:          "[]",
		RelatedCardsJSON: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Search should find it
	results, err := database.SearchArticles("Chase Sapphire", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != id {
		t.Errorf("expected ID %d, got %d", id, results[0].ID)
	}
}

func TestSearchArticles_StatusFilter(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create a draft article (should NOT appear in search)
	database.CreateGeneratedArticle(&GeneratedArticle{
		Title:          "Draft Article About Points",
		Slug:           "draft-points",
		Content:        "<p>Points and miles news.</p>",
		Type:           "summary",
		Status:         StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
		TLDR:           "Draft",
		KeyDetails:     "[]",
		Links:          "[]",
		RelatedCardsJSON: "[]",
	})

	results, err := database.SearchArticles("Points", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for draft article, got %d", len(results))
	}
}

func TestSyncCardFTS(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	_, err := database.CreateReferencePage(&ReferencePage{
		Slug:               "amex-platinum",
		Title:              "American Express Platinum Card",
		Category:           "travel",
		Issuer:             "American Express",
		SectionsJSON:       `[{"title":"Overview","content":"<p>The Amex Platinum offers premium travel benefits.</p>"}]`,
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		ScoutsVerdict:      "Great for frequent travelers",
		KeywordsJSON:       `["Amex Platinum"]`,
		Status:             "published",
	})
	if err != nil {
		t.Fatal(err)
	}

	results, err := database.SearchCards("Platinum travel", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "American Express Platinum Card" {
		t.Errorf("unexpected title: %s", results[0].Title)
	}
}

func TestDeleteArticleFTS(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	id, _ := database.CreateGeneratedArticle(&GeneratedArticle{
		Title:          "Deletable Article",
		Slug:           "deletable",
		Content:        "<p>This will be deleted.</p>",
		Type:           "summary",
		Status:         StatusApproved,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
		TLDR:           "Delete me",
		KeyDetails:     "[]",
		Links:          "[]",
		RelatedCardsJSON: "[]",
	})

	// Verify it's in FTS
	results, _ := database.SearchArticles("Deletable", 10, 0)
	if len(results) != 1 {
		t.Fatal("expected article in FTS before delete")
	}

	// Delete from FTS
	database.DeleteArticleFTS(id)

	results, _ = database.SearchArticles("Deletable", 10, 0)
	if len(results) != 0 {
		t.Errorf("expected 0 results after FTS delete, got %d", len(results))
	}
}
```

### Step 5.7: Run FTS tests

- [ ] Run `go test ./internal/db/ -run TestSync -v` and `go test ./internal/db/ -run TestSearch -v` and `go test ./internal/db/ -run TestDelete -v`.

```bash
go test ./internal/db/ -run "TestSync|TestSearch|TestDelete" -v
```

Expected: all tests PASS.

### Step 5.8: Add search handler

- [ ] Add `handleSearch` to `internal/web/public.go`.

```go
func (app *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	// Sanitize FTS5 special characters by wrapping in double quotes
	ftsQuery := query
	if query != "" {
		ftsQuery = "\"" + strings.ReplaceAll(query, "\"", "\"\"") + "\""
	}

	pageStr := r.URL.Query().Get("page")
	page := 1
	if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
		page = p
	}

	const perPage = 10 // per type, so up to 20 total per page
	offset := (page - 1) * perPage

	var articleResults, cardResults []db.SearchResult
	articleHasMore := false
	cardHasMore := false

	if ftsQuery != "" {
		var err error
		// Query articles and cards independently, paginated separately
		articleResults, err = app.db.SearchArticles(ftsQuery, perPage+1, offset)
		if err != nil {
			articleResults = nil
		}
		if len(articleResults) > perPage {
			articleHasMore = true
			articleResults = articleResults[:perPage]
		}

		cardResults, err = app.db.SearchCards(ftsQuery, perPage+1, offset)
		if err != nil {
			cardResults = nil
		}
		if len(cardResults) > perPage {
			cardHasMore = true
			cardResults = cardResults[:perPage]
		}
	}

	data := map[string]interface{}{
		"Title":          "Search",
		"Query":          query,
		"ArticleResults": articleResults,
		"CardResults":    cardResults,
		"Page":           page,
		"HasPrev":        page > 1,
		"HasNext":        articleHasMore || cardHasMore,
		"PrevPage":       page - 1,
		"NextPage":       page + 1,
	}
	app.renderTemplate(w, "search.html", data)
}
```

Add `"strconv"` to the imports in `public.go` if not already present. (It is not currently imported — add it.)

### Step 5.9: Register search route

- [ ] Add `GET /search` in `internal/web/routes.go`.

After the `/cards/{slug}` route, add:

```go
	// Search.
	app.mux.HandleFunc("GET /search", app.handleSearch)
```

### Step 5.10: Create search template

- [ ] Create `templates/public/search.html`.

```html
{{template "layout" .}}
{{define "content"}}
<div class="search-page">
    <h1>Search</h1>
    <form class="search-form" action="/search" method="GET">
        <input type="text" name="q" value="{{.Query}}" placeholder="Search articles and cards..." class="search-input-full" autofocus>
        <button type="submit" class="search-submit">Search</button>
    </form>

    {{if .Query}}
        {{if and (not .ArticleResults) (not .CardResults)}}
        <div class="search-empty">
            <p>No results found for "{{.Query}}"</p>
        </div>
        {{end}}

        {{if .ArticleResults}}
        <div class="search-section">
            <h2>Articles</h2>
            {{range .ArticleResults}}
            <div class="search-result">
                <div class="search-result-header">
                    <span class="type-badge badge-{{.Type}}">{{.Type}}</span>
                    <a href="/article/{{.Slug}}" class="search-result-title">{{.Title}}</a>
                </div>
                <p class="search-result-snippet">{{.Snippet | safeHTML}}</p>
                <span class="search-result-meta">{{.Date}}</span>
            </div>
            {{end}}
        </div>
        {{end}}

        {{if .CardResults}}
        <div class="search-section">
            <h2>Cards</h2>
            {{range .CardResults}}
            <div class="search-result">
                <div class="search-result-header">
                    <span class="type-badge badge-card">Card</span>
                    <a href="/cards/{{.Slug}}" class="search-result-title">{{.Title}}</a>
                </div>
                <p class="search-result-snippet">{{.Snippet | safeHTML}}</p>
                <span class="search-result-meta">{{.Date}}</span>
            </div>
            {{end}}
        </div>
        {{end}}

        {{if or .HasPrev .HasNext}}
        <div class="search-pagination">
            {{if .HasPrev}}<a href="/search?q={{.Query}}&page={{.PrevPage}}" class="pagination-link">&larr; Previous</a>{{end}}
            {{if .HasNext}}<a href="/search?q={{.Query}}&page={{.NextPage}}" class="pagination-link">Next &rarr;</a>{{end}}
        </div>
        {{end}}
    {{else}}
        <div class="search-empty">
            <p>Enter a search term to find articles and card reference pages.</p>
        </div>
    {{end}}
</div>
{{end}}
```

### Step 5.11: Add search icon to nav in layout.html

- [ ] Update `templates/layout.html` to add a search icon in the nav bar.

Replace the `<div class="nav-links">` block:

```html
        <div class="nav-links">
            <a href="/cards/">Cards</a>
            <div class="nav-search">
                <button id="search-toggle" type="button" class="search-icon" aria-label="Search">
                    <svg xmlns="http://www.w3.org/2000/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/></svg>
                </button>
                <form action="/search" method="GET" class="nav-search-form" id="nav-search-form">
                    <input type="text" name="q" placeholder="Search..." class="nav-search-input" id="nav-search-input">
                </form>
            </div>
            <button id="theme-toggle" type="button">Toggle Theme</button>
        </div>
```

### Step 5.12: Add search CSS styles

- [ ] Append search styles to `static/css/style.css`.

Add at the end of the file:

```css
/* Search - Nav */
.nav-search {
    display: flex;
    align-items: center;
    gap: 0.25rem;
}

.search-icon {
    background: none;
    border: none;
    color: var(--text);
    cursor: pointer;
    padding: 0.25rem;
    display: flex;
    align-items: center;
}

.nav-search-form {
    display: none;
    overflow: hidden;
    transition: width 0.2s ease;
}

.nav-search-form.active {
    display: block;
}

.nav-search-input {
    width: 160px;
    padding: 0.3rem 0.5rem;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg);
    color: var(--text);
    font-size: 0.85rem;
}

/* Search - Page */
.search-page {
    max-width: 800px;
    margin: 2rem auto;
    padding: 0 1rem;
}

.search-form {
    display: flex;
    gap: 0.5rem;
    margin: 1.5rem 0;
}

.search-input-full {
    flex: 1;
    padding: 0.6rem 1rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--card-bg);
    color: var(--text);
    font-size: 1rem;
}

.search-submit {
    padding: 0.6rem 1.5rem;
    background: var(--accent);
    color: #fff;
    border: none;
    border-radius: 6px;
    cursor: pointer;
    font-size: 1rem;
}

.search-section {
    margin: 2rem 0;
}

.search-section h2 {
    font-size: 1.1rem;
    margin-bottom: 1rem;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
}

.search-result {
    padding: 1rem 0;
    border-bottom: 1px solid var(--border);
}

.search-result-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.25rem;
}

.search-result-title {
    font-size: 1.1rem;
    font-weight: 600;
}

.search-result-snippet {
    color: var(--text-muted);
    font-size: 0.9rem;
    line-height: 1.5;
    margin: 0.25rem 0;
}

.search-result-snippet mark {
    background: rgba(37, 99, 235, 0.15);
    color: var(--text);
    padding: 0.1em 0.2em;
    border-radius: 2px;
}

.search-result-meta {
    font-size: 0.8rem;
    color: var(--text-muted);
}

.search-empty {
    text-align: center;
    padding: 3rem 0;
    color: var(--text-muted);
}

.search-pagination {
    display: flex;
    justify-content: space-between;
    margin: 2rem 0;
}

.pagination-link {
    padding: 0.5rem 1rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    color: var(--accent);
}

.badge-card {
    background: var(--text-muted);
    color: #fff;
}
```

### Step 5.13: Add search JavaScript

- [ ] Append search expand/collapse logic to `static/js/main.js`.

Add at the end of the file:

```javascript
// Search Toggle
document.addEventListener('DOMContentLoaded', function() {
    var searchToggle = document.getElementById('search-toggle');
    var searchForm = document.getElementById('nav-search-form');
    var searchInput = document.getElementById('nav-search-input');

    if (searchToggle && searchForm && searchInput) {
        searchToggle.addEventListener('click', function() {
            var isActive = searchForm.classList.contains('active');
            if (isActive) {
                // If there's text, submit. Otherwise, close.
                if (searchInput.value.trim()) {
                    searchForm.submit();
                } else {
                    searchForm.classList.remove('active');
                }
            } else {
                searchForm.classList.add('active');
                searchInput.focus();
            }
        });

        searchInput.addEventListener('keydown', function(e) {
            if (e.key === 'Escape') {
                searchForm.classList.remove('active');
                searchInput.value = '';
            }
        });
    }
});
```

### Step 5.14: Build and verify

- [ ] Run `go build ./cmd/milesahead/` and `go test ./internal/db/ -run "TestSync|TestSearch|TestDelete" -v`.

```bash
go build ./cmd/milesahead/ && go test ./internal/db/ -run "TestSync|TestSearch|TestDelete" -v
```

### Step 5.15: Commit

- [ ] Stage and commit with message: `feat: add full-text search with FTS5, search page, and nav search icon`

---

## Task 6: Auto-Update Detection

**Files:**
- Create: `internal/cardupdate/detector.go`
- Create: `internal/cardupdate/detector_test.go`
- Modify: `internal/ai/prompts.go` — add `CardUpdateDetectionPrompt`
- Modify: `internal/web/server.go` — add `CardUpdateDetector` interface + field
- Modify: `internal/web/admin.go` — call detector after approval
- Modify: `internal/db/db.go` — add `AppendRecentChange` helper
- Modify: `cmd/milesahead/main.go` — create Detector, inject into App

### Step 6.1: Add CardUpdateDetectionPrompt to prompts.go

- [ ] Add the AI prompt function to `internal/ai/prompts.go`.

```go
// CardUpdateDetectionPrompt builds a prompt for detecting whether an article
// contains information that should update a section of a reference card page.
func CardUpdateDetectionPrompt(articleTitle, articleContent, cardTitle, sectionTitle, currentSectionContent string) string {
	truncatedArticle := articleContent
	if len(truncatedArticle) > 2000 {
		truncatedArticle = truncatedArticle[:2000]
	}
	truncatedSection := currentSectionContent
	if len(truncatedSection) > 1000 {
		truncatedSection = truncatedSection[:1000]
	}

	return fmt.Sprintf(`You are a credit card reference page editor.

Does this article contain information that should update this section of the card page?

Article Title: %s
Article Content:
%s

Card: %s
Section: %s
Current Section Content:
%s

If the article contains NEW information that should update this section, return a JSON object:
{"action": "update", "content": "<updated section content in HTML>", "summary": "<one-line summary of what changed>"}

If the article does NOT contain information relevant to this section, return exactly:
NO_UPDATE

Rules:
- Only propose updates for concrete factual changes (new benefits, changed fees, new bonuses, etc.)
- Do NOT propose updates for general news or opinions
- The updated content should be the FULL section content (not just the diff), incorporating the new information
- Keep the same HTML formatting style as the current content
- Be conservative: when in doubt, return NO_UPDATE`, articleTitle, truncatedArticle, cardTitle, sectionTitle, truncatedSection)
}
```

### Step 6.2: Add AppendRecentChange to db.go

- [ ] Add helper method to `internal/db/db.go`.

```go
// AppendRecentChange appends a recent change entry to a reference page's recent_changes_json.
func (d *Database) AppendRecentChange(cardID int64, change RecentChange) error {
	page, err := d.GetReferencePage(cardID)
	if err != nil {
		return err
	}

	var changes []RecentChange
	if page.RecentChangesJSON != "" && page.RecentChangesJSON != "[]" {
		json.Unmarshal([]byte(page.RecentChangesJSON), &changes)
	}

	// Prepend new change (most recent first), keep max 10
	changes = append([]RecentChange{change}, changes...)
	if len(changes) > 10 {
		changes = changes[:10]
	}

	data, err := json.Marshal(changes)
	if err != nil {
		return err
	}

	_, err = d.db.Exec(`UPDATE reference_pages SET recent_changes_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(data), cardID)
	return err
}
```

### Step 6.3: Add CardUpdateDetector interface to App

- [ ] In `internal/web/server.go`, add the interface and field.

Add the interface above the `App` struct:

```go
// CardUpdateDetector detects and drafts card updates when articles are approved.
type CardUpdateDetector interface {
	DetectAndDraft(ctx context.Context, articleID int64) error
}
```

Add `"context"` to imports in `server.go`.

Add the field to `App`:

```go
type App struct {
	db             *db.Database
	cfg            *config.Config
	articleManager *article.Manager
	voteSvc        *vote.Service
	newsletterSvc  NewsletterService
	cardDetector   CardUpdateDetector
	templates      map[string]*template.Template
	mux            *http.ServeMux
	startedAt      time.Time
}
```

Update `NewApp` signature to accept the detector and a shutdown context:

```go
func NewApp(cfg *config.Config, database *db.Database, mgr *article.Manager, voteSvc *vote.Service, newsletterSvc NewsletterService, cardDetector CardUpdateDetector, shutdownCtx context.Context) (*App, error) {
	app := &App{
		db:             database,
		cfg:            cfg,
		articleManager: mgr,
		voteSvc:        voteSvc,
		newsletterSvc:  newsletterSvc,
		cardDetector:   cardDetector,
		mux:            http.NewServeMux(),
		startedAt:      time.Now(),
		shutdownCtx:    shutdownCtx,
	}
```

Also add `shutdownCtx context.Context` field to `App`:

```go
	shutdownCtx    context.Context
```

Update `NewAppWithTemplateDir` similarly (add `cardDetector CardUpdateDetector, shutdownCtx context.Context` params and set them).

### Step 6.4: Create the Detector struct

- [ ] Create `internal/cardupdate/detector.go`.

```go
package cardupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// sectionsToCheck are the reference page sections that may be updated by articles.
var sectionsToCheck = []string{
	"overview",
	"key_benefits",
	"fees",
	"signup_bonus",
	"earning_rates",
	"travel_credits_perks",
}

// Detector checks newly approved articles against reference pages and
// drafts AI-proposed section updates.
type Detector struct {
	client ai.HFClientInterface
	model  string
	db     *db.Database
	sem    chan struct{} // semaphore for concurrency control
}

// NewDetector creates a new Detector with the given AI client, model, and database.
func NewDetector(client ai.HFClientInterface, model string, database *db.Database) *Detector {
	return &Detector{
		client: client,
		model:  model,
		db:     database,
		sem:    make(chan struct{}, 2), // max 2 concurrent detections
	}
}

// DetectAndDraft checks if the approved article mentions any reference page cards
// and drafts AI-proposed updates for matching sections.
func (d *Detector) DetectAndDraft(ctx context.Context, articleID int64) error {
	// Acquire semaphore
	select {
	case d.sem <- struct{}{}:
		defer func() { <-d.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Panic recovery
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in card update detection", "article_id", articleID, "panic", r)
		}
	}()

	article, err := d.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("loading article: %w", err)
	}

	// Load all published reference pages with keywords
	pages, err := d.db.ListPublishedReferencePages("")
	if err != nil {
		return fmt.Errorf("loading reference pages: %w", err)
	}

	// Find cards whose keywords match the article
	var matchedCards []db.ReferencePage
	for _, page := range pages {
		var keywords []string
		if page.KeywordsJSON != "" && page.KeywordsJSON != "[]" {
			json.Unmarshal([]byte(page.KeywordsJSON), &keywords)
		}
		keywords = append(keywords, page.Title)

		contentLower := strings.ToLower(article.Title + " " + article.Content)
		for _, kw := range keywords {
			if kw != "" && strings.Contains(contentLower, strings.ToLower(kw)) {
				matchedCards = append(matchedCards, page)
				break
			}
		}

		if len(matchedCards) >= 3 {
			break
		}
	}

	if len(matchedCards) == 0 {
		slog.Info("no card matches for article", "article_id", articleID)
		return nil
	}

	slog.Info("card matches found", "article_id", articleID, "cards", len(matchedCards))

	for _, card := range matchedCards {
		// Check context between cards
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Append recent change
		err := d.db.AppendRecentChange(card.ID, db.RecentChange{
			Date:        time.Now().Format("2006-01-02"),
			Summary:     article.Title,
			ArticleID:   article.ID,
			ArticleSlug: article.Slug,
		})
		if err != nil {
			slog.Warn("failed to append recent change", "card_id", card.ID, "error", err)
		}

		// Parse sections
		var sections []db.ReferenceSection
		if card.SectionsJSON != "" && card.SectionsJSON != "[]" {
			json.Unmarshal([]byte(card.SectionsJSON), &sections)
		}

		// Check each eligible section
		for _, section := range sections {
			// Check context between AI calls
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Skip sections unlikely to change from news
			if !isCheckableSection(section.Key) {
				continue
			}

			prompt := ai.CardUpdateDetectionPrompt(
				article.Title, article.Content,
				card.Title, section.Title, section.Content,
			)

			resp, err := d.client.Generate(d.model, prompt, 1024)
			if err != nil {
				slog.Warn("AI call failed for section", "card", card.Title, "section", section.Key, "error", err)
				continue
			}

			resp = strings.TrimSpace(resp)
			if resp == "NO_UPDATE" || strings.HasPrefix(resp, "NO_UPDATE") {
				continue
			}

			// Parse the update response
			update, err := parseUpdateResponse(resp)
			if err != nil {
				slog.Warn("failed to parse update response", "card", card.Title, "section", section.Key, "error", err)
				continue
			}

			// Add to pending updates
			err = addPendingUpdate(d.db, card.ID, article.ID, article.Title, section.Key, section.Content, update.Content, update.Summary)
			if err != nil {
				slog.Warn("failed to store pending update", "card", card.Title, "section", section.Key, "error", err)
			} else {
				slog.Info("pending update created", "card", card.Title, "section", section.Key, "summary", update.Summary)
			}
		}
	}

	return nil
}

func isCheckableSection(key string) bool {
	for _, s := range sectionsToCheck {
		if s == key {
			return true
		}
	}
	return false
}

type updateResponse struct {
	Action  string `json:"action"`
	Content string `json:"content"`
	Summary string `json:"summary"`
}

func parseUpdateResponse(resp string) (*updateResponse, error) {
	// Find JSON in response
	if idx := strings.Index(resp, "{"); idx != -1 {
		resp = resp[idx:]
	}
	if idx := strings.LastIndex(resp, "}"); idx != -1 {
		resp = resp[:idx+1]
	}

	var update updateResponse
	if err := json.Unmarshal([]byte(resp), &update); err != nil {
		return nil, err
	}
	if update.Action != "update" {
		return nil, fmt.Errorf("unexpected action: %s", update.Action)
	}
	return &update, nil
}

func addPendingUpdate(database *db.Database, cardID, articleID int64, articleTitle, sectionKey, currentContent, proposedContent, summary string) error {
	page, err := database.GetReferencePage(cardID)
	if err != nil {
		return err
	}

	var updates []db.PendingUpdate
	if page.PendingUpdatesJSON != "" && page.PendingUpdatesJSON != "[]" {
		json.Unmarshal([]byte(page.PendingUpdatesJSON), &updates)
	}

	// Auto-increment ID
	maxID := 0
	for _, u := range updates {
		if u.ID > maxID {
			maxID = u.ID
		}
	}

	updates = append(updates, db.PendingUpdate{
		ID:              maxID + 1,
		ArticleID:       articleID,
		ArticleTitle:    articleTitle,
		SectionKey:      sectionKey,
		CurrentContent:  currentContent,
		ProposedContent: proposedContent,
		Summary:         summary,
		CreatedAt:       time.Now().Format("2006-01-02"),
	})

	data, err := json.Marshal(updates)
	if err != nil {
		return err
	}

	page.PendingUpdatesJSON = string(data)
	return database.UpdateReferencePage(page)
}
```

### Step 6.5: Wire detector into admin.go approve handler

- [ ] In `internal/web/admin.go`, update `handleApproveArticle` to call the detector in a goroutine.

Replace the current `handleApproveArticle`:

```go
func (app *App) handleApproveArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	if err := app.articleManager.Approve(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to approve: %v", err), http.StatusBadRequest)
		return
	}

	// Trigger auto-update detection in background (non-blocking)
	if app.cardDetector != nil {
		go app.cardDetector.DetectAndDraft(app.shutdownCtx, id)
	}

	http.Redirect(w, r, "/admin/dashboard?msg=approved", http.StatusSeeOther)
}
```

### Step 6.6: Wire detector into main.go

- [ ] In `cmd/milesahead/main.go`, create the Detector and pass it to `NewApp`.

Add import:
```go
	"github.com/kumaranik/milesahead/internal/cardupdate"
```

Before the `web.NewApp` call, create the detector:

```go
	// Card update detector
	detector := cardupdate.NewDetector(hfClient, cfg.AI.CriticModel, database)
```

Update the `NewApp` call:

```go
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc, newsletterSvc, detector, ctx)
```

### Step 6.7: Write detector tests

- [ ] Create `internal/cardupdate/detector_test.go`.

```go
package cardupdate

import (
	"context"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// mockAIClient implements ai.HFClientInterface for testing.
type mockAIClient struct {
	responses map[string]string
	calls     int
}

func (m *mockAIClient) Generate(model, prompt string, maxTokens int) (string, error) {
	m.calls++
	for keyword, resp := range m.responses {
		if contains(prompt, keyword) {
			return resp, nil
		}
	}
	return "NO_UPDATE", nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func setupTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestDetectAndDraft_NoMatches(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create an article with no card keywords
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:            "General Travel News",
		Slug:             "general-travel-news",
		Content:          "<p>Some general travel news with no card mentions.</p>",
		Type:             "summary",
		Status:           db.StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &mockAIClient{responses: map[string]string{}}
	detector := NewDetector(client, "test-model", database)

	err = detector.DetectAndDraft(context.Background(), id)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if client.calls != 0 {
		t.Errorf("expected 0 AI calls for no matches, got %d", client.calls)
	}
}

func TestDetectAndDraft_WithMatch(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create a reference page
	_, err := database.CreateReferencePage(&db.ReferencePage{
		Slug:               "amex-gold",
		Title:              "American Express Gold Card",
		Category:           "travel",
		Issuer:             "American Express",
		SectionsJSON:       `[{"key":"overview","title":"Overview","content":"<p>The Amex Gold is a mid-tier card.</p>"},{"key":"signup_bonus","title":"Signup Bonus","content":"<p>60K bonus points.</p>"}]`,
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		ScoutsVerdict:      "Good card",
		KeywordsJSON:       `["Amex Gold", "American Express Gold"]`,
		Status:             "published",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create an article that mentions the card
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:            "Amex Gold Bonus Increased to 75K",
		Slug:             "amex-gold-bonus-75k",
		Content:          "<p>American Express has increased the Amex Gold signup bonus to 75K points.</p>",
		Type:             "deal",
		Status:           db.StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}

	client := &mockAIClient{
		responses: map[string]string{
			"Signup Bonus": `{"action": "update", "content": "<p>75K bonus points after $6K spend in 6 months.</p>", "summary": "Bonus increased from 60K to 75K"}`,
		},
	}
	detector := NewDetector(client, "test-model", database)

	err = detector.DetectAndDraft(context.Background(), articleID)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Verify recent change was appended
	page, _ := database.GetReferencePageBySlug("amex-gold")
	if page.RecentChangesJSON == "[]" {
		t.Error("expected recent change to be appended")
	}

	// Verify pending update was created
	if page.PendingUpdatesJSON == "[]" {
		t.Error("expected pending update to be created")
	}
}

func TestDetectAndDraft_ContextCancellation(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create a reference page and article
	database.CreateReferencePage(&db.ReferencePage{
		Slug:               "test-card",
		Title:              "Test Card",
		Category:           "travel",
		Issuer:             "Test",
		SectionsJSON:       `[{"key":"overview","title":"Overview","content":"<p>Test.</p>"}]`,
		RecentChangesJSON:  "[]",
		PendingUpdatesJSON: "[]",
		KeywordsJSON:       `["Test Card"]`,
		Status:             "published",
	})

	articleID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:            "Test Card Update",
		Slug:             "test-card-update",
		Content:          "<p>Test Card has new features.</p>",
		Type:             "summary",
		Status:           db.StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	})

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &mockAIClient{responses: map[string]string{}}
	detector := NewDetector(client, "test-model", database)

	err := detector.DetectAndDraft(ctx, articleID)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestDetectAndDraft_MaxThreeCards(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Create 5 published reference pages
	for i := 0; i < 5; i++ {
		slug := fmt.Sprintf("card-%d", i)
		title := fmt.Sprintf("Card %d", i)
		database.CreateReferencePage(&db.ReferencePage{
			Slug:               slug,
			Title:              title,
			Category:           "travel",
			Issuer:             "Test",
			SectionsJSON:       `[{"key":"overview","title":"Overview","content":"<p>Overview.</p>"}]`,
			RecentChangesJSON:  "[]",
			PendingUpdatesJSON: "[]",
			KeywordsJSON:       fmt.Sprintf(`["%s"]`, title),
			Status:             "published",
		})
	}

	// Create an article mentioning all cards
	articleID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:            "All Cards Update",
		Slug:             "all-cards-update",
		Content:          "<p>Card 0, Card 1, Card 2, Card 3, Card 4 all have updates.</p>",
		Type:             "summary",
		Status:           db.StatusApproved,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		KeyDetails:       "[]",
		Links:            "[]",
		RelatedCardsJSON: "[]",
	})

	client := &mockAIClient{responses: map[string]string{}}
	detector := NewDetector(client, "test-model", database)

	err := detector.DetectAndDraft(context.Background(), articleID)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Should have checked at most 3 cards (max 3 sections = 3 AI calls max,
	// but "overview" is the only checkable section per card, so 3 calls)
	if client.calls > 3 {
		t.Errorf("expected at most 3 AI calls (3 cards * 1 checkable section), got %d", client.calls)
	}
}
```

Add `"fmt"` to the import block in the test file.

### Step 6.8: Run detector tests

- [ ] Run `go test ./internal/cardupdate/ -v`.

```bash
go test ./internal/cardupdate/ -v
```

Expected: all tests PASS.

### Step 6.9: Update existing callers of NewApp

- [ ] Update ALL existing callers of `NewApp` / `NewAppWithTemplateDir` to pass the new `cardDetector` and `shutdownCtx` parameters.

**Files that need updating (exhaustive list):**
- `cmd/milesahead/main.go` — pass the real detector and shutdown context
- `tests/integration_test.go` — 2 call sites (lines ~299, ~732)
- `internal/web/public_test.go` — 1 call site (line ~99)
- `internal/web/admin_test.go` — 2 call sites (lines ~133, ~687)
- `internal/web/auth_test.go` — 1 call site (line ~67)

For each test file, pass `nil` for `cardDetector` and `context.Background()` for `shutdownCtx`:

```go
app, err := web.NewApp(cfg, database, mgr, voteSvc, newsletterSvc, nil, context.Background())
```

For the health test in Step 2.4, since we directly construct the `App` struct, just add the new fields:

```go
	app := &App{
		db:          database,
		cfg:         &config.Config{},
		mux:         http.NewServeMux(),
		startedAt:   time.Now().Add(-5 * time.Minute),
		shutdownCtx: context.Background(),
	}
```

### Step 6.10: Build and verify

- [ ] Run `go build ./cmd/milesahead/` and `go test ./internal/cardupdate/ -v`.

```bash
go build ./cmd/milesahead/ && go test ./internal/cardupdate/ -v
```

### Step 6.11: Commit

- [ ] Stage and commit with message: `feat: add auto-update detection for reference pages on article approval`

---

## Post-Implementation Checklist

- [ ] Run `go test ./...` to verify all tests pass
- [ ] Run `go build ./cmd/milesahead/` to verify clean build
- [ ] Run `go build ./cmd/backfill-scouts-take/` to verify backfill script builds
- [ ] Verify the app starts: `go run ./cmd/milesahead/ -config config.yaml`
- [ ] Verify `/healthz` returns `{"status":"ok","db":"ok","uptime":"..."}`
- [ ] Verify `/search` page loads and search works
- [ ] Verify graceful shutdown: `kill -TERM <pid>` produces "shutdown complete"
