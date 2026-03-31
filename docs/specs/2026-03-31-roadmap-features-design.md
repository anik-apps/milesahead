# Roadmap Features — Design Specification

**Date:** 2026-03-31

---

## 1. Overview

Six features to improve infrastructure reliability, content quality, and user experience:

1. Graceful shutdown with signal handling
2. Health check endpoint
3. Auto-linking card mentions in articles
4. Scout's Take backfill for existing articles
5. Full-text search on articles and reference pages
6. Auto-update detection for reference pages

---

## 2. Feature 1: Graceful Shutdown

### Goal
Handle SIGINT/SIGTERM cleanly — stop accepting requests, finish in-progress work, shut down within a timeout.

### Design
- Replace `http.ListenAndServe` with an explicit `http.Server` struct to enable `.Shutdown()`
- Create `context.Context` with cancel at top of `main()`
- Listen for `SIGINT` and `SIGTERM` via `os/signal.Notify`
- On signal: log "shutting down...", cancel context, stop scheduler (`sched.Stop()`), call `httpServer.Shutdown(ctx)` with 30-second timeout
- Pass shutdown context to scan job closure (captured from outer scope) so in-progress AI calls respect cancellation via `ctx.Done()` checks
- If timeout expires, force exit
- Log "shutdown complete"

### Files
- Modify: `cmd/milesahead/main.go`

### No new dependencies.

---

## 3. Feature 2: Health Check

### Goal
Expose `GET /healthz` for monitoring and load balancers.

### Design
**Response (healthy):**
```json
{"status": "ok", "db": "ok", "uptime": "2h35m"}
```
HTTP 200.

**Response (degraded):**
```json
{"status": "degraded", "db": "error", "error": "...", "uptime": "2h35m"}
```
HTTP 503.

- Runs `SELECT 1` against SQLite to verify DB connectivity
- Includes uptime (time since process start)
- No auth required, no rate limiting
- JSON response (not HTML)

### Files
- Modify: `internal/web/public.go` — add handler
- Modify: `internal/web/routes.go` — add route

---

## 4. Feature 3: Auto-Linking Card Mentions in Articles

### Goal
When a new article is generated, automatically link card name mentions to reference pages and show related cards in the sidebar.

### Design

**Auto-link function:**
```go
func AutoLink(content string, cards []db.ReferencePage) (linkedContent string, matched []db.ReferencePage)
```
- Scans article HTML for keyword matches (case-insensitive)
- Replaces first mention of each card with `<a href="/cards/{slug}">{keyword}</a>`
- Skips if keyword is already inside an `<a>` tag or `<code>` block
- Only links first mention per card per article
- Keywords sorted by length descending (longer matches first)
- Returns modified HTML + list of matched cards

**Storage for related cards:**
- New column: `ALTER TABLE generated_articles ADD COLUMN related_cards_json TEXT DEFAULT '[]'`
- Stores JSON array of `[{"slug": "amex-platinum", "title": "American Express Platinum Card"}]`
- Populated during scan job, read at template render time

**Integration:**
- Called in the scan job in `main.go` (after `pipeline.Process()` and before `CreateFromPipeline()`) — the pipeline itself does not perform auto-linking; `main.go` calls `linker.AutoLink()` and populates `PipelineResult.RelatedCards` before passing to the article manager
- `PipelineResult.Content` is replaced with the linked HTML; `PipelineResult.RelatedCards` is a new field populated by `main.go`

**Sidebar:**
- Article handler reads `related_cards_json`, passes to template as `RelatedCards`
- Rendered as a "Related Cards" section in the sidebar with links to `/cards/{slug}`

### Files
- New: `internal/linker/linker.go` — auto-link function (isolated, testable)
- New: `internal/linker/linker_test.go`
- Modify: `internal/db/migrations.go` — add `related_cards_json` column
- Modify: `internal/db/models.go` — add `RelatedCardsJSON` field to `GeneratedArticle`
- Modify: `internal/db/db.go` — add `related_cards_json` to ALL `GeneratedArticle` SELECT lists and `Scan()` calls (cross-cutting: `GetGeneratedArticle`, `GetGeneratedArticleBySlug`, `ListArticlesByStatus`, `ListPublishedArticles`, `ListPublishedArticlesByType`, `GetFeaturedArticle`, `scanGeneratedArticles`, `CreateGeneratedArticle`)
- Modify: `internal/ai/pipeline.go` — add `RelatedCards` to `PipelineResult`
- Modify: `cmd/milesahead/main.go` — call after pipeline, before save
- Modify: `internal/article/manager.go` — persist `RelatedCardsJSON` in `CreateFromPipeline`
- Modify: `internal/web/public.go` — parse and pass related cards to article template
- Modify: `templates/public/article.html` — add Related Cards sidebar section

### Rules
- Only new articles get auto-linked (not retroactive)
- Max one link per card per article
- Don't link inside existing `<a>` tags or `<code>` blocks

---

## 5. Feature 4: Scout's Take Backfill

### Goal
Generate Scout's Take for all existing approved articles that don't have one.

### Design

**One-time script:** `cmd/backfill-scouts-take/main.go`

1. Query all approved articles where `scouts_take = ''`
2. For each, call NVIDIA API with `ScoutsTakePrompt(personaName, title, tldr, content)`
3. Update the article's `scouts_take` field in DB
4. 1-second delay between API calls
5. Log progress per article
6. On API error: log the error, skip the article, continue with the next one
7. At the end, report: N succeeded, M failed, K skipped

**Flags:**
- `-db` — database path (default `./data/milesahead.db`)
- `-api-key` — NVIDIA API key (required)
- `-api-url` — API base URL (default `https://integrate.api.nvidia.com/v1`)
- `-persona` — persona name (default `Scout`)
- `-dry-run` — show which articles would be updated without calling API

### Files
- New: `cmd/backfill-scouts-take/main.go`

### No changes to the main app.

---

## 6. Feature 5: Full-Text Search

### Goal
Search across articles and reference pages with a dedicated search page and nav search icon.

### Database

**FTS5 virtual tables** (in migrations):
```sql
CREATE VIRTUAL TABLE IF NOT EXISTS articles_fts USING fts5(title, tldr, content);
CREATE VIRTUAL TABLE IF NOT EXISTS cards_fts USING fts5(title, sections_text, scouts_verdict);
```

**Note:** We use standalone FTS tables (not content-sync) to avoid trigger complications with `sections_json` → plain text extraction. The Go code manages sync explicitly.

**Sync approach (Go-managed, not triggers):**
- On article insert: call `db.SyncArticleFTS(id)` which strips HTML from content and upserts into `articles_fts`
- On reference page insert/update: call `db.SyncCardFTS(id)` which extracts plain text from `sections_json` (iterates sections, strips HTML from each content field), and upserts into `cards_fts`
- On delete: call `db.DeleteArticleFTS(id)` / `db.DeleteCardFTS(id)`
- FTS sync is called only on **content-modifying operations**: `CreateGeneratedArticle`, `CreateReferencePage`, `UpdateReferencePage`. NOT on `UpdateArticleStatus` — status filtering is handled at query time via JOIN, so status-only changes don't need FTS resync

**Initial population:** A migration function in Go that populates both FTS tables from existing data, stripping HTML as needed.

**Status filtering:** Search queries JOIN back to the source table to filter:
- Articles: `WHERE ga.status = 'approved'`
- Cards: `WHERE rp.status = 'published'`

**HTML in snippets:** Content is stripped of HTML tags before indexing into FTS, so `snippet()` produces clean text excerpts.

### Search Handler

`GET /search?q=term&page=1`

- Query both FTS tables using FTS5 `MATCH` syntax
- Use `snippet()` for highlighted result excerpts
- Combine results: articles first, then cards
- 20 results per page with next/prev links
- Pagination strategy: query articles first (up to 20), then cards to fill remaining slots. If articles fill the page, cards appear on subsequent pages. This avoids complex UNION queries.
- Empty query → show search page with empty state

### Search UI

**Nav bar:** Search icon (magnifying glass) that expands to a small text input on click. Pressing Enter or clicking the icon submits to `/search?q=`.

**Search page (`/search`):** Full search box at top, results below grouped into "Articles" and "Cards" sections. Each result shows:
- Type badge (article type or "Card")
- Title (linked)
- Snippet with highlighted match
- Date (for articles) or issuer (for cards)

Pagination: "Previous" / "Next" links at bottom when results exceed 20.

### Files
- Modify: `internal/db/migrations.go` — FTS5 tables + initial population
- Modify: `internal/db/db.go` — `SyncArticleFTS`, `SyncCardFTS`, `DeleteArticleFTS`, `DeleteCardFTS`, `SearchArticles(query, limit, offset)`, `SearchCards(query, limit, offset)` methods
- Modify: `internal/web/routes.go` — `GET /search`
- Modify: `internal/web/public.go` — search handler
- New: `templates/public/search.html` — results page
- Modify: `templates/layout.html` — search icon in nav
- Modify: `static/css/style.css` — search bar and results styles
- Modify: `static/js/main.js` — expand/collapse nav search input

---

## 7. Feature 6: Auto-Update Detection

### Goal
When an article is approved, detect if it mentions any reference page cards, add to recent changes, and draft AI-proposed section updates for admin review.

### Trigger
After `UpdateArticleStatus(id, "approved")` in the admin approve handler. Runs in a background goroutine (non-blocking).

### Dependency Injection
The `Detector` struct is created in `main.go` with the AI client and DB, then injected into `App` as an interface:
```go
type CardUpdateDetector interface {
    DetectAndDraft(ctx context.Context, articleID int64) error
}
```
The approve handler calls `app.cardDetector.DetectAndDraft(ctx, id)` in a goroutine, using the **application-level shutdown context** (not the request context, which is cancelled after redirect). This avoids adding the AI client directly to `App`.

The `App` struct gains a `cardDetector CardUpdateDetector` field. The `NewApp` constructor gains a corresponding parameter. In `main.go`, the `Detector` is created with the AI client and DB, then passed to `NewApp`.

### Flow

1. Load the approved article (title + content + slug)
2. Load all published reference pages with keywords
3. For each card whose keywords match the article (max 3 cards):
   a. **Recent change:** Append to `recent_changes_json`:
      ```json
      {"date": "2026-03-31", "summary": "Article title here", "article_id": 123, "article_slug": "article-slug"}
      ```
   b. **AI update draft:** For key sections (overview, key_benefits, fees, signup_bonus, earning_rates, travel_credits_perks), call AI with `CardUpdateDetectionPrompt`. AI returns either `NO_UPDATE` or proposed new content + summary.
   c. Store proposed updates in `pending_updates_json` with auto-incrementing ID (max existing ID + 1)
   d. Save the card

### AI Prompt
New function: `CardUpdateDetectionPrompt(articleTitle, articleContent, cardTitle, sectionTitle, currentSectionContent) string`

Asks: "Does this article contain information that should update this section of the card page? If yes, return the updated section content and a one-line summary. If no, return NO_UPDATE."

### Concurrency Control
- A buffered semaphore channel (capacity 2) limits concurrent detection goroutines
- Each goroutine wraps in `defer recover()` to catch panics without crashing the server
- Goroutines respect the graceful shutdown context from Feature 1 — check `ctx.Done()` between AI calls

### Rate Limiting
- Max 3 cards per article approval
- Skip sections unlikely to change from news: "competitor_comparison", "best_for", "redemption_options"
- 6 sections checked per card × 3 cards max = 18 AI calls max per approval

### Admin Review
Already implemented:
- `GET /admin/cards/{id}/updates` — shows pending updates
- `POST /admin/cards/{id}/updates/{uid}/approve` — applies update to section
- `POST /admin/cards/{id}/updates/{uid}/reject` — removes pending update

### Files
- New: `internal/cardupdate/detector.go` — detection logic + `Detector` struct
- New: `internal/cardupdate/detector_test.go`
- Modify: `internal/ai/prompts.go` — `CardUpdateDetectionPrompt`
- Modify: `internal/web/server.go` — add `CardUpdateDetector` interface to `App`
- Modify: `internal/web/admin.go` — call detector after article approval (goroutine with semaphore)
- Modify: `internal/db/db.go` — `AppendRecentChange(cardID, change)` helper

---

## 8. Implementation Order

```
Feature 1 (Graceful Shutdown) → Feature 2 (Health Check) → independent after this
                                                            ├─ Feature 3 (Auto-Linking)
                                                            ├─ Feature 4 (Scout Backfill)
                                                            ├─ Feature 5 (Full-Text Search)
                                                            └─ Feature 6 (Auto-Update Detection)
```

Features 1 and 2 are sequential (shutdown first, then health check uses the same server pattern). Features 3-6 are independent and can be implemented in any order.

Recommended order: 1 → 2 → 3 → 4 → 5 → 6

---

## 9. Testing

| Feature | Test Approach |
|---------|--------------|
| Graceful Shutdown | Manual test: send SIGTERM, verify clean exit within 30s |
| Health Check | Unit test: mock DB, verify 200/503 responses with uptime |
| Auto-Linking | Unit test: test `AutoLink()` with various HTML inputs, `<a>` tags, `<code>` blocks, keyword overlaps |
| Scout Backfill | Integration test: run script against test DB with mock API; verify skip-on-error behavior |
| Full-Text Search | Unit test: insert test data, sync FTS, verify queries return correct results with status filtering |
| Auto-Update Detection | Unit test: mock AI responses, verify pending updates stored correctly; test semaphore limits; test panic recovery |
