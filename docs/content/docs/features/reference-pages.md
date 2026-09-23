---
title: Reference Pages
weight: 1
---

Reference pages are curated, evergreen guides for credit cards. They provide structured information that stays up to date as new articles are published.

## Card Coverage

The project ships with 52 credit cards across 6 categories:

| Category | Count | Examples |
|----------|-------|----------|
| Airline | 14 | Delta SkyMiles Reserve, United Club Infinite, Alaska Visa Signature |
| Travel | 10 | Amex Platinum, Chase Sapphire Reserve, Capital One Venture X |
| Cash-back | 9 | Citi Double Cash, Chase Freedom Flex, Discover it Cash Back |
| Hotel | 7 | Hilton Aspire, Marriott Bonvoy Brilliant, World of Hyatt |
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

When a new article is published that mentions a credit card, the system creates cross-links:

- The article links to the relevant reference page(s)
- The reference page's "Recent Changes" section gets an entry linking back to the article
- Keywords stored on each reference page (`keywords_json`) drive the matching

The seed data includes 42+ keyword variants per category to maximize link coverage across different ways blogs refer to cards (e.g., "CSR", "Chase Sapphire Reserve", "Sapphire Reserve").

## Card Images

Each reference page has an `image_url` field for a card image. These are set in the seed data and can be updated through the admin dashboard.

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

A card page can carry a designed infographic: a 1024x1536 portrait image that
summarises the card's earning, redemption and credits.

It is not placed inline. In the article column it would render at under half its
designed size, leaving its body text around 7px. Instead a cropped **teaser takes
the card-art slot** (the sidebar on desktop, the header card on mobile), since the
artwork already shows the card, and opens the full image in a native `<dialog>`
viewer. `static/js/card-infographic.js` does the upgrade; without JavaScript the
teaser is a plain link to the image.

Infographics are declared in `internal/web/cardinfographics.go`:

```go
var cardInfographics = map[string]cardInfographic{
    "amex-platinum": {Alt: "...", Checked: "Sep 22, 2026", Facts: []string{"$895", "$219"}},
}
```

- **Alt** is a full text alternative, because the image is almost entirely text.
- **Checked** is shown in the caption. An image cannot be corrected by a reseed the
  way prose can, so readers are told how current it is.
- **Facts** are figures the image states. `TestCardInfographics_FactsMatchCardProse`
  fails if one disappears from the card's `cards.json` text, so a fee or credit
  change cannot leave the image contradicting the page unnoticed. It cannot catch a
  *second, older* figure elsewhere in the prose, so check the card's text against
  the image when adding one.

To add a card, generate both images and add the map entry:

```bash
scripts/infographic-images.sh chase-sapphire-reserve ~/Downloads/infographic.png
```

This writes `static/img/infographic-<slug>.jpg` (the full image, about 400 KB,
loaded only when the viewer opens) and `static/img/infographic-<slug>-teaser.jpg`
(its top 1024x640 region at 480x300, about 45 KB). Infographics render for
everyone: unlike the video embed they are first-party images with no third-party
request, so there is no `?beta=1` gate.

## Admin Management

Reference pages can be managed through the admin dashboard:

- Edit any section's content
- Update the Scout's Verdict
- Change the card image URL
- Review and approve pending updates (AI-drafted section changes triggered by new articles)
- Set page status (draft, published)
