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

// Process generates an article and verifies it against the sources.
func (p *Pipeline) Process(articleType string, sources []SourceArticle) (*PipelineResult, error) {
	content, err := p.generator.Generate(articleType, sources)
	if err != nil {
		return nil, err
	}

	return p.buildResult(content, articleType, sources)
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
	verification, err := p.critic.Verify(content, sources)
	if err != nil {
		return nil, err
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

// extractTitle returns the first line of the generated content as the title.
func extractTitle(content string) string {
	if idx := strings.Index(content, "\n"); idx != -1 {
		return content[:idx]
	}
	return content
}
