package ai

import (
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
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "## ") + "</h3>"
		} else if strings.HasPrefix(trimmed, "### ") {
			lines[i] = "<h3>" + strings.TrimPrefix(trimmed, "### ") + "</h3>"
		}
	}
	return strings.Join(lines, "\n")
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
