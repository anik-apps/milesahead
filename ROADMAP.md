# Miles Ahead — Roadmap

## Completed

- [x] Core platform (scanner, AI pipeline, admin dashboard, public site)
- [x] SQLite database with 12 tables
- [x] Dual-model AI pipeline (Nemotron generator + Llama 70B critic)
- [x] Image pipeline (5 sources + vision model scoring)
- [x] Two-column article layout with TL;DR sidebar
- [x] 52 credit card reference pages with Scout persona
- [x] Auto-linking in competitor comparison sections
- [x] Admin CRUD for reference pages
- [x] CI/CD with GitHub Actions (build, test, deploy, seed)
- [x] Observability (Grafana Cloud via Alloy)
- [x] Structured logging (JSON)
- [x] OCI ARM deployment with Caddy reverse proxy
- [x] Hugo documentation site (Cloudflare Pages)
- [x] Graceful shutdown with signal handling (30s timeout)
- [x] Health check endpoint (`/healthz`)
- [x] Auto-linking card mentions in generated articles
- [x] Scout's Take backfill script
- [x] Full-text search (FTS5) with search page and nav search icon
- [x] Auto-update detection (drafts card updates on article approval)
- [x] Scout personality voice for article generation
- [x] SEO (sitemap.xml, robots.txt, JSON-LD, Open Graph meta tags)
- [x] Traffic analytics (path normalization, response size, unique visitors)
- [x] Grafana dashboards (Business + Service) with CI auto-deploy
- [x] Article tags — AI-generated from 8 categories, tag filter pills on homepage
- [x] Weekly newsletter — Scout's Pick, Card of the Week, tag-grouped articles, newspaper-style template
- [x] Newsletter admin workflow — AI picks candidates, admin approves, auto-approve fallback
- [x] Card comparison tool — side-by-side comparison of 2-3 cards with structured data
- [x] Structured card data — earning rates, credits, benefits, protections, transfer partners for 52 cards
- [x] CI/CD refactored into separate build/deploy/seed/grafana jobs

## Up Next

- [ ] Additional model providers (Groq, Gemini) — reduce dependency on NVIDIA API
- [ ] OAuth/SSO login — replace single bcrypt password with Google OAuth
- [ ] Affiliate link integration — monetize card reference pages
- [ ] Social media auto-posting — auto-share to Twitter/X, Bluesky
- [ ] Mobile app / PWA — offline reading, push notifications

## Future

- [ ] Multi-user admin with roles
- [ ] API for third-party consumption
- [ ] Webhook integrations (Slack, Discord, Telegram)
- [ ] A/B testing for article generation models
- [ ] Article quality scoring based on admin approval patterns
- [ ] Geographic content targeting (US vs international deals)

---

*Items are picked up based on priority and need, not strictly sequential.*
