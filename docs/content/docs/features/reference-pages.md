---
title: Reference Pages
weight: 1
---

Reference pages are curated, evergreen guides for credit cards. They provide structured information that stays up to date as new articles are published.

## Card Coverage

The project ships with 41 credit cards across 6 categories:

| Category | Count | Examples |
|----------|-------|----------|
| Travel | 10 | Amex Platinum, Chase Sapphire Reserve, Capital One Venture X |
| Cash-back | 9 | Citi Double Cash, Chase Freedom Flex, Discover it Cash Back |
| Airline | 6 | Delta SkyMiles Reserve, United Club Infinite |
| Business | 5 | Amex Business Platinum, Chase Ink Business Preferred |
| Hotel | 5 | Hilton Aspire, Marriott Bonvoy Brilliant |
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
```

The seed script:
- Reads card definitions from `cards.json`
- Checks each slug against the database
- Inserts new cards or updates existing ones (with `-force`)
- Reports counts: inserted, updated, skipped

## Admin Management

Reference pages can be managed through the admin dashboard:

- Edit any section's content
- Update the Scout's Verdict
- Change the card image URL
- Review and approve pending updates (AI-drafted section changes triggered by new articles)
- Set page status (draft, published)
