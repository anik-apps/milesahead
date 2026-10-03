---
title: Article Workflow
weight: 2
---

Articles flow through a small state machine from generation to publication. Nothing is published without an admin approving it in the dashboard.

## State Machine

```
   AI pipeline
       |
       |  score >= critic_threshold      score < critic_threshold
       v                                  v
  +---------+                        +---------+
  | pending |                        |  draft  |
  +----+----+                        +----+----+
       |                                  |
       +--------------+-------------------+
                      |  admin decides (review queue)
        +-------------+--------------+
        |             |              |
   +----v-----+  +----v-----+  +-----v--------------+
   | approved |  | rejected |  | revision_requested |
   +----+-----+  +----------+  +-----+--------------+
        |                            |  rewrite (reconciler)
   unpublish                         v
        |                     pending or draft
   +----v--------+            (by the new score)
   | unpublished |
   +-------------+
     approve again, reject, or unpublish & revise
```

### Status Definitions

| Status | Description | Review queue group |
|--------|-------------|--------------------|
| `pending` | Passed the critic threshold (75 in production), new or rewritten; awaiting review | Ready to review |
| `draft` | Scored below the threshold, new or rewritten; awaiting review | Ready to review |
| `approved` | Published and visible on the public site | (Published page) |
| `rejected` | Discarded by the admin, not shown publicly | - |
| `revision_requested` | The admin asked for a rewrite; it is queued or running | Being rewritten, or Needs a decision once its retry budget is spent |
| `unpublished` | Previously approved, taken down | Needs a decision |

A rewrite lands as `pending` or `draft` by its new critic score, so it comes back to Ready to review either way. An unpublished article answers 410 to readers; one that was never published answers 404.

### How Revisions Run

Feedback arrives from the dashboard: **Request rewrite** in the preview or a
row's actions menu, or **Unpublish and request a rewrite…** on a published
article. Each stores the feedback, sets `revision_requested`, and immediately
starts the AI rewrite in the background.

The code also has an email path (a review email whose reply is read over
IMAP), but it is not live in production: nothing sends review emails
(`mail.Sender.SendAdminNotification` has no caller), the deploy config leaves
IMAP unset so polling is disabled at startup (`mail polling disabled`), and
the token resolver in `cmd/milesahead` is a placeholder.

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
  Shutdown is bounded to 85s in total, inside systemd's 90s stop timeout.
- **Metrics.** `milesahead_revision_reconcile_total{outcome}` counts processed,
  failed and skipped articles, `error` when an article's retry state could not
  be read (not attempted), and `pass_skipped` for overlapping passes;
  `milesahead_revisions_waiting{state}` gauges due, backoff and exhausted
  articles. `exhausted > 0` means an article needs the admin.
- **Editor notes.** A remark the model addresses to the admin instead of
  readers (`<div class="editor-note">Could not find a link for: ...</div>`) is
  split out of the content before it is graded or stored and kept in the
  admin-only `editor_notes` column. The dashboard preview shows it as "Editor
  note (not published)"; public pages, the feed, the newsletter, excerpts and
  search never do. Older rows that still hold a note inline are stripped
  wherever they are loaded or indexed, and `cmd/repair-editor-notes` moves
  their notes out of the stored content. See
  [AI Pipeline](../../architecture/ai-pipeline/#editor-notes).

## Admin Review Queue

The admin pages have their own shell (`templates/admin/_layout.html`,
`static/css/admin.css`, `static/js/admin.js`) and are `noindex`. A slim admin
bar replaces the public header, and its tabs show waiting work: **Review**
(articles to decide and to review), **Cards** (pending card updates and
flags), **Newsletter** (the day the next issue goes out, or a warning) and
**Sources** (active sources with nothing new for days).

### Groups and sort

The Review page (`/admin/dashboard`) groups the queue:

| Group | Holds |
|-------|-------|
| **Needs a decision** | Rewrites that gave up (retry budget spent) and articles taken down. Nothing happens until the admin acts. |
| **Ready to review** | `pending` and `draft` articles. Shows 25 at a time ("Showing 25 of N", with Show all). |
| **Being rewritten** | Waiting on a rewrite, e.g. "Failed 1×, retrying in 10 min". What is shown can still be approved or rejected. |

A **Sort** menu orders rows within each group: **Newest first** (the
default), Oldest first, Highest score, Lowest score (`?sort=newest`,
`oldest`, `score`, `score-low`; an unknown value falls back to newest).
Ready to review sorts by creation time, the other groups by last change;
score sorts put unscored articles last. The 25-row cap applies after
sorting. The sort carries through Show all, auto-refresh, the preview's
prev/next and j/k, and every action's redirect, including the error pages.

Rows show a small type pill, enrichment and critic chips, and the critic
score coloured against the threshold (at or above, within 10 below, further
below). While nothing is open the page refreshes itself when the queue
changes (it polls `/admin/dashboard/state`, a fingerprint of the queue);
with a preview or a menu open it shows a Refresh prompt instead.

### Row actions

Each row has a "⋯" actions menu, so most decisions need no preview:

- **Review rows:** Open preview, **Approve rev N** (or Republish rev N),
  Request rewrite… (opens the preview with the feedback box focused),
  Find a new image…, and Reject… (with a confirm). Only the actions the
  server allows for the article's state are offered (`article.Decidable`,
  `article.Revisable`).
- **Published rows** (`/admin/published`, newest or oldest first): View on
  site, Linked cards, Find a new image…, **Unpublish** (with an Undo toast
  rather than a confirm), and Unpublish and request a rewrite….

The menu button has `aria-haspopup`, `aria-expanded` and `aria-controls`;
the menu is `role="menu"`. Arrow keys, Home/End, Escape (focus returns to
the button), Tab and a click outside all work, and only one menu is open at
a time.

### Preview and decisions

The preview shows the article in the same order readers see it, with an
admin-only header: `Article Preview #id`, the revision, the critic score and
the position in the queue ("6 of 8", with prev/next). It also shows Scout's
Take, editor notes, the sources (admin-only), "No image yet" or "No Scout's
Take yet" while enrichment has not run, and a collapsed **Revision history**:
each request (who sent it, when, the text) and the rewrite that answered it
(revision number, critic score, word count before and after, failed checks,
the revised and previous versions), or the request's state (Rewriting…,
Failed N×, Gave up, Superseded).

A sticky action bar holds Request rewrite, **New image…**, Reject (with a
confirm) and **Approve rev N & next**, which moves on to the next article in
the queue's order.

- **Revision guard.** Approve and reject post the revision on screen. The
  status change is one write conditional on both status and
  `revision_number`, so if a rewrite landed since the page opened, the click
  is refused with "Article #N changed after you opened it".
- **Undo.** For 30 seconds after approving, an Undo toast puts the article
  back. Undo reads only a server-side record (`approval_undo`: the previous
  status and publish date, and an expiry), never values from the browser, and
  refuses if the article changed meanwhile. Card-update detection for an
  approved article waits out the same window.
- **Session expiry.** An expired session returns the admin to the same page
  after login, and typed feedback survives in `sessionStorage`.
- **Find a new image.** Under the photo, in the action bar or a row menu; see
  [Image Pipeline](../../architecture/image-pipeline/#find-a-new-image).

### Keyboard shortcuts

| Key | Action |
|-----|--------|
| `j` / `k` | Next / previous article (also ← / → while a preview is open) |
| `o` or `Enter` | Open the selected article |
| `a` | Approve (preview open) |
| `x` | Reject (preview open) |
| `r` | Request a rewrite (preview open) |
| `Esc` | Close the preview, help or menu |
| `?` | Show the shortcut help |

Shortcuts never fire while typing in a field or while an actions menu is
open. `node --test tests/js/*.test.mjs` covers them.

### Card editor and other admin pages

- **Cards:** a "Needs attention" group and a filter box on the list; the
  editor is described in [Reference Pages](../reference-pages/#admin-management).
- **Sources:** last article, articles this week and total per source, with
  stale rows tinted.
- **Newsletter:** says when the issue goes out and to how many confirmed
  subscribers, or why it will not; see [Newsletter](../newsletter/).
- **Subscribers:** Active, Pending and Unsubscribed, with a CSV export.

Forms keep their input on errors: a card form with a bad JSON field comes
back with a 400, everything as typed, and the error on its field.

## Article Types

The pipeline auto-detects article type from source content, or it can be set manually:

| Type | Description | Detection |
|------|-------------|-----------|
| `summary` | Standard news article covering a single topic; readers see it as "News" | Default when no deal keywords found |
| `deal` | Time-limited offer (bonus, sale, discount) | Triggered by 2+ deal keywords across sources |
| `roundup` | Multiple related stories grouped together | Set manually or by clustering logic |

## Reader-Facing Article Page

How a published article is laid out for readers (headline and summary first,
Key details, Scout's Take, card rows, related stories, ended offers) is on
[Reader Site](../reader-site/#article-page).

## Deduplication

Content deduplication happens at four levels:

1. **Raw article level** -- each scraped article has a SHA-256 hash of its title and link stored in the `hash` column. Duplicate hashes are rejected at insert time (an edited headline therefore makes a new row).
2. **Cluster level** -- the clustering engine (`internal/cluster`) groups raw articles from the last 48 hours by title similarity (`scanner.cluster_threshold`, 0.30; it was effectively 0.5, which split one announcement across clusters). The pipeline requires at least 2 unique source sites per cluster to reduce hallucination risk and ensure multi-source verification.
3. **Source overlap** -- a cluster that includes a raw article some generated article was written from (`article_sources`) or already covers (`covered_raws`) is skipped. Its remaining raw articles are recorded in `covered_raws` against that article, so late coverage cannot form a fresh cluster and a second article once the original sources leave the 48-hour window. `covered_raws` is separate from `article_sources`, so an article's public source list does not change.
4. **Story level** -- a cluster that passes the overlap check is compared with every article generated in the last 7 days, in any status. A TF-IDF prefilter keeps up to 5 articles scoring at least 0.25; if there are any, one short AI call on the `enrichment_model` chain decides whether the cluster is the same story and, if so, whether it is a materially new development (a follow-up, which is generated). If the call fails, a lexical score of at least 0.55 decides. `scanner.story_dedup` sets the mode: `shadow` (default) logs a `story dedup decision` line and counts `milesahead_story_dedup_decisions_total{decision,mode}` but still generates; `block` skips the duplicate and records its raw articles in `covered_raws` (reason `story_match`); `off` skips the check. The check exists because 16 of 98 articles generated 2026-09-21 to 09-30 repeated a story already written, and no pair shared a source post; it stays in shadow mode until its decisions have been compared with what was approved and rejected.

## Voting System

Published articles include an upvote/downvote widget. Votes are:

- Stored with an IP hash (not the raw IP) for privacy
- One vote per IP per article
- Monitored for anomalies using configurable thresholds:

| Config | Default | Purpose |
|--------|---------|---------|
| `votes.alert_threshold` | 10 | Minimum downvotes in the window before alerting |
| `votes.alert_ratio` | 0.4 | Share of the window's votes that must be downvotes |
| `votes.alert_window` | 24 | Hours of votes the check counts; 0 counts all of an article's votes |

When an article has at least `alert_threshold` downvotes cast in the last `alert_window` hours, and they are at least `alert_ratio` of its votes in those hours, the downvote that tips it over opens a **vote alert** (`vote_alerts` table) and the app logs `vote threshold exceeded`. Nothing else changes: the article stays published. Counting only the window means a burst of complaints alerts, while an article that drew a few downvotes long ago does not alert again on the next single one.

Open alerts show in the admin:

- a red count on the **Published** tab of the admin bar, on every admin page;
- a line on the Review page pointing to them;
- a **Many downvotes** panel at the top of the Published page listing each article with its downvotes and upvotes, with **Acknowledge** per alert and **Acknowledge all** when there are several.

An article has at most one open alert; further downvotes update its counts rather than opening another. Acknowledging clears the alert (`POST /admin/vote-alerts/{id}/acknowledge`, `/admin/vote-alerts/acknowledge-all`), and only votes cast after that count towards the article's next alert, so votes already seen do not alert twice. The **Many downvotes** chip on a Published row stays while the article has an open alert or its all-time votes cross the same line.

When `votes.alert_email` is set, each new alert is also emailed there (`Vote alert: TITLE`, with the counts and links to the article and the Published page). Empty, the default, nothing is emailed. Alerts used to be emailed to `mail.smtp.from`, the `noreply@` address nobody reads. OCI adds a List-Unsubscribe header to these emails; do not click Unsubscribe on them (see [Email](../../deployment/email/#ocis-list-unsubscribe-and-the-suppression-list)). `milesahead_votes_total{direction}` counts votes.
