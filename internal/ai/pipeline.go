package ai

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// KeyDetail represents a single key detail extracted from structured output.
type KeyDetail struct {
	Label string
	Value string
}

// ArticleLink represents a link extracted from structured output.
type ArticleLink struct {
	Text string
	URL  string
}

// PipelineResult holds the output of a full generate-then-verify cycle.
type PipelineResult struct {
	Content         string
	Title           string
	TLDR            string
	KeyDetails      []KeyDetail
	Links           []ArticleLink
	Type            string
	Verification    *VerificationResult
	PassedThreshold bool
}

// Pipeline orchestrates the generator and two critics to produce verified articles.
// Critic1 checks factual accuracy and source compliance.
// Critic2 checks logical consistency and common sense.
type Pipeline struct {
	generator *Generator
	critic    *Critic
	critic2   *Critic
	threshold int
}

// NewPipeline creates a new Pipeline with the given quality threshold (0-100).
// critic2 can be nil for single-critic mode (backwards compatible).
func NewPipeline(generator *Generator, critic *Critic, threshold int) *Pipeline {
	return &Pipeline{generator: generator, critic: critic, threshold: threshold}
}

// SetCritic2 adds a second critic for reasoning/consistency checks.
func (p *Pipeline) SetCritic2(critic2 *Critic) {
	p.critic2 = critic2
}

// ErrNotEnoughSources is returned when a cluster has fewer than 2 unique sources.
var ErrNotEnoughSources = fmt.Errorf("need at least 2 different sources to generate an article")

// Process generates an article, verifies it, and if the critic finds issues
// but the score is above threshold, auto-revises once before finalizing.
// Requires at least 2 sources from different sites to reduce hallucination risk.
func (p *Pipeline) Process(articleType string, sources []SourceArticle) (*PipelineResult, error) {
	// Require multiple sources from different sites
	uniqueSources := make(map[string]bool)
	for _, s := range sources {
		uniqueSources[s.Name] = true
	}
	if len(uniqueSources) < 2 {
		return nil, ErrNotEnoughSources
	}

	// Auto-detect article type if not specified
	if articleType == "" || articleType == "auto" {
		articleType = detectArticleType(sources)
	}

	slog.Info("article generated", "type", articleType, "sources", len(sources))

	content, err := p.generator.Generate(articleType, sources)
	if err != nil {
		return nil, err
	}

	content = cleanMarkdown(content)

	// Critic reviews the article
	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
	}
	verification.Score = adjustScore(verification)
	metrics.CriticScore.Observe(float64(verification.Score))

	slog.Info("critic scored", "score", verification.Score, "passed", verification.Score >= p.threshold, "issues", len(verification.Issues))

	// If issues found, generator revises, then critic reviews again
	if len(verification.Issues) > 0 {
		slog.Info("auto-revision triggered", "issues_count", len(verification.Issues))
		issuesStr := strings.Join(verification.Issues, "\n- ")
		revised, err := p.generator.GenerateWithFeedback(sources, content,
			"Fix ALL of the following issues. Do not add any information not in the original sources. Only state facts that are directly in the sources.",
			issuesStr)
		if err == nil && revised != "" {
			revised = cleanMarkdown(revised)
			v2, err := p.critic.Verify(revised, sources)
			if err == nil {
				v2.Score = adjustScore(v2)
				if v2.Score >= verification.Score {
					content = revised
					verification = v2
				}
			}
		}
	}

	parsedTitle, tldr, keyDetails, links, parsedBody := parseStructuredContent(content)
	if parsedTitle == "" {
		parsedTitle = extractTitle(content)
	}
	if parsedBody == "" {
		content = formatContent(content)
	} else {
		content = parsedBody
	}

	return &PipelineResult{
		Content:         content,
		Title:           parsedTitle,
		TLDR:            tldr,
		KeyDetails:      keyDetails,
		Links:           links,
		Type:            articleType,
		Verification:    verification,
		PassedThreshold: verification.Score >= p.threshold,
	}, nil
}

// ProcessWithFeedback revises a previously generated article using feedback,
// then verifies the revision.
func (p *Pipeline) ProcessWithFeedback(sources []SourceArticle, previousOutput, feedback, criticIssues string) (*PipelineResult, error) {
	content, err := p.generator.GenerateWithFeedback(sources, previousOutput, feedback, criticIssues)
	if err != nil {
		return nil, err
	}

	return p.buildResult(content, "", sources)
}

func (p *Pipeline) buildResult(content, articleType string, sources []SourceArticle) (*PipelineResult, error) {
	content = cleanMarkdown(content)

	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
	}
	verification.Score = adjustScore(verification)
	metrics.CriticScore.Observe(float64(verification.Score))

	parsedTitle, tldr, keyDetails, links, parsedBody := parseStructuredContent(content)
	if parsedTitle == "" {
		parsedTitle = extractTitle(content)
	}
	if parsedBody == "" {
		content = formatContent(content)
	} else {
		content = parsedBody
	}

	return &PipelineResult{
		Content:         content,
		Title:           parsedTitle,
		TLDR:            tldr,
		KeyDetails:      keyDetails,
		Links:           links,
		Type:            articleType,
		Verification:    verification,
		PassedThreshold: verification.Score >= p.threshold,
	}, nil
}

// parseStructuredContent extracts sections from the generator's structured output.
func parseStructuredContent(raw string) (title, tldr string, keyDetails []KeyDetail, links []ArticleLink, body string) {
	lines := strings.Split(raw, "\n")

	// Title: first non-empty line
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" {
			title = t
			break
		}
	}

	// TLDR: line starting with "TLDR:"
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "TLDR:") {
			tldr = strings.TrimSpace(strings.TrimPrefix(t, "TLDR:"))
			break
		}
	}

	// Key details: between KEY_DETAILS_START and KEY_DETAILS_END
	inDetails := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "KEY_DETAILS_START" {
			inDetails = true
			continue
		}
		if t == "KEY_DETAILS_END" {
			inDetails = false
			continue
		}
		if inDetails && strings.HasPrefix(t, "- ") {
			item := strings.TrimPrefix(t, "- ")
			if idx := strings.Index(item, ": "); idx != -1 {
				keyDetails = append(keyDetails, KeyDetail{
					Label: strings.TrimSpace(item[:idx]),
					Value: strings.TrimSpace(item[idx+2:]),
				})
			}
		}
	}

	// Links: between LINKS_START and LINKS_END
	inLinks := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "LINKS_START" {
			inLinks = true
			continue
		}
		if t == "LINKS_END" {
			inLinks = false
			continue
		}
		if inLinks && strings.HasPrefix(t, "- ") {
			item := strings.TrimPrefix(t, "- ")
			if idx := strings.Index(item, " | "); idx != -1 {
				links = append(links, ArticleLink{
					Text: strings.TrimSpace(item[:idx]),
					URL:  strings.TrimSpace(item[idx+3:]),
				})
			}
		}
	}

	// Body: everything after LINKS_END
	foundLinksEnd := false
	var bodyLines []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "LINKS_END" {
			foundLinksEnd = true
			continue
		}
		if foundLinksEnd {
			bodyLines = append(bodyLines, line)
		}
	}
	body = strings.TrimSpace(strings.Join(bodyLines, "\n"))

	// If no LINKS_END was found, fall back to everything after the title
	if !foundLinksEnd {
		body = raw
		if idx := strings.Index(raw, "\n"); idx != -1 {
			body = strings.TrimSpace(raw[idx+1:])
		}
	}

	return
}

// adjustScore enforces consistency between the score and the check results.
// Each failed check deducts points, and the score is capped accordingly.
func adjustScore(v *VerificationResult) int {
	score := v.Score
	failCount := 0
	if !v.FactualCheck {
		failCount++
	}
	if !v.AttributionCheck {
		failCount++
	}
	if !v.CompletenessCheck {
		failCount++
	}
	if !v.HallucinationCheck {
		failCount++
	}

	// Hallucination is the most severe — cap at 60
	if !v.HallucinationCheck {
		if score > 60 {
			score = 60
		}
	}

	// Each other failed check deducts 10 points
	otherFails := failCount
	if !v.HallucinationCheck {
		otherFails--
	}
	score -= otherFails * 10

	// Cap: can't be above 85 with any failed check, can't be above 70 with 2+ failures
	if failCount > 0 && score > 85 {
		score = 85
	}
	if failCount >= 2 && score > 70 {
		score = 70
	}
	if score < 0 {
		score = 0
	}

	return score
}

// detectArticleType determines the article type from source content.
// Deal: time-limited offers (bonuses, sales, discounts, signup offers)
// Summary: news, changes, analysis, reviews
func detectArticleType(sources []SourceArticle) string {
	dealKeywords := []string{"bonus", "sale", "discount", "offer", "deal", "save", "off", "promotion", "sign-up", "signup", "limited time", "flash", "promo"}
	dealScore := 0
	for _, s := range sources {
		lower := strings.ToLower(s.Title + " " + s.Content[:min(200, len(s.Content))])
		for _, kw := range dealKeywords {
			if strings.Contains(lower, kw) {
				dealScore++
			}
		}
	}
	if dealScore >= 2 {
		return "deal"
	}
	return "summary"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// combinedCritique runs both critics and merges their results.
func (p *Pipeline) combinedCritique(content string, sources []SourceArticle) *VerificationResult {
	// Critic 1: factual accuracy, source compliance
	v1, err := p.critic.Verify(content, sources)
	if err != nil {
		// If critic 1 fails, return a failing score
		return &VerificationResult{Score: 0, Issues: []string{"critic 1 error: " + err.Error()}}
	}
	v1.Score = adjustScore(v1)

	// Critic 2: reasoning, consistency (if configured)
	if p.critic2 == nil {
		return v1
	}

	v2, err := p.critic2.Verify2(content, sources)
	if err != nil {
		// If critic 2 fails, just use critic 1 results
		return v1
	}
	v2.Score = adjustScore(v2)

	// Merge: take the lower score, combine issues, AND any failed checks
	merged := &VerificationResult{
		Score:              v1.Score,
		Issues:             append(v1.Issues, v2.Issues...),
		FactualCheck:       v1.FactualCheck && v2.FactualCheck,
		AttributionCheck:   v1.AttributionCheck && v2.AttributionCheck,
		CompletenessCheck:  v1.CompletenessCheck && v2.CompletenessCheck,
		HallucinationCheck: v1.HallucinationCheck && v2.HallucinationCheck,
	}
	if v2.Score < merged.Score {
		merged.Score = v2.Score
	}
	merged.Score = adjustScore(merged)
	return merged
}

// cleanMarkdown converts common Markdown remnants to HTML.
func cleanMarkdown(s string) string {
	// Convert lines of dashes/equals (section dividers) to <hr>
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Lines that are mostly dashes or equals (e.g. "---", "====", "----------")
		if len(trimmed) >= 3 {
			allDashes := true
			for _, c := range trimmed {
				if c != '-' && c != '=' && c != ' ' {
					allDashes = false
					break
				}
			}
			if allDashes {
				lines[i] = `<hr class="topic-break">`
			}
		}
		// Also catch inline dashes like "Topic -------------------------------- Content"
		if strings.Contains(lines[i], "--------") {
			parts := strings.SplitN(lines[i], "--------", 2)
			before := strings.TrimSpace(parts[0])
			after := ""
			if len(parts) > 1 {
				// Remove any remaining dashes from the second part
				cleaned := strings.TrimLeft(parts[1], "-")
				after = strings.TrimSpace(cleaned)
			}
			result := ""
			if before != "" {
				result += before + "\n"
			}
			result += `<hr class="topic-break">`
			if after != "" {
				result += "\n" + after
			}
			lines[i] = result
		}
	}
	s = strings.Join(lines, "\n")

	// Convert **bold** to <strong>bold</strong>
	for strings.Contains(s, "**") {
		start := strings.Index(s, "**")
		rest := s[start+2:]
		end := strings.Index(rest, "**")
		if end == -1 {
			break
		}
		s = s[:start] + "<strong>" + rest[:end] + "</strong>" + rest[end+2:]
	}
	// Convert ## Heading to <h3>Heading</h3>
	// Convert * bullet and - bullet to <li>bullet</li>
	lines = strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "## ") + "</h3>"
		} else if strings.HasPrefix(trimmed, "### ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "### ") + "</h3>"
		} else if strings.HasPrefix(trimmed, "* ") {
			lines[i] = "<li>" + strings.TrimPrefix(trimmed, "* ") + "</li>"
		} else if strings.HasPrefix(trimmed, "- ") {
			lines[i] = "<li>" + strings.TrimPrefix(trimmed, "- ") + "</li>"
		}
	}
	return strings.Join(lines, "\n")
}

// formatContent transforms plain-text content into proper HTML with paragraphs,
// headings, and lists. It runs after cleanMarkdown in the pipeline.
func formatContent(content string) string {
	// If already has HTML paragraph tags, skip formatting
	if strings.Contains(content, "<p>") {
		return content
	}

	// Remove the title line (first line is extracted separately)
	if idx := strings.Index(content, "\n"); idx != -1 {
		content = strings.TrimSpace(content[idx+1:])
	} else {
		// Content is just the title with no body
		return ""
	}

	if content == "" {
		return ""
	}

	// Split into blocks on double newlines
	blocks := regexp.MustCompile(`\n\s*\n`).Split(content, -1)

	// Filter out empty blocks first
	var cleanBlocks []string
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block != "" {
			cleanBlocks = append(cleanBlocks, block)
		}
	}

	var result []string
	for i, block := range cleanBlocks {
		lines := strings.Split(block, "\n")

		// Check if this block is an unordered list (all lines are <li> items or start with * or -)
		if isUnorderedList(lines) {
			result = append(result, "<ul>")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "<li>") {
					result = append(result, line)
				} else if strings.HasPrefix(line, "* ") {
					result = append(result, "<li>"+strings.TrimPrefix(line, "* ")+"</li>")
				} else if strings.HasPrefix(line, "- ") {
					result = append(result, "<li>"+strings.TrimPrefix(line, "- ")+"</li>")
				} else if line != "" {
					result = append(result, "<li>"+line+"</li>")
				}
			}
			result = append(result, "</ul>")
			continue
		}

		// Check if this block is an ordered list (lines starting with 1. 2. etc.)
		if isOrderedList(lines) {
			result = append(result, "<ol>")
			olRegex := regexp.MustCompile(`^\d+\.\s*`)
			for _, line := range lines {
				line = strings.TrimSpace(line)
				text := olRegex.ReplaceAllString(line, "")
				if text != "" {
					result = append(result, "<li>"+text+"</li>")
				}
			}
			result = append(result, "</ol>")
			continue
		}

		// Check if this is a single-line heading candidate (must be followed by more content)
		if len(lines) == 1 {
			line := strings.TrimSpace(lines[0])
			// Already an <h3> from cleanMarkdown
			if strings.HasPrefix(line, "<h3>") {
				result = append(result, line)
				continue
			}
			// Short line without period at end looks like a heading,
			// but only if it's followed by more content blocks
			if i < len(cleanBlocks)-1 && isHeadingCandidate(line) {
				result = append(result, "<h3>"+line+"</h3>")
				continue
			}
		}

		// Default: wrap the block in <p> tags
		text := strings.Join(lines, " ")
		text = strings.TrimSpace(text)
		if text != "" {
			// Don't wrap if it's already an HTML block element
			if strings.HasPrefix(text, "<h3>") || strings.HasPrefix(text, "<ul>") || strings.HasPrefix(text, "<ol>") || strings.HasPrefix(text, "<hr") {
				result = append(result, text)
			} else {
				result = append(result, "<p>"+text+"</p>")
			}
		}
	}

	return strings.Join(result, "\n")
}

// isUnorderedList returns true if all non-empty lines in the block are list items.
func isUnorderedList(lines []string) bool {
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "<li>") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "- ") {
			count++
		} else {
			return false
		}
	}
	return count > 0
}

// isOrderedList returns true if all non-empty lines start with a number followed by a period.
func isOrderedList(lines []string) bool {
	olRegex := regexp.MustCompile(`^\d+\.\s`)
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if olRegex.MatchString(line) {
			count++
		} else {
			return false
		}
	}
	return count > 0
}

// isHeadingCandidate returns true if a line looks like a heading:
// short (under 80 chars), doesn't end with a period, and isn't already HTML.
func isHeadingCandidate(line string) bool {
	if len(line) > 80 {
		return false
	}
	if strings.HasSuffix(line, ".") {
		return false
	}
	if strings.HasPrefix(line, "<") {
		return false
	}
	// Must have at least some alphabetic content
	if len(strings.TrimSpace(line)) < 3 {
		return false
	}
	return true
}

// extractTitle returns the first line of the generated content as the title,
// stripping any Markdown or HTML artifacts.
func extractTitle(content string) string {
	title := content
	if idx := strings.Index(content, "\n"); idx != -1 {
		title = content[:idx]
	}
	// Strip Markdown bold markers
	title = strings.ReplaceAll(title, "**", "")
	// Strip Markdown heading markers
	title = strings.TrimLeft(title, "# ")
	// Strip HTML tags
	for {
		start := strings.Index(title, "<")
		if start == -1 {
			break
		}
		end := strings.Index(title[start:], ">")
		if end == -1 {
			break
		}
		title = title[:start] + title[start+end+1:]
	}
	return strings.TrimSpace(title)
}
