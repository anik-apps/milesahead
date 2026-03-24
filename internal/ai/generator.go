package ai

import (
	"crypto/sha256"
	"fmt"
)

// Generator uses an HF model to produce articles from source material.
type Generator struct {
	client HFClientInterface
	model  string
	cache  *Cache
}

// NewGenerator creates a new Generator.
func NewGenerator(client HFClientInterface, model string, cache *Cache) *Generator {
	return &Generator{client: client, model: model, cache: cache}
}

// Generate produces an article of the given type from the provided sources.
// Supported types: "deal", "roundup", "summary".
func (g *Generator) Generate(articleType string, sources []SourceArticle) (string, error) {
	prompt, err := g.selectPrompt(articleType, sources)
	if err != nil {
		return "", err
	}

	cacheKey := g.cacheKey(prompt)

	if g.cache != nil {
		if cached, ok := g.cache.Get(cacheKey); ok {
			return cached, nil
		}
	}

	result, err := g.client.Generate(g.model, prompt, 1024)
	if err != nil {
		return "", fmt.Errorf("generator: %w", err)
	}

	if g.cache != nil {
		g.cache.Set(cacheKey, result)
	}

	return result, nil
}

// GenerateWithFeedback revises a previously generated article using critic
// feedback.
func (g *Generator) GenerateWithFeedback(sources []SourceArticle, previousOutput, feedback, criticIssues string) (string, error) {
	prompt := RevisionPrompt(sources, previousOutput, feedback, criticIssues)

	result, err := g.client.Generate(g.model, prompt, 1024)
	if err != nil {
		return "", fmt.Errorf("generator revision: %w", err)
	}

	return result, nil
}

func (g *Generator) selectPrompt(articleType string, sources []SourceArticle) (string, error) {
	switch articleType {
	case "deal":
		return DealAlertPrompt(sources), nil
	case "roundup":
		return RoundupPrompt(sources), nil
	case "summary":
		return SummaryPrompt(sources), nil
	default:
		return "", fmt.Errorf("unknown article type: %s", articleType)
	}
}

func (g *Generator) cacheKey(prompt string) string {
	h := sha256.Sum256([]byte(prompt))
	return fmt.Sprintf("gen:%x", h)
}
