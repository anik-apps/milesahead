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

func formatSourceLinks(sources []SourceArticle) string {
	var parts []string
	for _, s := range sources {
		parts = append(parts, fmt.Sprintf("[%s] = %s", s.Name, s.URL))
	}
	return strings.Join(parts, "\n")
}

const writingStyle = `
WRITING STYLE — THIS IS CRITICAL:
- Write like a knowledgeable friend giving practical advice. Calm, informative, matter-of-fact.
- DO NOT sound like a salesman. Never call things "incredible", "fantastic", "amazing", "exciting", or "deal of a lifetime."
- Instead, be practical: "If you've been sitting on Amex points and planning a trip to Japan, now is a good time to transfer."
  "This is worth considering if you're in the market for a new travel card." "Here's what changed and what it means for your points."
- You CAN use the occasional witty aside, idiom, or light humor — but sparingly. One per article, max.
  Example: "Rome wasn't built in a day, but your award ticket to Rome can be booked in about five minutes."
- Vary sentence length. Be direct. Cut filler words.
- Use "you" and "your" to speak directly to the reader.
- Focus on practical implications: what should the reader DO with this information?
- Avoid robotic phrases: "In this article", "It is worth noting", "According to sources", "Let's dive in".
- NEVER invent, fabricate, or add information not present in the provided sources. If the sources only discuss one deal, write about THAT deal only. Do not pad the article with other topics, programs, or deals you happen to know about. Stick strictly to what the sources say.
- NO source article links or citation markers. Do not link to TPG, OMAAT, Doctor of Credit, etc.
  Do not mention source site names UNLESS reporting an unconfirmed claim from a specific site.
  WRONG: "American Express is offering a 40% bonus [TPG]."
  WRONG: "According to The Points Guy, there is a 40% bonus."
  WRONG: <a href="https://thepointsguy.com/...">link</a>
  RIGHT: "American Express is offering a 40% transfer bonus to ANA through March 31."
  EXCEPTION (unconfirmed): "The Points Guy is reporting that Chase may change Sapphire Reserve benefits in April — take this with a grain of salt until there's official confirmation."
- You SHOULD include links to the actual deal, product, or official page when helpful:
  RIGHT: <a href="https://www.amazon.com/events/bigspringsale">Amazon Big Spring Sale</a>
  RIGHT: <a href="https://creditcards.chase.com/rewards-credit-cards/sapphire/preferred">Chase Sapphire Preferred</a>
  RIGHT: <a href="https://www.united.com">book on United.com</a>
  These are links to the actual content/product, NOT to the news article that reported on it.
`

const htmlFormat = `
OUTPUT FORMAT:
- Output HTML only. No Markdown (no **, no ##, no [text](url)).
- Use <strong> for bold, <em> for italic, <h3> for subheadings, <ul>/<li> for lists, <p> for paragraphs.
- The first line must be the article title as plain text (no HTML tags, no formatting).
- After the title line, write the full article body in HTML.
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
1. HALLUCINATION — Go through EVERY claim, fact, program name, product, deal, and detail in the article. For EACH one, find the specific source that supports it. If you cannot find a source for a claim, it is HALLUCINATED. Common hallucinations include:
   - Mentioning deals, cards, or programs NOT discussed in any source
   - Adding sections about topics NOT covered by any source
   - Inventing specific numbers, dates, fees, or benefits not in the sources
   - Creating "United summer sale" or "Chase card changes" when no source mentions these
   If ANY hallucination is found, set hallucination_check to false and score MUST be below 50.

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

// RevisionPrompt builds a prompt for revising an article based on feedback.
func RevisionPrompt(sources []SourceArticle, previousOutput, feedback, criticIssues string) string {
	return fmt.Sprintf(`You are a miles-and-points content editor revising an article.

Fix every issue listed below. Keep the engaging, human tone.
%s
%s
Previous Article:
%s

Admin Feedback:
%s

Critic Issues:
%s

Sources (for your reference only — do NOT cite these in the article):
%s
Write the revised article now.`, writingStyle, htmlFormat, previousOutput, feedback, criticIssues, formatSources(sources))
}
