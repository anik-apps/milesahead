---
title: Reader Site
weight: 3
---

What readers see at [milesahead.kumaranik.com](https://milesahead.kumaranik.com): the home page, articles, the card pages, compare, search, error pages and the newsletter sign-up. Handlers are in `internal/web`, templates in `templates/`, styles and scripts in `static/`. Every page works in light and dark themes.

## Header, Footer and Accessibility

- The header has flat **Cards** and **Compare** links (marked with `aria-current` on their pages), a search toggle (`aria-expanded`, Escape closes it) and, when sign-up is open, a **Newsletter** pill (an envelope alone on phones). On phones the theme toggle moves to the footer.
- A skip link, a site-wide `:focus-visible` ring, 44 px header and footer targets, underlined in-text links, and colours checked to at least 4.5:1 (the dark accent buttons 7.5:1, the deal badge 5.7:1).
- Branding (the mountain mark, header mist, home hero strip and footer band) is SVG in `static/img/brand/`; PNG copies for email, the favicon and the default share image come from `scripts/brand-images.sh`.

## Home Page

- A compact hero strip with a descriptive heading; the first story starts about 343 px down on a phone. When sign-up is open, the hero has a "Get it weekly by email →" link.
- **One filter row** (`<nav aria-label="Filter articles">`): a type switch (**All · Deals · News**), a divider, then six topic chips (Airlines, Hotels, Credit Cards, Transfer Bonuses, Bank Bonuses, Travel Deals). Type and topic combine (`/?type=deal&tag=hotels`); a topic link keeps the type and the switch keeps the topic. On phones the row scrolls sideways with an edge fade instead of wrapping.
- "News" is the reader-facing name of the `summary` type; URLs (`?type=summary`) and the admin still say summary.
- Types with no published articles (today Roundups and Alerts) are hidden and come back once one is published.
- Tags trimmed from the bar still filter: `/?tag=loyalty-programs` shows a removable chip.
- When something is filtered, a strip reads "Showing N articles · [Deals ✕] [Hotels ✕] · Clear all"; each ✕ removes only its own filter. An empty result names the filters; unknown values get the same empty state, escaped.
- Offers that have ended (see [Ended offers](#ended-offers)) leave the default feed; filtered views keep them, badged.
- Grid cards offer the display image as a sharper alternative only at 769-1024 px; see [Image Pipeline](../../architecture/image-pipeline/#stored-fields).

## Article Page

Measured on seeded data at 1280×800, the headline moved from 677 to 189 px down and the story from 870 to 721 px; on a 390 px phone the story starts at 746 px instead of 1,173.

1. Type and tag badges, the headline, the ended-offer note if any, then a meta line: date, **By Scout** (`site.persona_name`) and tags.
2. The TL;DR as a larger standfirst.
3. The photo as a 280 px band (170 px on phones), `alt=""`, with width and height set, and a `srcset` when the two stored URLs are sizes of one picture.
4. With `ai.enrichment_infographics: on`, an "At a glance" chart of the key details sits under Key Details in the sidebar, and under the folded key details on phones (see [Article Infographics](../../architecture/image-pipeline/#article-infographics)). On in production since October 2026.
5. The story, about 73 characters per line. Only off-site links open a new tab (with `noopener`).
6. **Key details** and links: a sticky sidebar on desktop (not sticky when it holds an "At a glance" chart, which can make it taller than the window), a "Key details (n)" disclosure on phones.
7. **Scout's Take** as an h2, straight after the story.
8. **Card rows** for the cards the article names, the ones in the headline first: fee, welcome bonus, up to 180 characters of verdict, **Full review** and **Apply at {Issuer} ↗** (the new tab is announced).
9. **Three related stories** sharing a tag (live first, never the article itself) and "More in {Topic} →".
10. One line with the vote buttons and share links (copy link, email, X), with no third-party scripts.
11. A newsletter sign-up box, when sign-up is open.

The news sites an article was written from are not shown anywhere on the public page; the admin preview lists them. Editor notes never reach the page.

### Ended offers

An article's offer end date comes from its Key Details (`internal/offer`, stored in `expires_on` and recomputed at startup). Only a fixed list of labels counts ("Deadline", "Booking Deadline", "Registration Deadline", "Promotion Period", "Booking Window", "Expiration Date", ...), and the offer ends on the latest date any of them names. A value saying the offer is ongoing or extended without a new date leaves no end date. The day is judged in Hawaii time, the westernmost US zone, so an "11:59 PM PT" deadline is never called early.

Once ended, the offer leaves the default home feed and the newsletter, is badged "Ended" ("Deadline passed" for a news story) in filtered views, search, card pages and the RSS feed, and its page shows a note under the headline linking to the latest stories in its topic. The deadline row is marked **Ended** and apply buttons switch to the outline style.

## Cards

### Card index (`/cards/`)

- **Filters, one row:** category chips with counts, a divider, and a **No annual fee** toggle (`fee=none`, a link with `role="button"` and `aria-pressed`). The toggle matches only fees recorded as $0, so an unrecorded fee never counts, and it greys out when it would show nothing. On phones the chips scroll sideways and the toggle sits on its own line.
- **Results line:** "N cards · Cash Back · No annual fee · Clear" on the left (Clear only when something is filtered; it keeps the sort), and **Sort** on the right: By category (the default, grouped with headings), Name (A-Z), Lowest fee, Highest fee. Unknown fees sort last. Sort applies on change; without JavaScript a small **Go** button submits. Sort never changes which cards show.
- **Old links** still filter: `?fee=under-100|100-400|over-400` and `?issuer=...` show as removable items on the results line ("Under $100 ✕").
- **Tiles** show the fee, the best earn rate with its condition, and the offer (figures only ever copied from the stored text). Card art is sized, and lazy-loaded after the first six. A card whose fee is not recorded shows no fee rather than $0.

### Card page (`/cards/{slug}`)

- A **summary panel** under the heading: card art and infographic, annual fee, welcome offer, top earn rates, the verdict's first sentence, **Apply at {Issuer} ↗**, and a labelled **Compare** form that works without JavaScript and on phones, listing the rivals the page names first.
- Earn rates are ranked by `rankEarnRates` (`internal/web/earnrates.go`): bonus rates that apply however you book, highest first; otherwise the flat rate; then portal rates where they beat both, naming the portal ("5x flights · 5x hotels via Amex Travel"). Caps, top-category-only, choose-one and rotating categories are flagged.
- The sidebar holds only the table of contents, with the current section highlighted; on phones, sticky section pills.
- **Scout's Verdict** is an h2, and every card page ends with **Next steps**: apply, one-tap comparisons, more in the category, and the compare form.

The sections, seed data, infographics and videos are covered in [Reference Pages](../reference-pages/).

## Compare (`/compare`)

- Two or three cards side by side: fee and welcome bonus as rows, earning rates split into **direct** and **travel-portal** rows, credits, benefits, protections and rewards currency.
- Every note is printed under its value; nothing is hover-only.
- A star marks the best value only when every value is a number in the same unit and the best is not a top-category or rotating rate. Text, mixed units and ties get no star; the lowest fee gets one.
- Sections are keyboard-operable toggles, open by default. Rows none of the cards have are hidden, and the section says how many. An "Only differences" switch needs JavaScript.
- The card header stays pinned under the site header. On phones each row's label sits above its values, so all three cards fit at 390 px.
- The picker is a plain GET form grouped by issuer, with popular comparisons. A duplicate, unknown or single card gets a clear notice, and only published cards can be compared. The card pages' form (`?cards=a,b`) submits here.

## Search (`/search`)

- Every word must match, in any order. The last word, and any word of three letters or more, also matches as a prefix ("sapph" finds Sapphire). Porter stemming makes "lounges" find "lounge", and accents are folded.
- A short nickname list (`searchNicknames` in `internal/db/search.go`) expands csr, csp, cfu, cff, cip, amex, plat, vx, c1, boa/bofa, ur, mr, ty, woh, pp and ftf, plus "american express", "capital one" and "bank of america".
- If nothing has every word, pages with any of them are shown, and the summary says so.
- Cards whose title holds every word sit in a strip above the articles. Titles weigh ten times the body, pages with the words side by side rank first, and ended offers come after live ones.
- Results show a type badge, date, a count line and one numbered pager; the empty state has tips.

The indexes are SQLite FTS5 tables (`articles_fts`, `cards_fts`) with the `porter unicode61 remove_diacritics 2` tokenizer, holding plain text (entities decoded; excerpts escaped when read). Editor notes are stripped before indexing. When `ftsIndexVersion` changes, both are rebuilt once at startup, recorded in `search_index_versions`; each rebuild runs in one transaction that only commits when every source row is indexed, so a failure keeps the old index.

## Error Pages

- Unknown URLs, articles and cards get a branded 404 (`error.html`). An unknown article or card slug is turned into a search of its words, and the best matches are offered.
- An article that is not published answers 404, or 410 if it was once published, with nothing of the article in the page (no headline in `<title>` or the share tags) and `noindex`.
- Server errors are logged; readers see a branded 500, never the Go error. Only "not found" becomes a 404; a database failure is a 500.

## Newsletter Sign-up

When `newsletter.signup_enabled` is on (it is in production), sign-up is linked from the header, the footer, the home hero and a compact box at the end of every article. `/subscribe` is a labelled form; the flow (check your inbox, confirm, unsubscribe and resubscribe) is described in [Newsletter](../newsletter/#sign-up-and-unsubscribe). With the switch off, there are no placements and `/subscribe` says sign-up opens soon.

## Caching and Compression

- HTML is `Cache-Control: no-cache`.
- Static files whose URL carries a content hash (`?v=`: the stylesheet, `main.js`, infographics) are cached for a year; other static files for an hour.
- Caddy compresses responses (`encode zstd gzip` in `deploy/milesahead.caddy`): HTML went from 32.8 to about 7 KB and CSS from 76 to about 15 KB. See [Deployment](../../deployment/).
- The RSS feed is at `/feed.xml`; `sitemap.xml` and `robots.txt` are served by the app.
