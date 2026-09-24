---
title: Image Pipeline
weight: 2
---

The image pipeline finds relevant, freely-licensed images for generated articles. It searches multiple APIs in parallel, then uses a two-phase AI scoring system to pick the best match.

## Flow

```
  Article Title + TLDR
          |
          v
  +-------------------+
  |  Parallel Search   |  5 APIs queried concurrently
  |  (per query term)  |  Max 3 results per API
  +--------+----------+
           |
           v
  +-------------------+
  |   Deduplicate     |  Remove duplicate URLs
  |   + Diversify     |  Max 3 per source, cap at 15
  +--------+----------+
           |
           v
  +-------------------+
  |  Phase 1: Text    |  Score descriptions/tags
  |  Pre-Filter       |  using text model
  +--------+----------+
           |
           v
  +-------------------+
  |  Phase 2: Vision  |  Send actual images to
  |  Model Scoring    |  Llama Vision for scoring
  +--------+----------+
           |
           v
  Top-scored ImageResult
  (URL, Thumbnail, Attribution)
```

## Image Sources

All sources provide freely-licensed images. The searcher queries each API in parallel for every search term.

| Source | API Key Required | License | Notes |
|--------|-----------------|---------|-------|
| Unsplash | Yes (free) | Unsplash License | High-quality photos, landscape orientation |
| Pexels | Yes (free) | Pexels License | Good variety, landscape orientation |
| Openverse | No | CC-licensed | Aggregates Flickr, Wikimedia, and other open sources |
| Wikimedia Commons | No | Various CC/PD | Uses MediaWiki API with thumbnail generation |

## Two-Phase Scoring

### Phase 1: Text Pre-Filter

The text model (same as the critic model) scores image candidates based on their metadata:

- Image description or alt text
- Tags from the source API
- Source attribution
- The search query that found it

Each candidate receives a relevance score from 1-10. Candidates are sorted by score, and the top 5 proceed to Phase 2.

### Phase 2: Vision Model

The vision model (`meta/llama-3.2-90b-vision-instruct`) receives the actual image thumbnail along with the article title and summary. It scores each image on specificity:

| Score | Meaning |
|-------|---------|
| 9-10 | Shows the exact brand, product, or place mentioned |
| 7-8 | Closely related (correct type but not the specific one) |
| 5-6 | Generic but relevant category |
| 1-4 | Wrong or irrelevant |

If all vision API calls fail, the pipeline falls back to the text-phase scores with no penalty.

## Selection Process

1. Search all 5 APIs in parallel with the article's query terms
2. Deduplicate by URL
3. Enforce source diversity: max 3 images per source, cap total at 15
4. Phase 1 text scoring narrows to top 5 candidates
5. Phase 2 vision scoring picks the best match
6. Walking down the scores, the first candidate whose URLs actually load and
   fit their byte caps becomes the article's image (see below)

## Image Sizes

Openverse returns each provider's full-size file, and a Wikimedia Commons
original can be a 6000 px, 13 MB camera image. Before any candidate is scored,
URLs on hosts that can serve a smaller copy are rewritten to one
(`internal/images/sizing.go`):

| Host | Display image (`image_url`) | Thumbnail (`image_thumbnail`) |
|------|-----------------------------|-------------------------------|
| upload.wikimedia.org | 1280 px `/thumb/` step | 500 px step |
| Flickr | unchanged (`_b`, 1024 px) | 500 px size (no suffix) |
| Unsplash, Pexels | the API's `regular` / `large` | the API's `small` / `medium` |

Wikimedia only serves its standard steps (20, 40, 60, 120, 250, 330, 500, 960,
1280, 1920, 3840); any other width is a 400. It does not refuse a step wider
than the original, it upscales, so the original's width (from Openverse or
the Commons API) decides the step and an original of unknown width is left
alone. Wikimedia also rejects generic HTTP clients, so every request carries
a `MilesAhead/1.0` User-Agent.

At pick time each candidate's URLs are fetched with a ranged GET and must
answer 200/206 within a byte cap: 1 MiB for the display image, 512 KiB for the
thumbnail. A host that does not state the size has it measured, up to the cap.

Rows stored before this existed are converted by the enrichment reconciler: at
the start of every pass, including the one ~90 s after each deploy, it swaps
Wikimedia originals and oversized Wikimedia/Flickr thumbnails for sized URLs,
fetching each one first and leaving any field it cannot confirm unchanged. A
Wikimedia file that Commons reports deleted, and whose URL answers 404, is
cleared so enrichment picks a new image.

## Stored Fields

The selected image is stored on the `generated_articles` record:

| Field | Description |
|-------|-------------|
| `image_url` | Display image: article hero, featured card and `og:image`; at most ~1280 px wide where the host allows |
| `image_thumbnail` | Homepage card image; about 500 px wide where the host allows |
| `image_attribution` | Credit line (e.g. "Photo by X on Unsplash") |
