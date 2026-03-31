# Miles Ahead — Roadmap

## Completed

- [x] Core platform (scanner, AI pipeline, admin dashboard, public site)
- [x] SQLite database with 12 tables
- [x] Dual-model AI pipeline (Nemotron generator + Llama 70B critic)
- [x] Image pipeline (5 sources + vision model scoring)
- [x] Two-column article layout with TL;DR sidebar
- [x] 41 credit card reference pages with Scout persona
- [x] Auto-linking in competitor comparison sections
- [x] Admin CRUD for reference pages
- [x] CI/CD with GitHub Actions (build, test, deploy, seed)
- [x] Observability (Grafana Cloud via Alloy)
- [x] Structured logging (JSON)
- [x] OCI ARM deployment with Caddy reverse proxy
- [x] Hugo documentation site

## Up Next

- [ ] Auto-update detection (scan articles for card keyword matches, draft section updates)
- [ ] Auto-linking card mentions in generated articles
- [ ] Scout's Take backfill for existing articles
- [ ] Full-text search on articles
- [ ] Graceful shutdown with signal handling
- [ ] Health check endpoint (`/healthz`)

## Future

- [ ] Additional model providers (Groq, Gemini)
- [ ] OAuth/SSO login
- [ ] Analytics dashboard
- [ ] SEO improvements (sitemap.xml, structured data)
- [ ] Affiliate link integration
- [ ] Social media auto-posting

---

*Items are picked up based on priority and need, not strictly sequential.*
