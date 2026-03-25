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

const htmlInstructions = `
IMPORTANT OUTPUT FORMAT RULES:
- Output HTML only. Do NOT use Markdown syntax (no **, no ##, no [] links).
- Use <strong> for bold, <em> for italic, <h3> for subheadings, <ul>/<li> for lists, <p> for paragraphs.
- For source citations, use HTML links: <a href="SOURCE_URL">[Source Name]</a>
- The first line must be the article title as plain text (no HTML tags, no ** markers).
- After the title line, write the full article body in HTML.`

// DealAlertPrompt builds a prompt for a concise deal-alert post with HTML
// source citations.
func DealAlertPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points deal alert writer.

Given the following source articles, extract the key details:
- Loyalty program name
- Bonus amount / transfer ratio
- Start and end dates
- Any restrictions or conditions

Write a concise deal-alert post with inline source citations linking to the original articles.

Source link mapping:
%s

Sources:
%s
%s
Write the deal alert post now.`, formatSourceLinks(sources), formatSources(sources), htmlInstructions)
}

// RoundupPrompt builds a prompt for a roundup article that compares, ranks,
// and cross-references multiple deals with HTML links.
func RoundupPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points roundup writer.

Given the following source articles, compare and rank the deals discussed.
Cross-reference information across sources with inline citations linking to the originals.

Source link mapping:
%s

Sources:
%s
%s
Write the roundup post now.`, formatSourceLinks(sources), formatSources(sources), htmlInstructions)
}

// SummaryPrompt builds a prompt that synthesizes multiple perspectives into a
// balanced summary article with HTML source links.
func SummaryPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points summary writer.

Given the following source articles, synthesize the different perspectives into a balanced summary.
Present all viewpoints fairly with inline citations linking to the originals.

Source link mapping:
%s

Sources:
%s
%s
Write the summary post now.`, formatSourceLinks(sources), formatSources(sources), htmlInstructions)
}

// CriticPrompt builds a prompt that instructs the model to verify a generated
// article against its sources, returning a JSON verdict.
func CriticPrompt(generatedArticle string, sources []SourceArticle) string {
	return fmt.Sprintf(`You are a fact-checking critic for miles-and-points content.

Verify the following generated article against the original source articles.

Check for:
1. Factual accuracy — do all numbers, dates, and claims match the sources?
2. Attribution — is every factual claim cited with an HTML link <a href="...">[Source]</a>? Plain text [Source] without a link is a FAILURE.
3. Completeness — are key details from the sources included?
4. Hallucination — does the article contain information NOT in the sources?
5. Orphan citations — are there citations to sources that were not provided?
6. HTML format — is the content valid HTML (not Markdown)? Are there any ** markers or ## headings that should be HTML tags?

Generated Article:
%s

Original Sources:
%s

Return ONLY a JSON object with this exact structure (no other text):
{"score": <0-100>, "issues": [<array of issue strings>], "factual_check": <bool>, "attribution_check": <bool>, "completeness_check": <bool>, "hallucination_check": <bool>}`, generatedArticle, formatSources(sources))
}

// RevisionPrompt builds a prompt that instructs the model to revise a
// previously generated article based on critic feedback.
func RevisionPrompt(sources []SourceArticle, previousOutput, feedback, criticIssues string) string {
	return fmt.Sprintf(`You are a miles-and-points content editor.

Revise the following article based on the feedback and issues listed below.
Address every feedback point. Ensure all citations are HTML links.

Previous Article:
%s

Feedback:
%s

Critic Issues:
%s

Source link mapping:
%s

Original Sources:
%s
%s
Write the revised article now.`, previousOutput, feedback, criticIssues, formatSourceLinks(sources), formatSources(sources), htmlInstructions)
}
