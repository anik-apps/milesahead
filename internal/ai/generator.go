package ai

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
)

// Generator uses an HF model to produce articles from source material.
// Transient errors (rate limit, 5xx, network/timeout) AND permanent
// model-unavailable errors (404/410) on the primary fall through to the next
// model in the fallback chain.
type Generator struct {
	client    HFClientInterface
	model     string
	fallbacks []string
	cache     *Cache
}

// NewGenerator creates a new Generator. fallbackModels is a comma-separated
// list, tried left to right; empty disables fallback. A list rather than a
// single model because the site once ran 26 days without publishing when the
// primary AND the lone fallback were retired upstream in the same window.
func NewGenerator(client HFClientInterface, model, fallbackModels string, cache *Cache) *Generator {
	return &Generator{
		client:    client,
		model:     model,
		fallbacks: splitModels(fallbackModels),
		cache:     cache,
	}
}

// splitModels parses a comma-separated model list, ignoring blanks.
func splitModels(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// callWithFallback runs the primary model and, when the failure is one another
// model could plausibly survive, walks the fallback chain in order.
//
// Genuine configuration errors (401 auth, 400 malformed) still propagate
// immediately, since every model in the chain would fail them identically.
func (g *Generator) callWithFallback(prompt string, maxTokens int) (string, error) {
	result, err := g.client.Generate(g.model, prompt, maxTokens)
	if err == nil {
		return result, nil
	}
	if len(g.fallbacks) == 0 || !shouldTryAnotherModel(err) {
		return "", err
	}

	firstErr := err
	for _, fb := range g.fallbacks {
		slog.Warn("generator model failed, trying next in chain",
			"failed", g.model, "next", fb, "err", err)
		result, err = g.client.Generate(fb, prompt, maxTokens)
		if err == nil {
			slog.Info("generator fallback succeeded", "model", fb, "primary", g.model)
			return result, nil
		}
		if !shouldTryAnotherModel(err) {
			break
		}
	}
	return "", fmt.Errorf("primary %q failed (%v); all %d fallback(s) failed, last: %w",
		g.model, firstErr, len(g.fallbacks), err)
}

// shouldTryAnotherModel reports whether a different model might succeed where
// this one failed -- either a transient upstream fault, or this model being
// permanently gone.
func shouldTryAnotherModel(err error) bool {
	return isRetryableModelError(err) || isModelUnavailableError(err)
}

// isModelUnavailableError reports whether the model itself is gone rather than
// the request being bad. Providers retire models on their own schedule and
// answer 410 Gone (or 404) forever after; that is precisely the case a fallback
// exists for, even though it is a permanent 4xx rather than a transient fault.
func isModelUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 410") || strings.Contains(msg, "status 404")
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
	case strings.Contains(msg, "rate limited after"): // legacy wording, kept for safety
		return true
	case strings.Contains(msg, "status 429"): // throttled, retries exhausted
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
