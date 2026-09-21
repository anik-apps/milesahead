package ai

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// Token budgets for the enrichment calls.
//
// These were 256. Measured against the provider, 256 is enough for a direct
// model (mistral-nemotron uses 53-73 and finishes cleanly) but not for a
// reasoning one: at 256 openai/gpt-oss-20b returns empty content and
// nvidia/nemotron-3-super-120b-a12b returns a truncated half-sentence. 512 was
// sufficient for every reasoning model tested, and costs nothing extra when the
// model does not need it -- max_tokens is a ceiling, not a target.
const (
	ScoutsTakeTokens = 512
	TagsTokens       = 512
	KeyDetailTokens  = 1024
)

// TryModels runs a prompt against each model in turn, moving on when the
// failure is one another model might survive.
//
// Enrichment calls used to target a single model with no fallback, so when that
// model's backend degraded every article in the window lost the field
// permanently. Measured during one such window, the configured critic answered
// 6 of 24 raw attempts while two other models stayed healthy throughout.
func TryModels(client HFClientInterface, models []string, prompt string, maxTokens int) (string, error) {
	if len(models) == 0 {
		return "", fmt.Errorf("no models configured")
	}
	var firstErr error
	for i, m := range models {
		result, err := client.Generate(m, prompt, maxTokens)
		if err == nil {
			if i > 0 {
				slog.Info("enrichment fallback succeeded", "model", m, "primary", models[0])
			}
			return result, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !shouldTryAnotherModel(err) {
			return "", err
		}
		if i < len(models)-1 {
			slog.Warn("enrichment model failed, trying next",
				"failed", m, "next", models[i+1], "err", err)
		}
	}
	return "", fmt.Errorf("all %d model(s) failed, first error: %w", len(models), firstErr)
}

// GenerateScoutsTake produces the persona commentary for an article.
func GenerateScoutsTake(client HFClientInterface, models []string, persona, title, tldr, content string) (string, error) {
	out, err := TryModels(client, models,
		ScoutsTakePrompt(persona, title, tldr, content), ScoutsTakeTokens)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("scouts take came back empty")
	}
	return out, nil
}

// ExtractDetails pulls key details and links out of a finished article.
func ExtractDetails(client HFClientInterface, models []string, title, content string) ([]KeyDetail, []ArticleLink, error) {
	resp, err := TryModels(client, models, ExtractDetailsPrompt(title, content), KeyDetailTokens)
	if err != nil {
		return nil, nil, err
	}
	jsonStr := extractJSON(resp)
	if jsonStr == "" {
		return nil, nil, fmt.Errorf("no JSON object in details response")
	}
	var parsed struct {
		KeyDetails []KeyDetail   `json:"key_details"`
		Links      []ArticleLink `json:"links"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil, nil, fmt.Errorf("parsing details response: %w", err)
	}
	if len(parsed.KeyDetails) == 0 {
		// Storing an empty list looks identical to a successful extraction that
		// found nothing, and the article would never be retried.
		return nil, nil, fmt.Errorf("details response contained no key details")
	}
	return parsed.KeyDetails, parsed.Links, nil
}

// ExtractTags picks topic tags, keeping only ones on the approved list.
func ExtractTags(client HFClientInterface, models []string, title, content string) ([]string, error) {
	resp, err := TryModels(client, models, ArticleTagsPrompt(title, content), TagsTokens)
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if i := strings.Index(resp, "["); i != -1 {
		resp = resp[i:]
	}
	if i := strings.LastIndex(resp, "]"); i != -1 {
		resp = resp[:i+1]
	}
	var parsed []string
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		return nil, fmt.Errorf("parsing tags response: %w", err)
	}
	valid := make(map[string]bool, len(ValidTags))
	for _, t := range ValidTags {
		valid[t] = true
	}
	var filtered []string
	for _, t := range parsed {
		if valid[t] {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no valid tags in response %q", resp)
	}
	return filtered, nil
}

// GenerateImageQueries asks for stock-photo search terms for an article.
func GenerateImageQueries(client HFClientInterface, models []string, title, tldr string) ([]string, error) {
	resp, err := TryModels(client, models, ImageQueriesPrompt(title, tldr), TagsTokens)
	if err != nil {
		return nil, err
	}
	resp = strings.TrimSpace(resp)
	if i := strings.Index(resp, "["); i != -1 {
		resp = resp[i:]
	}
	if i := strings.LastIndex(resp, "]"); i != -1 {
		resp = resp[:i+1]
	}
	var queries []string
	if err := json.Unmarshal([]byte(resp), &queries); err != nil {
		return nil, fmt.Errorf("parsing image queries: %w", err)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("no image queries returned")
	}
	return queries, nil
}
