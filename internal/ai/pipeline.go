package ai

import (
	"regexp"
	"strings"
)

// PipelineResult holds the output of a full generate-then-verify cycle.
type PipelineResult struct {
	Content         string
	Title           string
	Type            string
	Verification    *VerificationResult
	PassedThreshold bool
}

// Pipeline orchestrates the generator and critic to produce verified articles.
type Pipeline struct {
	generator *Generator
	critic    *Critic
	threshold int
}

// NewPipeline creates a new Pipeline with the given quality threshold (0-100).
func NewPipeline(generator *Generator, critic *Critic, threshold int) *Pipeline {
	return &Pipeline{generator: generator, critic: critic, threshold: threshold}
}

// Process generates an article, verifies it, and if the critic finds issues
// but the score is above threshold, auto-revises once before finalizing.
func (p *Pipeline) Process(articleType string, sources []SourceArticle) (*PipelineResult, error) {
	content, err := p.generator.Generate(articleType, sources)
	if err != nil {
		return nil, err
	}

	content = cleanMarkdown(content)

	// First critic pass
	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
	}

	// Enforce score consistency: failed checks must lower the score
	verification.Score = adjustScore(verification)

	// If there are issues but score is still passable, auto-revise once
	if len(verification.Issues) > 0 && verification.Score >= p.threshold {
		issuesStr := strings.Join(verification.Issues, "; ")
		revised, err := p.generator.GenerateWithFeedback(sources, content, "Fix the following issues found by the critic", issuesStr)
		if err == nil && revised != "" {
			revised = cleanMarkdown(revised)
			// Re-verify the revision
			v2, err := p.critic.Verify(revised, sources)
			if err == nil {
				v2.Score = adjustScore(v2)
				// Use the revision if it's at least as good
				if v2.Score >= verification.Score {
					content = revised
					verification = v2
				}
			}
		}
	}

	title := extractTitle(content)
	content = formatContent(content)

	return &PipelineResult{
		Content:         content,
		Title:           title,
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

	title := extractTitle(content)
	content = formatContent(content)

	return &PipelineResult{
		Content:         content,
		Title:           title,
		Type:            articleType,
		Verification:    verification,
		PassedThreshold: verification.Score >= p.threshold,
	}, nil
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

	// Each failed check deducts 15 points
	penalty := failCount * 15
	if score-penalty < score {
		score -= penalty
	}

	// Cap: can't be above 80 with any failed check, can't be above 60 with 2+ failures
	if failCount > 0 && score > 80 {
		score = 80
	}
	if failCount >= 2 && score > 60 {
		score = 60
	}
	if score < 0 {
		score = 0
	}

	return score
}

// cleanMarkdown converts common Markdown remnants to HTML.
func cleanMarkdown(s string) string {
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
	lines := strings.Split(s, "\n")
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
