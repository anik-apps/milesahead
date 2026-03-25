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
- NO source citations or links in the article body. Do not mention source names (TPG, OMAAT, etc.) UNLESS the article is about an unconfirmed report from a specific site.
  WRONG: "American Express is offering a 40% bonus [TPG]."
  WRONG: "According to The Points Guy, there is a 40% bonus."
  RIGHT: "American Express is offering a 40% transfer bonus to ANA through March 31."
  EXCEPTION (unconfirmed): "The Points Guy is reporting that Chase may change Sapphire Reserve benefits in April — take this with a grain of salt until there's official confirmation."
`

const htmlFormat = `
OUTPUT FORMAT:
- Output HTML only. No Markdown (no **, no ##, no [text](url)).
- Use <strong> for bold, <em> for italic, <h3> for subheadings, <ul>/<li> for lists, <p> for paragraphs.
- The first line must be the article title as plain text (no HTML tags, no formatting).
- After the title line, write the full article body in HTML.
- Do NOT include any source links or citation markers in the output.`

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

Given the following source articles, compare and rank the deals discussed.
Synthesize the information into one cohesive, entertaining article.
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
1. FACTUAL ACCURACY — Does every number, date, percentage, program name, and specific claim in the article match what the sources say? Flag ANY discrepancy, even small ones.
2. COMPLETENESS — Are the most important details from the sources included? Did the article miss anything a reader would need to know?
3. HALLUCINATION — Does the article contain ANY information, claims, or details that are NOT supported by the provided sources? This includes made-up dates, invented restrictions, or assumed conditions.
4. READABILITY — Does the article read naturally? Does it flow well? Is it engaging and not robotic? Does the tone feel like a real person wrote it?
5. HTML VALIDITY — Is the output clean HTML? No Markdown artifacts (**, ##, []() links)? Are tags properly opened and closed?
6. SOURCE LEAKAGE — Does the article mention source names (TPG, OMAAT, Doctor of Credit, etc.) in the body? It should NOT, unless reporting an unconfirmed claim from a specific site.
7. LINK CHECK — The article should NOT contain any <a href> links or citation markers in the body. The only exception is if reporting an unconfirmed claim attributed to a specific source.
8. COHERENCE — Does the article make logical sense from start to finish? Are there any contradictions, non-sequiturs, or confusing passages?

Generated Article:
%s

Original Sources:
%s

Return ONLY a JSON object (no other text):
{"score": <0-100>, "issues": [<array of specific issue strings>], "factual_check": <bool>, "attribution_check": <bool>, "completeness_check": <bool>, "hallucination_check": <bool>}

Scoring guide:
- 90-100: Publish-ready. ALL four checks must be true. All facts verified, reads great, no issues.
- 70-89: Minor issues. At most ONE check can be false. Small gaps but fundamentally sound.
- 50-69: Significant issues. Two or more checks are false. Missing key facts or unverified claims.
- Below 50: Major problems. Hallucinations, factual errors, or incoherent writing.

CRITICAL: If any check is false, the score MUST be below 90. If two or more checks are false, the score MUST be below 70. Do NOT give a high score with failed checks.`, generatedArticle, formatSources(sources))
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
