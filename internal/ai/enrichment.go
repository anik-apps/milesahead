package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/svg"
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

	// InfographicTokens is an order of magnitude larger because the answer is
	// markup, not a sentence: a labelled bar chart runs 2-4 KB, and a
	// reasoning model spends its budget on layout arithmetic before it emits
	// the first tag. Truncation here is total -- the sanitiser rejects an
	// unterminated document outright -- so the ceiling is set well clear.
	InfographicTokens = 6144
)

// InfographicCallTimeout bounds one infographic call.
//
// InfographicTokens is above the short-call threshold, so through the shared
// client it would get the generator's timeout (900s in production) and its
// retries -- one hung call could hold an enrichment pass for most of an hour.
// The infographic gets its own client instead (HFClient.WithRequestTimeout,
// no retries): a fast model writes a 2-4 KB chart well inside two minutes,
// the model chain is the retry within a pass, and the reconciler is the retry
// across passes. Nothing waits on a chart.
const InfographicCallTimeout = 120 * time.Second

// ErrNoInfographic means the model judged the article to have nothing worth
// charting. That is a legitimate answer for a story with no numbers in it, and
// is distinguished from failure so callers do not log it as one.
var ErrNoInfographic = errors.New("article has no chartable numbers")

// TryModels runs a prompt against each model in turn, moving on when the
// failure is one another model might survive.
//
// Enrichment calls used to target a single model with no fallback, so when that
// model's backend degraded every article in the window lost the field
// permanently. Measured during one such window, the configured critic answered
// 6 of 24 raw attempts while two other models stayed healthy throughout.
//
// It is the one fallback helper for every short call, not only enrichment:
// image text-scoring, the newsletter pick and card-update detection run
// through it too, against the same enrichment_model chain.
//
// The critic chain runs through it as well, via tryModels, which also reports
// which model answered.
func TryModels(client HFClientInterface, models []string, prompt string, maxTokens int) (string, error) {
	result, _, err := tryModels(client, models, prompt, maxTokens)
	return result, err
}

// tryModels is TryModels, also returning the model that answered.
func tryModels(client HFClientInterface, models []string, prompt string, maxTokens int) (string, string, error) {
	if len(models) == 0 {
		return "", "", fmt.Errorf("no models configured")
	}
	var firstErr error
	for i, m := range models {
		result, err := client.Generate(m, prompt, maxTokens)
		if err == nil {
			if i > 0 {
				slog.Info("model fallback succeeded", "model", m, "primary", models[0])
			}
			return result, m, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if !shouldTryAnotherModel(err) {
			return "", "", err
		}
		if i < len(models)-1 {
			slog.Warn("model failed, trying next in chain",
				"failed", m, "next", models[i+1], "err", err)
		}
	}
	return "", "", fmt.Errorf("all %d model(s) failed, first error: %w", len(models), firstErr)
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
	// Enrichment works from the article alone, with no sources to map a
	// number to, so a take that echoes "Source 1" is refused rather than
	// published; the reconciler retries it on a later pass.
	if refs := FindSourceRefs(out); len(refs) > 0 {
		return "", fmt.Errorf("scouts take refers to a source by number: %q", refs)
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
	// As with Scout's Take there are no sources here to map a "Source 1" to,
	// so a detail that carries one is dropped.
	details := parsed.KeyDetails[:0]
	for _, d := range parsed.KeyDetails {
		if refs := FindSourceRefs(d.Label + " " + d.Value); len(refs) > 0 {
			slog.Warn("dropped key detail that refers to a source by number", "label", d.Label, "refs", refs)
			continue
		}
		details = append(details, d)
	}
	if len(details) == 0 {
		// Storing an empty list looks identical to a successful extraction that
		// found nothing, and the article would never be retried.
		return nil, nil, fmt.Errorf("details response contained no usable key details")
	}
	return details, parsed.Links, nil
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

// GenerateInfographic asks a model to draw the article's numbers, and returns
// markup that has been through the allowlist sanitiser.
//
// Nothing here trusts the model's output. svg.Sanitize bounds and rewrites
// it, and, as with Scout's Take, a chart whose words refer to a source by
// number is refused so a later pass can try again. Outlet names are checked
// by the caller, which holds the outlet table.
func GenerateInfographic(client HFClientInterface, models []string, title, tldr, keyDetailsJSON string) (string, error) {
	raw, err := TryModels(client, models,
		InfographicPrompt(title, tldr, keyDetailsJSON), InfographicTokens)
	if err != nil {
		return "", err
	}
	if strings.Contains(raw, "NO_INFOGRAPHIC") && !strings.Contains(raw, "<svg") {
		return "", ErrNoInfographic
	}
	clean, err := svg.Sanitize(raw)
	if err != nil {
		return "", fmt.Errorf("sanitising infographic: %w", err)
	}
	words, err := svg.Text(clean)
	if err != nil {
		return "", fmt.Errorf("reading infographic text: %w", err)
	}
	if refs := FindSourceRefs(words); len(refs) > 0 {
		return "", fmt.Errorf("infographic refers to a source by number: %q", refs)
	}
	return clean, nil
}
