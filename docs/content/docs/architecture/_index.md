---
title: Architecture
weight: 2
---

## System Overview

```
                    +-----------+
                    |  Sources  |  (RSS feeds, blog scraping)
                    +-----+-----+
                          |
                    +-----v-----+
                    |  Scanner  |  internal/scanner
                    +-----+-----+
                          |
                    +-----v-----+
                    | Clustering |  internal/cluster
                    +-----+-----+
                          |
              +-----------v-----------+
              |      AI Pipeline      |  internal/ai
              |  Generator -> Critic  |
              |  -> Revision -> Score |
              +-----------+-----------+
                          |
                    +-----v-----+
                    |  Images   |  internal/images
                    | Search +  |
                    |  Scoring  |
                    +-----+-----+
                          |
              +-----------v-----------+
              |   Article Manager     |  internal/article
              | (status, revisions)   |
              +-----------+-----------+
                          |
              +-----------v-----------+
              |      Web Server       |  internal/web
              | Public + Admin + API  |
              +-----------+-----------+
                          |
              +-----------v-----------+
              |   SQLite Database     |  internal/db
              +-----------------------+
```

## Key Components

| Package | Directory | Purpose |
|---------|-----------|---------|
| Scanner | `internal/scanner` | Fetches RSS feeds and scrapes blog pages on configurable intervals |
| Clustering | `internal/cluster` | Groups raw articles about the same topic using content similarity |
| AI Pipeline | `internal/ai` | Generates articles, runs dual-critic verification, handles revisions |
| Article Manager | `internal/article` | Manages article lifecycle (draft, pending, approved, rejected) and revisions |
| Web Server | `internal/web` | HTTP server with public pages, admin dashboard, and JSON API |
| Database | `internal/db` | SQLite database with WAL mode, migrations, and model definitions |
| Scheduler | `internal/scheduler` | Cron-based job scheduling for scanning, newsletter, and maintenance |
| Images | `internal/images` | Multi-source image search and two-phase AI scoring |
| Metrics | `internal/metrics` | Prometheus metrics for monitoring (critic scores, image search, pipeline) |
| Newsletter | `internal/newsletter` | Daily newsletter generation and email delivery |
| Offers | `internal/offer` | Reads an article's offer end date from its Key Details (stored as `expires_on`) and decides whether it has ended in the site's timezone |
| Config | `internal/config` | YAML configuration loading with environment variable interpolation |
| Votes | `internal/vote` | Reader voting system with anomaly detection alerts |

## Database Tables

The application uses SQLite with the following tables:

| Table | Purpose |
|-------|---------|
| `sources` | Blog sources with RSS URLs and scrape selectors |
| `raw_articles` | Scraped articles with content-hash deduplication |
| `generated_articles` | AI-generated articles with status, metadata, and structured fields |
| `article_sources` | Many-to-many link between generated and raw articles |
| `verification_results` | Critic scores and check results per revision |
| `admin_feedback` | Admin or email feedback on articles |
| `article_revisions` | Revision history with before/after content |
| `article_votes` | Reader upvote/downvote records (IP-hashed) |
| `newsletters` | Generated newsletter records with send status |
| `subscribers` | Email subscribers with double opt-in confirmation |
| `admin_sessions` | Session tokens with expiry |
| `reference_pages` | Curated reference pages (credit cards) with structured sections |

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | Go |
| Database | SQLite (WAL mode) via `modernc.org/sqlite` |
| AI Models | NVIDIA API (Nemotron, Llama 70B, Llama Vision) |
| HTTP | Go standard library `net/http` |
| Templates | Go `html/template` |
| RSS Parsing | `gofeed` |
| HTML Scraping | `goquery` |
| Scheduling | `robfig/cron` |
| Email | `gomail` (SMTP), `go-imap` (IMAP) |
| Metrics | Prometheus client |
| Hosting | OCI ARM (free tier), Caddy reverse proxy, systemd |
