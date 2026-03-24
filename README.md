# Miles Ahead

A self-hosted content aggregation platform that scans major miles and points websites, generates articles using AI inference models, and publishes them after admin review.

## Features

- **Automated Scanning** — RSS and HTML scraping of 6+ miles/points sources every 2-4 hours
- **AI-Powered Content** — Dual-model pipeline: one generates articles, another verifies accuracy
- **Admin Review Workflow** — Dashboard + email-based review with approve/reject/revision feedback
- **Magazine-Style Website** — Public site with dark/light mode, category filters, and RSS feed
- **Daily Newsletter** — Newspaper-style email digest sent to subscribers
- **Vote System** — Public thumbs up/down with admin alerting on negative thresholds
- **Zero Cost** — Runs entirely on free-tier infrastructure (OCI, Hugging Face, Gmail)

## Architecture

Single Go binary (monolith) with SQLite database.

```
Scanner (RSS/HTML) → Topic Clustering → AI Pipeline (Generate → Verify)
                                              ↓
Admin Dashboard ← Email Notifications ← Article Manager (State Machine)
                                              ↓
Public Website ← Approved Articles → Newsletter → Subscribers
```

## Tech Stack

- **Language:** Go 1.22+
- **Database:** SQLite (WAL mode, pure Go driver)
- **Templates:** Go html/template (server-rendered)
- **AI:** Hugging Face Inference API (free tier)
- **Email:** OCI Email Delivery (outbound), Gmail IMAP (inbound)
- **Deployment:** OCI free-tier compute, nginx, systemd

## Source Sites

| Source | Method |
|---|---|
| The Points Guy | RSS (HTML fallback) |
| One Mile at a Time | RSS (HTML fallback) |
| Doctor of Credit | RSS (HTML fallback) |
| FlyerTalk | RSS (HTML fallback) |
| View from the Wing | RSS (HTML fallback) |
| The Frequent Miler | RSS (HTML fallback) |

Sources are configurable — add or remove via the admin dashboard.

## Getting Started

### Prerequisites

- Go 1.22+
- A Hugging Face API token (free)
- Gmail account with app password (for admin email replies)
- OCI Email Delivery credentials (for outbound email)

### Setup

```bash
# Clone
git clone https://github.com/anik-apps/milesahead.git
cd milesahead

# Configure
cp config.example.yaml config.yaml
# Edit config.yaml with your credentials

# Build and run
make build
./milesahead -config config.yaml
```

### Configuration

All configuration is in `config.yaml`. Sensitive values can be set via environment variables:

| Variable | Purpose |
|---|---|
| `HF_API_TOKEN` | Hugging Face inference API token |
| `SMTP_USER` | OCI Email Delivery SMTP username |
| `SMTP_PASSWORD` | OCI Email Delivery SMTP password |
| `IMAP_USER` | Gmail address for admin replies |
| `IMAP_APP_PASSWORD` | Gmail app password |

## Article Workflow

```
Scan Sources → Cluster Topics → Generate Article → AI Verification
                                                        ↓
                                              Score >= threshold?
                                              ├─ Yes → Pending (admin review)
                                              └─ No  → Draft (retry next cycle)

Pending → Admin reviews (dashboard or email reply)
        ├─ Approve    → Published (visible on site)
        ├─ Reject     → Archived
        └─ Feedback   → AI regenerates → Re-verify → Pending again

Published → Admin can unpublish (via dashboard or email)
          → Public can vote (threshold alerts admin)
```

## Development

```bash
make test      # Run all tests
make build     # Build binary
make lint      # Run go vet
make run       # Build and run with config.yaml
```

## Deployment

See `deploy/` for:
- `milesahead.service` — systemd service file
- `nginx.conf` — nginx reverse proxy with TLS
- `backup.sh` — SQLite backup to OCI Object Storage

## License

Private project.
