---
title: Getting Started
weight: 1
---

## Prerequisites

- **Go 1.26+** (`go.mod` pins `go 1.26.1`)
- **Node 18+**, only for `make test-js` (the admin pages' browser-side tests)
- **NVIDIA API key** for AI generation and critique (free tier available at [build.nvidia.com](https://build.nvidia.com))
- Optional: Unsplash and Pexels API keys for the image pipeline (Openverse and Wikimedia Commons need none)
- Optional: an SMTP relay, for confirmation emails and the newsletter

## Quick Start

```bash
# Clone the repository
git clone https://github.com/anik-apps/milesahead.git
cd milesahead

# Copy and edit the configuration
cp config.example.yaml config.yaml
# Edit config.yaml with your API key and admin password hash

# Build and run
go build -o milesahead ./cmd/milesahead
./milesahead -config config.yaml
```

The server listens on `server.port` (`config.example.yaml` uses `8080`; production uses `9090`). Visit `/` for the public site and `/admin/login` for the admin dashboard. The SQLite database is created on first run, with every table and index.

To check a config file without starting anything (the deploy runs this on the config it generates):

```bash
./milesahead -check-config -config config.yaml   # "config ok: ..." or exit 1
```

## Configuration

All settings live in a single `config.yaml` file. `config.example.yaml` documents every key, and the reasons behind the production values. A value that is exactly `${VAR_NAME}` is replaced with that environment variable after the file is parsed; `${...}` inside a longer value is left alone, so secret contents are never expanded again.

| Section | Key | Description |
|---------|-----|-------------|
| `server` | `port` | HTTP port |
| `server` | `domain` | Public domain name, used for absolute links (email, feeds, og tags) |
| `admin` | `password_hash` | bcrypt hash of the admin password |
| `db` | `path` | Path to the SQLite database file |
| `scanner` | `default_interval` | Minutes between scans (180 in production) |
| `scanner` | `cluster_threshold` | Minimum title similarity for two posts to be one story (0.30; 0 uses `cluster.DefaultThreshold`) |
| `scanner` | `user_agent` | HTTP User-Agent for scanning |
| `scanner` | `story_dedup` | Story-level duplicate check: `shadow` (default), `block` or `off` |
| `ai` | `generator_model` | Model for article generation (`nvidia/nemotron-3-ultra-550b-a55b`) |
| `ai` | `fallback_generator_model` | Comma-separated chain tried when the generator fails or is retired |
| `ai` | `critic_model` | Model, or comma-separated chain tried in order, for verification only (`nvidia/nemotron-3.5-super-vl-120b-a12b,nvidia/nemotron-3.5-lightning-30b-a3b`) |
| `ai` | `critic_disable_thinking` | Send `enable_thinking: false` on critic requests only (default `false`; production `true`) |
| `ai` | `critic2_model` | Optional second critic; empty in production |
| `ai` | `vision_model` | Image relevance scoring: a model, or a comma-separated list tried in order (default `meta/llama-3.2-11b-vision-instruct`) |
| `ai` | `request_timeout` | Seconds per generation or verification call (900 in production) |
| `ai` | `enrichment_model` | Comma-separated chain of fast models for every short AI call: story dedup, enrichment, image text-scoring, newsletter pick, card-update detection (`openai/gpt-oss-20b,nvidia/nemotron-3.5-super-vl-120b-a12b`). Unset, the generator fallbacks then the critics are used |
| `ai` | `enrichment_interval` | Minutes between enrichment passes (default 15) |
| `ai` | `enrichment_max_attempts` | Passes before an article is left alone (3) |
| `ai` | `enrichment_max_duration` | Minutes one enrichment pass may run (10) |
| `ai` | `hf_api_token` | NVIDIA API key |
| `ai` | `hf_base_url` | API base URL (`https://integrate.api.nvidia.com/v1`) |
| `ai` | `critic_threshold` | Score at or above which an article is saved as `pending` rather than `draft`. There is no default in code, so set it (production `75`) |
| `ai` | `max_retries` | Retries per model call on a retryable error |
| `ai` | `cache_ttl` | AI response cache TTL in seconds |
| `ai` | `unsplash_key` | Unsplash API key for image search |
| `ai` | `pexels_key` | Pexels API key for image search |
| `ai` | `alert_email` | Where model-health alerts are emailed (a model dying, a list on a backup or empty, publishing stopped). Empty uses `votes.alert_email`; with neither, alerts show only on the admin Models tab. Production sets it from the `ALERT_EMAIL` secret. See [Model Health](../features/model-health/) |
| `ai` | `health_check_interval` | Minutes between checks of every model list (absent or 0 is 60; negative turns the hourly check off, and one still runs at boot) |
| `mail` | `smtp.host`, `smtp.port`, `smtp.user`, `smtp.password`, `smtp.from` | Outbound email; see [Email](../deployment/email/) |
| `mail` | `imap.*` | Mailbox for review-by-email replies. Review by email is not built, so the poller never starts; startup logs `review by email is off (mail.imap not configured)` with the deploy's placeholders, and the settings page says "Review by email: off" |
| `newsletter` | `send_day` | Day the weekly issue goes out: a weekday name or its first three letters (default `saturday`). The issue is built at 20:00 the day before |
| `newsletter` | `send_time` | Time it goes out, 24-hour `HH:MM` (default `08:00`) |
| `newsletter` | `timezone` | IANA zone for `send_day` and `send_time`, and for which day counts as "today" for issue dates (default `America/New_York`). The sign-up page, confirmation email and admin pages say the schedule in words ("every Saturday at 8:00 AM ET"). A value that cannot be read fails the config check |
| `newsletter` | `signup_enabled` | Opens newsletter sign-up (default `false`; on in production). See [Newsletter](../features/newsletter/#the-sign-up-switch) |
| `newsletter` | `unsubscribe_mailbox` | Adds a `mailto:` to `List-Unsubscribe`; leave empty unless someone reads that mailbox |
| `votes` | `alert_threshold` | Minimum downvotes in the window before alerting |
| `votes` | `alert_ratio` | Share of the window's votes that must be downvotes |
| `votes` | `alert_window` | Hours of votes the alert check counts (production 24; 0 counts all votes) |
| `votes` | `alert_email` | Where vote alerts are also emailed (the `ALERT_EMAIL` deploy secret). Empty by default: alerts are shown on the admin Published page only |
| `site` | `persona_name` | AI persona name (default `Scout`) |

## Admin Password Hash

Generate a bcrypt hash for your admin password:

```bash
htpasswd -nbBC 12 "" "your-password" | tr -d ':\n' | sed 's/$2y/$2a/'
```

Or use any bcrypt generator and paste the hash into `config.yaml` under `admin.password_hash`.

## Seeding Reference Pages

The project ships with 53 credit card reference pages in `cmd/seed-cards/cards.json`, and their structured comparison data in `cmd/seed-structured/structured-data.json`. Seed them into the database:

```bash
go run ./cmd/seed-cards -db ./data/milesahead.db
go run ./cmd/seed-structured -db ./data/milesahead.db
```

Both skip cards that already exist. Use `-force` to overwrite, and `-slug` to limit that to named cards:

```bash
# Overwrite only named cards -- -force alone rewrites every card
go run ./cmd/seed-cards -db ./data/milesahead.db -force -slug amex-platinum,amex-green
```

Tests and local previews load the same card data, plus 22 made-up articles, through `internal/seeddata`, never a copy of the production database.

## Adding Sources

Sources are managed on the admin **Sources** page. Each source needs:

- **Name** -- display name, used internally (articles never name it)
- **URL** -- base URL of the blog
- **RSS URL** (optional) -- RSS feed URL for scanning
- **Scrape Selector** (optional) -- CSS selector for HTML scraping fallback
- **Scan Interval** (optional) -- override the default interval in minutes

The page shows each source's last article, articles this week and total, and tints sources with nothing new for days. Every source, active or not, is on the list of outlets articles must not name; see [AI Pipeline](../architecture/ai-pipeline/#source-references-and-outlet-mentions).
