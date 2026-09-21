package ai

import (
	"crypto/sha256"
	"errors"
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
		fallbacks: SplitModels(fallbackModels),
		cache:     cache,
	}
}

// SplitModels parses a comma-separated model list, trimming whitespace and
// ignoring blanks. Exported so callers parse the chain exactly as the Generator
// does: main.go once used a bare strings.Split, so "a, b" made preflight probe
// " b" (404, a false "model no longer exists" alarm) while the Generator called
// "b" perfectly happily.
func SplitModels(s string) []string {
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

	primaryErr := err
	tried := 0
	for _, fb := range g.fallbacks {
		slog.Warn("generator model failed, trying next in chain",
			"failed", g.model, "next", fb, "err", err)
		tried++
		result, err = g.client.Generate(fb, prompt, maxTokens)
		if err == nil {
			slog.Info("generator fallback succeeded", "model", fb, "primary", g.model)
			return result, nil
		}
		if !shouldTryAnotherModel(err) {
			break
		}
	}
	// The primary error is usually the diagnostically interesting one, so it is
	// the one wrapped; the last fallback error is rendered for context. "tried"
	// rather than len(fallbacks) because the loop can stop early.
	return "", fmt.Errorf("primary %q failed; %d of %d fallback(s) tried, last error %v: %w",
		g.model, tried, len(g.fallbacks), err, primaryErr)
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
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.ModelGone()
	}
	return false
}

// isRetryableModelError reports whether a model error looks like a transient
// upstream failure worth retrying against a different model.
//
// Errors carrying an *APIError are classified on its status code. Anything else
// reaching here came from outside HFClient -- a transport failure wrapped by
// http.Client, say -- so only those few shapes are matched textually. Note that
// the provider's response body is deliberately NOT consulted: it is attacker-
// adjacent free text, and matching "status 404" inside it once caused a plain
// 400 to burn the entire fallback chain.
func isRetryableModelError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Transient()
	}
	// Network/timeout from http.Client.Do, which never becomes an APIError.
	return strings.Contains(err.Error(), "http request:")
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
