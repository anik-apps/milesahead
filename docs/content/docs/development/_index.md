---
title: Development
weight: 5
---

## Project Structure

```
milesahead/
├── cmd/
│   ├── milesahead/          # Main application entry point
│   │   └── main.go
│   ├── seed-cards/          # Reference page seed tool
│   │   ├── main.go
│   │   └── cards.json       # 52 credit card definitions
│   └── seed-structured/     # Structured comparison data seed tool
│       ├── main.go
│       └── structured-data.json
├── internal/
│   ├── ai/                  # AI pipeline (generator, critic, prompts, cache)
│   ├── article/             # Article manager and revision handling
│   ├── cluster/             # Source article clustering
│   ├── config/              # YAML config loading
│   ├── db/                  # SQLite database, models, migrations
│   ├── images/              # Image search (5 APIs) and scoring
│   ├── mail/                # SMTP/IMAP email handling
│   ├── metrics/             # Prometheus metrics definitions
│   ├── newsletter/          # Newsletter generation and delivery
│   ├── scanner/             # RSS/scrape source scanner
│   ├── scheduler/           # Cron job scheduling
│   ├── vote/                # Voting system with anomaly detection
│   └── web/                 # HTTP server, routes, admin, public, auth
├── templates/               # Go HTML templates
├── static/                  # CSS, JS, images
├── tests/                   # Integration tests
├── deploy/                  # Deployment configs (systemd, caddy, alloy)
├── data/                    # SQLite database (gitignored)
├── config.yaml              # Local configuration
├── config.example.yaml      # Example configuration
├── Makefile                 # Build, test, run, lint targets
└── go.mod                   # Go module definition
```

## Running Locally

```bash
# Build and run
make run

# Or build separately
make build
./milesahead -config config.yaml
```

The server starts on the configured port. The SQLite database is created automatically on first run with all tables and indexes.

## Testing

```bash
# Run all tests with verbose output
make test

# Run all tests with race detection
go test ./... -v -race

# Run tests for a specific package
go test ./internal/ai/... -v

# Run integration tests only
go test ./tests/... -v
```

The project includes unit tests for most packages:

| Package | Test File | Coverage |
|---------|-----------|----------|
| `internal/ai` | `cache_test.go`, `critic_test.go`, `generator_test.go`, `hfclient_test.go`, `pipeline_test.go` | Pipeline flow, scoring, caching |
| `internal/article` | `manager_test.go`, `revision_test.go` | Article lifecycle, revisions |
| `internal/cluster` | `cluster_test.go` | Clustering logic |
| `internal/db` | `db_test.go` | Database operations |
| `internal/newsletter` | `newsletter_test.go` | Newsletter generation |
| `internal/scheduler` | `scheduler_test.go` | Job scheduling |
| `internal/vote` | `vote_test.go` | Voting and anomaly detection |
| `internal/web` | `admin_test.go`, `auth_test.go`, `public_test.go` | HTTP handlers |
| `tests/` | `integration_test.go` | End-to-end integration |

## Code Style

```bash
# Run static analysis
make lint

# This runs:
go vet ./...
```

The project follows standard Go conventions:
- All packages under `internal/` are not importable externally
- Models are defined in `internal/db/models.go`
- Database migrations are in `internal/db/migrations.go` (run automatically on startup)
- Prompts are centralized in `internal/ai/prompts.go`

## Database Migrations

Migrations run automatically when the application starts. They are defined in `internal/db/migrations.go` and use `CREATE TABLE IF NOT EXISTS` and `ALTER TABLE ADD COLUMN` with duplicate-column error handling for idempotency.

To add a new column:

1. Add the field to the model struct in `internal/db/models.go`
2. Add an `ALTER TABLE` statement to the `alterStatements` slice in `migrations.go`
3. Update any relevant queries in `internal/db/db.go`

## Adding a New Feature

A typical workflow for adding a new feature:

1. **Database** -- add models to `internal/db/models.go` and migrations to `migrations.go`
2. **Business logic** -- create or extend a package under `internal/`
3. **Web handlers** -- add routes in `internal/web/routes.go`, handlers in the appropriate file (`admin.go`, `public.go`, or `api.go`)
4. **Templates** -- add or modify Go templates in `templates/`
5. **Tests** -- write unit tests in the package and integration tests in `tests/`
6. **Config** -- add any new config fields to `internal/config/` and `config.example.yaml`
