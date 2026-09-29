---
title: AI Pipeline
weight: 1
---

The AI pipeline is the core of article generation. It takes clustered source articles and produces verified, structured content through a generate-critique-revise loop.

## Pipeline Flow

```
  Clustered Sources (2+ unique sites required)
          |
          v
  +------------------+
  |    Generator     |  Nemotron Super 49B
  | (type detection) |  Detects: deal, summary, roundup
  +--------+---------+
           |
           v
  +------------------+
  |    Critic #1     |  Llama 3.3 70B Instruct
  | Factual, source, |
  | attribution,     |
  | hallucination    |
  +--------+---------+
           |
     issues found?
      /         \
    no           yes
     |            |
     |     +------v-------+
     |     |  Auto-Revise |  Generator rewrites
     |     +------+-------+  with issue list
     |            |
     |     +------v-------+
     |     | Re-Verify    |  Critic scores again
     |     +------+-------+  Keep better version
     |            |
     +------+-----+
            |
            v
  +------------------+
  | Detail Extraction|  Extracts key details
  | (if needed)      |  and links via AI
  +--------+---------+
           |
           v
  +------------------+
  |  Scout's Take    |  Brief editorial opinion
  +--------+---------+
           |
           v
  +------------------+
  |  Image Pipeline  |  See Image Pipeline docs
  +--------+---------+
           |
           v
     PipelineResult
```

## Models

| Role | Model | Purpose |
|------|-------|---------|
| Generator | `nvidia/llama-3.3-nemotron-super-49b-v1.5` | Article generation and revision |
| Critic | `meta/llama-3.3-70b-instruct` | Factual verification and scoring |
| Vision | `meta/llama-3.2-90b-vision-instruct` | Image relevance scoring (Phase 2) |

All models are accessed through the NVIDIA API at `https://integrate.api.nvidia.com/v1`.

## Generation Process

The generator receives clustered source articles and produces structured output. Article type is auto-detected from source content:

- **deal** -- time-limited offers (bonuses, sales, discounts, signup offers)
- **summary** -- news, changes, analysis, reviews
- **roundup** -- multiple related stories grouped together

The generator prompt enforces a specific writing style: informative and practical, not promotional. The output uses structural markers for parsing:

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
| `AttributionCheck` | Claims are properly attributed to sources |
| `CompletenessCheck` | Key information from sources is included |
| `HallucinationCheck` | No invented facts or unsupported claims |

The critic produces a score from 0-100 which is then adjusted:

- Hallucination failure caps the score at 60
- Each other failed check deducts 10 points
- Any failure caps the score at 85
- Two or more failures cap the score at 70

Articles scoring at or above `critic_threshold` (default 75) are marked as passing.

## Revision Flow

When the critic finds issues, the pipeline automatically triggers one revision cycle:

1. The generator receives the original content plus the list of issues
2. It rewrites the article addressing each issue
3. The critic re-evaluates the revised version
4. The higher-scoring version is kept

Admins can also trigger manual revisions by providing feedback text, through the dashboard or by replying to the review email. Those rewrites are retried by a revision reconciler until they succeed or exhaust their retry budget; see [Article Workflow](../../features/article-workflow/#how-revisions-run).

## Detail Extraction

If the structured output parsing does not yield key details, a separate AI call extracts them. The extraction prompt asks for:

- **Key details** -- label/value pairs summarizing the most important facts
- **Links** -- relevant URLs mentioned in the article

## Scout's Take

Each article includes a brief editorial opinion from the "Scout" persona. This is generated separately and stored in the `scouts_take` field. It provides a practical recommendation or perspective beyond the factual reporting.

## Image Pipeline

After article generation, the [image pipeline](../image-pipeline) finds and scores a relevant image. See the dedicated page for details.
