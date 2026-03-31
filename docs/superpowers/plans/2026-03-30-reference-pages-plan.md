# Reference Pages & Scout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add curated reference pages for credit cards with Scout persona verdicts, auto-linking from articles, and auto-update detection.

**Architecture:** New `reference_pages` DB table with flexible JSON sections. Public pages at `/cards/{slug}`. Admin CRUD at `/admin/cards`. Auto-linking as post-processing on article generation. Auto-update detection scans new articles for card keyword matches and drafts section updates.

**Tech Stack:** Go, SQLite, Go html/template, NVIDIA AI API for content generation

**Config:** Persona name is configurable via `config.yaml` under `site.persona_name` (default: "Scout"). All templates and prompts reference this config value.

**Spec:** `docs/superpowers/specs/2026-03-30-reference-pages-and-scout-design.md`

---

## Phase 1: Foundation (DB + Admin + Public Pages)

### Task 1: Database — reference_pages table and model

**Files:**
- Modify: `internal/db/migrations.go`
- Modify: `internal/db/models.go`
- Modify: `internal/db/db.go`

- [ ] **Step 1: Add ReferenceSection, RecentChange, PendingUpdate structs to models.go**

```go
type ReferenceSection struct {
    Key     string `json:"key"`
    Title   string `json:"title"`
    Content string `json:"content"`
}

type RecentChange struct {
    Date        string `json:"date"`
    Summary     string `json:"summary"`
    ArticleID   int64  `json:"article_id"`
    ArticleSlug string `json:"article_slug"`
}

type PendingUpdate struct {
    ID              int    `json:"id"`
    ArticleID       int64  `json:"article_id"`
    ArticleTitle    string `json:"article_title"`
    SectionKey      string `json:"section_key"`
    CurrentContent  string `json:"current_content"`
    ProposedContent string `json:"proposed_content"`
    Summary         string `json:"summary"`
    CreatedAt       string `json:"created_at"`
}

type ReferencePage struct {
    ID                int64
    Slug              string
    Title             string
    Category          string
    Issuer            string
    SectionsJSON      string
    RecentChangesJSON string
    PendingUpdatesJSON string
    ScoutsVerdict     string
    KeywordsJSON      string
    ImageURL          string
    Status            string
    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

- [ ] **Step 1b: Add persona config to config.go**

Add to `internal/config/config.go`:
```go
type SiteConfig struct {
    PersonaName string `yaml:"persona_name"`
}
```
Add `Site SiteConfig` to the main `Config` struct. Default to "Scout" if empty. Add to `config.yaml` and `config.example.yaml`:
```yaml
site:
  persona_name: "Scout"
```

- [ ] **Step 2: Add migration for reference_pages table**

Add to `internal/db/migrations.go`:
```sql
CREATE TABLE IF NOT EXISTS reference_pages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT UNIQUE NOT NULL,
    title TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT 'credit-card',
    issuer TEXT NOT NULL DEFAULT '',
    sections_json TEXT NOT NULL DEFAULT '[]',
    recent_changes_json TEXT NOT NULL DEFAULT '[]',
    pending_updates_json TEXT NOT NULL DEFAULT '[]',
    scouts_verdict TEXT NOT NULL DEFAULT '',
    keywords_json TEXT NOT NULL DEFAULT '[]',
    image_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_reference_pages_slug ON reference_pages(slug);
CREATE INDEX IF NOT EXISTS idx_reference_pages_category ON reference_pages(category);
```

Also add `scouts_take` column to generated_articles:
```sql
ALTER TABLE generated_articles ADD COLUMN scouts_take TEXT DEFAULT '';
```

- [ ] **Step 3: Add CRUD methods to db.go**

```go
func (d *Database) CreateReferencePage(page *ReferencePage) (int64, error)
func (d *Database) GetReferencePage(id int64) (*ReferencePage, error)
func (d *Database) GetReferencePageBySlug(slug string) (*ReferencePage, error)
func (d *Database) ListReferencePages(category string) ([]ReferencePage, error)
func (d *Database) ListPublishedReferencePages(category string) ([]ReferencePage, error)
func (d *Database) UpdateReferencePage(page *ReferencePage) error
func (d *Database) DeleteReferencePage(id int64) error
func (d *Database) GetReferencePagesByKeyword(keyword string) ([]ReferencePage, error)
```

- [ ] **Step 4: Run tests, verify build**

```bash
go build ./cmd/milesahead
go test ./internal/db/ -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/db/
git commit -m "feat: reference_pages table, model, and CRUD"
```

---

### Task 2: Admin — Reference page management

**Files:**
- Modify: `internal/web/admin.go`
- Modify: `internal/web/routes.go`
- Create: `templates/admin/cards.html`
- Create: `templates/admin/card-edit.html`
- Create: `templates/admin/card-updates.html`

- [ ] **Step 1: Add admin handlers**

In `internal/web/admin.go`:
```go
func (app *App) handleAdminCards(w, r)           // GET /admin/cards — list all
func (app *App) handleAdminCardNew(w, r)         // GET /admin/cards/new — new form
func (app *App) handleAdminCardCreate(w, r)      // POST /admin/cards — create
func (app *App) handleAdminCardEdit(w, r)        // GET /admin/cards/{id}/edit — edit form
func (app *App) handleAdminCardUpdate(w, r)      // POST /admin/cards/{id}/edit — save
func (app *App) handleAdminCardDelete(w, r)      // POST /admin/cards/{id}/delete
func (app *App) handleAdminCardUpdates(w, r)     // GET /admin/cards/{id}/updates — pending
func (app *App) handleApproveCardUpdate(w, r)    // POST /admin/cards/{id}/updates/{uid}/approve
func (app *App) handleRejectCardUpdate(w, r)     // POST /admin/cards/{id}/updates/{uid}/reject
```

- [ ] **Step 2: Add routes**

In `internal/web/routes.go`, add inside the adminMux:
```go
adminMux.HandleFunc("GET /admin/cards", app.handleAdminCards)
adminMux.HandleFunc("GET /admin/cards/new", app.handleAdminCardNew)
adminMux.HandleFunc("POST /admin/cards", app.handleAdminCardCreate)
adminMux.HandleFunc("GET /admin/cards/{id}/edit", app.handleAdminCardEdit)
adminMux.HandleFunc("POST /admin/cards/{id}/edit", app.handleAdminCardUpdate)
adminMux.HandleFunc("POST /admin/cards/{id}/delete", app.handleAdminCardDelete)
adminMux.HandleFunc("GET /admin/cards/{id}/updates", app.handleAdminCardUpdates)
adminMux.HandleFunc("POST /admin/cards/{id}/updates/{uid}/approve", app.handleApproveCardUpdate)
adminMux.HandleFunc("POST /admin/cards/{id}/updates/{uid}/reject", app.handleRejectCardUpdate)
```

- [ ] **Step 3: Create templates**

`templates/admin/cards.html`:
- List all reference pages grouped by issuer
- Show title, category, status, section count, pending update count
- Links to edit, delete, view updates
- "Add New Card" button

`templates/admin/card-edit.html`:
- Form with: title, slug, category, issuer, keywords (comma-separated), image URL, status
- For each section: title, key, content (textarea with HTML)
- Scout's Verdict textarea
- "Add Section" button (JS to add new section fieldset)
- Save/Cancel buttons

`templates/admin/card-updates.html`:
- List pending updates for this card
- Each shows: article title, section affected, diff (current vs proposed), date
- Approve/Reject buttons per update

- [ ] **Step 4: Add "Cards" to admin nav**

Update all admin templates to add "Cards" link in the admin nav bar.

- [ ] **Step 5: Run tests, verify build**

- [ ] **Step 6: Commit**

```bash
git commit -m "feat: admin CRUD for reference pages with section editing"
```

---

### Task 3: Public — Card listing and detail pages

**Files:**
- Modify: `internal/web/public.go`
- Modify: `internal/web/routes.go`
- Create: `templates/public/cards.html`
- Create: `templates/public/card.html`
- Modify: `static/css/style.css`
- Modify: `templates/layout.html`

- [ ] **Step 1: Add public handlers**

```go
func (app *App) handleCardsList(w, r)    // GET /cards/ — listing grouped by issuer
func (app *App) handleCardDetail(w, r)   // GET /cards/{slug} — full card page
```

- [ ] **Step 2: Add routes**

```go
app.mux.HandleFunc("GET /cards/", app.handleCardsList)
app.mux.HandleFunc("GET /cards/{slug}", app.handleCardDetail)
```

- [ ] **Step 3: Create card listing template**

`templates/public/cards.html`:
- Hero section: "Credit Card Guide — by Scout"
- Cards grouped by issuer in a grid
- Each card shows: image, title, one-liner from Scout's verdict, issuer badge
- Link to `/cards/{slug}`

- [ ] **Step 4: Create card detail template**

`templates/public/card.html`:
- Hero: card image + title + issuer badge
- Table of contents (jump links to each section)
- Each section rendered as HTML with anchor IDs
- Scout's Verdict section with Scout branding
- Recent Changes section (chronological, with article links)
- Responsive: sections stack on mobile

- [ ] **Step 5: Add CSS for card pages**

```css
.card-page-hero { ... }
.card-toc { ... }
.card-section { ... }
.scouts-verdict { background; border-left accent; ... }
.recent-changes { ... }
.card-grid { display: grid; ... }
.card-grid-item { ... }
```

- [ ] **Step 6: Add "Cards" to main navigation**

Update `templates/layout.html` — add "Cards" link in nav.

- [ ] **Step 7: Run tests, verify build**

- [ ] **Step 8: Commit**

```bash
git commit -m "feat: public card listing and detail pages with Scout's Verdict"
```

---

### Task 4: Seed 19 cards with default sections

**Files:**
- Create: `cmd/seed-cards/main.go` (one-time tool)

- [ ] **Step 1: Create seed script**

Script that inserts 19 cards with:
- Title, slug, issuer, category="credit-card"
- Keywords array (card name variants)
- Empty sections with correct structure (10 sections per card, content TBD)
- Status: "draft"

The 19 cards from the spec with their keywords.

- [ ] **Step 2: Run seed script locally**

```bash
go run ./cmd/seed-cards
```

- [ ] **Step 3: Verify in admin UI**

Visit `/admin/cards` — should see 19 draft cards.

- [ ] **Step 4: Clean up seed script, commit**

```bash
git commit -m "feat: seed 19 credit card reference pages"
```

---

## Phase 2: AI Content Generation

### Task 5: Generate content for all 19 cards

**Files:**
- Modify: `internal/ai/prompts.go`
- Create: `cmd/generate-cards/main.go` (one-time tool)

- [ ] **Step 1: Add card content generation prompt**

```go
func CardContentPrompt(title, issuer string, sectionTitles []string) string
```

Prompt asks AI to generate content for each section of a credit card page. Uses the same critic model (Llama 3.3 70B) for structured output.

- [ ] **Step 2: Add Scout's Verdict prompt**

```go
func ScoutsVerdictPrompt(title string, sections []ReferenceSection) string
```

Prompt generates Scout's opinionated verdict based on the card's sections.

- [ ] **Step 3: Create generation script**

Script that:
1. Loads all draft cards from DB
2. For each card, generates content for each section via AI
3. Generates Scout's Verdict
4. Updates the card in DB
5. Logs progress

- [ ] **Step 4: Run generation**

```bash
go run ./cmd/generate-cards
```

- [ ] **Step 5: Review in admin, publish**

- [ ] **Step 6: Commit**

---

## Phase 3: Auto-Linking

### Task 6: Link card mentions in articles

**Files:**
- Modify: `internal/ai/pipeline.go` or `cmd/milesahead/main.go`

- [ ] **Step 1: Add auto-linker function**

```go
func autoLinkCards(content string, cards []ReferencePage) string
```

- Loads all published reference pages with their keywords
- Scans article content for keyword matches
- Replaces first occurrence with `<a href="/cards/{slug}">{keyword}</a>`
- Skips if already inside an `<a>` tag
- Only links first mention per card per article

- [ ] **Step 2: Wire into article creation pipeline**

In the scan job in main.go, after `pipeline.Process()` and before `CreateFromPipeline()`:
```go
cards, _ := database.ListPublishedReferencePages("")
result.Content = autoLinkCards(result.Content, cards)
```

- [ ] **Step 3: Add related cards to article sidebar**

When an article mentions cards, add them to the sidebar:
```go
result.RelatedCards = findRelatedCards(result.Content, cards)
```

- [ ] **Step 4: Update article template sidebar**

Add "Related Cards" section to `templates/public/article.html` sidebar.

- [ ] **Step 5: Commit**

---

## Phase 4: Auto-Update Detection

### Task 7: Detect and draft updates for reference pages

**Files:**
- Modify: `cmd/milesahead/main.go`
- Modify: `internal/ai/prompts.go`

- [ ] **Step 1: Add update detection prompt**

```go
func CardUpdateDetectionPrompt(articleTitle, articleContent, cardTitle, currentSectionContent string) string
```

Asks AI: "Does this article contain information that should update this section of the card page? If yes, draft the updated section content."

- [ ] **Step 2: Add detection to scan job**

After article creation, for each matched card:
1. Add entry to `recent_changes_json`
2. For each section, call detection prompt
3. If AI says update needed, add to `pending_updates_json`
4. Save card

- [ ] **Step 3: Admin notification for pending updates**

Show badge count on "Cards" nav link when pending updates exist.

- [ ] **Step 4: Commit**

---

## Phase 5: Scout's Take on Articles

### Task 8: Add Scout's Take to generated articles

**Files:**
- Modify: `internal/ai/prompts.go`
- Modify: `internal/ai/pipeline.go`
- Modify: `internal/article/manager.go`
- Modify: `templates/public/article.html`
- Modify: `static/css/style.css`

- [ ] **Step 1: Add Scout's Take prompt**

```go
func ScoutsTakePrompt(title, tldr, content string) string
```

1-2 sentence opinionated take from Scout about the article.

- [ ] **Step 2: Generate Scout's Take in pipeline**

After article generation, make a separate AI call to generate Scout's Take. Store in `scouts_take` field.

- [ ] **Step 3: Add to article template**

```html
{{if .ScoutsTake}}
<div class="scouts-take">
    <div class="scout-label">Scout's Take</div>
    <p>{{.ScoutsTake}}</p>
</div>
{{end}}
```

- [ ] **Step 4: Add CSS**

```css
.scouts-take {
    background: var(--card-bg);
    border-left: 4px solid var(--accent);
    padding: 1rem 1.25rem;
    margin: 1.5rem 0;
    border-radius: 0 8px 8px 0;
}
.scout-label {
    font-weight: 700;
    font-size: 0.85rem;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--accent);
    margin-bottom: 0.5rem;
}
```

- [ ] **Step 5: Commit**

---

## Task Dependencies

```
Task 1 (DB) → Task 2 (Admin) → Task 3 (Public) → Task 4 (Seed)
                                                 → Task 5 (AI Generate)
Task 1 → Task 6 (Auto-Link) [can run after Task 1]
Task 1 → Task 7 (Auto-Update) [can run after Task 2]
Task 1 → Task 8 (Scout's Take) [independent]
```

**Parallelizable after Task 1:**
- Tasks 2+3+4 (sequential)
- Task 5 (after 4)
- Task 6 (after 1)
- Task 7 (after 2)
- Task 8 (after 1)
