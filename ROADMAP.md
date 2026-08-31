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

- [x] **Database backups** — `deploy/backup.sh` was documented as the backup mechanism but had never run: `oci` CLI absent, no `~/.oci`, `/opt/milesahead/backups` never created, no cron (not installed) or systemd timer, and `DB_PATH` pointed at the Go binary rather than `data/milesahead.db`. Rewritten to take a WAL-safe `VACUUM INTO` snapshot via `python3` (the `sqlite3` CLI is also absent; python3 ships SQLite 3.45.1), verify it with `integrity_check` before trusting it, gzip to ~32 MB and keep 30 days. Installed by a daily systemd timer, with `.github/workflows/backup.yml` pulling a copy off-box to a GitHub release so no storage credential lives on the VM.
- [ ] **Delete stale on-server files** — two 45 MB `milesahead.db.bak.cleanup-20260508-*` files in `/opt/milesahead/data/` (90 MB of dead weight).
- [ ] **Reclaim ~28 GB on the server** — Docker images and build cache are ~28 GB reclaimable on a 66%-full root. *Fix:* `docker builder prune -af` + `docker image prune -a`, then a scheduled prune.
- [ ] **A bad Caddy config can silently break five sites** — `deploy.yml:194-195` copies `milesahead.caddy` into the shared `/opt/fynans/caddy-sites/` then reloads with `2>/dev/null || true`, so a syntax error stays invisible until the next container restart. *Fix:* `caddy validate` before the copy; drop the error suppression on reload.
- [ ] **`/static/` directory listing is enabled and crawlable** — anything dropped there is publicly discoverable by walking the index. *Fix:* 404 directory requests in the static handler, or `Disallow: /static/` in robots.txt.
- [ ] **Compare `<select>` has no accessible name** (`card.html`, WCAG 4.1.2 A) and navigates on change (3.2.2 A). *Fix:* `<h4>` → `<label for>`; replace `onchange="window.location"` with a submit button.
- [ ] **No binary rollback on deploy** — `deploy.yml:185,202` overwrites without keeping the previous binary. *Fix:* `cp milesahead milesahead.prev` before the swap.
- [ ] **CI Go version pin is inert** — workflows pin `1.22` while `go.mod` needs `1.26.1`, so `GOTOOLCHAIN=auto` re-downloads the toolchain six times per push. *Fix:* bump `setup-go` to `1.26.x`.
- [x] **Card data refresh** — CLEAR+ raised to $219 on 2026-07-01 (verified against Amex's own benefits page, which lists $219 for all five eligible cards), so all four cards carrying it were stale at $209; Amex Green's derived "pays them $59/year" was recomputed to $69. The $100 Saks credit ended 2026-07-01 and was removed. The seed prose also carried a duplicated Equinox `<li>` that would have rendered twice. Seeders gained a `-slug` filter so a corrective `-force` touches only the named cards — a bare `-force` would have rewritten prose on 30 of 52.
- [ ] **Apply the card data refresh to production** — the seeders skip existing rows, so a deploy will not pick this up. Run once against prod (after confirming a backup exists):
      `seed-cards -force -slug amex-platinum,amex-business-platinum,hilton-honors-amex-aspire,amex-green` and the same flags for `seed-structured`. Verified on a copy of prod: changes exactly those 4 cards, leaves the other 48 byte-identical.

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
