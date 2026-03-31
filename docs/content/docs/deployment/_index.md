---
title: Deployment
weight: 4
---

Miles Ahead runs on a single OCI ARM instance with automated CI/CD via GitHub Actions.

## Infrastructure

| Component | Technology | Details |
|-----------|-----------|---------|
| Server | OCI ARM (free tier) | Ampere A1, Ubuntu |
| Reverse Proxy | Caddy | Shared via Docker Compose (`/opt/fynans/caddy-sites/`) |
| Process Manager | systemd | `milesahead.service` |
| CI/CD | GitHub Actions | `ci.yml` (test) + `deploy.yml` (build + deploy) |
| Observability | Grafana Cloud | Metrics via Alloy (Prometheus + Loki) |
| Database | SQLite | WAL mode, stored at `/opt/milesahead/data/milesahead.db` |

## CI/CD Pipeline

### CI (`ci.yml`)

Runs on every push and pull request to `main`:

1. `go vet ./...` -- static analysis
2. `go test ./... -v -race` -- unit tests with race detection
3. `go build ./cmd/milesahead` -- build verification
4. Screenshot tests -- builds the binary, starts the server, captures desktop/tablet/mobile screenshots with Playwright

### Deploy (`deploy.yml`)

Runs on push to `main` and manual trigger:

1. **Build** -- cross-compile for `linux/arm64`, upload binary + seed-cards + cards.json as artifacts
2. **Setup** (manual trigger only, `full_setup: true`) -- creates app user, deploys config, systemd service, Caddy config, installs and configures Grafana Alloy
3. **Deploy** -- SSH into the server, copy binary + templates + static files, atomic swap, restart service, seed reference pages, verify health

The deploy performs an atomic binary swap:
```
scp milesahead -> /opt/milesahead/milesahead.new
mv milesahead.new -> milesahead
systemctl restart milesahead
```

## Manual Setup

For first-time deployment, trigger the workflow with `full_setup: true`. This:

1. Creates the `milesahead` system user
2. Creates `/opt/milesahead/data`, `/opt/milesahead/templates`, `/opt/milesahead/static`
3. Deploys `config.yaml` with secrets from GitHub Actions
4. Installs and enables the systemd service
5. Deploys the Caddy site config to the shared `caddy-sites/` directory
6. Installs Grafana Alloy and deploys its config

## Deploy Files

| File | Purpose |
|------|---------|
| `deploy/milesahead.service` | systemd unit file |
| `deploy/milesahead.caddy` | Caddy site configuration (reverse proxy) |
| `deploy/alloy-config.alloy` | Grafana Alloy config for metrics and logs |
| `deploy/alloy.service` | systemd unit for Alloy with Grafana credentials |
| `deploy/backup.sh` | Database backup script |

## Observability

Metrics are exposed in Prometheus format by the application (`internal/metrics`) and scraped by Grafana Alloy running on the same host. Alloy forwards metrics and logs to Grafana Cloud.

Tracked metrics include:
- Critic scores (histogram)
- Image search success/error rates by source
- Image scoring success/error rates (text and vision phases)
- Image candidate counts
- Pipeline execution times

## Secrets

The following secrets are configured in GitHub Actions:

| Secret | Purpose |
|--------|---------|
| `OCI_SSH_KEY` | SSH private key for deployment |
| `OCI_HOST` | Server hostname/IP |
| `OCI_USER` | SSH user (typically `ubuntu`) |
| `NVIDIA_API_KEY` | NVIDIA API key for AI models |
| `ADMIN_PASSWORD_HASH` | bcrypt hash of admin password |
| `UNSPLASH_KEY` | Unsplash API key |
| `PEXELS_KEY` | Pexels API key |
| `PIXABAY_KEY` | Pixabay API key |
| `GRAFANA_API_KEY` | Grafana Cloud API key |
| `GRAFANA_PROM_URL` | Grafana Cloud Prometheus endpoint |
| `GRAFANA_PROM_USER` | Grafana Cloud Prometheus user |
| `GRAFANA_LOKI_URL` | Grafana Cloud Loki endpoint |
| `GRAFANA_LOKI_USER` | Grafana Cloud Loki user |
