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
- [ ] **Card facts go stale, and production keeps the stalest copy** ([#66](https://github.com/anik-apps/milesahead/issues/66)) — nothing re-checks a card after it is written, and the normal seed step skips cards that already exist, so a correction merged to `cards.json` never reaches production without a hand-dispatched `-force -slug` reseed (`/cards/amex-gold` sat contradicting itself this way). Verifying 21 cards against issuer pages on 2026-09-23 found 69 places where production differed from the seed file: the seed was right in 42, *neither* was right in 23 (both stale: the 2025 Sapphire, Amex Platinum and AAdvantage Executive refreshes were never propagated, and expired promotions were still presented as current), and production was right in only 3 — two prose sections plus a "Recent Changes" list a reseed does not touch. Approved card updates and admin edits (`handleApproveCardUpdate`) do write to production without touching `cards.json`, so production-ahead text can exist, but it is rare and nothing records it. *Fix:* (1) a `seed-cards -check` report that diffs the seed against production per section before any `-force`; it can show *that* a section differs but not *which side is newer*, because approvals and admin edits leave no provenance, so record `updated_at` and source per section if direction ever matters; (2) re-verify cards on a schedule and store expiry dates for time-limited offers, since the dominant failure is facts going stale on both sides, not the two copies disagreeing.
- [ ] **Delete stale on-server files** — two 45 MB `milesahead.db.bak.cleanup-20260508-*` files in `/opt/milesahead/data/` (90 MB of dead weight).
- [ ] **Reclaim ~28 GB on the server** — Docker images and build cache are ~28 GB reclaimable on a 66%-full root. *Fix:* `docker builder prune -af` + `docker image prune -a`, then a scheduled prune.
- [ ] **A bad Caddy config can silently break five sites** — `deploy.yml:194-195` copies `milesahead.caddy` into the shared `/opt/fynans/caddy-sites/` then reloads with `2>/dev/null || true`, so a syntax error stays invisible until the next container restart. *Fix:* `caddy validate` before the copy; drop the error suppression on reload.
- [ ] **`/static/` directory listing is enabled and crawlable** — anything dropped there is publicly discoverable by walking the index. *Fix:* 404 directory requests in the static handler, or `Disallow: /static/` in robots.txt.
- [ ] **Compare `<select>` has no accessible name** (`card.html`, WCAG 4.1.2 A) and navigates on change (3.2.2 A). *Fix:* `<h4>` → `<label for>`; replace `onchange="window.location"` with a submit button.
- [ ] **No binary rollback on deploy** — `deploy.yml:185,202` overwrites without keeping the previous binary. *Fix:* `cp milesahead milesahead.prev` before the swap.
- [x] **Articles stuck in "Revising" for hours** — a rewrite ran only from the Revise
      button's one-shot goroutine, so an AI failure or a deploy restart mid-run left the
      article in `revision_requested` for good; email replies and unpublish-and-revise
      set the status but never started a rewrite at all, and the scheduler's
      `AddRevisionJob` was never registered. A revision reconciler now sweeps
      `revision_requested` every 5 minutes and ~45s after startup; every path also kicks
      the rewrite immediately. A per-article in-flight guard and a compare-and-set on
      store stop double rewrites (previously two concurrent runs could both write
      revision N+1), and failures back off 5→40 min and stop after 5, with the count and
      last error on the dashboard. `milesahead_revision_reconcile_total{outcome}` and
      `milesahead_revisions_waiting{state}` report it; an alert on
      `milesahead_revisions_waiting{state="exhausted"} > 0` is still to be added to
      `deploy/grafana-alerts.json`.
- [x] **CI Go version pin is inert** — workflows pinned `1.22` while `go.mod` needs `1.26.1`, so `GOTOOLCHAIN=auto` re-downloaded the toolchain on every invocation and the module cache, keyed on the installed version, never matched (`Failed to restore: /usr/bin/tar ... exit code 2` on every run). Replaced with `go-version-file: go.mod` rather than another hardcoded number, so it cannot drift again.
- [x] **Card data refresh** — CLEAR+ raised to $219 on 2026-07-01 (verified against Amex's own benefits page, which lists $219 for all five eligible cards), so all four cards carrying it were stale at $209; Amex Green's derived "pays them $59/year" was recomputed to $69. The $100 Saks credit ended 2026-07-01 and was removed. The seed prose also carried a duplicated Equinox `<li>` that would have rendered twice. Seeders gained a `-slug` filter so a corrective `-force` touches only the named cards — a bare `-force` would have rewritten prose on 30 of 52.
- [x] **Apply the card data refresh to production** — applied 2026-08-31 with the scoped `-slug` reseed after taking a verified backup. Live pages now show CLEAR+ at $219 on all four cards, no active Saks credit, and the Equinox credit present in the Platinum prose. `recent_changes_json` was untouched, so the historical "Saks credit to be removed" changelog entry correctly survives.
- [x] **Stale cross-card comparison data** — `chase-sapphire-reserve`'s `competitor_comparison` described the Amex Platinum with pre-September-2025-refresh figures ("$200 hotel + $240 digital entertainment + $100 Saks + ... ($1,500+)"), and `citi-strata-elite` cited Saks as an example of Platinum's merchant credits. Fixed in the September 2026 verification of all 52 cards, which checked every card, comparison tables included, against issuer sources; neither card mentions Saks or the old figures now.

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
- [x] **Critic retired, publication stopped again** — `mistralai/mistral-nemotron` was
      retired upstream on 2026-09-28 08:00 UTC; every critic call returned 410, so no
      article passed verification, every revision failed, and image text-scoring, the
      newsletter pick and card-update detection (all hardwired to the critic model)
      failed too. The critic is now `deepseek-ai/deepseek-v4.1-flash` (86 and 88 on a
      real article; minutes per reply). The side jobs, and the pipeline's inline
      key-details call, now run on the `enrichment_model` chain via `ai.TryModels`, so
      they fall over a retired model and never wait on the slow critic under the 30s
      short-call cap. The preflight probes article-path models under the full request
      timeout (a 30s probe would have reported the new critic dead) and now covers the
      enrichment chain.
- [x] **Critic overloaded, every article dropped** — from 2026-09-29 the new critic,
      `deepseek-ai/deepseek-v4.1-flash`, answered every call with a gateway 504 after
      ~5 minutes, so every generated article was skipped and every revision failed.
      `critic_model` is now a chain, tried via `ai.TryModels`:
      `nvidia/nemotron-3-super-120b-a12b` then `moonshotai/kimi-k3`, both with thinking
      off (`critic_disable_thinking`, critic requests only). Measured on the real critic
      prompt: nemotron-super 75 and 95 in 28s / 1.7s, kimi-k3 62 and 62, deepseek 50
      and 50 in 67-101s. The preflight probes every critic in the chain and publishing
      is viable if any answers; `critic scored` logs the model. Caveat: nemotron-super is
      also a generator fallback, so a fallback-written article is self-graded.
- [ ] **Alert when the startup preflight finds no reachable critic** — the 2026-09-28
      retirement logged `ai preflight: no CRITIC is reachable` at ERROR on every startup
      for about two days and nobody saw it. The preflight now exports
      `milesahead_ai_preflight_models_reachable{role}` (set once per process when the
      preflight finishes); add a rule to `deploy/grafana-alerts.json` on
      `milesahead_ai_preflight_models_reachable{role="critic"} == 0`. NoData should NOT
      fire here: the series is absent for the first minutes after every boot while a
      slow critic is probed. Also find out why the generation-drought rule, which should
      have fired about 25 hours into this outage, did not get anyone's attention.
- [x] **Deduplicate across clusters, not just within them** — the raw-ID dedupe
      (`HasArticleForRawIDs`) can only catch a cluster reusing a source article. When one
      story lands in two disjoint clusters, the clusters share no sources by construction
      and the check correctly passes, so the site publishes the same story twice
      (articles 1030 and 1031, 2026-09-21). Measured 2026-09-21..30: 16 of 98 generated
      articles were same-story duplicates across 12 stories, and no pair shared a raw
      article. Three causes: one event split into two clusters in the same scan (~10),
      late coverage resurfacing once the original sources left the 48h window (4-6),
      and new coverage days later. Addressed by two checks:
      **covered raws** (blocking) — a cluster skipped for overlapping an article now
      records its other raws in `covered_raws` against that article, and the raw-ID check
      (`ArticleForRawIDs`) reads it, so late coverage cannot resurface after rolloff; the
      article's public source list is unchanged. **Story check** (`internal/storydedup`,
      `scanner.story_dedup`, shadow by default) — a TF-IDF prefilter against every article
      from the last 7 days (any status), then one short call on the enrichment chain
      deciding same story / materially new development; falls back to lexical >= 0.55 if
      the call fails. Replayed on 99 production articles, the prefilter keeps all 10
      known duplicate pairs at >= 0.25 and needs an AI call for 42 clusters.
- [ ] **Review the story check's shadow decisions, then switch it to block** — after a
      few days of scans, tally the `story dedup decision` log lines with
      `would_block=true` against the admin's approve/reject of the article generated
      from the same `cluster_raw_ids` (and `milesahead_story_dedup_decisions_total`). If
      the would-blocks are real duplicates, set `story_dedup: "block"` in `deploy.yml`.
      Smoke-tested on five known pairs: two duplicates caught, two legitimate stories
      kept; 1124 vs 1110 was judged a follow-up (the launch moved from October 2026 to
      January 2027), which is arguably right.
- [ ] **Clustering merge pass (lower priority)** — clustering is single-pass greedy and
      never merges, which is how one event splits into two clusters in the same scan. An
      average-linkage merge of clusters above the threshold would stop most same-scan
      splits before either duplicate check has to.
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
      full 300s client timeout. Now configurable via `vision_model`. The side jobs that
      called the critic model directly (image text-scoring, newsletter pick, card-update
      detection, inline key details) now use the `enrichment_model` chain; the critic
      itself is still a single model with no chain.

- [x] **Homepage thumbnails rotted away** — 46 of the 80 approved articles with an image
      pointed at a URL a browser could no longer fetch. Openverse's `/thumb/` proxy 424s
      whenever its upstream fetch fails, and Pixabay's signed hotlinks 400 once they
      expire ("This URL is invalid or has expired") — the API terms require downloading
      rather than hotlinking. Openverse rows already held the provider's own stable CDN
      link in `image_url`, so it is promoted to `image_thumbnail`; Pixabay is dropped as a
      source and its rows cleared for the enrichment reconciler to re-pick. Newly chosen
      images are now liveness-probed before they are stored, so a dead URL is caught at
      pick time instead of on the homepage.
- [x] **Card infographics** — designed infographics (1024 px wide, 2:3 or taller) on thirty-one card pages: Amex
      Platinum, Amex Gold, Amex Green, Bilt Palladium, Bilt Blue, Bilt Obsidian, Sapphire
      Reserve, Sapphire Preferred, Freedom Flex, Freedom Unlimited, Citi Strata Elite, Citi
      Strata Premier, Citi / AAdvantage Executive, Capital One Venture X, Capital One
      Venture, Capital One Savor, Capital One Quicksilver, Citi Double Cash, Discover it Cash Back, Hilton
      Aspire, Marriott Bonvoy Brilliant, Marriott Bonvoy Boundless, IHG One Rewards Premier,
      World of Hyatt, BoA Premium Rewards Elite, BoA Premium Rewards, Wells Fargo Autograph,
      Wells Fargo Active Cash, Atmos Rewards Summit, United Quest and Delta SkyMiles Reserve.
      Citi Double Cash is the first design taller than 2:3, at 1024x1818.
      A compact "Infographic / Expand" bar under the
      card art opens the full image in a native `<dialog>` viewer; inline in the article
      column the image rendered at 46% of its size, too small to read. The 400-600 KB full image loads only when the viewer opens, and without
      JavaScript the bar is a plain link to it. The image's content is also given as a
      structured text version, collapsed under it, and image URLs carry a content hash so a replaced
      image is never served from a stale cache. Each entry lists the figures the image
      states, and a test fails if one disappears from the card's `cards.json` text, since an
      image cannot be corrected by a reseed the way prose can. That check sees the seed
      file only, so admin edits to the live prose bypass it.
- [x] **Site branding** — the band at the bottom of every infographic (mountain mark,
      "MilesAhead" wordmark, "Travel further. Earn smarter.", misty ridges, the plane and
      POINTS • TRAVEL • DEALS • GUIDES) now brands the site: a misty header on every page,
      the home hero, and a footer band with real links, the four words included. The art is
      vector (36 KB of SVG in `static/img/brand/`) with a dark-theme version; email,
      the favicon and the default share image use PNG copies, since Outlook and Gmail do not
      show SVG. The name is one word everywhere, as on the infographics. The phone header
      was 101px and wrapped the logo and "Toggle Theme" into "Cards"; it is now one line,
      with a sun/moon toggle that follows the OS dark-mode setting before first paint.

## Up Next

- [ ] **Newsletter signup and delivery are broken end to end** — found in the
      2026-09-23 UX review, and confirmed in the code. Nobody who signs up on the site
      ever receives the newsletter, and nobody who receives it can unsubscribe.
      1. **No confirmation email is sent.** `handleSubscribe` stores the address and a
         token, then says "check your email", but `mail.Sender.SendConfirmation` has no
         caller and the web `App` has no mail sender. The weekly send goes only to
         confirmed subscribers, so web signups are silently dropped. *Fix:* pass the
         sender into `web.App` and send the confirmation from `handleSubscribe` in a
         tracked background goroutine; log and alert on failure; test that it is sent.
      2. **Every Unsubscribe link fails.** The newsletter is rendered once for all
         recipients with `UnsubscribeURL: domain + "/unsubscribe"`, no token, so each
         click shows "Invalid unsubscribe link". There is no `List-Unsubscribe` header
         either, which Gmail and Yahoo require of bulk senders and CAN-SPAM expects to
         work. *Fix:* a per-recipient `/unsubscribe?token=...`, plus `List-Unsubscribe`
         and `List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058) handled by
         a POST endpoint. `GET /unsubscribe` should show a confirm button that POSTs,
         so link scanners cannot unsubscribe people.
      3. **There is no way to sign up.** No page has a form, and `GET /subscribe`
         returns 405 (`routes.go` registers only POST). The one link to it is in the
         home page's empty state. *Fix:* a `GET /subscribe` page, and a labelled email
         form (`type=email`, `autocomplete=email`) in the footer band, after Scout's
         Take on every article, and on the home page, with a one-line promise and a
         privacy link. Add Subscribe to the footer links only once 1 and 2 work.
      4. **Harden the form before it goes on every page.** Rate-limit `POST /subscribe`
         (the existing `rateLimitMiddleware`), add a honeypot field, and answer new and
         existing addresses with the same "check your inbox" message: today "This email
         is already subscribed" tells anyone whether an address is on the list. Make
         confirmation idempotent, so a second click (or a mail scanner opening the link
         first) shows success rather than "Invalid or expired".
      5. **Email readability.** Most newsletter text is 9-12px, and the gold section
         labels are 2.29:1 on white. *Fix:* body text at least 14px, labels at least
         12px, contrast at least 4.5:1.

      Order: 1 and 2 first (they break mail already going out), then 3 and 4 together.
      Already fixed alongside the branding change: the daily digest and admin preview
      rendered relative links, and the lead story linked to `/articles/`, a 404.

- [ ] Card explainer videos — YouTube-hosted, embedded per card page (Amex Platinum, Sapphire Reserve, Strata Elite so far)
- [ ] **More card infographics** — each is `scripts/infographic-images.sh <slug> <png>` plus an entry in `internal/web/cardinfographics.go`. Verify the card's prose against the image before adding one: the drift test only confirms the image's figures appear somewhere in the text, so it cannot see a *second, older* figure. Sapphire Reserve's prose once claimed the retired 1.5 cents per point and a $100 Global Entry credit beside an image that correctly said 1 cent plus Points Boost and $120; the test passed anyway.
- [ ] **Revisit video heading framing** — headings currently vary with the verdict: positive cards ask "is the $X fee worth it?", while the Strata Elite asks "do the credits cover the $595 fee?" because that video concludes they do not. Deliberate, so a heading never promises an answer the video does not give — but it means the phrasing is not uniform across cards. Worth a look once there are enough videos to judge whether the inconsistency reads as considered or as sloppy.
- [ ] Additional model providers (Groq, Gemini) — reduce dependency on NVIDIA API
- [ ] OAuth/SSO login — replace single bcrypt password with Google OAuth
- [ ] Affiliate link integration — monetize card reference pages
- [ ] Social media auto-posting — auto-share to Twitter/X, Bluesky
- [ ] Mobile app / PWA — offline reading, push notifications

## UX Review (2026-09-23)

Five reviews of the live site (home, articles, card pages, compare and search, site
chrome, accessibility, performance and mobile), each finding checked independently
against screenshots, the code and the live site. The newsletter findings are the first
item under Up Next; the branding, the phone header, the favicon and the default share
image shipped in #73. Everything else is here, most severe first.

### P0: broken for readers today

- [x] **44 of 52 card pages were wider than a phone screen** — comparison tables in card
      sections had no scroll box, so the page grew to 566-694px on a 390px phone, slid
      sideways and lost the sticky header. Each table now scrolls inside its own labelled
      region, with edge shadows showing there is more; measured 0 of 52 afterwards.
- [x] **"Was this article helpful?" did nothing** — the script looked for `.vote-section`,
      which the template never rendered, and sent `1`/`-1` where `/api/vote` accepts
      `up`/`down`; all 87 articles showed 0 / 0. Votes now record, confirm on screen
      (announced to screen readers), survive a reload, and counts appear only from ten
      votes.
- [ ] **The home page downloads 16.5 MB** — two thumbnails are raw Wikimedia originals
      (13.2 MB and 3 MB) shown at 100-360px; seven articles use originals.
      `internal/images/search.go` stores the Openverse original as both image and
      thumbnail. *Fix:* sized Wikimedia/Flickr URLs at standard widths, corrected existing
      rows, lazy loading and dimensions on home images. In progress on
      `fix/home-image-weight`.
- [ ] **Expired deals are shown as current** — 16 of 87 articles are past the deadline in
      their own Key Details, 4 of them on the home page (e.g. Hilton Aspire 175K, ended
      July 29). *Fix:* a stored end date, ended offers left out of the default home feed,
      an "Ended" badge elsewhere, a banner on the article, and none in the newsletter. In
      progress on `feat/expired-deals`.

### P1: findability and conversion

- [ ] **Card pages lack a key-facts strip** — the fee appears only mid-sentence, and Scout's
      Verdict sits at 9,546px of 11,019px on a phone. *Fix:* annual fee, welcome offer, top
      earn rates and the verdict's first sentence under the H1, from the DB fields.
- [ ] **Card sidebar taller than a laptop screen** — sticky at `top: 5rem` but 914px tall,
      so "Card Details" and "Compare with…" stay below the fold for the whole page at
      1440x900 and below. *Fix:* put the CTA and Compare above the TOC, or cap the sidebar
      at `calc(100vh - 6rem)` with its own scroll.
- [ ] **Card-page CTA and compare** — "Card Details →" opens the issuer's site in a new tab
      with no cue; the page ends with no next step; the Compare control does not exist on
      phones and its `<select>` has no label and navigates on change. *Fix:* "Apply at
      {issuer} ↗", an end-of-page block (apply, compare with the rivals the page already
      names, related cards), and a labelled form with a Compare button.
- [ ] **Article card embeds** — the story's own card is missing (the Freedom Flex article
      embeds Reserve and Platinum, not Flex), "View card" leaves for the issuer, and "Top
      earnings" always shows dining and groceries ("1x Dining · 1x Groceries" for Amex
      Platinum) because `formatTopEarning` walks a fixed category order. *Fix:* primary
      card first, link to `/cards/{slug}`, rank earn rates numerically including the
      direct/portal variants.
- [ ] **Home reaches only 20 of 87 stories** — no pagination (`?page=2` is ignored), and two
      filter rows that look combinable silently reset each other; "Roundups" and "Alerts"
      lead to empty pages. *Fix:* "Older stories" paging that keeps filters, one merged
      filter row, hide empty filters.
- [ ] **Compare** — 40 caveats per comparison are hover-only `title` tooltips; Benefits and
      Protections toggles are click-only `<tr>`s keyboard users cannot open; winner stars
      use only the portal rate in "D | P" rows and "1 year" beats "Yes"; phones see 1.3 of 3
      cards; no links to card pages. *Fix:* visible notes, `<button aria-expanded>` toggles,
      split D/P rows, a legend, a mobile layout, links per column.
- [ ] **Search** — the whole query is one exact FTS phrase, so "sapph", "reserve sapphire"
      and "csr" find nothing, and card matches sit below ten articles. *Fix:* per-word
      matching with a prefix on the last word, title weighting, a small synonym map
      (csr, csp, amex), cards first when a title matches.
- [ ] **/cards/ index** — 52 cards with no filter, sort or fee; 22 screens on a phone.
      *Fix:* category, fee band and issuer filters with a live count; fee, offer and top
      earn rates on each tile; lazy-loaded art with fixed dimensions.
- [ ] **Trust signals** — article sources are loaded but never rendered; no byline, About,
      corrections or privacy page, though the site collects emails. *Fix:* a Sources list
      per article, a byline linking to an About page on how stories are researched, and a
      privacy page linked next to every email field.

### P2: polish and accessibility

- [ ] **Contrast** — in dark mode, white on the `#58a6ff` accent is 2.53:1 (active filter
      pills, search button); the DEAL badge is 3.83:1 in both themes; dark-mode links in
      paragraphs are distinguished by colour alone (2.14:1). *Fix:* an `--on-accent`
      token, a darker deal red, underlined in-text links.
- [ ] **404 page** — unknown URLs get Go's bare text "404 page not found"; unpublished
      articles return 200 "Under Review". *Fix:* a branded 404 through the layout with
      search and links; 404 or 410 for unpublished articles.
- [ ] **Compression and caching** — HTML, CSS and JS go out uncompressed (CSS 56 KB, 10 KB
      gzipped) and nothing sends Cache-Control. *Fix:* `encode zstd gzip` in the Caddy site,
      then content-hashed static URLs with long cache lifetimes.
- [ ] **Home on phones** — the featured story's white title sits on a nearly unshaded photo
      (3.2-4.0:1), and its overlay stops partway, leaving ~475px of unlinked photo on
      desktop; each feed card is ~400px tall. *Fix:* a guaranteed scrim, a fixed-ratio
      featured card, clamped excerpts.
- [ ] **Excerpts** cut mid-word and run headings into sentences, in cards, meta
      descriptions and RSS. *Fix:* use the TL;DR, or cut at a sentence boundary.
- [ ] **Smaller accessibility fixes** — skip link; `aria-expanded` and Escape on the Cards
      and search toggles; Scout's Verdict as a heading; `scroll-padding-top` so anchors
      clear the sticky header; internal article links no longer forced into new tabs;
      labels on the search field; a visible `:focus-visible` ring site-wide.

## Future

- [ ] Multi-user admin with roles
- [ ] API for third-party consumption
- [ ] Webhook integrations (Slack, Discord, Telegram)
- [ ] A/B testing for article generation models
- [ ] Article quality scoring based on admin approval patterns
- [ ] Geographic content targeting (US vs international deals)

---

*Items are picked up based on priority and need, not strictly sequential.*
