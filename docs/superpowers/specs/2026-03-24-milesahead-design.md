# Miles Ahead — Design Specification

**Project:** Miles & Points Article Aggregation Platform
**Domain:** `milesahead.kumaranik.com`
**Date:** 2026-03-24

---

## 1. Overview

Miles Ahead is a self-hosted content aggregation platform that scans major miles and points websites, generates articles using free AI inference models, and publishes them after admin review. The system prioritizes zero cost (all free-tier services), editorial quality (dual-model AI pipeline with human oversight), and simplicity (single Go binary).

### Goals
- Aggregate content from 6+ miles/points sources automatically
- Generate three article types: deal alerts, roundups, and summaries
- Ensure accuracy via a two-model AI pipeline (generator + critic)
- Give admin full editorial control with email-based and dashboard-based workflows
- Serve a public magazine-style website with newsletter support
- Run entirely on free-tier infrastructure

### Non-Goals
- Real-time content (2-4 hour scan interval is acceptable)
- Multi-user admin (single admin for now, OAuth-ready for later)
- Mobile app
- Monetization features

---

## 2. Architecture

### Monolith Design

Single Go binary containing all components, deployed to an OCI free-tier compute instance.

```
┌─────────────────────────────────────────────────────────┐
│                   Miles Ahead (Go Binary)                │
│                                                          │
│  ┌──────────┐  ┌──────────────┐  ┌────────────────────┐ │
│  │ Scheduler │→│   Scanner    │→│    AI Pipeline      │ │
│  │  (cron)   │  │ RSS + HTML  │  │ Generator → Critic  │ │
│  └──────────┘  └──────────────┘  └────────────────────┘ │
│                                          ↓               │
│  ┌──────────────┐  ┌────────────┐  ┌─────────────────┐  │
│  │ Mail Inbound │  │Mail Outbound│  │ Article Manager │  │
│  │ (Gmail IMAP) │  │(OCI SMTP)  │  │ (state machine) │  │
│  └──────────────┘  └────────────┘  └─────────────────┘  │
│                                          ↓               │
│  ┌──────────────────────────┐  ┌──────────────────────┐  │
│  │     SQLite (WAL mode)    │←│     Web Server       │  │
│  └──────────────────────────┘  │  Admin + Public      │  │
│                                 └──────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

### Justification
- Single VM deployment, no IPC overhead
- Go handles concurrency with goroutines (scanner, mail poller, web server run concurrently)
- SQLite with WAL mode supports concurrent reads safely
- Easy to reason about, test, and deploy

---

## 3. Data Model

### sources
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | Auto-increment |
| name | TEXT | Display name (e.g., "The Points Guy") |
| url | TEXT | Website URL |
| rss_url | TEXT | RSS feed URL (nullable, falls back to HTML scraping) |
| scrape_selector | TEXT | CSS selector for HTML scraping (nullable) |
| scan_interval | INTEGER | Override interval in minutes (nullable, uses default) |
| active | BOOLEAN | Whether to scan this source |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### raw_articles
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| source_id | INTEGER FK | References sources.id |
| title | TEXT | Original article title |
| url | TEXT | Original article URL |
| content | TEXT | Extracted article content |
| published_at | DATETIME | Original publish date |
| hash | TEXT UNIQUE | SHA-256 of title+url for dedup |
| created_at | DATETIME | When we scraped it |

### generated_articles
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| title | TEXT | Generated article title |
| slug | TEXT UNIQUE | URL-friendly slug |
| content | TEXT | Generated article HTML with inline source citations |
| type | TEXT | "deal", "roundup", "summary" |
| status | TEXT | "draft", "pending", "approved", "rejected", "revision_requested", "unpublished" |
| sources_json | TEXT | JSON array of source URLs/titles for quick display |
| revision_number | INTEGER | Starts at 1, increments on revision |
| featured | BOOLEAN | Whether to show in featured slot |
| created_at | DATETIME | |
| updated_at | DATETIME | |
| published_at | DATETIME | When admin approved (nullable) |

### article_sources
| Column | Type | Description |
|---|---|---|
| generated_article_id | INTEGER FK | |
| raw_article_id | INTEGER FK | |
| PRIMARY KEY | composite | (generated_article_id, raw_article_id) |

### verification_results
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| generated_article_id | INTEGER FK | |
| revision_number | INTEGER | Which revision was verified |
| score | INTEGER | 0-100 accuracy score |
| issues_json | TEXT | JSON array of specific issues found |
| factual_check | BOOLEAN | All numbers/dates match sources |
| attribution_check | BOOLEAN | All claims have source citations |
| completeness_check | BOOLEAN | No key info missed from sources |
| hallucination_check | BOOLEAN | No unsupported claims |
| verified_at | DATETIME | |

### admin_feedback
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| generated_article_id | INTEGER FK | |
| feedback_text | TEXT | Admin's revision instructions |
| source | TEXT | "email" or "dashboard" |
| created_at | DATETIME | |

### article_revisions
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| generated_article_id | INTEGER FK | |
| revision_number | INTEGER | |
| previous_content | TEXT | Content before revision |
| new_content | TEXT | Content after revision |
| feedback_id | INTEGER FK | References admin_feedback.id |
| created_at | DATETIME | |

### article_votes
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| article_id | INTEGER FK | References generated_articles.id |
| vote | TEXT | "up" or "down" |
| ip_hash | TEXT | SHA-256 of voter IP (privacy-preserving dedup) |
| created_at | DATETIME | |

### newsletters
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| date | DATE UNIQUE | One newsletter per day |
| subject | TEXT | Email subject line |
| html_content | TEXT | Rendered newspaper-style HTML |
| status | TEXT | "draft", "sent" |
| article_count | INTEGER | |
| sent_at | DATETIME | |

### subscribers
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| email | TEXT UNIQUE | |
| confirmed | BOOLEAN | Double opt-in confirmed |
| token | TEXT UNIQUE | Confirmation/unsubscribe token |
| subscribed_at | DATETIME | |
| confirmed_at | DATETIME | |

### admin_sessions
| Column | Type | Description |
|---|---|---|
| token | TEXT PK | Session token |
| expires_at | DATETIME | |
| created_at | DATETIME | |

---

## 4. Article Status Workflow

```
                    ┌──────────────────────────────────────┐
                    ↓                                      │
draft → pending → approved → unpublished                   │
            │                    │                         │
            ├→ rejected          └→ feedback → Generator   │
            │                                    ↓         │
            └→ revision_requested → Generator → Critic ──→─┘
                      ↑                ↓
                      │            Critic
                      │                ↓
                      └──── (if fails threshold)
                                stays draft
```

**State transitions:**
- `draft`: AI-generated but critic score below threshold, or HF API error
- `pending`: Passed critic verification, awaiting admin review
- `approved`: Admin approved, visible on public site
- `rejected`: Admin rejected, archived
- `revision_requested`: Admin provided feedback, awaiting AI regeneration
- `unpublished`: Was approved but taken down (admin action or vote threshold), enters revision cycle

---

## 5. Scanner

### Source Configuration (initial)
| Source | RSS | Fallback |
|---|---|---|
| The Points Guy | Yes | HTML scrape |
| One Mile at a Time | Yes | HTML scrape |
| Doctor of Credit | Yes | HTML scrape |
| FlyerTalk | Yes (forum feed) | HTML scrape |
| View from the Wing | Yes | HTML scrape |
| The Frequent Miler | Yes | HTML scrape |

### Scanning Flow
1. Scheduler triggers every 2-4 hours (configurable per source)
2. For each active source:
   - Try RSS feed first → parse entries
   - If RSS unavailable/fails → fall back to HTML scraping with configured CSS selectors
3. For each article found:
   - Compute SHA-256 hash of title+URL
   - Skip if hash exists in `raw_articles` (dedup)
   - Store new articles in `raw_articles`
4. After scanning all sources, trigger topic clustering

### Topic Clustering
- Group raw articles by topic similarity (title + content overlap)
- Simple approach: TF-IDF cosine similarity on titles, threshold-based grouping
- Each cluster becomes a candidate for article generation
- Single-source articles generate deal alerts or summaries
- Multi-source clusters generate roundups or cross-referenced summaries

---

## 6. AI Pipeline

### Models (Hugging Face Inference API — Free Tier)
| Role | Default Model | Rationale |
|---|---|---|
| Generator | Mistral 7B Instruct | Strong structured writing, good free availability |
| Critic | Llama 3 8B Instruct | Different family avoids shared blind spots, analytical |

Both configurable via `config.yaml`. Can swap to any HF-hosted model.

### Generation

**Input:** Clustered raw articles + article type determination

**Prompt strategy by type:**
- **Deal Alert:** Extract key details (program, bonus percentage, dates, requirements). Write concise, actionable post. Every claim gets inline source citation.
- **Roundup:** Compare multiple related deals/offers across sources. Rank or summarize. Highlight best value. Cross-reference source perspectives.
- **Summary:** Synthesize multiple source perspectives on a topic. Balanced overview. Attribute differing viewpoints to specific sources.

**Source citation rules:**
- Every factual claim must have an inline citation: e.g., "40% transfer bonus to ANA [TPG, DofC]"
- Citations link to the original article URLs
- If an article is *about* another site's reporting, source references remain as editorial context
- Otherwise, sources serve as attribution markers only

### Verification (Critic)

**Input:** Generated article + original source articles

**Checks performed:**
1. **Factual check** — numbers, percentages, dates in generated article match sources
2. **Attribution check** — every claim has at least one source citation; citations match actual source content
3. **Completeness check** — no key information from sources is missing
4. **Hallucination check** — no claims unsupported by any source
5. **Orphan check** — no citations that don't match source content

**Output:** Score (0-100) + JSON array of specific issues

**Threshold:** Configurable (default: 70). Below threshold → stays in `draft`. Above → `pending`.

### Feedback-Driven Regeneration

When admin requests revision:
1. Generator re-runs with: original sources + previous output + admin feedback + critic issues
2. Prompt instructs model to address specific feedback points
3. Critic re-verifies the new output (mandatory before returning to admin)
4. If passes threshold → `pending` (admin sees revised article)
5. If fails → stays in `draft`, admin notified of failure
6. Revision history preserved in `article_revisions`

### Rate Limiting & Resilience
- HF free tier: ~1000 requests/day
- Request queue with exponential backoff
- Cache model responses keyed on input hash
- If HF unavailable: articles stay in `draft`, retry next cycle
- Log all API errors for debugging

---

## 7. Web Interface

### Public Site

**URL:** `milesahead.kumaranik.com`

**Style:** Magazine Grid (dark/light mode, toggle persisted in cookie)

**Pages:**
- **Homepage:** Featured article (large, left) + sidebar stack (3 articles, right) + bottom grid (3 columns). Category filter tabs: All, Deals, Roundups, Alerts, Summaries.
- **Article page:** Full content with inline source citations (linked to originals), published date, article type badge, thumbs up/down buttons, related articles.
- **Newsletter subscribe:** Email input + double opt-in confirmation flow.
- **RSS feed:** `/feed.xml` — all approved articles.

**SEO:** Meta tags, Open Graph tags, clean URLs (`/article/{slug}`), semantic HTML.

**Public feedback:**
- Thumbs up/down on every article. Dual dedup: cookie/localStorage for client-side UX (hide buttons after voting), ip_hash in `article_votes` table for server-side enforcement (reject duplicate votes from same IP even if cookies cleared)
- If thumbs down count exceeds threshold OR down ratio exceeds configured percentage within a time window → auto-email admin

### Admin Dashboard

**URL:** `milesahead.kumaranik.com/admin`

**Auth:** Password login (bcrypt hash in config). Session token in cookie. OAuth-ready interface design for future multi-user support.

**Pages:**
- **Pending queue:** Articles awaiting review, sorted by creation date. Each shows: title, type badge, verification score, source count, revision number. Expandable: full content with inline citations, critic issues, revision history with diffs. Actions: Approve, Reject, Request Revision (text field).
- **Published articles:** List with vote counts (up/down), flagged articles exceeding negative vote threshold. Unpublish button → takes article off public site immediately, option to reject permanently or provide feedback for revision cycle. Unpublished articles show "under review" placeholder on public URL.
- **Sources:** Add/edit/disable scan sources, configure RSS URL, scrape selector, scan interval.
- **Subscribers:** List, export CSV.
- **Newsletter:** Preview next daily digest, manual send trigger, history of sent newsletters.
- **Settings:** Model configuration, scan intervals, notification preferences, vote thresholds.

### Templates

Go `html/template` with a shared layout. Template structure:
```
templates/
  layout.html          — base layout (head, nav, footer, theme toggle)
  public/
    home.html          — magazine grid homepage
    article.html       — single article page
    subscribe.html     — newsletter subscription
  admin/
    login.html         — admin login
    dashboard.html     — pending queue
    published.html     — published articles list
    sources.html       — source management
    subscribers.html   — subscriber list
    newsletter.html    — newsletter preview/send
    settings.html      — configuration
  email/
    notification.html  — admin notification (article for review)
    newsletter.html    — newspaper-style daily digest
    confirm.html       — subscriber confirmation
```

---

## 8. Email

### Outbound (OCI Email Delivery — SMTP)
- **Admin notifications:** When articles move to `pending`, send email with article content, source citations, verification score, and instructions to reply with approve/reject/feedback.
- **Newsletter:** Daily newspaper-style digest of approved articles from last 24 hours. Sent at configured time (default 8 AM). Only sent if there are new articles.
- **Subscriber confirmation:** Double opt-in email with confirmation link.

### Inbound (Gmail — IMAP Polling)
- Dedicated Gmail address (e.g., `milesahead.review@gmail.com`)
- App Password for IMAP authentication
- Poll every 2-3 minutes for new replies
- Each admin notification uses a unique reply-to: `review+{article-token}@gmail.com` (Gmail plus addressing)
- Parse reply body:
  - "approve" (case-insensitive) → approve article
  - "reject" (case-insensitive) → reject article
  - "unpublish" (case-insensitive) → unpublish a previously approved article (works on any article notification email, even after approval)
  - Any other text → treat as revision feedback; if article is already approved/published, unpublishes it first and then triggers revision cycle
- Admin can reply to any past article notification email at any time — the article token maps to the article regardless of current status
- Mark processed emails as read

---

## 9. Configuration

Single `config.yaml` file, overridable by environment variables.

```yaml
server:
  port: 8080
  domain: milesahead.kumaranik.com

admin:
  password_hash: "$2a$12$..."  # bcrypt

db:
  path: /opt/milesahead/data/milesahead.db

scanner:
  default_interval: 180  # minutes
  user_agent: "MilesAhead/1.0"

ai:
  generator_model: "mistralai/Mistral-7B-Instruct-v0.3"
  critic_model: "meta-llama/Meta-Llama-3-8B-Instruct"
  hf_api_token: "${HF_API_TOKEN}"
  critic_threshold: 70
  max_retries: 3
  cache_ttl: 3600  # seconds

mail:
  smtp:
    host: smtp.email.oci.oraclecloud.com
    port: 587
    user: "${SMTP_USER}"
    password: "${SMTP_PASSWORD}"
    from: "noreply@milesahead.kumaranik.com"
  imap:
    host: imap.gmail.com
    port: 993
    user: "${IMAP_USER}"
    password: "${IMAP_APP_PASSWORD}"
    poll_interval: 120  # seconds

newsletter:
  send_time: "08:00"
  timezone: "America/New_York"

votes:
  alert_threshold: 10       # absolute thumbs down count
  alert_ratio: 0.4          # 40% down ratio
  alert_window: 24           # hours
```

---

## 10. Deployment

### Infrastructure (OCI Free Tier)
- **Compute:** ARM or AMD instance (shared with other services or dedicated)
- **Object Storage:** Daily SQLite backup (20 GB free)
- **Email Delivery:** Outbound SMTP (100 emails/day free)
- **DNS:** A record for `milesahead.kumaranik.com` → instance IP

### Server Setup
- Nginx reverse proxy with TLS (Let's Encrypt certbot)
- Systemd service for the Go binary with auto-restart
- SQLite database at `/opt/milesahead/data/milesahead.db`
- Config at `/opt/milesahead/config.yaml`
- Daily cron: backup SQLite to OCI Object Storage

### CI/CD (GitHub Actions)
- **On push to main:** Build, lint, test
- **On tag (v*):** Cross-compile for linux/amd64 + linux/arm64, deploy via SSH
- **Secrets:** OCI SSH key, HF_API_TOKEN, SMTP credentials, IMAP credentials

### Binary Deployment
```bash
# Build
GOOS=linux GOARCH=arm64 go build -o milesahead ./cmd/milesahead

# Deploy
scp milesahead user@oci-instance:/opt/milesahead/
ssh user@oci-instance "sudo systemctl restart milesahead"
```

---

## 11. Project Structure

```
scanner/
  cmd/
    milesahead/
      main.go              — entry point, wires all components
  internal/
    config/
      config.go            — YAML + env var config loading
    db/
      db.go                — SQLite connection, migrations
      models.go            — Go structs for all tables
    scanner/
      scanner.go           — orchestrates RSS + scraper
      rss.go               — RSS feed parser
      scraper.go           — HTML scraper with CSS selectors
    ai/
      pipeline.go          — orchestrates generator → critic flow
      generator.go         — HF API calls for article generation
      critic.go            — HF API calls for verification
      prompts.go           — prompt templates by article type
      cache.go             — response caching
    cluster/
      cluster.go           — topic similarity grouping
    article/
      manager.go           — status state machine, CRUD
      revision.go          — feedback-driven regeneration
    mail/
      outbound.go          — OCI SMTP email sending
      inbound.go           — Gmail IMAP polling + parsing
      templates.go         — email template rendering
    newsletter/
      newsletter.go        — daily digest generation + sending
    vote/
      vote.go              — thumbs up/down + threshold alerting
    web/
      server.go            — HTTP server setup, middleware
      routes.go            — route registration
      public.go            — public site handlers
      admin.go             — admin dashboard handlers
      auth.go              — admin authentication
      api.go               — JSON API endpoints (votes, etc.)
    scheduler/
      scheduler.go         — cron-like job scheduling
  templates/
    layout.html
    public/
      home.html
      article.html
      subscribe.html
    admin/
      login.html
      dashboard.html
      published.html
      sources.html
      subscribers.html
      newsletter.html
      settings.html
    email/
      notification.html
      newsletter.html
      confirm.html
  static/
    css/
      style.css            — main styles, light/dark mode
    js/
      main.js              — theme toggle, vote handlers
    img/
      logo.svg
  config.example.yaml
  go.mod
  go.sum
  Makefile
  Dockerfile               — optional, for containerized deployment
  .github/
    workflows/
      ci.yml               — build + test
      deploy.yml           — tag-triggered deployment
```

---

## 12. Key Go Dependencies

| Package | Purpose |
|---|---|
| `net/http` | Web server (stdlib) |
| `html/template` | Server-side rendering (stdlib) |
| `database/sql` + `modernc.org/sqlite` | SQLite driver (pure Go, no CGO) |
| `github.com/mmcdole/gofeed` | RSS/Atom feed parsing |
| `github.com/PuerkitoBio/goquery` | HTML scraping with CSS selectors |
| `github.com/emersion/go-imap` | IMAP client for Gmail polling |
| `gopkg.in/gomail.v2` | SMTP email sending |
| `gopkg.in/yaml.v3` | Config file parsing |
| `golang.org/x/crypto/bcrypt` | Password hashing |
| `github.com/robfig/cron/v3` | Cron scheduler |

No heavy frameworks. Stdlib where possible.

---

## 13. Security Considerations

- Admin password: bcrypt hashed, never stored in plaintext
- Session tokens: cryptographically random, short-lived (24h), HTTP-only cookies
- CSRF protection on all form submissions
- Rate limiting on login endpoint (prevent brute force)
- Voter IP addresses stored as SHA-256 hashes only
- SQL injection: parameterized queries only (no string concatenation)
- XSS: Go templates auto-escape by default
- Email tokens: cryptographically random, single-use for confirmation
- TLS enforced via nginx + Let's Encrypt
- Sensitive config values via environment variables, not committed to git
- `.gitignore`: config.yaml (real), *.db, .env

---

## 14. Future Considerations (Not in Scope)

- OAuth/SSO for multi-user admin
- Full-content RSS feed (current `/feed.xml` serves excerpts only)
- Email inbound via dedicated mail server (replace Gmail)
- Additional AI models beyond Hugging Face
- Full-text search on articles
- Analytics dashboard
- API for third-party consumption
- Mobile-responsive improvements beyond basic responsive CSS
