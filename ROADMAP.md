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
- [x] **CI Go version pin is inert** — workflows pinned `1.22` while `go.mod` needs `1.26.1`, so `GOTOOLCHAIN=auto` re-downloaded the toolchain on every invocation and the module cache, keyed on the installed version, never matched (`Failed to restore: /usr/bin/tar ... exit code 2` on every run). Replaced with `go-version-file: go.mod` rather than another hardcoded number, so it cannot drift again.
- [x] **Card data refresh** — CLEAR+ raised to $219 on 2026-07-01 (verified against Amex's own benefits page, which lists $219 for all five eligible cards), so all four cards carrying it were stale at $209; Amex Green's derived "pays them $59/year" was recomputed to $69. The $100 Saks credit ended 2026-07-01 and was removed. The seed prose also carried a duplicated Equinox `<li>` that would have rendered twice. Seeders gained a `-slug` filter so a corrective `-force` touches only the named cards — a bare `-force` would have rewritten prose on 30 of 52.
- [x] **Apply the card data refresh to production** — applied 2026-08-31 with the scoped `-slug` reseed after taking a verified backup. Live pages now show CLEAR+ at $219 on all four cards, no active Saks credit, and the Equinox credit present in the Platinum prose. `recent_changes_json` was untouched, so the historical "Saks credit to be removed" changelog entry correctly survives.
- [ ] **Stale cross-card comparison data** — `chase-sapphire-reserve`'s `competitor_comparison` still describes the Amex Platinum with pre-September-2025-refresh figures: "$200 hotel + $240 digital entertainment + $100 Saks + ... ($1,500+)". Hotel is now $600, digital entertainment $300, Saks is gone, and the credits total is roughly $3,000. `citi-strata-elite` also cites Saks as an example of Platinum's scattered merchant credits. Both need verifying against Amex before rewriting, like the main refresh did.

- [x] **Article generation stopped for 26 days, silently** — the provider retired
      `openai/gpt-oss-120b` (generator), `meta/llama-3.3-70b-instruct` (critic) and
      `qwen/qwen3.5-122b-a10b` (the fallback) within the same window; every run since
      2026-08-26 failed with `410 Gone` at WARN level with nothing alerting on it.
      Three compounding bugs, all fixed: 410/404 were classified non-retryable so the
      fallback never fired; the lone fallback was itself dead; and `HFClient` retried
      only 429/503, so a transient 500 killed a request outright. Fallback is now a
      chain, model-gone triggers it, all 5xx retry, and a startup preflight probes
      every configured model. Models re-selected empirically with `cmd/ab-models`.
- [x] **Config changes never reached production** — `config.yaml` was written only by
      the `setup` job, gated behind `if: github.event.inputs.full_setup == 'true'`, so a
      normal push never updated it: model names changed in this repo silently did not
      deploy. Worse, `setup` and `deploy` both only needed `build`, so they raced —
      `deploy` restarts milesahead while `setup` writes the config, and nothing restarts
      it afterwards, meaning even the manual path applied config only by luck of timing.
      Config now has its own ungated job that `deploy` depends on, so the restart always
      follows the write.
- [ ] **Per-article pipeline status in the admin view** — show, for every article, which
      steps are done, in progress, or failed: generated, critic-verified, key details
      extracted, Scout's Take written, image selected, tags assigned, cards auto-linked.
      Today an editor cannot tell a thin article apart from one still being worked on, and
      the only way to find degraded articles is a SQL query. That blindness is what let
      24% of articles publish with missing fields for two and a half months.
      The data now exists to build it: `enrichment_attempts` and `enrichment_last_at` give
      progress and staleness, `milesahead_enrichment_failures_total{step}` gives per-step
      failure counts, and each field's emptiness is directly queryable (noting
      `key_details` uses the literal string `'null'`, not NULL). Worth surfacing
      `enrichment_attempts >= max` distinctly as "given up" rather than "pending", and
      offering a per-article retry that resets the counter.
- [x] **Enrichment has no pass-level deadline or liveness signal** — a pass had no bound
      of its own, so on a degraded provider it could outlast its interval many times over;
      every later tick then logged a skip and `milesahead_articles_degraded` froze at its
      last value, making a stalled reconciler indistinguishable from an idle one. Passes
      are now bounded by `enrichment_max_duration` (checked between articles), skips and
      deadline hits are counted in `milesahead_enrichment_passes_total{outcome}`, the
      degraded gauge refreshes on every outcome rather than only on success, and
      `milesahead_enrichment_last_success_timestamp_seconds` gives a liveness signal that
      goes stale when passes stop completing. A Grafana rule alerts when it has not
      advanced in an hour against a 15-minute interval.

- [ ] **No failure memory across model calls** — `TryModels` and `callWithFallback` are
      stateless, so a timing-out model burns its full retry budget on every field of every
      article: worst case 10 articles x 4 fields x 2 models x 4 attempts = 320 attempts
      against a provider already known degraded. *Fix:* per-model failure counter with a
      cooldown, scoped to the pass at minimum.
- [ ] **Retries are invisible in metrics** — `AIAPIErrorsTotal` only increments on terminal
      failure and `AIAPIDuration` measures the whole retry chain including backoff, so a
      call that fails three times then succeeds looks like one slow success. No metric
      would have shown "a quarter of calls are timing out" — the exact condition that ran
      unnoticed for 2.5 months.
- [ ] **Shutdown cannot interrupt AI work** — `Generate` takes no `context.Context`, its
      backoff `time.Sleep` is uninterruptible, and the `errors.Is(err, context.Canceled)`
      guard is dead code since the context is created internally from `Background()`.
      Separately the 90s startup enrichment goroutine is registered with neither cron nor
      `main`'s context, so `Stop()` neither waits for nor cancels it: a SIGTERM at ~85s
      lets it wake and run a pass against a closed database.
- [ ] **Overlap guard is per-process only** — `atomic.Bool` cannot serialise across a
      deploy, so a restart mid-pass gives two reconcilers on the same rows, each
      incrementing `enrichment_attempts`. *Fix:* a DB-level lease.
- [ ] **408 is not classified transient**, and a per-model 400 aborts the whole fallback
      chain instead of trying the next model (`Generate` always sends `temperature: 0.7`,
      which some models reject).
- [ ] **Enrichment scope is unbounded by status** — the reconciler enriches all `pending`
      and `draft` articles, not just approved: ~226 degraded rows, up to ~900 model calls,
      for articles that may never publish. Worth an explicit cost decision.
- [x] **Alert on generation drought** — `ArticlesGeneratedTotal` and `AIAPIErrorsTotal`
      were already being scraped into Grafana Cloud throughout the outage; no alert was
      wired to them. Two rules now ship from `deploy/grafana-alerts.json` with stable
      UIDs, deployed idempotently alongside the dashboards. Note the obvious rule
      (`== 0`) would NOT have caught it: the counter is a CounterVec, so during the
      outage its series was absent rather than zero, which is why the drought rule
      treats NoData as firing. Runbook at `docs/runbooks/generation-drought.md`.
- [ ] **Deduplicate across clusters, not just within them** — the raw-ID dedupe
      (`HasArticleForRawIDs`) can only catch a cluster reusing a source article. When one
      story lands in two disjoint clusters, the clusters share no sources by construction
      and the check correctly passes, so the site publishes the same story twice
      (articles 1030 and 1031, 2026-09-21). Lowering the clustering threshold fixed that
      case, but the class remains: a title-similarity check against recently generated
      articles would close it properly.
- [x] **Enrichment failures are invisible** — `extract details`, `scouts take`, image
      query generation and image scoring were five best-effort inline calls in the
      article-creation loop, each logging WARN and continuing, so a transient provider
      problem permanently cost the article a field. Root cause found in the production
      journal: from 2026-07-01 the provider's latency crossed the client timeout on ~24%
      of calls, and transport errors were never retried at all — the retry loop only
      handled HTTP status codes. Enrichment now runs as a scheduled reconciliation pass
      (`internal/enrich`) against its own model chain, bounded by `enrichment_attempts`,
      with `milesahead_enrichment_failures_total{step}` and `milesahead_articles_degraded`
      to make the gap visible. A missed field is now retried on the next pass instead of
      being lost, and the ~62 historical articles are picked up automatically.

- [ ] **Audit remaining hardcoded models** — `meta/llama-3.2-90b-vision-instruct` was
      hardcoded in `main.go` and times out on every call, so each scored image burned the
      full 300s client timeout. Now configurable via `vision_model`. Direct
      `hfClient.Generate(cfg.AI.CriticModel, ...)` calls in `main.go` still bypass the
      fallback chain entirely.

- [x] **Homepage thumbnails rotted away** — 46 of the 80 approved articles with an image
      pointed at a URL a browser could no longer fetch. Openverse's `/thumb/` proxy 424s
      whenever its upstream fetch fails, and Pixabay's signed hotlinks 400 once they
      expire ("This URL is invalid or has expired") — the API terms require downloading
      rather than hotlinking. Openverse rows already held the provider's own stable CDN
      link in `image_url`, so it is promoted to `image_thumbnail`; Pixabay is dropped as a
      source and its rows cleared for the enrichment reconciler to re-pick. Newly chosen
      images are now liveness-probed before they are stored, so a dead URL is caught at
      pick time instead of on the homepage.
- [x] **Card infographics** — designed 1024x1536 infographics on thirteen card pages: Amex
      Platinum, Amex Gold, Amex Green, Bilt Palladium, Sapphire Reserve, Sapphire Preferred,
      Citi Strata Elite, Citi Strata Premier, Capital One Venture X, Hilton Aspire, Marriott
      Bonvoy Brilliant, BoA Premium Rewards Elite and World of Hyatt. A compact "Infographic / Expand" bar under the
      card art opens the full image in a native `<dialog>` viewer; inline in the article
      column the image rendered at 46% of its size, too small to read. The 400 KB full image loads only when the viewer opens, and without
      JavaScript the bar is a plain link to it. The image's content is also given as a
      structured text version, collapsed under it, and image URLs carry a content hash so a replaced
      image is never served from a stale cache. Each entry lists the figures the image
      states, and a test fails if one disappears from the card's `cards.json` text, since an
      image cannot be corrected by a reseed the way prose can. That check sees the seed
      file only, so admin edits to the live prose bypass it.

## Up Next

- [ ] Card explainer videos — YouTube-hosted, embedded per card page (Amex Platinum, Sapphire Reserve, Strata Elite so far)
- [ ] **More card infographics** — each is `scripts/infographic-images.sh <slug> <png>` plus an entry in `internal/web/cardinfographics.go`. Verify the card's prose against the image before adding one: the drift test only confirms the image's figures appear somewhere in the text, so it cannot see a *second, older* figure. Sapphire Reserve's prose still claims the retired 1.5 cents per point and a $100 Global Entry credit beside an image that correctly says 1 cent plus Points Boost and $120.
- [ ] **Revisit video heading framing** — headings currently vary with the verdict: positive cards ask "is the $X fee worth it?", while the Strata Elite asks "do the credits cover the $595 fee?" because that video concludes they do not. Deliberate, so a heading never promises an answer the video does not give — but it means the phrasing is not uniform across cards. Worth a look once there are enough videos to judge whether the inconsistency reads as considered or as sloppy.
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
