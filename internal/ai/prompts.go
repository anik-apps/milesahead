package ai

import (
	"fmt"
	"strings"
)

// SourceArticle represents a source used to generate or verify content.
type SourceArticle struct {
	Name    string
	URL     string
	Title   string
	Content string
}

func formatSources(sources []SourceArticle) string {
	var b strings.Builder
	for i, s := range sources {
		fmt.Fprintf(&b, "--- Source %d: [%s] ---\n", i+1, s.Name)
		fmt.Fprintf(&b, "Title: %s\n", s.Title)
		fmt.Fprintf(&b, "URL: %s\n", s.URL)
		fmt.Fprintf(&b, "Content:\n%s\n\n", s.Content)
	}
	return b.String()
}

func formatSourceURLs(sources []SourceArticle) string {
	var parts []string
	for _, s := range sources {
		parts = append(parts, fmt.Sprintf("- %s: %s", s.Name, s.URL))
	}
	return strings.Join(parts, "\n")
}

func formatSourceLinks(sources []SourceArticle) string {
	var parts []string
	for _, s := range sources {
		parts = append(parts, fmt.Sprintf("[%s] = %s", s.Name, s.URL))
	}
	return strings.Join(parts, "\n")
}

const writingStyle = `
WRITING STYLE — THIS IS CRITICAL:

VOICE & TONE:
- Write like a knowledgeable friend who happens to be a miles-and-points nerd. Conversational, witty, and opinionated — not a brochure.
- Have a point of view. Tell readers whether something is actually worth it or not. Be direct about downsides.
- Use humor where it fits naturally — don't force it, but don't be dry either. Make it specific to the topic, not generic clichés.
- Use analogies and real-world comparisons to make points land: "That's roughly the cost of two domestic economy tickets" is better than "This represents significant value."
- DO NOT sound like a salesman. Never call things "incredible", "fantastic", "amazing", "exciting", or "deal of a lifetime."

OPENINGS — VARIETY IS KEY:
- Every article must open differently. Never fall into a pattern. Here are EXAMPLES of the range (use these as inspiration, not a template):
  - Lead with the news: "Chase just bumped the Sapphire Preferred bonus to 80K — the highest public offer in over a year."
  - Lead with the math: "At 0.5 cents per point, this Wyndham buy is the cheapest we've seen since 2024."
  - Lead with the take: "This deal looks better on paper than it actually is. Here's why."
  - Lead with context: "Third transfer bonus from Amex this quarter. They're clearly trying to move points before the devaluation hits."
  - Lead with the practical: "If your renewal is coming up and you're on the fence, this tips the math."
  - Lead with surprise: "United just did something nobody expected — lie-flat seats in economy."
- BANNED openers: "If you've been...", "If you're looking for...", "In the world of...", "When it comes to...", "Are you ready for..."
- Each article in a batch MUST have a distinct opening style. Do not repeat patterns across articles.

BODY WRITING:
- Vary sentence length. Short punchy sentences between longer explanations.
- Use "you" and "your" to speak directly to the reader.
- Focus on practical implications: what should the reader DO with this information?
- Be specific with numbers: "That's $450 round-trip to Tokyo in business class" beats "significant savings on premium cabin travel."
- When comparing: be opinionated. "The Venture X is the better card here, full stop" is more useful than "both cards have their merits."
- Avoid robotic phrases: "In this article", "It is worth noting", "According to sources", "Let's dive in", "Let's break down", "without further ado".
- DO NOT use cliché idioms or proverbs. No "Rome wasn't built in a day", no "the early bird catches the worm."

FACTUAL INTEGRITY:
- NEVER invent, fabricate, or add information not present in the provided sources. Stick strictly to what the sources say.
- NO source article links or citation markers in the body. Do not link to TPG, OMAAT, Doctor of Credit, etc.
  Do not mention source site names UNLESS reporting an unconfirmed claim from a specific site.
  WRONG: "American Express is offering a 40% bonus [TPG]."
  WRONG: "According to The Points Guy, there is a 40% bonus."
  WRONG: <a href="https://thepointsguy.com/...">link</a>
  RIGHT: "American Express is offering a 40% transfer bonus to ANA through March 31."
  EXCEPTION (unconfirmed report): When a claim can only be attributed to one source and is not officially confirmed, link the source name to their article:
  RIGHT: <a href="https://thepointsguy.com/article-url" target="_blank" rel="noopener">The Points Guy is reporting</a> that Chase may change Sapphire Reserve benefits in April — take this with a grain of salt until there's official confirmation.

LINKS:
- You SHOULD include links to the actual deal, product, or official page when helpful:
  RIGHT: <a href="https://www.amazon.com/events/bigspringsale">Amazon Big Spring Sale</a>
  RIGHT: <a href="https://creditcards.chase.com/rewards-credit-cards/sapphire/preferred">Chase Sapphire Preferred</a>
  RIGHT: <a href="https://www.united.com">book on United.com</a>
  These are links to the actual content/product, NOT to the news article that reported on it.
`

const htmlFormat = `
OUTPUT FORMAT:
- Output HTML only. No Markdown.
- The output must have this exact structure:

Line 1: Article title (plain text, no HTML tags)
Line 2: Empty line
Line 3: TLDR: A 1-2 sentence summary of the key takeaway
Line 4: Empty line
Line 5+: KEY_DETAILS_START
- Detail 1 label: value
- Detail 2 label: value
(include 3-6 bullet points with the most important facts: bonus amounts, deadlines, costs, requirements)
KEY_DETAILS_END
Line: Empty line
Line: LINKS_START
- Link text | URL
(include 2-4 links to actual deal pages, booking sites, or official program pages — NOT news articles)
LINKS_END
Line: Empty line
Line+: The full article body in HTML (<p>, <h3>, <ul>, <li>, <strong>, <em>)

Example:
Wyndham Points 100% Bonus Through May 1

TLDR: Wyndham Rewards is doubling purchased points through May 1 — works out to about 0.5 cents per point if you have a specific redemption in mind.

KEY_DETAILS_START
- Bonus: 100% extra points on purchases
- Deadline: May 1, 2026
- Minimum purchase: 1,000 points
- Effective cost: ~0.5¢/point with bonus
KEY_DETAILS_END

LINKS_START
- Buy Wyndham Points | https://www.wyndhamrewards.com/buy-points
- Wyndham Hotel Search | https://www.wyndham.com
LINKS_END

<p>If you've been eyeing a Wyndham property...</p>

- Do NOT link to source news articles (TPG, OMAAT, etc.). DO link to actual deals, products, and official pages.
- For ROUNDUP articles covering multiple topics: use <hr class="topic-break"> between each topic section to create clear visual separation. Each topic should have its own <h3> heading.`

// DealAlertPrompt builds a prompt for a deal-alert post written in a natural,
// engaging style with no source citations in the body.
func DealAlertPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points deal alert writer.

Given the following source articles, extract the key details:
- Loyalty program name
- Bonus amount / transfer ratio
- Start and end dates
- Any restrictions or conditions

Write an engaging deal-alert post that makes readers excited about this opportunity.
%s
%s
Sources (for your reference only — do NOT cite these in the article):
%s
Write the deal alert post now.`, writingStyle, htmlFormat, formatSources(sources))
}

// RoundupPrompt builds a prompt for a roundup article with natural writing.
func RoundupPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points roundup writer.

Given the following source articles, cover each major topic in its own clearly separated section.
Each topic gets an <h3> heading followed by its content, then an <hr class="topic-break"> before the next topic.
End with a brief wrap-up paragraph (no <hr> before the wrap-up).
%s
%s
Sources (for your reference only — do NOT cite these in the article):
%s
Write the roundup post now.`, writingStyle, htmlFormat, formatSources(sources))
}

// SummaryPrompt builds a prompt for a summary article with personality.
func SummaryPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points summary writer.

Given the following source articles, synthesize the information into a balanced, insightful summary.
Add your own analysis and practical advice for readers.
%s
%s
Sources (for your reference only — do NOT cite these in the article):
%s
Write the summary post now.`, writingStyle, htmlFormat, formatSources(sources))
}

// CriticPrompt builds a comprehensive verification prompt. The critic checks
// factual accuracy, content quality, readability, and HTML validity.
func CriticPrompt(generatedArticle string, sources []SourceArticle) string {
	return fmt.Sprintf(`You are a thorough editorial critic for a miles-and-points website.

Review the following generated article against the original source articles. You must verify EVERYTHING.

VERIFICATION CHECKLIST:

*** MOST IMPORTANT CHECK — DO THIS FIRST ***
1. HALLUCINATION — Check if the article contains MAJOR fabricated claims: entire topics, deals, or programs that no source discusses. Minor issues are NOT hallucinations:
   - HALLUCINATION (fail): Inventing an entire section about "Chase card changes" when no source mentions Chase
   - HALLUCINATION (fail): Adding specific numbers ($100 fee, 50K bonus) not found in any source
   - NOT hallucination (pass): Creative writing phrases, idioms, or witty asides ("Rome wasn't built in a day")
   - NOT hallucination (pass): A fact mentioned in at least ONE source, even if not in all sources
   - NOT hallucination (pass): Saying "next year" when a source says "2027" (same meaning in context)
   - NOT hallucination (pass): General practical advice ("check your account", "act quickly")
   Only set hallucination_check to false for genuinely fabricated factual claims.

2. FACTUAL ACCURACY — For claims that ARE supported by sources, do the specific numbers, dates, and details match exactly?
3. COMPLETENESS — Are the most important details from the sources included?
4. READABILITY — Does the article read naturally? Does it flow well? Is it engaging and not robotic? Does the tone feel like a real person wrote it?
5. HTML VALIDITY — Is the output clean HTML? No Markdown artifacts (**, ##, []() links)? Are tags properly opened and closed?
6. SOURCE LEAKAGE — Does the article link to or mention source news sites (TPG, OMAAT, Doctor of Credit, View from the Wing, FlyerTalk, Frequent Miler) in the body? It should NOT, unless reporting an unconfirmed claim. Links to thepointsguy.com, onemileatatime.com, doctorofcredit.com, viewfromthewing.com are ALWAYS wrong.
7. LINK CHECK — The article should NOT link to source news articles. It MAY link to actual deals, products, or official pages (amazon.com, chase.com, united.com, etc.). Check every <a href> tag: if it points to a source news site, that's a FAILURE.
8. COHERENCE — Does the article make logical sense from start to finish? Are there any contradictions, non-sequiturs, or confusing passages?

Generated Article:
%s

Original Sources:
%s

Return ONLY a JSON object (no other text):
{"score": <0-100>, "issues": [<array of specific issue strings>], "factual_check": <bool>, "attribution_check": <bool>, "completeness_check": <bool>, "hallucination_check": <bool>}

Scoring guide:
- 90-100: Publish-ready. ALL four checks must be true. Every claim verified against sources. No extra content.
- 75-89: Minor issues. At most ONE check false (not hallucination). Small factual gaps only.
- 50-74: Significant issues. Missing key facts, unverified claims, or minor hallucinations.
- Below 50: HALLUCINATION DETECTED or major factual errors. Article contains claims not in any source.

CRITICAL RULES:
- If hallucination_check is false, score MUST be below 50. Period. No exceptions.
- If any other check is false, score MUST be below 90.
- If two or more checks are false, score MUST be below 75.
- An article that invents deals, programs, or facts not in the sources is NEVER above 50.`, generatedArticle, formatSources(sources))
}

// Critic2Prompt is a reasoning-focused second critic that checks logical
// consistency, common sense, and coherence. It runs after the first critic
// and catches issues that require deeper reasoning.
func Critic2Prompt(generatedArticle string, sources []SourceArticle) string {
	return fmt.Sprintf(`You are a reasoning-focused editor reviewing a miles-and-points article.

Your job is to find LOGICAL INCONSISTENCIES and COMMON SENSE ERRORS that a surface-level review would miss.

Read the article carefully and check:

1. LOGICAL CONSISTENCY — Does every statement logically follow? Examples of failures:
   - Saying a regional jet (CRJ, ERJ) can fly transatlantic routes (it can't — range is ~1500 miles)
   - Saying a domestic-only program works for international bookings
   - Contradicting earlier statements in the same article
   - Claiming a card has benefits that don't match its known tier

2. GEOGRAPHIC/AVIATION COMMON SENSE — Are routes, aircraft, and destinations plausible?
   - Can the mentioned aircraft actually fly the mentioned routes?
   - Are the cities paired with the right airports/airlines?
   - Do the distances make sense for the aircraft type?

3. NUMERICAL CONSISTENCY — Do the numbers add up?
   - If 40%% bonus is applied to 100K points, the result should be 140K not something else
   - Are "starting at" prices consistent with "up to" claims?

4. TEMPORAL CONSISTENCY — Do dates and timeframes make sense?
   - Are sale dates in the future (not past)?
   - Do "limited time" claims have reasonable durations?

5. TONE AND CLAIMS — Does the article make promises or claims beyond what's reasonable?
   - Does it overstate the value of a deal?
   - Does it recommend specific actions without enough information?

Article to review:
%s

Original sources for reference:
%s

Return ONLY a JSON object (no other text):
{"score": <0-100>, "issues": [<array of specific issue strings>], "factual_check": <bool>, "attribution_check": <bool>, "completeness_check": <bool>, "hallucination_check": <bool>}

Scoring:
- 90-100: Logically sound, no inconsistencies.
- 70-89: Minor nits but no logical errors.
- Below 70: Contains logical contradictions or common sense failures.
- Below 50: Major logical errors that would embarrass the publication.`, generatedArticle, formatSources(sources))
}

// ImageScoringPrompt builds a prompt for scoring image candidates for relevance
// to an article. It asks the AI to score each candidate 1-10.
func ImageScoringPrompt(articleTitle, articleTLDR string, candidates []string) string {
	var b strings.Builder
	b.WriteString("You are an image editor selecting the best image for a miles-and-points article.\n\n")
	b.WriteString(fmt.Sprintf("Article Title: %s\n", articleTitle))
	b.WriteString(fmt.Sprintf("Article Summary: %s\n\n", articleTLDR))
	b.WriteString("Below are candidate images. Score each one from 1 to 10 for relevance to this specific article topic.\n\n")
	b.WriteString("SCORING GUIDELINES:\n")
	b.WriteString("- 9-10: Directly shows the specific subject (e.g., the actual airline, hotel, or credit card mentioned)\n")
	b.WriteString("- 7-8: Closely related (e.g., an airport for an airline deal, a travel scene for a points article)\n")
	b.WriteString("- 5-6: Somewhat related (e.g., generic travel imagery for a specific deal)\n")
	b.WriteString("- 1-4: Barely related or irrelevant\n")
	b.WriteString("- PREFER specific images over generic ones: an actual airline plane > generic airport, a specific hotel > generic building\n\n")
	b.WriteString("Candidates:\n")
	for _, c := range candidates {
		b.WriteString(c + "\n")
	}
	b.WriteString("\nReturn ONLY a JSON object (no other text):\n")
	b.WriteString(`{"scores": [{"index": 0, "score": 8, "reason": "Shows the specific airline mentioned"}, ...]}`)
	b.WriteString("\n")
	return b.String()
}

// ExtractDetailsPrompt asks the AI to extract key details and links from an article.
func ExtractDetailsPrompt(title, content string) string {
	truncated := content
	if len(truncated) > 2000 {
		truncated = truncated[:2000]
	}
	return fmt.Sprintf(`Extract key details and useful links from this article. Return ONLY a JSON object.

Article Title: %s
Article Content:
%s

Return this exact JSON format (no other text):
{
  "key_details": [
    {"label": "Bonus", "value": "100%% extra points"},
    {"label": "Deadline", "value": "May 1, 2026"},
    {"label": "Cost", "value": "$95 annual fee"}
  ],
  "links": [
    {"text": "Apply Now", "url": "https://example.com/apply"},
    {"text": "Program Details", "url": "https://example.com/details"}
  ]
}

Rules:
- key_details: 3-6 bullet points with the most important facts (bonus amounts, deadlines, costs, requirements, eligibility)
- links: 2-4 links to actual deal pages, booking sites, or official program pages found in the article content. Extract real URLs from the HTML <a href> tags. Do NOT invent URLs.
- If the article has no links, return an empty links array.`, title, truncated)
}

// ArticleTagsPrompt builds a prompt that asks the AI to pick 1-3 topic tags
// from a predefined list based on the article's title and content.
func ArticleTagsPrompt(title, content string) string {
	truncated := content
	if len(truncated) > 2000 {
		truncated = truncated[:2000]
	}
	return fmt.Sprintf(`Given this article, pick 1-3 topic tags from the list below that best describe it.

ALLOWED TAGS (pick ONLY from this list):
airlines, hotels, credit-cards, transfer-bonuses, points-deals, travel-deals, loyalty-programs, bank-bonuses

Article Title: %s
Article Content:
%s

Return ONLY a JSON array of 1-3 tags, nothing else. Example: ["credit-cards", "points-deals"]`, title, truncated)
}

// ScoutsTakePrompt builds a prompt for generating a short, opinionated take
// on an article. The persona is practical, direct, and slightly opinionated.
func ScoutsTakePrompt(personaName, title, tldr, content string) string {
	truncated := content
	if len(truncated) > 2000 {
		truncated = truncated[:2000]
	}
	return fmt.Sprintf(`You are %s, a practical and direct miles-and-points advisor. You give honest, slightly opinionated takes on travel news and deals.

Given this article, write a 1-2 sentence take that captures your honest opinion. Be practical and direct. Tell readers whether this matters and what they should do about it. Do not be generic — reference specific details from the article.

Do NOT start with "Look," or "Listen," or any attention-grabbing opener. Just state your take directly.

Article Title: %s
Summary: %s
Content:
%s

Return ONLY your 1-2 sentence take, nothing else. No quotes, no labels, no prefix.`, personaName, title, tldr, truncated)
}

// CardUpdateDetectionPrompt builds a prompt that asks the AI whether a newly
// approved article warrants an update to a specific section of a credit card
// reference page. The AI returns either the literal string NO_UPDATE or a JSON
// object with "updated_content" and "summary" keys.
func CardUpdateDetectionPrompt(articleTitle, articleContent, cardTitle, sectionTitle, currentSectionContent string) string {
	truncatedArticle := articleContent
	if len(truncatedArticle) > 2000 {
		truncatedArticle = truncatedArticle[:2000]
	}
	truncatedSection := currentSectionContent
	if len(truncatedSection) > 1000 {
		truncatedSection = truncatedSection[:1000]
	}
	return fmt.Sprintf(`You are a credit card reference page editor. A new article has been published that may affect a credit card's reference page.

Determine whether the section below needs to be updated based on the article content. If the article contains relevant new information for this section, return a JSON object with the updated content and a short summary of the change. If the article is NOT relevant to this section, return exactly: NO_UPDATE

Article Title: %s
Article Content:
%s

Card: %s
Section: %s
Current Section Content:
%s

If an update is needed, return ONLY a JSON object (no other text):
{"updated_content": "<the full updated section content in HTML>", "summary": "<1 sentence describing what changed>"}

If no update is needed, return ONLY: NO_UPDATE`, articleTitle, truncatedArticle, cardTitle, sectionTitle, truncatedSection)
}

// RevisionPrompt builds a prompt for revising an article based on feedback.
func RevisionPrompt(sources []SourceArticle, previousOutput, feedback, criticIssues string) string {
	return fmt.Sprintf(`You are a miles-and-points content editor revising an article.

The admin has requested the following change. Implement EXACTLY what they asked — nothing more, nothing less.

=== ADMIN FEEDBACK ===
%s
=== END FEEDBACK ===

IMPORTANT: Only make the changes the admin requested. Do not add editor notes, do not add warnings, do not add disclaimers unless the admin specifically asked for them. If the admin asked for a link and you can find a URL in the sources, add it as: <a href="URL" target="_blank" rel="noopener">descriptive text</a>. If you cannot find a URL, just leave the text as-is without adding any notes about missing links.

Also fix any critic issues if present.
%s
%s
Previous Article:
%s

Critic Issues:
%s

Source article URLs (can be used as links if no better URL is available):
%s

Sources (for your reference only):
%s
Write the revised article now.`, feedback, writingStyle, htmlFormat, previousOutput, criticIssues, formatSourceURLs(sources), formatSources(sources))
}
