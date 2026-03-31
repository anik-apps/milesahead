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
| `revision_requested` | Admin has provided feedback; AI will revise |
| `unpublished` | Previously approved article taken down |

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
