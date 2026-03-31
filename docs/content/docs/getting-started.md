---
title: Getting Started
weight: 1
---

## Prerequisites

- **Go 1.22+** (the project uses Go modules)
- **NVIDIA API key** for AI generation and critique (free tier available at [build.nvidia.com](https://build.nvidia.com))
- Optional: Unsplash, Pexels, and Pixabay API keys for the image pipeline

## Quick Start

```bash
# Clone the repository
git clone https://github.com/anik-apps/milesahead.git
cd milesahead

# Copy and edit the configuration
cp config.example.yaml config.yaml
# Edit config.yaml with your API keys and settings

# Build and run
go build -o milesahead ./cmd/milesahead
./milesahead
```

The server starts on port `9090` by default. Visit `http://localhost:9090` for the public site and `http://localhost:9090/admin/login` for the admin dashboard.

## Configuration

All settings live in a single `config.yaml` file. Environment variables can be interpolated with `${VAR_NAME}` syntax.

| Section | Key | Description |
|---------|-----|-------------|
| `server` | `port` | HTTP port (default `9090`) |
| `server` | `domain` | Public domain name |
| `admin` | `password_hash` | bcrypt hash of admin password |
| `db` | `path` | Path to SQLite database file |
| `ai` | `generator_model` | Model for article generation (e.g. `nvidia/llama-3.3-nemotron-super-49b-v1.5`) |
| `ai` | `critic_model` | Model for article verification (e.g. `meta/llama-3.3-70b-instruct`) |
| `ai` | `hf_api_token` | NVIDIA API key |
| `ai` | `hf_base_url` | API base URL (e.g. `https://integrate.api.nvidia.com/v1`) |
| `ai` | `critic_threshold` | Minimum critic score to auto-approve (default `75`) |
| `ai` | `max_retries` | Max generation retries on failure |
| `ai` | `cache_ttl` | AI response cache TTL in seconds |
| `ai` | `unsplash_key` | Unsplash API key for image search |
| `ai` | `pexels_key` | Pexels API key for image search |
| `ai` | `pixabay_key` | Pixabay API key for image search |
| `scanner` | `default_interval` | Default scan interval in minutes |
| `scanner` | `user_agent` | HTTP User-Agent for scanning |
| `site` | `persona_name` | AI persona name (default `Scout`) |
| `mail` | `smtp.*` | SMTP settings for outbound email |
| `mail` | `imap.*` | IMAP settings for inbound email feedback |
| `newsletter` | `send_time` | Daily newsletter send time (e.g. `08:00`) |
| `newsletter` | `timezone` | Timezone for newsletter scheduling |
| `votes` | `alert_threshold` | Minimum votes before alerting |
| `votes` | `alert_ratio` | Downvote ratio that triggers an alert |
| `votes` | `alert_window` | Alert window in hours |

## Admin Password Hash

Generate a bcrypt hash for your admin password:

```bash
htpasswd -nbBC 12 "" "your-password" | tr -d ':\n' | sed 's/$2y/$2a/'
```

Or use any online bcrypt generator and paste the hash into `config.yaml` under `admin.password_hash`.

## Seeding Reference Pages

The project ships with 41 credit card reference pages in `cmd/seed-cards/cards.json`. Seed them into the database:

```bash
go run ./cmd/seed-cards -db ./data/milesahead.db
```

Use `-force` to overwrite existing cards:

```bash
go run ./cmd/seed-cards -db ./data/milesahead.db -force
```

## Adding Sources

Sources are managed through the admin dashboard at `/admin`. Each source needs:

- **Name** -- display name for attribution
- **URL** -- base URL of the blog
- **RSS URL** (optional) -- RSS feed URL for scanning
- **Scrape Selector** (optional) -- CSS selector for HTML scraping fallback
- **Scan Interval** (optional) -- override the default interval in minutes
