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

// DealAlertPrompt builds a prompt that instructs the model to extract key
// details (program, bonus, dates) and write a concise deal-alert post with
// inline citations [SourceName].
func DealAlertPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points deal alert writer.

Given the following source articles, extract the key details:
- Loyalty program name
- Bonus amount / transfer ratio
- Start and end dates
- Any restrictions or conditions

Write a concise deal-alert post. Use inline citations like [SourceName] after every factual claim.

Sources:
%s

Write the deal alert post now. Start with a title on the first line.`, formatSources(sources))
}

// RoundupPrompt builds a prompt for a weekly roundup article that compares,
// ranks, and cross-references multiple deals.
func RoundupPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points roundup writer.

Given the following source articles, compare and rank the deals discussed.
Cross-reference information across sources. Use inline citations like [SourceName] after every factual claim.

Sources:
%s

Write the roundup post now. Start with a title on the first line.`, formatSources(sources))
}

// SummaryPrompt builds a prompt that synthesizes multiple perspectives into a
// balanced summary article.
func SummaryPrompt(sources []SourceArticle) string {
	return fmt.Sprintf(`You are a miles-and-points summary writer.

Given the following source articles, synthesize the different perspectives into a balanced summary.
Present all viewpoints fairly. Use inline citations like [SourceName] after every factual claim.

Sources:
%s

Write the summary post now. Start with a title on the first line.`, formatSources(sources))
}

// CriticPrompt builds a prompt that instructs the model to verify a generated
// article against its sources. The model must return a JSON object with score,
// issues, and boolean check fields.
func CriticPrompt(generatedArticle string, sources []SourceArticle) string {
	return fmt.Sprintf(`You are a fact-checking critic for miles-and-points content.

Verify the following generated article against the original source articles.

Check for:
1. Factual accuracy — do all claims match the sources?
2. Attribution — is every claim properly cited with [SourceName]?
3. Completeness — are key details from the sources included?
4. Hallucination — does the article contain information NOT in the sources?
5. Orphan citations — are there citations to sources that were not provided?

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
Keep all source citations intact and address every feedback point.

Previous Article:
%s

Feedback:
%s

Critic Issues:
%s

Original Sources:
%s

Write the revised article now. Start with a title on the first line.`, previousOutput, feedback, criticIssues, formatSources(sources))
}
