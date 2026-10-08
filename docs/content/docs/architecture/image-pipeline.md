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

Separately from the photo, enrichment can add an **"At a glance"** chart: a few
numbers from the article's `key_details`, shown in a panel under Key Details.
A model chooses the numbers; the site draws the chart. No text-to-image model
is reachable with this project's credentials, and a chart of four numbers is
better drawn by the site anyway.

It is opt-in, with `ai.enrichment_infographics`:

| Mode | Drawn | Admin (review preview, Published) | Article page |
|---|---|---|---|
| `off` (default) | no | charts already stored | no |
| `preview` | yes | yes | no |
| `on` (production since October 2026) | yes | yes | yes |

Off, an article missing only its chart counts as complete: it is not listed for
enrichment, charged an attempt or charted. The admin still shows any chart that
was stored before, so charts drawn in `preview` can be reviewed, removed or
redrawn whatever the mode; only article pages depend on `on`.

### What the model returns

A small JSON object, read by `internal/infographic`: the kind of chart, a
headline, its rows (label, value, an optional detail) and an optional note (a
date or condition) and takeaway. Four kinds:

| Kind | For | Drawn as |
|---|---|---|
| `change` | one number that moved: a fee, a ratio, a price | before, crossed out, then after in the accent colour; one above the other when either is too long to sit side by side |
| `compare` | two to five amounts of the same kind and unit | bars, longest in the accent colour, lengths computed by the site |
| `figures` | numbers that do not compare | one large headline figure and one to four more in a grid |
| `timeline` | dates | dated steps, the one that matters marked |

The model chooses no colours, sizes or positions. It used to write the chart as
SVG, and its charts were inconsistent: colours picked from a list, filled boxes,
labels cut off at the edge, and bars whose lengths claimed comparisons the
numbers did not make. Drawn by the site, every chart uses the same four designs
in the site's own type and colours, real dark mode included, and its text wraps
instead of being clipped.

A chart is refused when it does not fit its kind (`Chart.Validate`): the wrong
number of rows (a single figure is not worth a chart), a field over its length
limit, an unknown kind, a row of a change or comparison with no label, a
change to the same value, and for `compare` any value that is not exactly one
number, or rows in different units
(the value with its number taken out: "%", "$", "miles"). That is the rule
against meaningless bars, enforced rather than only asked for: a ratio ("1:3"),
a range ("7,000-24,000"), or a percentage beside a multiplier cannot be a bar.

### The call

One call per article on the `ai.infographic_model` chain (the
`enrichment_model` chain when unset), after the image and only once key details
exist. Production puts nemotron-3-super first and gpt-oss-20b second, the
reverse of `enrichment_model`: when the model drew SVG, gpt-oss-20b timed out at
120 seconds on 4 of 10 real articles, while nemotron-3-super answered every call
that reached it. It asks for up to 12,288 tokens, since a reasoning model spends
most of its budget before it answers and a reply cut off mid-reasoning has no
chart in it. That is more than the 30-second short-call class allows, so it has
its own client: 120 seconds per model (`ai.InfographicCallTimeout`) and no
retries, rather than the generator's `request_timeout` and retries. With
production's two-model chain one chart can take up to 240 seconds.

The chain is the retry within a pass, and not only for failed calls: a chart
that fails any check goes to the next model too. Only when no model's chart is
usable is the article left for the next pass. A model that answers that there is
nothing to chart ends the chain.

The chart is put off, without using an attempt, when the pass deadline has
passed, when less of the pass is left than the whole chain could take (120
seconds per model), and while an admin's "Find a new image" request is pending
for the article (the new image is only saved once the article is done, so it
should not wait on a chart). Only the first of these ends the pass; otherwise
later articles still get their quicker fields.

There are three outcomes. A chart is stored in `infographic_json`. A failure
(every model's call failed or its chart was refused) leaves the column empty
for a later pass and uses an attempt. An article with nothing to compare gets
the sentinel `none`, so it is not asked again.

Missing charts are not counted in `milesahead_articles_degraded`: every article
written before the feature lacks one.

### Words and numbers in the chart

The chart's headline, labels, values, details, note and takeaway are public text
derived from the article, with no revision after them, like Scout's Take. They
get the same checks, read whole (`Chart.Text`): a reference to a source by
number ("Source 1") or a news-site name outside the subject exception refuses
the chart. `cmd/flag-outlet-mentions` reads stored charts too.

Every number in the chart must also be one the article states
(`ai.UnsupportedChartNumbers`), in its title, summary, key details or body.
Numbers are compared by value: thousands separators, currency signs and units
are ignored, and a "$3k" matches "3,000". A made-up, miscopied or derived
figure (a sum the article does not give) refuses the chart. The one exemption is
bare integers up to 10 with no currency, unit or separator in the chart's
words, where the article may spell a small count out ("three nights"). A
comparison's values get no exemption (`ai.UnsupportedValues`): they are bar
lengths, and an invented "3" beside a stated "8" would draw a wrong one. The
prompt asks for no derived figures, so a model is not refused for something it
was not told.

What no check can catch is a sentence that gets the article wrong, or a label
on the wrong number. The prompt asks the takeaway to keep the article's rules
and conditions as it states them (activate before you spend, not after), and
the review below is for the rest.

### Reviewing charts

The review queue preview shows the article's chart where the page will, under
Key Details, with **Remove chart** and **Redraw chart** beneath it. Published
articles are not in the queue, so the Published page shows each row's chart
folded under "Chart" (with "(not on the site yet)" until the mode is `on`), and
the same two actions in the row menu. This is how charts drawn for the archive
in `preview` are checked before `on` publishes them.

- **Remove chart** stores `none`: the chart leaves the page at once and is not
  drawn again.
- **Redraw chart** clears the column and resets the article's enrichment
  attempts, so the reconciler draws a new chart on a coming pass (any other
  field still missing is retried with that budget too). It takes an optional
  note on what was wrong ("label the bars by airline"), up to 300 characters,
  stored in `infographic_note` and given to the model on that draw; it is
  cleared once a chart or "nothing to chart" is stored, and kept if the draw
  fails. The note is followed but is not a source: numbers still come from
  the article, so a wrong number in the article needs a rewrite. While the
  redraw waits, the preview shows the note and Published marks the row
  "Redrawing chart".

### After a rewrite

A rewrite ("Request rewrite", or "Unpublish and request a rewrite") changes
the article's text, and its key details and chart are derived from that text,
so they are refreshed with it (`db.ApplyRevision`). The rewrite's own key
details, which the model writes with the article, replace the old ones, with
the offer end date they state; if it wrote none, the old ones are cleared and
enrichment extracts them again. The chart is cleared and drawn again from the
new key details on the next pass, and the attempts are reset so enrichment
picks the article up. A chart that was removed, or found nothing to chart,
stays so; Redraw chart asks again. A redraw note waiting from before the
rewrite is used on that draw. The summary and Scout's Take are not refreshed
by a rewrite.

A stored chart the site cannot draw (it no longer passes `Chart.Validate`, or a
row edited by hand) is shown nowhere; the admin marks it "Chart unusable" and
offers the same two actions, and the server logs it once rather than on every
page view.

### Drawing and placement

`templates/_infographic.html` draws the panel, shared by the article page, the
review preview and Published; `.glance` in `static/css/style.css` styles it like
the Key Details panel. The chart's words reach the page only as escaped text:
nothing the model writes is markup. Bar lengths are percentages of the longest
bar, computed by `Chart.View` from the values.

On desktop the panel sits in the sidebar, between Key Details and Links. A
sidebar holding a chart is not sticky, since it can be taller than the window.
On phones, where the key details fold into one line under the summary, the panel
follows that line, open, before the photo. It is not shown on the homepage.
