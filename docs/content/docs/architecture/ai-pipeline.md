---
title: AI Pipeline
description: "How a cluster of scanned posts becomes a checked article: generation, cleanup, the critic chain, one auto-revision and the score that decides pending or draft."
weight: 1
---

The AI pipeline turns a cluster of scanned posts into a verified article. It writes, checks and, when the check finds something to fix, rewrites once. Scout's Take, tags and the image are added later by the [enrichment reconciler](#enrichment), not here.

## Pipeline Flow

{{< flow "ai-pipeline" >}}

An admin's rewrite (`ProcessWithFeedback`, via `buildResult`) puts the new
reply through the same cleanup, checks and scoring, but not the
auto-revision or the separate key-detail call: the admin reads the result.

A cluster needs posts from at least two different sites; with fewer the pipeline returns `ErrNotEnoughSources` and nothing is generated. Before a cluster reaches the generator, the scan loop (`cmd/milesahead/scan.go`) runs the two duplicate checks described in [Article Workflow](../../features/article-workflow/#deduplication).

## Models

| Role | Config key | Model | Purpose |
|------|------------|-------|---------|
| Generator | `generator_model` | `nvidia/nemotron-3-ultra-550b-a55b` | Article generation and revision |
| Generator fallbacks | `fallback_generator_model` | `openai/gpt-oss-20b`, `nvidia/nemotron-3.5-super-vl-120b-a12b` | Tried in order when the generator fails or is retired |
| Critic | `critic_model` | `nvidia/nemotron-3.5-super-vl-120b-a12b`, `nvidia/nemotron-3.5-lightning-30b-a3b` | Factual verification and scoring only; tried in order, thinking off |
| Short-call chain | `enrichment_model` | `nvidia/nemotron-3.5-lightning-30b-a3b`, `nvidia/nemotron-3.5-super-vl-120b-a12b`, `openai/gpt-oss-20b`; thinking off | Story dedup, detail extraction, Scout's Take, tags, image queries, image text-scoring, newsletter pick, card-update detection |
| Vision | `vision_model` | `nvidia/nemotron-3.5-super-vl-120b-a12b`, `meta/llama-3.2-11b-vision-instruct`; thinking off, images sent inline | Image relevance scoring (Phase 2) |

All models are accessed through the NVIDIA API at `https://integrate.api.nvidia.com/v1`. `config.example.yaml` records why each was chosen, with the measurements behind each choice. A chain is tried left to right on a retryable error (429, 5xx, timeout) or a model that is gone (404, 410); a `model fallback succeeded` or `model failed, trying next in chain` line is logged.

Generation and verification calls get the full `request_timeout` (900 s in production): the generator takes 105-510 s per article. Short calls are capped at 30 s (`ai.ShortCallTimeout`), so only fast models belong in `enrichment_model`.

## Critic Chain and Thinking Off

The critic is used for verification and nothing else; the short calls run on the `enrichment_model` chain. Keeping them apart means a failed critic stops publication without taking image scoring, the newsletter and card updates down with it, as happened when `mistralai/mistral-nemotron` was retired on 2026-09-28.

The critic is a chain because it failed as a single model twice in two days: retired on 2026-09-28, then its replacement, `deepseek-ai/deepseek-v4.1-flash`, answered every call with a gateway 504 from 2026-09-29. A chain is not enough on its own: both critics of the next chain were retired upstream, `moonshotai/kimi-k3` on 2026-10-15 and `nvidia/nemotron-3-super-120b-a12b` on 2026-10-20, and production published nothing for 2.5 days until the chain was replaced. One reachable critic is enough to publish. With more than one critic, each retries once before the chain moves on, so a critic stuck on 5-minute gateway timeouts hands over after about 10 minutes instead of 20.

A critic error drops the article from the scan; a score below `critic_threshold` only saves it as a draft. A critic that answers with JSON that cannot be parsed is an error, not a reason to try the next critic.

With `critic_disable_thinking: true`, critic requests carry `chat_template_kwargs: {"enable_thinking": false}`. They go through a copy of the client (`HFClient.WithThinkingDisabled`). With thinking on, the retired nemotron-3-super truncated or leaked its reasoning and scored a real article 45 instead of 75. `enrichment_disable_thinking: true` does the same for every call on the `enrichment_model` chain, through a second copy, and vision requests always send it; generation never does. With thinking on, on the real enrichment prompts (512-1024 tokens), nemotron-3.5-lightning answered 0 of 32 calls and nemotron-3.5-super-vl 9 of 32: both spent the budget reasoning and were cut off. With it off, both answered 32 of 32 in 0.3-3.6 s on average (2026-10-29). gpt-oss-20b accepts the field and reasons anyway.

Measured on 2026-10-22 with `ab-models -critic-candidates`: every candidate graded the same 5 real articles from the production generator, thinking off, score after the pipeline's adjustment, threshold 75:

| Critic | Valid JSON | Average | At or above 75 | Latency |
|---|---|---|---|---|
| nemotron-3.5-super-vl-120b-a12b | 5 of 5 | 71 | 3 of 5 | ~2 s |
| nemotron-3.5-lightning-30b-a3b | 4 of 5 | 78 | 3 of 5 | ~3 s |
| deepseek-v4.1-flash | 5 of 5 | 53 | 0 of 5 | 22 s |
| z-ai/glm-5.3-flash, z-ai/glm-5.3 | none: out of tokens thinking, or timed out | | | |

super-vl leads because it always returned valid JSON; lightning is more lenient and once returned JSON that would not parse, which drops the article instead of moving to the next critic. super-vl is the same family as the generator and also a generator fallback, so an article the generator wrote on that fallback is graded by the model that wrote it -- watch the approval rate. The `critic scored` log line and `verification_results` name the model that scored.

## Generation Process

The generator receives clustered source articles and produces structured output. Article type is auto-detected from source content:

- **deal** -- time-limited offers (bonuses, sales, discounts, signup offers)
- **summary** -- news, changes, analysis, reviews (shown to readers as "News")
- **roundup** -- multiple related stories grouped together

Each source is labelled for the model as `=== From <Outlet> ===`, without a number, in the order the scan saw it; revisions load sources in the same order. The prompt enforces a plain, practical style and these structural markers:

```
Title line
TLDR: One-sentence summary
KEY_DETAILS_START
- Label: Value
- Label: Value
KEY_DETAILS_END
LINKS_START
- Link Text | URL
LINKS_END
Article body content...
```

## Critique Process

The critic evaluates generated articles against the original sources on four dimensions:

| Check | What It Verifies |
|-------|-----------------|
| `FactualCheck` | All facts match the source material |
| `AttributionCheck` | No news site named outside the two exceptions, no numbered source references |
| `CompletenessCheck` | Key information from sources is included |
| `HallucinationCheck` | No invented facts or unsupported claims |

The critic produces a score from 0-100 which is then adjusted (`adjustScore`):

- Hallucination failure caps the score at 60
- Each other failed check deducts 10 points
- Any failure caps the score at 85
- Two or more failures cap the score at 70

Articles scoring at or above `critic_threshold` (75 in production) pass and are saved as `pending`; below it they are saved as `draft`. Both wait for the admin in the [review queue](../../features/article-workflow/#admin-review-queue).

## Critic Issues and the Passed-Check Filter

Any issue left on the verdict triggers the auto-revision: an extra generator rewrite and a second critic call (median 75 s plus two AI calls). The score and the checks deliberately do not gate it, because verdicts that passed every check at 85-95 still carried real errors in production (a miscalculated per-point price, a wrong joint-venture member, a "202026" typo).

What wasted revisions was "issues" that were not problems. Measured over 30 days, 117 of 131 verdicts (89%) had at least one issue, 98 of those with all four checks passing, and in 7 days of logs 68 of 71 first verdicts (96%) triggered a revision. Two changes address it:

- **The critic prompts** (`criticIssuesRules`) ask for fixes only: one plain string per issue saying what is wrong, where, and the fix. Passed checks, confirmations, praise and "acceptable" remarks are forbidden, and a clean article returns `[]`.
- **`dropPassedChecks`** (`internal/ai/criticissues.go`) removes an item only if every sentence carries a pass word and no negative or change word (but, not, missing, incorrect, should, minor, ...), or if it ends exactly "No issue." or "So no error.". Quoted article text is ignored, and it never empties the list of a verdict with a failed check. The log line `dropped critic issues that confirm a passed check` counts them.

Issues the critic returns as `{"problem", "fix"}` objects are flattened into strings rather than failing the parse. The source-reference and outlet issues below are added after filtering, so they always force a revision.

## Revision Flow

When an issue is left after filtering, the pipeline runs one revision cycle:

1. The generator receives the original content plus the list of issues
2. It rewrites the article addressing each issue
3. The critic re-evaluates the revised version
4. The higher-scoring version is kept; the revision wins a tie

There is only one: a mention of a news site that survives it stays on the verdict, and the article is kept with attribution failed for the admin to judge.

Admins can also ask for a rewrite with feedback text from the dashboard. Those rewrites go through the same guards and critic, and are retried by a revision reconciler until they succeed or exhaust their retry budget; see [Article Workflow](../../features/article-workflow/#how-revisions-run).

## Key Details

The generator's own `KEY_DETAILS` and `LINKS` sections are parsed from the **raw** reply (`replyDetails`), before Markdown cleaning turns `- ` lines into HTML list items. Until 2026-10-01 they were parsed after it and never found, so every article needed a separate extraction call: 70 of 70 in one week, 9 of which shipped without key details.

The same parse covers the first draft, an accepted auto-revision (its details replace the draft's) and admin revisions. Stray `**` is stripped, and a detail with a numbered source reference is dropped. Only a reply with no usable details costs a separate call on the `enrichment_model` chain, logged as `no key details in the generated article, extracting them separately`.

Key details also give an article its offer end date: `internal/offer` reads a short list of deadline labels and stores the latest date in `expires_on`.

## Source References and Outlet Mentions

Articles never name or refer to the news sites and blogs they are written from (every row of `sources`, a built-in list of 12, and the aliases TPG, OMAAT, VFTW, DoC and FM). Issuers, airlines, hotel chains and their official pages are not news sites. There are two exceptions:

1. The outlet is itself the subject of the story (a sale on Thrifty Traveler Premium, say).
2. An unconfirmed report that only that outlet carries, mentioned once, as a report.

The rule, with both exceptions word for word, is in the deal, roundup, summary, revision, Scout's Take and key-details prompts and both critic prompts. Generated text is also checked deterministically:

- **Numbered references** ("Source 1 notes...", "the first source", "Article 2") are flagged, never rewritten to the outlet's name (`internal/ai/sourcerefs.go`). The attribution check fails, the issue tells the writer to state the fact without attribution, and the auto-revision runs. Citation markers ("[1]") are removed.
- **Outlet mentions** (`internal/ai/outlets.go`): its name, an alias, its domain, or a written-out URL on its site, in the title, TLDR, key details or body. Matching is whole-word on visible text; "The" is optional, "LoyaltyLobby" matches "Loyalty Lobby", and all-lowercase phrases ("one mile at a time") do not trigger. Each outlet found adds one attribution issue quoting the sentence and stating both exceptions.
- **Links** to an outlet's site are unwrapped, keeping their text, before the critic sees them, and outlet entries are dropped from the link list. Articles link to official pages only. These removals alone do not trigger a revision.
- **Enrichment** has no revision after it, so a Scout's Take that names an outlet or has a numbered reference is refused (and retried next pass), and such a key detail is dropped, unless the title itself names the outlet.

To list stored articles that still name or link to an outlet, or carry a numbered reference, run the read-only report (it opens the database with `mode=ro` and has no write mode, since prose needs a person to fix it):

```bash
go run ./cmd/flag-outlet-mentions -db ./data/milesahead.db
go run ./cmd/flag-outlet-mentions -db ./data/milesahead.db -status approved,pending
```

An earlier `cmd/repair-source-refs` rewrote "Source N" to outlet names; it was removed when the policy changed.

## Editor Notes

A remark meant for the admin rather than readers ("Could not find a link for: ...") is an editor note: an element with the `editor-note` class. `internal/editornote` splits it out of the generator's reply before anything parses or grades it, so the key details, the critic and the auto-revision see only what readers will. Notes go to the admin-only `editor_notes` column (`json:"-"`, so no JSON output carries them); a revision's notes replace the previous ones. In notes only, "Source N" is spelled out as the outlet's name, which tells the admin which source a note is about.

The db layer splits again on create, revision and update, and every article read strips notes still inline in older rows, so pages, excerpts, the feed, the newsletter, meta tags, search and AI prompts never see them. The admin preview shows them as "Editor note (not published)". `cmd/repair-editor-notes` moves notes out of rows stored before this; see [Development](../../development/#one-off-and-maintenance-tools).

## Story Dedup

Before generation, each cluster that passed the raw-ID check is compared with every article generated in the last 7 days, in any status (`internal/storydedup`). A TF-IDF prefilter on titles keeps up to 5 articles scoring at least 0.25; if there are any, one short call on the `enrichment_model` chain answers "same story as which, and is it a materially new development?". A follow-up is generated. If the call fails, or names an article it was not shown, a lexical score of at least 0.55 decides.

`scanner.story_dedup` sets the mode: `shadow` (the default, and production's) logs `story dedup decision` with `would_block` and counts `milesahead_story_dedup_decisions_total{decision,mode}` but still generates; `block` skips the duplicate and records its posts in `covered_raws` (reason `story_match`); `off` skips the check. Details and the measurements behind the thresholds are in [Article Workflow](../../features/article-workflow/#deduplication).

## Preflight

At startup the app probes every configured model (generator, fallbacks, every critic, vision, the short-call chain) and logs `ai preflight complete`, or an ERROR if no critic answers (articles would be generated but none would publish) or no generator answers. Generator, fallback and critic probes use the same 4096-token budget and full `request_timeout` as real calls, so with a slow model the summary arrives minutes after boot. `milesahead_ai_preflight_models_reachable{role}` exports the count per role; `role="critic"` at 0 means nothing can publish.

`cmd/ab-models` measures candidate models against real source material before a change; see [Development](../../development/#one-off-and-maintenance-tools). The provider's model catalogue is not a liveness signal: of 81 models it listed, only 9 answered.

## Enrichment

Scout's Take, key details (when generation left none), tags and the article image are filled in by a background reconciliation pass (`internal/enrich`), not during generation. Inline, each was a best-effort call, and for two and a half months roughly a quarter of articles published with a field missing. Reconciled, a miss is simply picked up next pass.

- It runs every `enrichment_interval` minutes (15), and once about 90 s after startup.
- Every call goes to the `enrichment_model` chain, capped at 30 s.
- `enrichment_max_attempts` (3) bounds how many passes an article is retried; `enrichment_max_duration` (10 minutes) bounds one pass, checked between articles.
- Each pass first converts stored images to sized copies, then serves admin [Find a new image](../image-pipeline/#find-a-new-image) requests, then the rest.
- `milesahead_articles_degraded` counts articles still missing something, and `milesahead_enrichment_last_success_timestamp_seconds` feeds the "enrichment reconciler has stalled" alert (no completed pass for over an hour).

**Scout's Take** is a brief opinion in the voice of `site.persona_name` (Scout), stored in `scouts_take`. A take that names a news site or has a numbered reference is refused and retried. `cmd/backfill-scouts-take` uses the same function (`ai.GenerateScoutsTake`), so it goes through the same guard.

## Image Pipeline

The enrichment pass finds and scores a relevant image; see the [image pipeline](../image-pipeline/).
