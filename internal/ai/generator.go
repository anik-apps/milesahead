package ai

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
)

// Generator uses an HF model to produce articles from source material.
// If a fallback model is configured, retryable errors (rate limit, 5xx,
// network/timeout) on the primary trigger one retry against the fallback.
type Generator struct {
	client   HFClientInterface
	model    string
	fallback string
	cache    *Cache
}

// NewGenerator creates a new Generator. fallbackModel may be empty to disable
// fallback behavior.
func NewGenerator(client HFClientInterface, model, fallbackModel string, cache *Cache) *Generator {
	return &Generator{client: client, model: model, fallback: fallbackModel, cache: cache}
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

	result, err := g.callWithFallback(prompt, 4096)
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

	result, err := g.callWithFallback(prompt, 4096)
	if err != nil {
		return "", fmt.Errorf("generator revision: %w", err)
	}

	return result, nil
}

// callWithFallback runs the primary model and, on a retryable error, retries
// once against the fallback model if one is configured. Errors that aren't
// retryable (e.g. 4xx config errors like model-not-found) propagate directly
// without trying the fallback, since the fallback can't fix them.
func (g *Generator) callWithFallback(prompt string, maxTokens int) (string, error) {
	result, err := g.client.Generate(g.model, prompt, maxTokens)
	if err == nil {
		return result, nil
	}
	if g.fallback == "" || !isRetryableModelError(err) {
		return "", err
	}
	slog.Warn("primary generator model failed, trying fallback",
		"primary", g.model, "fallback", g.fallback, "err", err)
	result, fbErr := g.client.Generate(g.fallback, prompt, maxTokens)
	if fbErr != nil {
		return "", fmt.Errorf("primary failed (%v); fallback %q also failed: %w", err, g.fallback, fbErr)
	}
	return result, nil
}

// isRetryableModelError reports whether a model error looks like a transient
// upstream failure worth retrying against a different model. Matches on the
// error strings produced by HFClient.Generate (rate-limit exhaustion, 5xx,
// network/timeout, top-level retry exhaustion).
func isRetryableModelError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "rate limited after"):
		return true
	case strings.Contains(msg, "max retries exceeded"):
		return true
	case strings.Contains(msg, "http request:"): // network/timeout from http.Client.Do
		return true
	case strings.Contains(msg, "status 5"): // any 5xx
		return true
	}
	return false
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
