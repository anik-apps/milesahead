# Reference Pages & Scout Persona — Design Specification

**Date:** 2026-03-30

---

## 1. Overview

Add curated reference pages (starting with credit cards) and a virtual persona "Scout" who provides opinionated verdicts across the site. Reference pages are living documents that auto-update when related news comes in, with admin approval for content changes.

### Goals
- Curated, always-up-to-date reference pages for credit cards (and future categories)
- Auto-link articles to reference pages when they mention relevant cards
- Auto-detect news that affects reference pages, draft updates for admin approval
- "Scout's Verdict" personality throughout the site

### Initial Scope
- 16 credit card reference pages
- Scout persona on articles and reference pages
- Auto-linking in generated articles
- Auto-update detection and drafting

---

## 2. Data Model

### reference_pages table
| Column | Type | Description |
|---|---|---|
| id | INTEGER PK | |
| slug | TEXT UNIQUE | e.g., "amex-platinum" |
| title | TEXT | "American Express Platinum Card" |
| category | TEXT | "credit-card", "bank-rules", "loyalty-program" |
| issuer | TEXT | "Amex", "Chase", etc. |
| sections_json | TEXT | Ordered JSON array of sections (flexible per category) |
| recent_changes_json | TEXT | JSON array of {date, summary, article_id} |
| pending_updates_json | TEXT | JSON array of AI-drafted section updates awaiting approval |
| scouts_verdict | TEXT | HTML — Scout's opinionated take |
| image_url | TEXT | Card image or hero |
| keywords_json | TEXT | JSON array of detection keywords e.g., ["Amex Platinum", "American Express Platinum"] |
| status | TEXT | "published", "draft" |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### Sections structure (flexible per category)
```json
[
  {"key": "overview", "title": "Overview", "content": "<p>HTML</p>"},
  {"key": "key_benefits", "title": "Key Benefits", "content": "<ul>..."},
  {"key": "fees", "title": "Annual Fee & Costs", "content": "..."},
  ...
]
```

### Credit card default sections
1. Overview
2. Key Benefits
3. Annual Fee & Costs
4. Sign-up Bonus
5. Earning Rates
6. Redemption Options
7. Travel Credits & Perks
8. Competitor Comparison
9. Best For
10. Scout's Verdict

### Pending update structure
```json
[
  {
    "id": 1,
    "article_id": 259,
    "article_title": "Amex Platinum Removes Saks Benefit",
    "section_key": "travel_credits_perks",
    "current_content": "<p>old content</p>",
    "proposed_content": "<p>updated content</p>",
    "summary": "Removed Saks Fifth Avenue $100 credit for new applicants",
    "created_at": "2026-03-30T..."
  }
]
```

---

## 3. Initial 19 Cards

| Issuer | Card | Slug |
|---|---|---|
| Amex | Platinum | amex-platinum |
| Amex | Gold | amex-gold |
| Amex | Business Platinum | amex-business-platinum |
| Chase | Sapphire Preferred | chase-sapphire-preferred |
| Chase | Sapphire Reserve | chase-sapphire-reserve |
| Chase | Ink Business Preferred | chase-ink-business-preferred |
| Capital One | Venture X | capital-one-venture-x |
| Capital One | Venture | capital-one-venture |
| Capital One | SavorOne | capital-one-savorone |
| Citi | Custom Cash | citi-custom-cash |
| Citi | Strata Premier | citi-strata-premier |
| Citi | Strata Elite | citi-strata-elite |
| Citi | Double Cash | citi-double-cash |
| Bank of America | Premium Rewards | boa-premium-rewards |
| Bank of America | Premium Rewards Elite | boa-premium-rewards-elite |
| Amex | Hilton Honors Aspire | hilton-honors-amex-aspire |
| Bilt | Blue | bilt-blue |
| Bilt | Obsidian | bilt-obsidian |
| Bilt | Palladium | bilt-palladium |

---

## 4. Scout Persona

**Name:** Scout
**Tone:** Knowledgeable insider who gives straight, opinionated advice. Not salesy. Practical.
**Examples:**
- "This is the card you get if you eat out more than you cook. The 4x on dining pays for the annual fee in about two months of regular restaurant spending."
- "Solid card, but the Venture X does most of what this does for a lower fee. Unless you're deep in the Chase ecosystem, compare before committing."

### Where Scout appears:
- **Reference pages**: "Scout's Verdict" section at the bottom — 2-3 paragraph opinionated review
- **Articles**: "Scout's Take" box after the article content — 1-2 sentence quick opinion
- **Homepage**: Optional one-liner on featured card

### Scout's Verdict generation:
- AI generates initial verdict based on card details
- Admin reviews and can edit
- Auto-updated when significant news changes the value proposition (with approval)

---

## 5. Auto-Linking

When an article is generated, scan the content for card names and link to reference pages:

**Detection keywords per card:**
- "Amex Platinum" / "American Express Platinum" → `/cards/amex-platinum`
- "Chase Sapphire Preferred" / "CSP" → `/cards/chase-sapphire-preferred`
- etc.

**Rules:**
- Only link first mention in each article (don't over-link)
- Don't link if already inside an `<a>` tag
- Link format: `<a href="/cards/{slug}">{card name}</a>`
- Runs as a post-processing step after article generation

---

## 6. Auto-Update Flow

When a new article is created:

1. **Detection**: Scan article title + content for reference page keywords
2. **Recent Changes**: If match found, add entry to `recent_changes_json` immediately:
   ```json
   {"date": "2026-03-30", "summary": "Saks benefit removed for new applicants", "article_id": 259, "article_slug": "amex-platinum-saks-removed"}
   ```
3. **Section Update Draft**: AI reads the article + current section content → generates a proposed update → stored in `pending_updates_json`
4. **Admin Review**: Admin sees pending updates in `/admin/cards/{id}/updates`, can approve (replaces section content), edit, or reject each

---

## 7. URLs & Navigation

### Public:
- `/cards/` — listing of all published reference pages, grouped by issuer
- `/cards/{slug}` — individual card page with all sections

### Admin:
- `/admin/cards` — list all reference pages (published + draft)
- `/admin/cards/new` — create new reference page
- `/admin/cards/{id}/edit` — edit sections
- `/admin/cards/{id}/updates` — review pending AI updates

### Navigation:
- Add "Cards" to the main site nav (between hero and articles on homepage, or as a nav link)
- Reference pages linked from article sidebar when relevant

---

## 8. Card Page Template

```
[Hero: Card image/illustration]
[Title: American Express Platinum Card]
[Issuer badge] [Category badge]

[Table of Contents — jump links to sections]

[Section 1: Overview]
[Section 2: Key Benefits]
...
[Section N: Scout's Verdict]

[Recent Changes — chronological list with article links]
```

---

## 9. Article Integration

### Scout's Take box (on article pages):
```html
<div class="scouts-take">
  <div class="scout-avatar">Scout</div>
  <p>"Quick one-liner opinion about this deal/news."</p>
</div>
```

Generated alongside the article by the AI. Stored in a new `scouts_take` column on `generated_articles`.

### Sidebar card links (on article pages):
If the article mentions cards that have reference pages, show them in the sidebar:
```html
<div class="sidebar-section">
  <h4>Related Cards</h4>
  <ul>
    <li><a href="/cards/amex-platinum">Amex Platinum</a></li>
  </ul>
</div>
```

---

## 10. Initial Content Generation

For each of the 16 cards:
1. AI generates all sections using public knowledge + any existing articles about the card
2. AI generates Scout's Verdict
3. Admin reviews, edits, publishes
4. Auto-linking and auto-update detection begin

---

## 11. Implementation Order

1. Database: reference_pages table + scouts_take column on generated_articles
2. Admin CRUD for reference pages
3. Public card listing and detail pages
4. AI initial content generation for 16 cards
5. Scout's Verdict on articles
6. Auto-linking in article generation
7. Auto-update detection and drafting
8. Navigation updates
