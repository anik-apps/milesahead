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
| Pixabay | Yes (free) | Pixabay License | Large library, horizontal photos |
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
6. The highest-scored candidate becomes the article's image

## Stored Fields

The selected image is stored on the `generated_articles` record:

| Field | Description |
|-------|-------------|
| `image_url` | Full-size image URL for the article page |
| `image_thumbnail` | Smaller image URL for homepage cards |
| `image_attribution` | Credit line (e.g. "Photo by X on Unsplash") |

## Article Infographics

Separately from photo search, the enrichment reconciler asks a text model to draw each
article's `key_details` as an SVG diagram — usually a horizontal bar chart, sometimes a
timeline or a grid of stat cards.

There is no raster image model available to this project: the chat catalogue at
`integrate.api.nvidia.com` contains none, the NIM endpoints at `ai.api.nvidia.com/v1/genai/*`
reject the token, and the internal Vertex gateway key is scoped to a video model. Vector
markup from a text model turns out to suit the job better regardless — it is roughly 2 KB,
stays sharp at any size, uses `currentColor` so one diagram renders correctly in both light
and dark themes, keeps its text selectable and indexable, and is served from our own origin
rather than a third-party CDN that can expire the link.

### Why the sanitiser is not optional

The diagram is inlined into the page rather than loaded through `<img>`, so it executes in
the site's own origin — a single `<script>` or `onload=` inside it would be same-origin XSS
with reach into an admin session. The model that writes it is prompted with article text
scraped from third-party sites, so its output is attacker-influenced.

`internal/svg` handles this by allowlist and re-serialisation: elements and attributes not on
the list are dropped, dropped elements take their children with them, and everything that
survives is written out by the package's own encoder, so the result can only contain
constructs it knows how to emit. `<script>`, `<foreignObject>`, `<use>`, `<image>`, `<style>`,
`<a>`, animation elements, namespaced attributes, `style=`, and any `url(...)` value are all
removed. Root `width`/`height` are stripped so CSS controls the size.

The web layer then refuses to inline anything that does not begin with the exact opening tag
the sanitiser emits — not a second security boundary, but a check that the first one was
actually crossed before the markup reaches a page.

### Outcomes

A pass has three distinct results. A drawing is stored; a model error leaves the column empty
so a later pass retries; and an article with no comparable numbers gets the sentinel `none`,
which stops it being asked again. Without that sentinel an unchartable article would spend a
model call every pass, forever, and burn its whole enrichment attempt budget on the one field
it can never have.
