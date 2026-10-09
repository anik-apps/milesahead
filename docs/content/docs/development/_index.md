---
title: Development
weight: 5
---

## Project Structure

```
milesahead/
├── cmd/
│   ├── milesahead/           # The application: main.go (wiring, scheduler, shutdown)
│   │                         # and scan.go (clustering, duplicate checks, generation)
│   ├── seed-cards/           # Reference page seed tool + cards.json (53 cards)
│   ├── seed-structured/      # Structured comparison data seed tool + structured-data.json
│   ├── ab-models/            # Measures candidate models on real clusters (read-only)
│   ├── flag-outlet-mentions/ # Lists stored articles that name a news site (read-only)
│   ├── repair-editor-notes/  # Moves inline editor notes out of stored articles
│   └── backfill-scouts-take/ # Fills missing Scout's Takes through the enrichment guard
├── internal/
│   ├── ai/                   # Generator, critic chain, prompts, outlet and source-ref
│   │                         # guards, critic issue filter, preflight, HTTP client
│   ├── article/              # Article manager: decisions, undo, revisions, reconciler
│   ├── cardupdate/           # Drafts card section updates when an approved article affects a card
│   ├── cluster/              # Groups scanned posts into stories by title similarity
│   ├── config/               # YAML config loading and ${VAR} expansion
│   ├── db/                   # SQLite: schema and migrations, queries, search (FTS5),
│   │                         # admin queue, approval undo, image requests
│   ├── editornote/           # Splits admin-only editor notes out of article HTML
│   ├── enrich/               # Enrichment reconciler (Scout's Take, details, tags,
│   │                         # image, new-image requests) and stored-image resizing
│   ├── images/               # Image search (4 APIs), sizing, probes, identity, scoring
│   ├── infographic/          # An article's "At a glance" chart: reads and checks the
│   │                         # model's numbers, lays them out for the template
│   ├── linker/               # Links card mentions in articles to card pages
│   ├── mail/                 # SMTP sender (newsletter, confirmation, alerts) and IMAP poller
│   ├── metrics/              # Prometheus metric definitions
│   ├── newsletter/           # Weekly build and send, daily digest
│   ├── offer/                # Offer end dates from Key Details; "has it ended"
│   ├── scanner/              # RSS and scrape source scanner
│   ├── scheduler/            # Cron jobs, startup passes, overlap guard
│   ├── seeddata/             # Real card data + made-up articles for tests and previews
│   ├── storydedup/           # Story-level duplicate check before generation
│   ├── vote/                 # Voting with downvote alerts
│   └── web/                  # HTTP server: public pages, admin, subscribe, API
├── templates/                # Go HTML templates: public/, admin/, email/
├── static/                   # CSS, JS, images (card art, brand, infographics)
├── tests/                    # Integration tests (Go) and tests/js (node --test)
├── scripts/                  # Image and review helpers (see below)
├── deploy/                   # systemd units, Caddy site, Alloy config, backup script,
│                             # Grafana dashboards and alert rules
├── docs/                     # This documentation site (Hugo + Hextra)
├── config.example.yaml       # Example configuration, with the reasons for each value
├── Makefile                  # build, test, test-js, run, lint
└── go.mod                    # Go module definition
```

## Running Locally

```bash
# Build and run
make run

# Or build separately
make build
./milesahead -config config.yaml
```

The SQLite database is created automatically on first run with all tables and indexes. To preview the site with realistic content, seed the cards (`go run ./cmd/seed-cards` and `./cmd/seed-structured`); tests use `internal/seeddata`, which adds made-up articles to the same card data.

## Testing

```bash
# All Go tests
go test ./...

# With race detection, as CI and the deploy run them
go test ./... -race

# One package
go test ./internal/ai/... -v

# Integration tests only
go test ./tests/... -v

# The admin pages' browser-side logic (needs Node 18+)
make test-js            # node --test tests/js/*.test.mjs
```

Most packages have unit tests next to the code; handler tests render the real templates. Some tests worth knowing:

- `tests/integration_test.go` and `tests/newsletter_flow_test.go` (sign up, confirm from the email, per-reader `List-Unsubscribe`, one-click unsubscribe) run end to end.
- `cmd/milesahead` tests the scan loop through real scans (duplicate checks, story dedup), the revision reconciler's startup pass, bounded shutdown, and the deploy workflow's own "Generate application config" step: hostile secrets round-trip exactly, a non-numeric port is rejected, and the output passes `-check-config`.
- `internal/config` tests check that `config.example.yaml` loads with sane values (a pass shorter than its interval, an enrichment chain not led by the critic) and that `deploy.yml` mirrors its critic chain and story dedup mode.
- `tests/js/admin.test.mjs` covers the keyboard shortcuts, the row actions menus and the rewrite box's image reminder.

## Code Style

```bash
make lint    # go vet ./...
```

The project follows standard Go conventions:

- All packages under `internal/` are not importable externally
- Models are defined in `internal/db/models.go`
- Database migrations are in `internal/db/migrations.go` (run automatically on startup)
- Prompts are centralized in `internal/ai/prompts.go`

## CI

`.github/workflows/ci.yml` runs on every push and pull request to `main`:

1. **test:** `go vet ./...`, `go test ./... -v -race`, `node --test tests/js/*.test.mjs`, `go build ./cmd/milesahead`
2. **screenshot-test:** builds the binary, starts it with a test config, and captures desktop, tablet and mobile screenshots of the home and login pages with Playwright (uploaded as an artifact)

The deploy workflow repeats the test job before it builds anything; see [Deployment](../deployment/).

## Database Migrations

Migrations run automatically when the application starts (`migrate` in `internal/db/migrations.go`). Tables are created with `CREATE TABLE IF NOT EXISTS`; new columns are `ALTER TABLE ... ADD COLUMN` statements in the `alterStatements` slice, with "duplicate column" errors ignored, so every start is idempotent. After that, startup also:

- creates the search indexes, rebuilding them once when `ftsIndexVersion` changes (recorded in `search_index_versions`);
- reindexes any article that still holds an inline editor note;
- recomputes offer end dates (`expires_on`) from Key Details, writing only rows that changed.

To add a new column:

1. Add the field to the model struct in `internal/db/models.go`
2. Add an `ALTER TABLE` statement to `alterStatements` in `migrations.go`, with a comment saying why
3. Update the queries in `internal/db` that read or write it (every full article read scans the same column list)

## Adding a New Feature

1. **Database** -- add models to `internal/db/models.go` and migrations to `migrations.go`
2. **Business logic** -- create or extend a package under `internal/`
3. **Web handlers** -- add routes in `internal/web/routes.go`, handlers in the appropriate file
4. **Templates** -- add or modify Go templates in `templates/`
5. **Tests** -- unit tests in the package, integration tests in `tests/`, and `tests/js` for admin browser logic
6. **Config** -- add new fields to `internal/config/`, `config.example.yaml`, and the config the deploy workflow writes

## One-off and Maintenance Tools

Run these against a copy of the database, or take a backup first (`sudo systemctl start milesahead-backup.service` on the server).

| Tool | What it does |
|------|--------------|
| `cmd/ab-models` | Measures candidate models against real source material: real clustering, prompts and critic. Opens the database read-only. `-critic-candidates "a,b"` probes critics for the JSON contract; `-generators`, `-critic`, `-topics`, `-days`, `-critic-no-thinking`; `-full -out a.json` dumps whole articles, which `scripts/render_article_review.py` renders side by side. |
| `cmd/flag-outlet-mentions` | Lists articles that name or link to a news site, or carry a numbered source reference: id, status, title, field and the sentence. Read-only (`mode=ro`, `query_only`), with no write mode. `-status approved,pending` limits it. |
| `cmd/repair-editor-notes` | Moves editor notes still inline in stored content to `editor_notes`. A read-only dry run unless `-apply` is given; `-ids` limits it. Each article is one transaction (a compare-and-set on what was read, plus the search reindex); idempotent; takes no backup itself. |
| `cmd/backfill-scouts-take` | Fills missing Scout's Takes through `ai.GenerateScoutsTake`, the same guarded function enrichment uses. `-dry-run`, `-model` (a chain), `-api-key`, `-api-url`, `-persona`. |
| `cmd/seed-cards`, `cmd/seed-structured` | Seed card pages and comparison data; see [Reference Pages](../features/reference-pages/#seed-script). |

```bash
go run ./cmd/ab-models -critic-candidates "model-a,model-b" -generators model-a
go run ./cmd/flag-outlet-mentions -db ./data/milesahead.db -status approved,pending
go run ./cmd/repair-editor-notes -db ./data/milesahead.db          # dry run
go run ./cmd/repair-editor-notes -db ./data/milesahead.db -apply   # write
```

## Scripts

| Script | Purpose |
|--------|---------|
| `scripts/infographic-images.sh <slug> <png>` | Makes a card infographic and its teaser; see [Reference Pages](../features/reference-pages/#infographics) |
| `scripts/brand-images.sh` | Renders the PNG brand images (email, favicon, share image) from `scripts/brand/` with Chrome |
| `scripts/render_article_review.py` | Renders `ab-models -full` output as side-by-side HTML for review |
| `scripts/check_card_consistency.py` | Cross-checks card prose against structured card data and reports disagreements for a person to judge |

## Runbooks

`docs/runbooks/generation-drought.md` (in the repository, not on this site) covers the "no articles generated in 24h" alert.
