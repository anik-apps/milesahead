---
title: Image Pipeline
description: "How the enrichment pass finds an article image: four free image APIs, turned-down pictures, text and vision scoring, byte caps, and Find a new image requests."
weight: 2
---

The image pipeline finds relevant, freely-licensed images for generated articles. It searches four image APIs in parallel, then uses a two-phase AI scoring system to pick the best match. It runs inside the [enrichment reconciler](../ai-pipeline/#enrichment), for articles with no image yet and for an admin's [Find a new image](#find-a-new-image) request.

## Flow

{{< flow "image-pipeline" >}}

## Image Sources

All sources provide freely-licensed images. The searcher (`internal/images/search.go`) queries each API in parallel for every search term; Unsplash and Pexels are skipped when their key is not set. Every request carries a `MilesAhead/1.0` User-Agent.

| Source | API Key Required | License | Notes |
|--------|-----------------|---------|-------|
| Unsplash | Yes (free) | Unsplash License | High-quality photos, landscape orientation |
| Pexels | Yes (free) | Pexels License | Good variety, landscape orientation |
| Openverse | No | CC-licensed | Aggregates Flickr, Wikimedia Commons and other open collections; the result's provider is kept (`openverse/flickr`, ...) |
| Wikimedia Commons | No | Various CC/PD | Uses MediaWiki API with thumbnail generation |

## Two-Phase Scoring

### Phase 1: Text Pre-Filter

The text model chain (`enrichment_model`, tried in order; not the critic, which is too slow for this 30-second call) scores image candidates based on their metadata:

- Image description or alt text
- Tags from the source API
- Source attribution
- The search query that found it

Each candidate receives a relevance score from 1-10. Candidates are sorted by score, and the top 5 proceed to Phase 2.

### Phase 2: Vision Model

The vision model (`vision_model`, `meta/llama-3.2-11b-vision-instruct` by default) receives the actual image thumbnail along with the article title and summary. It scores each image on specificity:

| Score | Meaning |
|-------|---------|
| 9-10 | Shows the exact brand, product, or place mentioned |
| 7-8 | Closely related (correct type but not the specific one) |
| 5-6 | Generic but relevant category |
| 1-4 | Wrong or irrelevant |

If all vision API calls fail, the pipeline falls back to the text-phase scores with no penalty.

The vision model fetches each thumbnail itself. A sized Wikimedia thumbnail is
often a size nobody has asked for, which Wikimedia renders on the first
request -- usually in 0.2-3 s, sometimes 4-10 s, or with a 429 -- and a failed
fetch costs the candidate a x0.6 penalty. So before any thumbnail is sent,
the Wikimedia ones among the top 5 are requested once, two at a time
(`Searcher.Warm`; Wikimedia's robot policy asks for a concurrency of at most
2 against upload.wikimedia.org), and the vision model finds them rendered.
Warming waits at most 15 s in all, with no retry: a render cut off then
carries on at Wikimedia. The
searcher is an argument of `Scorer.SetVisionModel`, so vision scoring cannot
be switched on without it.

## Selection Process

1. Search all 4 APIs in parallel with the article's query terms (or the admin's search words, see [Find a new image](#find-a-new-image))
2. Drop pictures turned down for this article, then deduplicate by URL
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
answer 200/206 with an `image/*` content type, within a byte cap: 1 MiB for
the display image, 512 KiB for the thumbnail. A host that does not state the
size (no length, or a `Content-Range` total of `*`) has it measured, up to the
cap, with a plain GET that must also answer `image/*`; a size that cannot be
determined is refused, as is a size of 0. The same check
(`Searcher.CheckFit`) guards every URL the resize step below writes.

Each probe request waits up to 8 s. A Wikimedia thumbnail may be rendered on
that very request, so it waits up to 15 s and gets one retry after a timeout:
a render cut off by the timeout carries on at Wikimedia, and the retry
usually finds it done. A 429 is retried up to twice, after its `Retry-After`
(at most 5 s). A probe makes at most three requests however they fail, and
a whole probe, retries, waits and the GET that measures a file of unknown
size included, never takes more than 30 s. At pick time the probes also stop at the enrichment pass's
deadline, so a hanging image host cannot hold a pass past its end (and make
the scheduler skip the next one); an article cut short there gets no image
that pass. If the article's other model calls have already run past the
deadline, the image search does not start at all -- no queries, search,
warming or scoring -- and the article is not charged an attempt for it.

Rows stored before this existed are converted by the enrichment reconciler
(`internal/enrich/imagesize.go`). At the start of every pass, including the
one ~90 s after each deploy, it swaps Wikimedia originals and oversized
Wikimedia/Flickr thumbnails for sized URLs:

- Thumbnails stored at 800 px -- the Commons API rendering the image search
  used to ask for, a width Wikimedia now answers with 400 -- are repaired. The
  card thumbnail moves to 500 px; the display image is sized from the
  original, whose width is looked up from the same file name (1280 or 960 px,
  or the original itself if no step comes close to its width), so the
  featured card is not a 500 px image stretched to 1280. This covers files
  whose names run past 160 bytes, whose thumbnails MediaWiki names
  `<width>px-thumbnail.jpg`.
- Each new URL is fetched first; a field that cannot be confirmed keeps its
  current URL. If a step is an image over the byte cap, the next step down is
  tried (960 then 500 px for the display image, 330 px for the thumbnail). If
  every step is over the cap, the lightest one is taken as long as it is
  lighter than the file the row holds, or that file does not load at all (an
  800 px thumbnail, a 404, an HTML page): a copy just seen to load beats a
  broken one. That replacement for a broken file may be at most twice the cap
  (`brokenCeilingFactor`); if every copy is heavier, the image is cleared so
  enrichment picks a fresh one. Otherwise the row keeps its file.
- Original widths come from the Commons API, one request (20 files) a pass,
  newest articles first; titles Commons normalizes are followed. A file it
  has no answer for is set aside for 6 hours so it cannot hold the older rows
  back; an 800 px display image whose original has no answer takes the 500 px
  step rather than stay broken, as does one whose file Commons reports
  deleted while the original still loads.
- A Wikimedia file that Commons reports deleted, and whose URL answers 404 or
  410, is cleared so enrichment picks a new image. If the URL still loads it
  is kept.
- The step has its own budget so it cannot starve enrichment: 90 s, and no
  more than a quarter of the pass. No row starts once it has run out, and
  the requests of the row in progress stop at it too; a field whose check is
  cut short keeps its URL and is tried again next pass, like any field that
  could not be confirmed. At most 25 rows go to the network per pass,
  Wikimedia originals, 800 px thumbnails and the newest articles first. The
  rest wait for the next pass. A row that fails two passes running is set
  aside for 6 hours.
- Some rows cannot be converted: every sized copy is heavier than the file
  they hold, Commons reports the file deleted but its URL still loads, or
  Commons has no width for it. They keep their current, working file, are
  set aside for 6 hours at once and rechecked after.
- `milesahead_article_images_unsized` is the number of rows still waiting
  after the last pass, not counting those that cannot be converted, so it
  should reach 0 a few passes after a deploy and stay there.
  `milesahead_article_images_unconvertible` counts the others, and
  `milesahead_image_resize_total{outcome}` counts rows resized, left
  unchanged (`unconfirmed`), cleared and found `unconvertible`.

## Find a New Image

A rewrite only changes the text, and enrichment only picks an image for an
article that has none. So a different picture is asked for separately, with
**Find a new image**:

- under the photo in the admin preview, or **New image…** in its sticky
  action bar;
- in the "⋯" actions menu of a Review row or a Published row.

Each posts to `POST /admin/articles/{id}/new-image` (CSRF-protected, admin
only) with an optional **Search for…** box, e.g. "Hyatt Regency lobby".

- **Search words.** They are cleaned to one line of at most 100 characters,
  **replace** the generated image queries (so generated results cannot crowd
  them out), and the scorer is told "The editor asked for a picture of: …".
  The relevance bar, the byte caps and the fetchability checks are unchanged.
- **Queueing.** The request is a row in `article_image_requests` (`state`
  `pending` or `failed`, `hint`, `seq`). The next enrichment pass (every 15
  minutes) takes pending requests first, with a fresh attempt budget. As a
  side effect, any other field of that article that had used up its attempts
  is retried too.
- **Swap when found.** A request never clears the image: the current picture
  stays up, live or not, until a replacement is found. The new image,
  thumbnail and credit are then written in one transaction that also deletes
  the request, so readers never see an article without an image.
- **Turned-down pictures.** The image being replaced (display image and
  thumbnail) is recorded in `article_rejected_images` and never picked for
  that article again, at any size. `images.Identity` reduces a URL to the
  picture behind it: the Wikimedia file at any width, the Flickr or Pexels
  photo id, or the Unsplash photo path. `Searcher.SearchExcluding` drops those
  pictures before the per-source and total caps, so they do not take a slot.
- **No result fails at once.** If nothing new turns up besides turned-down
  pictures, nothing is relevant enough, or nothing loads, the request is
  marked `failed` straight away: the same words would find the same pictures
  again. A transient error (a timeout, a provider error) leaves it pending
  until its attempt budget is spent.
- **Races.** The image write is guarded by the request's `seq`: asking again
  while a pass is working on the earlier request means that pass cannot
  close the new one or swap in its image. A normal pass filling an empty
  image does not land on top of a request made during it either.
- **What the admin sees.** Rows and the preview show **"Finding a new
  image…"** while pending and **"No new image found — try other search
  words"** after a failure; the dashboard's poll notices both.
- **Rewrite box hint.** Typing image, photo, picture or pic into the rewrite
  feedback shows "Rewrites only change the text — use Find a new image to
  change the picture." It is a browser-side reminder and never blocks.

## Stored Fields

The selected image is stored on the `generated_articles` record:

| Field | Description |
|-------|-------------|
| `image_url` | Display image: article hero, featured card and `og:image`; at most ~1280 px wide where the host allows |
| `image_thumbnail` | Homepage card image; about 500 px wide where the host allows |
| `image_attribution` | Credit line (e.g. "Photo by X on Unsplash") |

On the home page, a bottom-grid card offers its display image as a sharper
alternative to the thumbnail, through a `<picture>` source that applies only
at 769-1024 px. There the grid has two columns and a card is up to 466 CSS px
wide, so a 500 px thumbnail is soft on a 2x screen, which takes the display
image instead. At three columns (above 1024 px) a card is at most 361 px, and
offering it there had every 2x desktop fetch all 16 display images: 2.6 times
the page's image bytes. The source lists the two URLs the row stores, no
other, each with the width its URL fixes, and only when:

- they are two sizes of one picture: the same Wikimedia file at two steps,
  the same Flickr photo at two sizes, or two Unsplash API sizes whose URLs
  differ only in `w=` (a URL that sets `h=`, `ar=` or anything else that
  changes the framing is left out);
- the thumbnail is at least 466 px wide, so a 1x screen never needs the
  display image. That leaves out Unsplash's 400 px `small` size, and a
  display image the resize settled for over its byte cap when the resize
  also chose the thumbnail: that thumbnail is then the 330 px step. The page
  does not know the display image's size, so a 500 px thumbnail the resize
  did not choose is not kept from an over-cap display image; the pick path
  confirms both fields within their caps;
- the display image is the wider, and neither URL has a comma or a space.

Phones and 1x screens, and every screen outside 769-1024 px, load the
thumbnail as before. The sidebar cards are 100 px wide at every size, so
they use the thumbnail alone.

## Article Infographics

Separately from the photo, enrichment can draw an article's `key_details` as an
SVG chart: usually a horizontal bar chart, sometimes a timeline or a few stat
cards. No text-to-image model is reachable with this project's credentials, so
the chart is markup written by a text model, which suits numbers better anyway.

It is opt-in, with `ai.enrichment_infographics`:

| Mode | Drawn | Admin (review preview, Published) | Article page |
|---|---|---|---|
| `off` (default, production) | no | charts already stored | no |
| `preview` | yes | yes | no |
| `on` | yes | yes | yes |

Off, an article missing only its chart counts as complete: it is not listed for
enrichment, charged an attempt or charted. The admin still shows any chart that
was stored before, so charts drawn in `preview` can be reviewed, removed or
redrawn whatever the mode; only article pages depend on `on`.

### The call

One call per article on the `ai.infographic_model` chain (the
`enrichment_model` chain when unset), after the image and only once key details
exist. Production puts nemotron-3-super first and gpt-oss-20b second, the
reverse of `enrichment_model`: drawing charts for ten real articles, gpt-oss-20b
timed out at 120 seconds on 4 of 10, while nemotron-3-super answered all four
calls that reached it, in 22 to 46 seconds. It asks for up to 12,288 tokens: at
6,144, a second run had nemotron-3-super spend the whole budget reasoning, with
no SVG written, on 4 of 10 articles, and none of 16 calls ran out at 12,288.
That is more than the 30-second short-call class allows, so it has its own client: 120 seconds per model
(`ai.InfographicCallTimeout`) and no retries, rather than the generator's
`request_timeout` and retries. With production's two-model chain one chart can
take up to 240 seconds.

The chain is the retry within a pass, and not only for failed calls: a chart
that fails any check below goes to the next model too. Only when no model's
chart is usable is the article left for the next pass. A model that answers
that there is nothing to chart ends the chain.

The chart is put off, without using an attempt, when the pass deadline has
passed, when less of the pass is left than the whole chain could take (120
seconds per model), and while an admin's "Find a new image" request is pending
for the article (the new image is only saved once the article is done, so it
should not wait on a chart). Only the first of these ends the pass; otherwise
later articles still get their quicker fields.

There are three outcomes. A chart is stored in `infographic_svg`. A failure
(every model's call failed or its chart was refused) leaves the column empty
for a later pass and uses an attempt. An article with
nothing to compare gets the sentinel `none`, so it is not asked again.

Missing charts are not counted in `milesahead_articles_degraded`: every article
written before the feature lacks one.

### Words and numbers in the chart

The chart's title, description and every label are public text derived from
the article, with no revision after them, like Scout's Take. They get the same
checks (`svg.Text` reads them, whole, so a name split across two labels is still
one name): a reference to a source by number ("Source 1") or a news-site name
outside the subject exception refuses the chart, to be drawn again.
`cmd/flag-outlet-mentions` reads stored charts too.

Every number in the chart must also be one the article states
(`ai.UnsupportedChartNumbers`), in its title, summary, key details or body.
Numbers are compared by value: thousands separators, currency signs and units
are ignored, and a "$3k" matches "3,000". A made-up, miscopied or derived
figure (a sum the article does not give) refuses the chart. The one exemption is
bare integers up to 10 with no currency, unit or separator: axis ticks, row
numbers and small counts. The prompt asks for no numbered axes and no derived
figures, so a model is not refused for something it was not told.

### Text past the edges

The chart is shown as an image, which clips at its 560x360 frame, so a label
that runs past an edge loses whatever falls outside: on the first charts the
production models drew, five of nine had row labels so long that the airline
names or tier percentages were cut off, with every number still in the markup.
`svg.Overflow` refuses those. It estimates each run of text's width from
Helvetica's character widths (Arial's are the same), at the font size, weight,
letter spacing and anchor the chart gives it, through any transforms, and
refuses the chart when one runs more than 2 units past an edge. On those nine
charts it refused exactly the five that were cut off. The estimate is for
macOS, iOS and Windows; Android's Roboto is a little narrower, and Linux's
DejaVu Sans about a tenth wider, so text that only just fits may clip there.
The prompt gives the limits a model can count to: 40 characters for the
headline, 50 for the takeaway, 16 for a row label.

The prompt also asks for what no check can catch, and the review below is for
those: bars only for amounts of the same kind and unit (never a ratio or a
percentage beside points), and a takeaway that keeps the article's rules and
conditions as it states them.

### Reviewing charts

The review queue preview shows the article's chart above the story, with
**Remove chart** and **Redraw chart**. Published articles are not in the queue,
so the Published page shows each row's chart folded under "Chart" (with "(not
on the site yet)" until the mode is `on`), and the same two actions in the row
menu. This is how charts drawn for the archive in `preview` are checked before
`on` publishes them.

- **Remove chart** stores `none`: the chart leaves the page at once and is not
  drawn again.
- **Redraw chart** clears the column and resets the article's enrichment
  attempts, so the reconciler draws a new chart on a coming pass (any other
  field still missing is retried with that budget too).

A stored chart that no longer passes the sanitiser is shown nowhere; the admin
marks it "Chart unusable" and offers the same two actions, and the server logs
it once rather than on every page view.

### Why an image, and the sanitiser

The page shows the chart as `<img src="data:image/svg+xml;base64,...">`, never
as inline SVG. An SVG loaded as an image runs no script, loads nothing it
references and has an opaque origin, and the site serves no URL that opens it
as a document. A bug in the sanitiser therefore cannot become script running on
the site.

`internal/svg` is still strict, at write and again at render (a row edited by
hand is re-checked before it is shown). It parses with `encoding/xml` in strict
mode, keeps only drawing elements and text (`svg`, `g`, `title`, `desc`,
`rect`, `circle`, `ellipse`, `line`, `polyline`, `polygon`, `path`, `text`,
`tspan`), drops an unknown element with everything inside it, keeps only
presentation attributes whose values use a fixed alphabet (no colon, slash,
quote, semicolon, backslash, ampersand or angle bracket, so no URL, `url()`,
CSS or markup can be spelt), writes only the first of a repeated attribute,
rejects namespaced attributes, and writes the result with its own encoder.
`<title>` and `<desc>` keep text only. Comments, processing instructions, DTDs,
`class`, `id`, `style`, anything after the root and nested `<svg>` are dropped.
It rejects input or output over 64 KB, more than 400 elements, nesting deeper
than 16, and a `viewBox` that is not four plain numbers with a size up to 4,000.

The image's alt text is the root's own `<title>` and `<desc>` (direct children
of `<svg>`; a chart without a root `<title>` is refused). A `<title>` deeper in
the drawing is a tooltip on one shape and is not used.

The cost of an image is theming: it cannot inherit the page's colours. The
sanitiser pins `currentColor` to the light theme's text colour, and the dark
theme inverts the image and turns its hues back (`filter: invert(1)
hue-rotate(180deg)`), which keeps each bar's colour family. Under forced colours
(Windows High Contrast) the chart keeps its own colours on a white background.

### Placement

At the top of the story, after the headline, summary and photo band, beside
Key Details on desktop. The canvas is 560×360 with 18 px labels, shown at most
560 px wide, so the labels stay about 11 px on a phone. It is not shown on the
homepage: shrunk to a card thumbnail a chart cannot be read.
