---
title: Reference Pages
weight: 1
---

Reference pages are curated, evergreen guides for credit cards. They provide structured information that stays up to date as new articles are published. This page covers their content, seed data and admin tools; how readers browse, filter and compare them is on [Reader Site](../reader-site/#cards).

## Card Coverage

The project ships with 53 credit cards across 6 categories:

| Category | Count | Examples |
|----------|-------|----------|
| Airline | 14 | Delta SkyMiles Reserve, United Club Infinite, Alaska Visa Signature |
| Travel | 10 | Amex Platinum, Chase Sapphire Reserve, Capital One Venture X |
| Cash-back | 9 | Citi Double Cash, Chase Freedom Flex, Discover it Cash Back |
| Hotel | 8 | Hilton Aspire, Marriott Bonvoy Brilliant, World of Hyatt |
| Business | 6 | Amex Business Platinum, Chase Ink Business Preferred |
| Rewards | 6 | Bilt Mastercard, Bank of America Premium Rewards Elite |

## Page Structure

Each reference page contains 9 structured sections plus a Scout's Verdict:

| Section | Content |
|---------|---------|
| Welcome Bonus | Current sign-up offer and spend requirements |
| Earning Rates | Points/miles per dollar by category |
| Annual Fee | Fee amount and any fee credits |
| Key Benefits | Lounge access, credits, insurance, etc. |
| Transfer Partners | Airline and hotel loyalty program transfers |
| Redemption Options | How to use points/miles for maximum value |
| Drawbacks | Limitations, restrictions, and downsides |
| Best For | Ideal cardholder profile |
| Recent Changes | Timeline of recent updates from published articles |

### Scout's Verdict

Each card has a **Scout's Verdict** -- a brief editorial summary from the Scout persona giving a practical recommendation. This follows the same writing style as article Scout's Takes: direct, practical, and non-promotional.

## Scout Persona

The `site.persona_name` config (default "Scout") defines the editorial voice used across articles and reference pages. Scout provides opinionated but well-reasoned takes grounded in the factual content.

## Auto-Linking

Cards are linked to articles in two steps:

- **When an article is generated** (`cmd/milesahead/scan.go`, `internal/linker`), the first mention of each published card in the content becomes a link to its page, and the two most-mentioned cards become the article's related cards (its card rows). An article that mentions three or more cards relies on the admin choosing them (Linked cards, `/admin/articles/{id}/cards`).
- **When an article is approved**, `internal/cardupdate` checks whether it changes anything on the cards it mentions and, if so, drafts pending section updates (a short AI call) for the admin to review. Detection waits out the 30-second approval Undo window.

Keywords stored on each reference page (`keywords_json`) drive the matching. The seed data includes 42+ keyword variants per category to maximize link coverage across different ways blogs refer to cards (e.g., "CSR", "Chase Sapphire Reserve", "Sapphire Reserve").

## Card Images

Card art is self-hosted: every card's `image_url` in the seed data is `/static/img/cards/<slug>.png`, a committed palette PNG with transparency, at most 480px wide. Hotlinking issuer images broke 15 cards when the issuers redeployed their sites, so `TestCardArt_IsSelfHosted` fails if any card points anywhere else or its file is missing.

The path is relative on the site. Places where the image leaves the site (og:image, JSON-LD, and the newsletter's Card of the Week) turn it into an absolute URL on the configured domain.

`image_url` can still be changed per card in the admin dashboard. A change to the seed file reaches existing production cards only through a scoped force-reseed (`seed-cards -force -slug <slug>`, or the Deploy workflow's `reseed_slugs` input), because a normal seed skips cards that already exist.

## Seed Script

Reference pages are seeded from `cmd/seed-cards/cards.json`:

```bash
# Seed new cards (skips existing slugs)
go run ./cmd/seed-cards -db ./data/milesahead.db

# Overwrite all cards with seed data
go run ./cmd/seed-cards -db ./data/milesahead.db -force

# Overwrite only named cards
go run ./cmd/seed-cards -db ./data/milesahead.db -force -slug amex-platinum,amex-green
```

The seed script:
- Reads card definitions from `cards.json`
- Checks each slug against the database
- Inserts new cards or updates existing ones (with `-force`)
- Reports counts: inserted, updated, skipped

Skipping existing slugs by default is what makes the deploy pipeline's seed step
safe to run on every push -- it can add a new card but can never silently revert
an edit made through the admin dashboard.

That also means correcting seed data does **not** reach production on its own; a
`-force` run is required. Prefer `-slug` for corrections: production diverges
from `cards.json` over time as cards are edited in the admin UI, so a bare
`-force` can rewrite far more than intended. `-slug` scopes the rewrite to the
cards you actually mean to change.

`cmd/seed-structured` works the same way and takes the same flags, but populates
the structured comparison columns (`earning_rates_json`, `credits_json`,
`benefits_json`, `protections_json`) used by the comparison tool. It skips cards
that already have structured data unless forced.

## Video Embeds

A card page can carry a YouTube explainer video, rendered as a full-width hero
above the article grid.

Videos are declared in `internal/web/cardvideos.go`, which maps a card slug to
its video ID plus the per-card heading, duration and whether a reviewed caption
track exists:

```go
var cardVideos = map[string]cardVideo{
    "amex-platinum": {ID: "...", Heading: "...", Duration: "3:30", Captions: true},
}
```

The player is a **click-to-load facade**: the page ships a poster image from
`static/img/video-poster-<slug>.jpg` and a play button, and `static/js/card-video.js`
only injects the `youtube-nocookie` iframe on click. No request reaches YouTube
until a visitor opts in, which keeps roughly 700 KB of third-party player code
off the page.

Embeds are gated on `?beta=1` so a video can be reviewed on the live site before
release; beta responses are also `noindex` and omit their canonical link.

### Heading convention

The heading should not promise an answer the video does not give. Where a video
concludes the credits outweigh the fee, "Watch: is the $895 fee worth it?" is
fair. Where it concludes they do not — the Strata Elite works out at -$85 a year
on credits alone — the heading asks whether the credits cover the fee instead,
so a reader is not set up for a verdict that never arrives.

That means phrasing varies with the verdict rather than following one template.
It is a deliberate trade of uniformity for honesty, and is worth revisiting once
there are enough videos to see how the variation reads.

## Infographics

A card page can carry a designed infographic: a portrait image, 1024 px wide and
1536 or more high, that summarises the card's earning, redemption and credits.

It is not placed inline. In the article column it would render at under half its
designed size, leaving its body text around 7px. Instead a compact **"Infographic /
Expand" bar sits under the card art** (in the sidebar on desktop, in the header card
on mobile) and opens the full image in a native `<dialog>` viewer. The bar adds
about 40px to the sticky sidebar; a full-width teaser there would add about 145px
and push the TOC and the Card Details button further below the fold.
`static/js/card-infographic.js` does the upgrade; without JavaScript the bar is a
plain link to the image. The page behind the viewer does not scroll while it is
open, and it opens with focus on its Close button.

Infographics are declared in `internal/web/cardinfographics.go`:

```go
var cardInfographics = map[string]cardInfographic{
    "amex-platinum": {
        Alt:     "Amex Platinum infographic: annual fee, ... The text version below lists every figure.",
        Text:    []string{"Annual fee: $895. ...", "Earn Membership Rewards: ..."},
        Checked: "Sep 22, 2026",
        Facts:   []string{"$895", "$219"},
    },
}
```

- **Alt** names the image briefly.
- **Text** is the image's content, one plain sentence group per section, offered as
  a collapsed "Text version" under the image in the viewer. It is the only way a screen-reader user
  gets what the image says, so it transcribes every figure and caveat, including
  cadences, thresholds and the "important to know" warnings. A single long alt
  string would be read as one unbroken run with nothing to navigate.
- **Checked** is shown in the caption. An image cannot be corrected by a reseed the
  way prose can, so readers are told how current it is.
- **Facts** are figures the image states. `TestCardInfographics_FactsMatchCardProse`
  fails if one disappears from the card's `cards.json` text, and
  `TestCardInfographics_TextVersionCarriesTheFacts` fails if the text version
  drops one. Two limits: it checks the **seed file only**, so an admin edit or an
  approved card update that changes the live prose will not trip it; and it
  cannot see a *second, older* figure elsewhere in the prose. Recheck the image
  whenever either happens to a card that has one.

To add a card, generate the images and add the map entry:

```bash
scripts/infographic-images.sh chase-sapphire-reserve ~/Downloads/infographic.png
```

This writes two files to `static/img/`:

| File | Size | Used for |
|---|---|---|
| `infographic-<slug>.jpg` | 1024 wide at the source's aspect ratio (1024x1536 for 2:3), ~400-600 KB | the viewer; loaded only when it opens |
| `infographic-<slug>-teaser.jpg` | 256x160, ~14 KB | the bar's thumbnail, shown at 84 CSS px |

The script scales any source to 1024 px wide and keeps its aspect ratio, so nothing
is cropped or stretched: a 1024x1536 source is unchanged and a 1080x1917 one becomes
1024x1818. The teaser is always the top 1024x640 region. It rejects anything
shorter than 2:3 (a landscape image, say) and flattens transparency onto white,
since JPEG has no alpha channel. The viewer's `width`/`height` attributes come from
the JPEG's header, read at startup, so a taller image needs no extra configuration;
`TestCardInfographics_CommittedImageSizes` fails if a committed image is not 1024
wide or its teaser is not 256x160.

Image URLs carry `?v=<content hash>`, computed from the files at startup. Static
files are served with only `Last-Modified`, so browsers cache them heuristically;
without the version, replacing an image would leave returning visitors on the old
artwork beside the new "Figures checked" date.

Infographics render for everyone: unlike the video embed they are first-party
images with no third-party request, so there is no `?beta=1` gate.

## Admin Management

Cards are managed under **Cards** in the admin bar. The list puts cards with open flags or pending updates in a **Needs attention** group and has a filter box.

The card editor:

- has a sticky bar with the card's status, its open flags, View page and Save, and warns about unsaved changes;
- lays fields out as labelled two-column rows, with each flag shown on its field and a **Resolve** button (status flags included);
- checks the JSON fields (earning rates, credits, benefits, protections) as you type, with a Format button; a bad value comes back with a 400, everything as typed, and the error and line number on its field;
- has collapsible, reorderable sections, with Undo on Remove;
- clears a field that was emptied, rather than keeping its old value.

**Card updates** (section changes drafted when an approved article affects a card) are shown as a word-level diff with the rendered before and after, and approved or rejected one by one. An update whose section no longer exists fails visibly.

Approving an update replaces the whole section with the proposed text. So the model is shown the complete section and told to return all of it, keeping every item the article doesn't change; its reply budget is 4,096 tokens so a long section fits. Until October 2026 it saw only the first 1,000 bytes. That covered most Key Benefits and Travel Credits sections, so drafts for those sections dropped everything past that point. Two guards now catch a draft that loses content anyway:

- **The detector** doesn't save a draft with more than one fewer list item or table row than the section. One fewer is allowed, because a single benefit can end.
- **The updates page** shows a warning on any such draft already waiting, including those made before the fix. Reject them unless the article really ended those benefits.

The reader-facing card index, card pages and compare tool are described in [Reader Site](../reader-site/#cards).
