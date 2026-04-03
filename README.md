# Miles Ahead

A self-hosted content aggregation platform that scans miles and points websites, generates articles using AI, and publishes them after admin review. Includes 52 curated credit card reference pages with the Scout persona.

**Live:** [milesahead.kumaranik.com](https://milesahead.kumaranik.com)

## Features

- **Automated Scanning** — RSS scraping of 6+ miles/points sources every 2-4 hours
- **AI Pipeline** — Dual-model: Nemotron generates articles, Llama 70B verifies accuracy
- **Image Pipeline** — 5-source search (Unsplash, Pexels, Pixabay, Openverse, Wikimedia) with vision model scoring
- **Reference Pages** — 52 credit card guides across 6 categories with Scout persona verdicts
- **Card Comparison** — Side-by-side comparison of 2-3 cards (earning rates, credits, benefits, protections, transfer partners)
- **Article Tags** — AI-generated tags from 8 categories with tag filter pills on homepage
- **Admin Dashboard** — Review queue with approve/reject/revise workflow, newsletter admin workflow
- **Magazine-Style Site** — Dark/light mode, two-column articles, category filters, RSS feed
- **Newsletter** — Weekly newspaper-style email with Scout's Pick, Card of the Week, tag-grouped articles
- **Zero Cost** — OCI free-tier compute, NVIDIA API, SQLite

## Tech Stack

- **Language:** Go 1.26+
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

Seed credit card reference pages and structured comparison data:
```bash
go run ./cmd/seed-cards
go run ./cmd/seed-structured
```

## Documentation

Full documentation is built with [Hugo](https://gohugo.io/) using the [Hextra](https://imfing.github.io/hextra/) theme.

**Online:** [docs.milesahead.kumaranik.com](https://docs.milesahead.kumaranik.com/) (deployed via Cloudflare Pages)

**Local:**
```bash
cd docs && hugo server
# Visit http://localhost:1313
```

Covers: [Getting Started](docs/content/docs/getting-started.md) · [Architecture](docs/content/docs/architecture/_index.md) · [AI Pipeline](docs/content/docs/architecture/ai-pipeline.md) · [Reference Pages](docs/content/docs/features/reference-pages.md) · [Deployment](docs/content/docs/deployment/_index.md) · [Development](docs/content/docs/development/_index.md)

## License

Private project.
