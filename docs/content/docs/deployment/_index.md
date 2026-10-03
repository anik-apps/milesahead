---
title: Deployment
weight: 4
---

MilesAhead runs on one OCI ARM VM, deployed by GitHub Actions on every push to
`main`. Outgoing mail goes through OCI Email Delivery; see [Email]({{< relref "email" >}}).

## Infrastructure

| Component | Technology | Details |
|-----------|-----------|---------|
| Server | OCI ARM, Always Free | VM.Standard.A1.Flex in `us-sanjose-1`, Ubuntu |
| App | Go binary under systemd | `milesahead.service`, port 9090, in `/opt/milesahead` |
| Reverse proxy | Caddy in Docker | Shared with other sites; one file per site in `/opt/fynans/caddy-sites/` |
| Database | SQLite, WAL mode | `/opt/milesahead/data/milesahead.db` |
| Backups | systemd timer + GitHub Actions | Daily snapshot on the server, copied off the box daily |
| DNS | Cloudflare | The site, the docs subdomain and the mail records |
| Mail | OCI Email Delivery | SMTP on port 587 with STARTTLS; see [Email]({{< relref "email" >}}) |
| Observability | Grafana Alloy → Grafana Cloud | Metrics, logs, dashboards and alert rules |
| CI/CD | GitHub Actions | `ci.yml`, `deploy.yml`, `backup.yml` |
| Docs | Cloudflare Pages | This site, built from `docs/` |

## CI/CD Pipeline

### CI (`ci.yml`)

Runs on every push and pull request to `main`:

- **test:** `go vet ./...`, `go test ./... -v -race`, `node --test tests/js/*.test.mjs`
  (the admin pages' browser-side logic) and `go build ./cmd/milesahead`.
- **screenshot-test:** builds and starts the app with a test config and captures
  the home and login pages at desktop, tablet and mobile sizes with Playwright,
  kept as a workflow artifact.

### Deploy (`deploy.yml`)

Runs on every push to `main`, and by hand (with options for a full first-time
setup and for force-reseeding named cards). Deploys queue rather than overlap
(`concurrency: deploy-production`), because two overlapping deploys once raced on
the staged binary and could have installed the older commit.

```
test ──> build ──┬──> config ──┐
                 │             ├──> deploy ──┬──> seed
                 └─────────────┘             └──> grafana
                 └──> setup  (manual full_setup only)
```

1. **test** repeats CI's checks: `go vet`, `go test ./... -race` and the node tests.
   Nothing is built, configured or shipped on a red tree. It was added after a
   deploy went out while CI was failing and every article page lost its related
   stories; it also gates manual runs.
2. **build** cross-compiles `milesahead`, `seed-cards` and `seed-structured` for
   `linux/arm64` and uploads them with the seed data.
3. **config** writes `config.yaml` on the runner from the repository secrets (see
   [Generated config](#generated-config)), checks it with
   `milesahead -check-config`, and copies it to the server (`chmod 600`). The
   deploy waits for it, so the restart always picks up the new config.
4. **deploy** copies the binary (as `milesahead.new`), templates and static files;
   validates `deploy/milesahead.caddy` inside the Caddy container before
   installing it and fails if the reload fails (the directory is shared, so a bad
   file would take every site down at the next restart); refreshes the Alloy
   config (a failed Alloy restart only warns); installs the backup script and
   timer; swaps the binary into place, restarts the service and fails unless it is
   active three seconds later.
5. **seed** runs `seed-cards` and `seed-structured`, which only add missing cards.
   A manual run with `reseed_slugs` force-reseeds just those cards, after taking a
   verified backup.
6. **grafana** pushes the dashboards and the alert rules when the Grafana secrets
   are set. A hibernating free-tier Grafana instance (HTTP 503 "Loading") is a
   warning, not a failed deploy.

**setup** runs only on a manual dispatch with `full_setup: true`: it creates the
app user and directories, installs the systemd unit and the Caddy site, installs
Grafana Alloy and writes its credentials into `alloy.service`.

### Generated config

The config job builds `config.yaml` from a heredoc in `deploy.yml`. Model names
and other settings live in the workflow, mirrored from `config.example.yaml`
(a test checks that the critic settings and the story-dedup mode match); secrets come from the repository secrets:

- Every secret is JSON-encoded with `jq` before it is written. A JSON string is a
  valid YAML double-quoted scalar, so a quote, backslash, `#`, `$`, backtick or
  newline in a password arrives byte for byte instead of breaking the file, and
  the shell never runs anything inside a secret.
- `SMTP_PORT` must be numeric (default 587). Unset SMTP secrets leave
  placeholders, which the app reads as "SMTP not configured".
- `go run ./cmd/milesahead -check-config -config /tmp/config.yaml` loads the file
  exactly as startup would. A bad value fails the deploy here instead of stopping
  the service. A test in `cmd/milesahead` runs this step under bash and `jq` with
  hostile secrets.
- In the app, `${NAME}` is expanded only when it is a whole value, after parsing,
  so secret contents are never expanded a second time.

## Secrets

Repository secrets, by name. Their values never appear in the repository or this
site.

| Secret | Used by | Purpose |
|--------|---------|---------|
| `OCI_SSH_KEY`, `OCI_HOST`, `OCI_USER` | deploy, backup | SSH to the server |
| `NVIDIA_API_KEY` | config | `ai.hf_api_token` |
| `ADMIN_PASSWORD_HASH` | config | `admin.password_hash` (bcrypt) |
| `UNSPLASH_KEY`, `PEXELS_KEY` | config | Image search |
| `SMTP_HOST`, `SMTP_USER`, `SMTP_PASSWORD` | config | `mail.smtp`; see [Email]({{< relref "email#github-secrets" >}}) |
| `SMTP_PORT`, `SMTP_FROM` | config | Optional; default 587 and `noreply@milesahead.kumaranik.com` |
| `GRAFANA_API_KEY`, `GRAFANA_PROM_URL`, `GRAFANA_PROM_USER`, `GRAFANA_LOKI_URL`, `GRAFANA_LOKI_USER` | setup | Alloy's credentials for Grafana Cloud |
| `GRAFANA_URL`, `GRAFANA_API_KEY`, `GRAFANA_DS_UID` | grafana | Pushing dashboards and alert rules |

The backup workflow also uses the built-in `GITHUB_TOKEN` to upload to the
`db-backups` release.

## Deploy Files

| File | Purpose |
|------|---------|
| `deploy/milesahead.service` | systemd unit for the app |
| `deploy/milesahead.caddy` | Caddy site: TLS, `encode zstd gzip`, security headers, reverse proxy to port 9090 |
| `deploy/alloy-config.alloy` | Grafana Alloy: scrape `/metrics`, ship the journal |
| `deploy/alloy.service` | systemd unit for Alloy, with the Grafana credentials |
| `deploy/backup.sh` | WAL-safe `VACUUM INTO` snapshot, verified, gzipped, 30 days kept |
| `deploy/milesahead-backup.service` | systemd oneshot that runs `backup.sh` |
| `deploy/milesahead-backup.timer` | Daily at 03:30, `Persistent=true`, up to 15 min random delay |
| `deploy/grafana-alerts.json` | Alert rules, with stable UIDs |
| `deploy/grafana-dashboard-business.json`, `deploy/grafana-dashboard-service.json` | Dashboards |

## Caddy, Compression and Caching

Caddy terminates TLS for `milesahead.kumaranik.com`, compresses HTML, CSS, JS, SVG
and the feeds with `encode zstd gzip` (images are already compressed), adds
`X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy` and removes the
`Server` header, then proxies to `host.docker.internal:9090`.

The app sets `Cache-Control` itself: HTML is `no-cache`; static files whose URL
carries a content hash (`?v=`: the stylesheet, scripts and infographics) are
cached for a year, other static files for an hour.

## Backups

The database is the only thing on the server that cannot be rebuilt from git and
the GitHub secrets, so it is the only thing backed up.

- **On the server**, `milesahead-backup.timer` runs `backup.sh` daily at 03:30. It
  snapshots with `VACUUM INTO` rather than copying the file: in WAL mode a plain
  `cp` silently drops committed pages still in the WAL (once, 25 rows). The
  snapshot must pass `PRAGMA integrity_check` before it is gzipped, and 30 days
  are kept locally in `/opt/milesahead/backups`.
- **Off the server**, `.github/workflows/backup.yml` runs daily at 08:17 UTC: it
  runs the same script over SSH, pulls the newest archive, checks that it restores
  and holds articles, and uploads it to the `db-backups` release, keeping the 14
  most recent. Shipping from a runner means no storage credential lives on the VM,
  which is what the backup protects against losing.

The deploy reinstalls the script and timer every time, so they cannot drift
uninstalled. A scoped card reseed takes a backup first and refuses to run without
one.

To restore: download an asset from the `db-backups` release, `gunzip` it, and put
it at `/opt/milesahead/data/milesahead.db` with the service stopped.

## Observability

The app serves Prometheus metrics at `/metrics` (`internal/metrics`). Grafana Alloy,
on the same host, scrapes them every 30 s and remote-writes them to Grafana
Cloud, and ships the `milesahead.service` journal to Grafana Cloud Loki.

Alert rules (`deploy/grafana-alerts.json`):

| Alert | Fires when | Metric |
|-------|-----------|--------|
| No articles generated in 24 h | fewer than 1 in 24 h, for an hour | `milesahead_articles_generated_total` |
| AI API failures sustained | more than 30 final failures in 6 h | `milesahead_ai_api_errors_total` |
| Enrichment reconciler has stalled | no completed pass for over an hour | `milesahead_enrichment_last_success_timestamp_seconds` |
| Confirmation emails failing | 2 or more failures in 6 h | `milesahead_emails_sent_total{kind="confirmation",outcome="failed"}` |
| Newsletter send failed | any failure in 6 h | `milesahead_emails_sent_total{kind="newsletter",outcome="failed"}` |

The generation-drought alert is the important one: in one outage scanning looked
healthy for 26 days while every generation failed. The runbook is
`docs/runbooks/generation-drought.md` in the repository.

Other metrics worth knowing: `milesahead_ai_preflight_models_reachable{role}`
(`role="critic"` at 0 means nothing can publish), `milesahead_articles_degraded`,
`milesahead_revisions_waiting{state}`, `milesahead_story_dedup_decisions_total`,
`milesahead_article_images_unsized`, `milesahead_subscribers_total` and the HTTP
request, duration and response-size series.

## Docs Site

This site is Hugo with the Hextra theme (a Hugo module), in `docs/`, published by
Cloudflare Pages at `docs.milesahead.kumaranik.com`. Its build settings live in
Cloudflare, not in this repository. To preview locally:

```bash
cd docs && hugo server
```
