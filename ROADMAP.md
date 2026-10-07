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
- [x] Article tags — AI-generated from 8 categories, six of them topic filters on the homepage
- [x] Weekly newsletter — Scout's Pick, Card of the Week, tag-grouped articles, newspaper-style template
- [x] Newsletter admin workflow — AI picks candidates, admin approves, auto-approve fallback
- [x] Card comparison tool — side-by-side comparison of 2-3 cards with structured data
- [x] Structured card data — earning rates, credits, benefits, protections, transfer partners for 52 cards
- [x] CI/CD refactored into separate build/deploy/seed/grafana jobs
- [x] Admin rework — own shell with counts on the tabs, grouped review queue with decisions in the preview (revision-checked approve, approve-and-next, undo, shortcuts), source health, card editor with inline flags, card updates as a word diff
- [x] Review queue sort (newest first by default, oldest, critic score) and per-row actions menus on Review and Published; compact type pills
- [x] Article infographics (off until the item in Up Next) — an "At a glance" chart under
      Key Details: a model picks the numbers as JSON (a change, a comparison, figures or a
      timeline), and the site draws them in its own type and colours. Outlet, numbered-source
      and unsupported-number guards on its words; a comparison's bars must share one unit
- [x] Find a new image — an admin action (preview, Review and Published row menus) with optional search words; the enrichment pass finds a replacement first in line with a fresh budget, swaps it in only once found (the live image stays meanwhile), and never re-picks a turned-down image at any size. Rewrite feedback that mentions a picture points here, since rewrites only change the text

## Reliability & Hardening

Found during the 2026-08-31 card-video review, ordered by severity.

- [x] **Deploy waits for the tests** — Deploy ran on every push to main without waiting for CI, so #90 shipped with failing tests (2026-10-01: CI was red on the related-stories query, and every article page lost its related stories). Deploy now has its own `test` job (vet, `go test -race`, the JS tests) and `build` needs it, so nothing is built, configured or shipped unless the tests pass. Manual dispatches (reseed, full setup) are gated the same way.
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
- [x] **Articles said "Source 1 notes..."** — the prompts labelled their inputs
      `--- Source 1: [Outlet] ---` and the generator copied the label into published copy;
      the featured article on 2026-10-01 read "Source 1 notes this offer 'doesn't come around
      often'". Four articles were affected (2 approved, 2 pending). Sources are now labelled
      by outlet only, every writing prompt forbids positional references, and the critic
      fails attribution on them. Generated text is also checked deterministically: a
      reference that maps to a source is rewritten to the outlet's name, and one that does
      not fails the attribution check and goes to the auto-revision. Scout's Take and key
      details refuse such text.
- [x] **Articles named the news sites they were written from** — the site owner's rule
      is that articles never name or refer to the outlets in the `sources` table (or
      TPG, OMAAT, DoC, VFTW, FM), except when the outlet is the subject of the story or
      for an unconfirmed report only it carries. The fix above made it worse: it
      rewrote "Source 1 notes" to "One Mile at a Time notes". Now a numbered reference
      is flagged rather than rewritten, every prompt carries the rule and both
      exceptions, and a deterministic check flags outlet mentions in the title, TLDR,
      key details and body for the one auto-revision (a survivor is kept, failing
      attribution, for the admin). Links to outlet sites are unwrapped. Enrichment
      refuses a Scout's Take, and drops a key detail, that names an outlet.
      `cmd/repair-source-refs`, which wrote outlet names into stored articles, is gone.
- [x] **Critic listed passed checks as issues** — the pipeline auto-revises whenever the
      critic returns issues, and 68 of 71 articles verified in the week to 2026-10-01 were
      revised (about 75 seconds and two extra AI calls each). Many issues were not problems:
      "Deadline of December 31, 2026 matches both sources.", "No contradictions — coherence
      is strong." The critic prompts now ask for fixes only and an empty list when nothing
      needs changing, issues that only confirm a pass are dropped after parsing (32 of 389
      stored issues, none of them real problems), and a critic reply that sends issues as
      objects is flattened rather than failing the article. The revision still fires on any
      remaining issue, whatever the score: verdicts scoring 85-95 with every check passing
      carried real errors.
- [x] **Editor notes were published, hidden only by CSS** — in March 2026 the revision
      prompt asked the model to append `<div class="editor-note">Could not find a link
      for: ...</div>` when it could not do what the admin asked. The note was stored in the
      article content and `display: none` hid it, but it was in the page source and the
      search index: searching "Could not find a link" returned articles 53, 57 and 66 with
      the note highlighted in the snippet. Four approved articles carry one (53, 57, 60,
      66). Notes are now split out of content before it is stored, into an admin-only
      `editor_notes` column shown in the dashboard preview and review email; loading an
      article, and the search index, strip any still inline; startup reindexes affected
      rows. `cmd/repair-editor-notes` moves them out of stored content (dry run by default).

## Up Next

- [ ] **Turn on article infographics** — merged off (`ai.enrichment_infographics: "off"`
      in config.example.yaml and deploy.yml; `TestDeployConfigMirrorsCritic` keeps the
      two equal). Set both to `"preview"`, let a few passes run, and judge the charts in
      the review queue preview and on Published (each row's "Chart"): numbers match the
      Key Details, labels fit, nothing names a news site. Remove or redraw any that are
      wrong, then set `"on"`. Checked locally against ten published articles with
      the production models. First run (2026-10-05): 9 drawn, every number in its
      article, but only 2-3 publishable: five had labels cut off at the edge, two
      drew bars for things that do not compare, one reversed the article's rule in
      its takeaway. After the edge check, the same-unit and takeaway rules, the
      nemotron-3-super-first chain and 12,288 tokens (2026-10-06): 7 drawn, 3
      correctly "nothing to chart", none cut off, misleading or wrong; 5 good, 2
      correct but thin (a tier chart whose labels repeat its values, a two-date
      timeline). One label was wrong in a way no check sees ("Max nights" on a
      points bar), so review every chart in `preview` before `on`.

- [ ] **Fix stored articles that name news sites** — `cmd/flag-outlet-mentions`
      (read-only) found 33 on 2026-10-01: 6 approved, 18 pending, 6 draft, 2 rejected,
      1 revision requested. The owner is fixing the 6 approved ones by hand; the rest
      need a revision request or an edit, after judging the two exceptions (articles
      1019 and 1022 are about Thrifty Traveler Premium itself). Article 336 also ends in
      a leaked revision changelog.

- [ ] **Run `repair-editor-notes -apply` on production** — dry run on 2026-10-01 lists
      articles 53, 57, 60 and 66, all approved. Take a backup first. Readers see no
      change; the notes move to the admin-only column.

- [ ] **Newsletter signup and delivery** — found in the 2026-09-23 UX review. Items 1-4
      are done (#99) and sign-up is open (`newsletter.signup_enabled: true` since
      2026-10-02; see below). Left: readability (5) and the follow-ups below.
      1. ~~**No confirmation email is sent.**~~ `cmd/milesahead/main.go` hands the SMTP
         sender to the web app (`SetConfirmationMailer`). The email is plain text plus
         HTML, links absolutely to `https://DOMAIN`, says what the newsletter is and when
         it comes (`newsletter.WeeklySendWhen`, held to the cron line by a test) and that
         the address is only used to send it. Failures are logged, counted in
         `milesahead_emails_sent_total{kind="confirmation",outcome="failed"}` and release
         the 10-minute claim; startup warns when the SMTP settings look unset.
      2. ~~**Every Unsubscribe link in the newsletter fails.**~~ Each copy now carries its
         reader's own `https://DOMAIN/unsubscribe?token=...`, in the body and in
         `List-Unsubscribe` (a mailto only if `newsletter.unsubscribe_mailbox` is set) and
         `List-Unsubscribe-Post: List-Unsubscribe=One-Click`, which `POST /unsubscribe`
         accepts. The issue is rendered once with a placeholder that the sender replaces
         per reader, and the sender refuses to send if any copy could not carry a link.
         Emails sent before this still have the bare `/unsubscribe`, which explains
         itself and offers a mailto.
      3. ~~**There is no way to sign up.**~~ `GET /subscribe` is a labelled form, now
         linked from the header (Newsletter; the envelope alone on phones), the footer,
         the home hero's subline and a compact box after Scout's Take on every article,
         each with the line "One email a week. Unsubscribe anytime. We only use your
         address to send the newsletter." A Privacy page link joins them once that page
         exists (below).
      4. ~~**Harden the form.**~~ Done: per-IP rate limit, a honeypot field, the same
         redirect to "check your inbox" for new, pending and subscribed addresses, and
         idempotent confirmation (a second click says "already subscribed").
         Unsubscribing keeps the row (`unsubscribed_at`) so the page can offer
         Resubscribe.
      5. **Email readability.** Most newsletter text is 9-12px, and the gold section
         labels are 2.29:1 on white. *Fix:* body text at least 14px, labels at least
         12px, contrast at least 4.5:1.

      Alerting: `milesahead_emails_sent_total{kind,outcome}` counts confirmation emails
      and newsletter sends (one "sent" per reader reached, one "failed" per send that
      stops); `deploy/grafana-alerts.json` has two rules, two or more failed
      confirmations in 6 hours and any failed newsletter send in 6 hours. Import them
      into Grafana with the others. While SMTP looks unset, the Saturday send is
      skipped with a warning (not a failure, so the alert stays quiet, and the issue
      stays unsent) and the admin's Send says SMTP is not configured.

      **Turned on 2026-10-02.** OCI Email Delivery in `us-sanjose-1`: email domain
      `milesahead.kumaranik.com` with DKIM selector `milesahead-202610`, approved sender
      `noreply@milesahead.kumaranik.com`, and a send-only IAM user `milesahead-smtp`
      (group `milesahead-smtp-senders`, policy `milesahead-smtp-send`: "use
      email-family") whose SMTP credentials are the `SMTP_HOST`/`SMTP_USER`/
      `SMTP_PASSWORD` repository secrets. Cloudflare has the DKIM CNAME, SPF
      (`include:rp.oracleemaildelivery.com`) and DMARC (`p=none`) on
      `milesahead.kumaranik.com`; a test message passed SPF, DKIM and DMARC at Gmail.
      `signup_enabled: true` in the deploy config. Free tier: 3,000 emails a month.
      - [ ] Sign up with a real address after the deploy, confirm it, and import the
        two email rules in `deploy/grafana-alerts.json` into Grafana.
      - [ ] Tighten DMARC to `p=quarantine` after a few weeks of clean sends.
      - [ ] Set the `ALERT_EMAIL` repository secret to a mailbox the owner reads, to
        get vote alerts by email (`votes.alert_email`). Until then they are listed on
        the admin Published page only, which they are either way. They used to be
        emailed to `noreply@`, which nobody reads.
      - OCI adds its **own** List-Unsubscribe to any message without an https one, and
        an unsubscribe through it lands on OCI's suppression list, not in the app. So
        every reader email (newsletters and the confirmation) carries the app's own
        per-reader link; OCI leaves those alone. Admin emails (vote alerts to
        `votes.alert_email`) still get OCI's header: don't use Gmail's Unsubscribe on them,
        or OCI will stop delivering them (remove the address from the suppression
        list in the OCI console to undo).
      - `newsletter.unsubscribe_mailbox` stays empty (no monitored mailbox), so
        `List-Unsubscribe` carries the https link only.

      Also found: the daily digest (admin "Send", not the weekly) shows its excerpts as
      raw HTML (`<p>...`), because `buildTemplateData` cuts `Content`, which is HTML,
      rather than the summary. The weekly uses the TL;DR and is not affected.

      Already fixed alongside the branding change: the daily digest and admin preview
      rendered relative links, and the lead story linked to `/articles/`, a 404.

- [ ] **About and Privacy pages** — designed in the 2026-10-01 site-chrome review (footer
      links, a privacy link beside every email field) but blocked on copy only the owner
      can write: how stories are researched and reviewed, and what is done with
      subscriber addresses. The sign-up placements (Newsletter 3) shipped with a
      one-line privacy promise in place of the link; add the Privacy link beside each
      email field once the page exists.

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

- [x] **Card pages lack a key-facts strip** — the fee appeared only mid-sentence, and Scout's
      Verdict sat at 9,546px of 11,019px on a phone. A summary panel under the H1 now gives
      the annual fee, welcome offer (cut down from the stored text, never with an invented
      figure), the best earning rates and the verdict's first sentence, at every width.
- [x] **Card sidebar taller than a laptop screen** — it was 914px tall, so "Card Details"
      and "Compare with…" stayed below the fold at 1440x900 and below. Apply and Compare
      are in the summary panel (on screen at 1280x800), the page is 1100px wide (text
      column 476px → 760px), and the sidebar holds only the table of contents (448px),
      no longer boxed by the header's `nav` styles, with the current section highlighted.
      One `--sticky-offset` sets every sticky and anchor offset.
- [x] **Card-page CTA and compare** — "Card Details →" opened the issuer's site in a new tab
      with no cue, 34 of 52 pages ended with no next step, and Compare was missing on
      phones, unlabelled and navigated on change. Now "Apply at {issuer} ↗" (with the new
      tab announced; "Card details" for cards closed to new applicants), a labelled GET
      compare form with a button on every screen size, the page's own rivals first, and a
      "Next steps" block on every card page: apply, one-tap comparisons with the rivals the
      page links, more cards in its category. Phone section pills stick under the header.
- [ ] **Article card embeds** — the story's own card is missing (the Freedom Flex article
      embeds Reserve and Platinum, not Flex), "View card" leaves for the issuer, and "Top
      earnings" always shows dining and groceries ("1x Dining · 1x Groceries" for Amex
      Platinum) because `formatTopEarning` walks a fixed category order. *Fix:* primary
      card first, link to `/cards/{slug}`, rank earn rates numerically including the
      direct/portal variants. (Earn-rate ranking done: `rankEarnRates`, shared with the card
      pages; the embed now reads "5x flights · 5x hotels via Amex Travel".)
- [x] **Home filters** — two filter rows looked combinable but silently reset each other,
      both "All"s showed as selected, and "Roundups" and "Alerts" led to empty pages. Now
      one row: a type switch listing only types with a published article ("Summaries" reads
      "News", as does the summary badge), then six topic chips (Loyalty Programs and Points
      Deals left out of the bar; their URLs still work). Type and topic apply together,
      every link keeps the other filter, and a strip lists the applied filters with a link
      to remove each. On phones the row scrolls sideways instead of wrapping.
- [ ] **Home reaches only 20 of 87 stories** — no pagination (`?page=2` is ignored).
      *Fix:* "Older stories" paging that keeps filters.
- [x] **Compare** — 40+ caveats per comparison were hover-only `title` tooltips; Benefits
      and Protections were click-only `<tr>`s keyboard users could not open; winner stars
      used only the portal rate in "D | P" rows and "1 year" beat "Yes"; phones saw 1.4 of 3
      cards; no links to card pages. Now every note is printed under its value; sections
      are open `<button aria-expanded>` toggles; direct and travel-portal rates are separate
      rows; a star needs comparable numbers in one unit and skips top/rotating-category
      rates; the fee is a row (lowest wins); a legend explains the marks. On phones each
      row's label sits above the values, so all three cards fit with the names pinned.
      Cards link to their pages, rows no card has are hidden and counted, catch-all rows
      are named per section, extra rows keep a fixed order, and an "Only differences"
      switch hides identical rows. The picker is a GET form grouped by issuer that works
      without JavaScript, offers popular pairs, and explains a duplicate or unknown card.
- [x] **Search** — the whole query was one exact FTS phrase, so "sapph", "reserve sapphire"
      and "csr" found nothing, and card matches sat below ten articles. Now every word
      must match, in any order, as a prefix and stemmed ("lounges" finds "lounge"), with
      nicknames (csr, csp, amex, vx, ...) and a fallback to any word that says so. Cards
      whose title holds every word come first, titles outweigh body text, ended offers
      follow live ones, and results show type, date and counts on one numbered pager. No
      results now offers tips and links instead of one grey line.
- [x] **/cards/ index** — 52 cards with no filter, sort or fee; 22 screens on a phone. Now a
      GET filter form (category chips and fee bands with live counts, issuer, sort) whose
      links keep each other's filters, compact tiles with fee, best earning rate and offer
      in place of the cut-off quote, and art in a fixed box, lazy after the first six. The
      phone page is about 10 screens unfiltered, 2-3 filtered.
      The three rows of controls (chips, fee bands, issuer and sort menus with Apply) are
      now one row of chips and a "No annual fee" toggle, with the sort as a menu on the
      results line that applies on change; older fee-band and issuer links still filter.
- [ ] **Trust signals** — articles now carry a byline (#90), but there is no About,
      corrections or privacy page, though the site collects emails. *Fix:* an About page
      on how stories are researched for the byline to link to, and a privacy page linked
      next to every email field. The two pages are designed and wait on owner-written copy
      (Up Next: About and Privacy pages).

### P2: polish and accessibility

- [x] **Contrast** — dark-mode white on the `#58a6ff` accent was 2.53:1; the DEAL badge
      3.83:1; in-text links were told apart by colour alone. Now an `--on-accent` token
      (near-black on the dark accent, 7.5:1), deal red `#c81e3a` (5.7:1), success and
      error messages at 4.8 and 5.9:1, header links 9:1, and underlined links in article
      and card prose.
- [x] **404 page** — unknown URLs got Go's bare text; unpublished articles returned 200
      "Under Review" with the headline in `<title>`. Now a branded 404 with search and
      links; an unknown article or card slug is searched and its best matches offered;
      unpublished articles are 404 (410 once published) with nothing of the article on
      the page; 500s are branded and the error is only logged.
- [x] **Compression and caching** — HTML, CSS and JS went out uncompressed (CSS 76 KB,
      15 KB gzipped) and nothing sent Cache-Control. Now `encode zstd gzip` in the Caddy
      site; `?v=`-hashed static URLs (stylesheet, script, infographics) are cached for a
      year, other static files for an hour, and HTML is `no-cache`.
- [ ] **Home on phones** — done: the hero is a 150px strip with a descriptive h1 instead
      of a 370px logo, and the featured title sits on a guaranteed scrim (at least 72%
      black behind any text; it measured 2.7:1 before). Still open: the overlay stops
      partway on desktop, leaving ~400px of unlinked photo, and each feed card is ~400px
      tall on phones. *Fix:* a fixed-ratio featured card, clamped excerpts.
- [ ] **Excerpts** cut mid-word and run headings into sentences, in cards, meta
      descriptions and RSS. *Fix:* use the TL;DR, or cut at a sentence boundary.
- [x] **Smaller accessibility fixes** — a skip link, `scroll-padding-top`, a
      `:focus-visible` ring site-wide, 44px header and footer targets, a labelled
      `type=search` field whose toggle has `aria-expanded` and closes on Escape, and the
      two-item Cards menu replaced by flat Cards and Compare links. Scout's Verdict is a
      heading (#91) and only links to other sites open in a new tab (#90).

## Future

- [ ] **Review by email** — reply APPROVE, REJECT, UNPUBLISH or feedback to a review
      email instead of opening the dashboard. Off today: the startup log says
      `review by email is off (mail.imap not configured)`, the settings page says
      "Review by email: off", and the IMAP poller never starts (with a mailbox
      configured it would only have marked its unread mail as read). Reply parsing
      and the poller exist in `internal/mail/inbound.go`. It would take:
      - outbound review notifications: call `SendAdminNotification` when an article
        lands in Ready to review, to an owner address like `votes.alert_email`, with a
        per-article reply-to token stored in the DB and expiring;
      - a real mailbox for the replies. OCI Email Delivery is outbound-only, so this
        needs an IMAP mailbox (e.g. Gmail with an app password, plus addressing
        `review+TOKEN@`) or an inbound webhook service (Cloudflare Email Workers,
        Mailgun or SendGrid inbound parse) posting to the app;
      - the token resolver (token to article ID and status), replacing the stub that
        was removed, and checks that the reply comes from the owner.
- [ ] Multi-user admin with roles
- [ ] API for third-party consumption
- [ ] Webhook integrations (Slack, Discord, Telegram)
- [ ] A/B testing for article generation models
- [ ] Article quality scoring based on admin approval patterns
- [ ] Geographic content targeting (US vs international deals)

---

*Items are picked up based on priority and need, not strictly sequential.*
