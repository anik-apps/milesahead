---
title: Architecture
weight: 2
toc: false
width: wide
---

MilesAhead is one Go binary on a small OCI ARM VM. Every three hours it scans
miles-and-points blogs, groups posts about the same story, skips stories it has
already written, and has a model on the NVIDIA API write the article and a second
model check it. An admin approves or rejects each article in the dashboard, while
a background pass adds Scout's Take, key details, tags and a photo. The same
binary serves the public site and the admin behind Caddy, sends the weekly
newsletter through OCI Email Delivery, and exposes metrics that Grafana Alloy
ships to Grafana Cloud. Everything lives in one SQLite file, backed up daily and
copied off the server.

## System overview

{{< architecture >}}

## Key components

| Package | Purpose |
|---------|---------|
| `internal/scanner` | Fetches RSS feeds and scrapes blog pages into `raw_articles`; SHA-256 of title and link rejects duplicates |
| `internal/cluster` | Groups the last 48 hours of posts about the same story by title similarity (`scanner.cluster_threshold`) |
| `internal/storydedup` | Story-level duplicate check against every article from the last 7 days: TF-IDF prefilter, then one short AI call (`scanner.story_dedup`) |
| `internal/ai` | Generator with fallbacks, critic chain, auto-revision, the passed-check filter, source-reference and news-site guards, key details, model preflight, prompts |
| `internal/editornote` | Splits admin-only editor notes (`<div class="editor-note">`) out of article content |
| `internal/enrich` | Enrichment reconciler: Scout's Take, key details, tags and the image every 15 minutes; resizes stored images; serves "Find a new image" requests |
| `internal/images` | Image search across four APIs, text and vision scoring, URL sizing and byte caps, and picture identity across sizes (`identity.go`) |
| `internal/article` | Article manager: status changes with the revision guard, undo, rewrites and the revision reconciler |
| `internal/cardupdate` | After an approval, drafts updates to credit-card pages the article affects, for the admin to review |
| `internal/linker` | Finds card mentions in an article and links them to the card pages |
| `internal/db` | SQLite (WAL) access, migrations, the FTS5 search indexes, the admin queue, image requests, approval undo, story coverage |
| `internal/web` | Public site, admin dashboard, sign-up and unsubscribe, JSON API, error pages, caching headers |
| `internal/scheduler` | Cron jobs: scan, enrichment, revision reconciler, newsletter build and send, mail polling |
| `internal/newsletter` | Weekly issue: build, AI pick, Card of the Week, per-reader send; the admin's manual send |
| `internal/mail` | Outbound SMTP (`outbound.go`: confirmations, newsletters with per-reader `List-Unsubscribe`, vote alerts to `votes.alert_email`) and the IMAP review-reply poller (`inbound.go`, not running in production) |
| `internal/offer` | Reads an offer's end date from its key details (`expires_on`) and decides, in Hawaii time, whether it has ended |
| `internal/metrics` | Prometheus metrics served at `/metrics` |
| `internal/vote` | Reader votes with downvote alerts: counted over `votes.alert_window`, recorded in `vote_alerts` for the admin Published page |
| `internal/config` | YAML config; `${NAME}` expands only when it is a whole value |
| `internal/seeddata` | Real card data plus made-up articles for tests and local previews, never production data |

The commands in `cmd/` are the server (`milesahead`, with the scan loop in
`scan.go`) and its tools: `seed-cards` and `seed-structured` (card pages and
comparison data), `ab-models` (measure candidate models), `flag-outlet-mentions`
(read-only report of articles naming a news site), `repair-editor-notes` (move old
inline notes out) and `backfill-scouts-take`. See
[Development]({{< relref "/docs/development" >}}) for how to run them.

## Database tables

One SQLite file in WAL mode. Migrations in `internal/db/migrations.go` run at every
start and are idempotent.

| Table | Purpose |
|-------|---------|
| `sources` | Blogs and news sites to scan, with RSS URL, scrape selector and interval |
| `raw_articles` | Scanned posts, unique by a hash of title and link |
| `generated_articles` | Articles with status, revision number, structured fields and enrichment state (columns below) |
| `article_sources` | Which posts an article was written from |
| `covered_raws` | Posts an article covers without citing them: late coverage skipped as a duplicate, with the reason. Read only by the duplicate check, so an article's sources stay as written |
| `verification_results` | Critic score, issues and the four checks for each revision |
| `admin_feedback` | Rewrite requests, from the dashboard or (in code) email |
| `article_revisions` | Each rewrite's before and after content, linked to the feedback it answered |
| `approval_undo` | The last approval of each article and the state before it, for the dashboard's 30-second Undo |
| `article_image_requests` | Pending or failed "Find a new image" requests, with the admin's search words and a sequence number |
| `article_rejected_images` | Pictures an admin turned down for an article, never picked for it again |
| `article_votes` | Reader up and down votes, one per hashed IP |
| `vote_alerts` | Articles whose recent votes crossed the alert line, with the counts, until the admin acknowledges them (at most one open per article) |
| `newsletters` | Issues with their HTML, status and send time |
| `newsletter_candidates` | The articles ranked for a weekly issue, and which is featured |
| `subscribers` | Newsletter addresses, confirmation and unsubscribe state |
| `admin_sessions` | Admin session tokens and expiry |
| `reference_pages` | Credit-card pages: sections, verdict, keywords, structured comparison data, pending updates, flags |
| `articles_fts`, `cards_fts` | FTS5 search indexes (porter stemming, accents folded) over articles and cards |
| `search_index_versions` | The version each search index was built with; a change rebuilds it once at startup |

Columns added to `generated_articles` over time: `tldr`, `key_details`, `links`,
`image_url`, `image_thumbnail`, `image_attribution`, `scouts_take`, `tags_json`,
`related_cards_json`, `linked_cards_heading`, `enrichment_attempts`,
`enrichment_last_at`, `expires_on` (the offer's last day), `revision_failures`,
`revision_retry_after`, `revision_last_error` (the revision reconciler's retry
state) and `editor_notes` (admin-only). `subscribers` gained `unsubscribed_at` (an
unsubscribe keeps the row) and `confirmation_sent_unix` (the 10-minute resend
cooldown).

## Tech stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.26, standard library `net/http` and `html/template` |
| Database | SQLite in WAL mode via `modernc.org/sqlite` (pure Go), FTS5 for search |
| AI models | NVIDIA API: `nvidia/nemotron-3-ultra-550b-a55b` writes (fallbacks `openai/gpt-oss-20b`, `nvidia/nemotron-3-super-120b-a12b`); critic chain `nvidia/nemotron-3-super-120b-a12b` then `moonshotai/kimi-k3`, thinking off; short calls on `openai/gpt-oss-20b` then `nvidia/nemotron-3-super-120b-a12b`; vision `meta/llama-3.2-11b-vision-instruct` |
| Images | Unsplash, Pexels, Openverse and Wikimedia Commons APIs |
| Feeds and scraping | `gofeed`, `goquery` |
| Scheduling | `robfig/cron` v3 |
| Email | `gomail` over SMTP to OCI Email Delivery; `go-imap` v2 for the (unused) reply poller |
| Front end | Server-rendered HTML, plain CSS and vanilla JavaScript, tested with `node --test` |
| Hosting | OCI ARM VM (VM.Standard.A1.Flex, `us-sanjose-1`, Always Free), Ubuntu, systemd; Caddy in Docker, shared with other sites |
| DNS | Cloudflare |
| Observability | Prometheus metrics, Grafana Alloy, Grafana Cloud (metrics, logs, dashboards, alerts) |
| CI/CD | GitHub Actions: tests gate every deploy |
| Docs | Hugo with the Hextra theme, on Cloudflare Pages |
