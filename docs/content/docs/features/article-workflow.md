---
title: Article Workflow
weight: 2
---

Articles flow through a state machine from initial generation to publication. The admin dashboard provides controls at each stage.

## State Machine

```
                +-------+
                | draft |  (initial AI generation)
                +---+---+
                    |
              admin reviews
                    |
         +----------+----------+
         |          |          |
    +----v---+ +---v----+ +---v-----------+
    |approved| |rejected| |revision_requested|
    +----+---+ +--------+ +---+-----------+
         |                     |
    published             AI revises
    to site                    |
         |               +-----v----+
    +----v------+        |  pending  |
    |unpublished|        +-----+-----+
    +-----------+              |
                          admin reviews
                               |
                    +----------+----------+
                    |          |          |
               approved   rejected   revision_requested
```

### Status Definitions

| Status | Description |
|--------|-------------|
| `draft` | Newly generated article, awaiting first admin review |
| `pending` | Revised article awaiting re-review |
| `approved` | Published and visible on the public site |
| `rejected` | Discarded by admin, not shown publicly |
| `revision_requested` | Admin has provided feedback (dashboard or email reply); AI will revise |
| `unpublished` | Previously approved article taken down |

### How Revisions Run

Feedback can arrive three ways: the dashboard's **Request Revision** button,
**Unpublish & revise** on a published article, or an email reply to the review
message. Each sets `revision_requested`, stores the feedback, and immediately
starts the AI rewrite in the background.

That immediate start is only for responsiveness. A **revision reconciler**
(`article.Manager.ProcessPendingRevisions`) runs every 5 minutes, plus once
about 45 seconds after startup, and rewrites every article still in
`revision_requested`. So a rewrite whose AI call failed, or that a deploy
restart killed mid-run, is picked up again rather than sitting in "Revising..."
indefinitely.

- **No double rewrites.** At most one rewrite per article runs at a time; a
  second request, or a reconciler pass, while one is running does nothing. The
  result is stored with a compare-and-set: if the article was approved or
  rejected meanwhile it is discarded, and if newer feedback arrived meanwhile
  the rewrite runs again against it. "Newest" is by feedback id throughout, so
  a clock step cannot make a rewrite answer an older note. A rewrite discarded
  twice for the same feedback counts as a failure, so no mismatch can loop
  uncharged.
- **Bounded retries.** A failed rewrite is retried with backoff (5, 10, 20, 40
  minutes). After 5 consecutive failures the reconciler stops and the dashboard
  shows **"Revision failed N times"** with the last error on hover. The article
  stays `revision_requested`; requesting the revision again resets the budget,
  or the admin can approve or reject it as is. The counters live in the
  `revision_failures`, `revision_retry_after` and `revision_last_error` columns,
  so a restart neither forgets the backoff nor grants a fresh budget.
- **Shutdown.** A rewrite in progress is abandoned rather than waited for (one
  AI call with retries can take minutes); nothing is stored until it finishes,
  so the article stays `revision_requested` and the startup pass redoes it.
  Shutdown is bounded to 75s in total, inside systemd's 90s stop timeout.
- **Metrics.** `milesahead_revision_reconcile_total{outcome}` counts processed,
  failed and skipped articles, `error` when an article's retry state could not
  be read (not attempted), and `pass_skipped` for overlapping passes;
  `milesahead_revisions_waiting{state}` gauges due, backoff and exhausted
  articles. `exhausted > 0` means an article needs the admin.

## Article Types

The pipeline auto-detects article type from source content, or it can be set manually:

| Type | Description | Detection |
|------|-------------|-----------|
| `summary` | Standard news article covering a single topic | Default when no deal keywords found |
| `deal` | Time-limited offer (bonus, sale, discount) | Triggered by 2+ deal keywords across sources |
| `roundup` | Multiple related stories grouped together | Set manually or by clustering logic |

## Two-Column Layout

Published articles display in a two-column layout on the article page:

- **Main column** -- article content (HTML)
- **Sidebar** -- key details (label/value pairs), relevant links, Scout's Take, and source attribution

## Deduplication

Content deduplication happens at two levels:

1. **Raw article level** -- each scraped article has a SHA-256 content hash stored in the `hash` column. Duplicate hashes are rejected at insert time.
2. **Cluster level** -- the clustering engine groups raw articles about the same topic. The pipeline requires at least 2 unique source sites per cluster to reduce hallucination risk and ensure multi-source verification.

## Voting System

Published articles include an upvote/downvote widget. Votes are:

- Stored with an IP hash (not the raw IP) for privacy
- One vote per IP per article
- Monitored for anomalies using configurable thresholds:

| Config | Default | Purpose |
|--------|---------|---------|
| `votes.alert_threshold` | 10 | Minimum votes before alerting |
| `votes.alert_ratio` | 0.4 | Downvote ratio that triggers alert |
| `votes.alert_window` | 24 | Window in hours for ratio calculation |

When the downvote ratio exceeds the threshold, the system flags the article for admin review.
