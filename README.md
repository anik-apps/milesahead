# Miles Ahead

A self-hosted content aggregation platform that scans miles and points websites, generates articles using AI, and publishes them after admin review. Includes 41 curated credit card reference pages with the Scout persona.

**Live:** [milesahead.kumaranik.com](https://milesahead.kumaranik.com)

## Features

- **Automated Scanning** — RSS scraping of 6+ miles/points sources every 2-4 hours
- **AI Pipeline** — Dual-model: Nemotron generates articles, Llama 70B verifies accuracy
- **Image Pipeline** — 5-source search (Unsplash, Pexels, Pixabay, Openverse, Wikimedia) with vision model scoring
- **Reference Pages** — 41 credit card guides across 6 categories with Scout persona verdicts
- **Admin Dashboard** — Review queue with approve/reject/revise workflow
- **Magazine-Style Site** — Dark/light mode, two-column articles, category filters, RSS feed
- **Newsletter** — Daily newspaper-style email digest
- **Zero Cost** — OCI free-tier compute, NVIDIA API, SQLite

## Tech Stack

- **Language:** Go 1.22+
- **Database:** SQLite (WAL mode)
- **AI:** NVIDIA API (Nemotron, Llama 70B, Llama Vision)
- **Deployment:** OCI ARM VM, Caddy, systemd, GitHub Actions CI/CD
- **Observability:** Grafana Cloud (Prometheus + Loki via Alloy)

## Quick Start

```bash
git clone https://github.com/anik-apps/milesahead.git
cd milesahead
cp config.example.yaml config.yaml
# Edit config.yaml with your NVIDIA API key and admin password
go build -o milesahead ./cmd/milesahead
./milesahead
```

Seed credit card reference pages:
```bash
go run ./cmd/seed-cards
```

## Documentation

Full documentation is built with [Hugo](https://gohugo.io/) using the [Hextra](https://imfing.github.io/hextra/) theme:

```bash
cd docs
hugo server
```

Then visit http://localhost:1313 for:
- [Getting Started](docs/content/docs/getting-started.md)
- [Architecture](docs/content/docs/architecture/_index.md)
- [AI Pipeline](docs/content/docs/architecture/ai-pipeline.md)
- [Reference Pages](docs/content/docs/features/reference-pages.md)
- [Deployment](docs/content/docs/deployment/_index.md)
- [Development](docs/content/docs/development/_index.md)

## License

Private project.
