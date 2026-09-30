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
  |    Generator     |  Nemotron 3 Ultra 550B
  | (type detection) |  Detects: deal, summary, roundup
  +--------+---------+
           |
           v
  +------------------+
  |    Critic #1     |  Nemotron 3 Super 120B, then Kimi K3
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

| Role | Config key | Model | Purpose |
|------|------------|-------|---------|
| Generator | `generator_model` | `nvidia/nemotron-3-ultra-550b-a55b` | Article generation and revision |
| Generator fallbacks | `fallback_generator_model` | `openai/gpt-oss-20b`, `nvidia/nemotron-3-super-120b-a12b` | Tried in order when the generator fails or is retired |
| Critic | `critic_model` | `nvidia/nemotron-3-super-120b-a12b`, `moonshotai/kimi-k3` | Factual verification and scoring only; tried in order, thinking off |
| Short-call chain | `enrichment_model` | `openai/gpt-oss-20b`, `nvidia/nemotron-3-super-120b-a12b` | Detail extraction, Scout's Take, tags, image queries, image text-scoring, newsletter pick, card-update detection |
| Vision | `vision_model` | `meta/llama-3.2-11b-vision-instruct` | Image relevance scoring (Phase 2) |

All models are accessed through the NVIDIA API at `https://integrate.api.nvidia.com/v1`. `config.example.yaml` records why each was chosen.

The critic is used for verification and nothing else; the short calls are capped at 30 seconds and run on the `enrichment_model` chain. Keeping them apart means a failed critic stops publication without taking image scoring, the newsletter and card updates down with it, as happened when `mistralai/mistral-nemotron` was retired on 2026-09-28.

The critic is a chain because it failed as a single model twice in two days: retired on 2026-09-28, then its replacement, `deepseek-ai/deepseek-v4.1-flash`, answered every call with a gateway 504 from 2026-09-29. A critic error drops the article from the scan; a score below `critic_threshold` only saves it as a draft. With `critic_disable_thinking: true`, critic requests (and only those) carry `chat_template_kwargs: {"enable_thinking": false}`; with thinking on, nemotron-3-super truncates or leaks its reasoning and scored a real article 45 instead of 75. It is the same family as the generator and also a generator fallback, so an article the generator wrote on that fallback is graded by the model that wrote it -- watch the approval rate. kimi-k3, the fallback critic, grades more strictly.

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

If the structured output parsing does not yield key details, a separate AI call on the `enrichment_model` chain extracts them. The extraction prompt asks for:

- **Key details** -- label/value pairs summarizing the most important facts
- **Links** -- relevant URLs mentioned in the article

## Scout's Take

Each article includes a brief editorial opinion from the "Scout" persona. This is generated separately and stored in the `scouts_take` field. It provides a practical recommendation or perspective beyond the factual reporting.

## Image Pipeline

After article generation, the [image pipeline](../image-pipeline) finds and scores a relevant image. See the dedicated page for details.
