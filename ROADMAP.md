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

## Reliability & Hardening

Found during the 2026-08-31 card-video review, ordered by severity.

- [ ] **Database backups do not exist** — `deploy/backup.sh` is documented as the backup mechanism but has never run: `oci` CLI not installed, no `~/.oci`, `/opt/milesahead/backups` absent, no cron or systemd timer, and `DB_PATH` points at the wrong file (`/opt/milesahead/milesahead.db` vs the real `data/milesahead.db`). 139 MB of production data with no recovery path. *Fix:* systemd timer running `sqlite3 … ".backup"` + gzip + 30-day retention, shipped off-box via rclone or `gh release upload`; drop the `oci` dependency. Also delete the two stale 45 MB `milesahead.db.bak.cleanup-*` files.
- [ ] **Reclaim ~28 GB on the server** — Docker images and build cache are ~28 GB reclaimable on a 66%-full root. *Fix:* `docker builder prune -af` + `docker image prune -a`, then a scheduled prune.
- [ ] **A bad Caddy config can silently break five sites** — `deploy.yml:194-195` copies `milesahead.caddy` into the shared `/opt/fynans/caddy-sites/` then reloads with `2>/dev/null || true`, so a syntax error stays invisible until the next container restart. *Fix:* `caddy validate` before the copy; drop the error suppression on reload.
- [ ] **`/static/` directory listing is enabled and crawlable** — anything dropped there is publicly discoverable by walking the index. *Fix:* 404 directory requests in the static handler, or `Disallow: /static/` in robots.txt.
- [ ] **Compare `<select>` has no accessible name** (`card.html`, WCAG 4.1.2 A) and navigates on change (3.2.2 A). *Fix:* `<h4>` → `<label for>`; replace `onchange="window.location"` with a submit button.
- [ ] **No binary rollback on deploy** — `deploy.yml:185,202` overwrites without keeping the previous binary. *Fix:* `cp milesahead milesahead.prev` before the swap.
- [ ] **CI Go version pin is inert** — workflows pin `1.22` while `go.mod` needs `1.26.1`, so `GOTOOLCHAIN=auto` re-downloads the toolchain six times per push. *Fix:* bump `setup-go` to `1.26.x`.
- [ ] **Card data refresh** — CLEAR+ credit is $219 as of 2026-07-01 (DB says $209, verified 2026-04-02); the $100 Saks credit was removed 2026-07-01 but is still listed as active; the $300 Equinox credit is in the DB but absent from the page prose.

## Up Next

- [ ] Card explainer videos — YouTube-hosted, embedded per card page (Amex Platinum first)
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
