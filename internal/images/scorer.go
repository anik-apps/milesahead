package images

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// Scorer uses AI to score image candidates for relevance to an article.
// Phase 1: Text model scores descriptions/tags to narrow to top 3.
// Phase 2: Vision model looks at actual images to pick the best one.
type Scorer struct {
	textClient  ai.HFClientInterface
	textModel   string
	visionURL   string // e.g., "https://integrate.api.nvidia.com/v1"
	visionToken string
	visionModel string // e.g., "meta/llama-3.2-90b-vision-instruct"
	httpClient  *http.Client
}

// NewScorer creates a Scorer with a text model for pre-filtering.
func NewScorer(textClient ai.HFClientInterface, textModel string) *Scorer {
	return &Scorer{
		textClient: textClient,
		textModel:  textModel,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetVisionModel enables vision-based scoring for the final selection.
func (s *Scorer) SetVisionModel(baseURL, token, model string) {
	s.visionURL = baseURL
	s.visionToken = token
	s.visionModel = model
}

type scoreResponse struct {
	Scores []struct {
		Index  int     `json:"index"`
		Score  float64 `json:"score"`
		Reason string  `json:"reason"`
	} `json:"scores"`
}

// Score runs the two-phase scoring pipeline.
func (s *Scorer) Score(title, tldr string, candidates []ImageCandidate) ([]ImageCandidate, error) {
	if len(candidates) == 0 {
		return candidates, nil
	}

	// Phase 1: Text-based pre-filtering using descriptions
	candidates = s.textScore(title, tldr, candidates)

	// Phase 2: Vision model scores the top candidates
	// Try up to 5 candidates to ensure we get at least 3 successful vision scores
	if s.visionModel != "" && s.visionURL != "" {
		top := 5
		if len(candidates) < top {
			top = len(candidates)
		}
		scored := s.visionScore(title, tldr, candidates[:top])
		copy(candidates[:top], scored)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Score > candidates[j].Score
		})
	}

	return candidates, nil
}

// textScore uses descriptions and metadata to pre-filter candidates.
func (s *Scorer) textScore(title, tldr string, candidates []ImageCandidate) []ImageCandidate {
	var descriptions []string
	for i, c := range candidates {
		desc := c.Description
		if desc == "" {
			desc = "(no description)"
		}
		descriptions = append(descriptions, fmt.Sprintf("%d. [%s] %s — %s", i, c.Source, c.Attribution, desc))
	}

	prompt := ai.ImageScoringPrompt(title, tldr, descriptions)
	response, err := s.textClient.Generate(s.textModel, prompt, 2048)
	if err != nil {
		slog.Warn("text image scoring failed", "error", err)
		metrics.ImageScoringTotal.WithLabelValues("text", "error").Inc()
		return candidates
	}
	metrics.ImageScoringTotal.WithLabelValues("text", "success").Inc()

	response = strings.TrimSpace(response)
	if idx := strings.Index(response, "{"); idx != -1 {
		response = response[idx:]
	}
	if idx := strings.LastIndex(response, "}"); idx != -1 {
		response = response[:idx+1]
	}

	var scores scoreResponse
	if err := json.Unmarshal([]byte(response), &scores); err != nil {
		slog.Warn("failed to parse text image scores", "error", err)
		return candidates
	}

	for _, sc := range scores.Scores {
		if sc.Index >= 0 && sc.Index < len(candidates) {
			candidates[sc.Index].Score = sc.Score
			slog.Info("image text-scored", "index", sc.Index, "score", sc.Score, "reason", sc.Reason)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates
}

// visionRequest is the structured request body for the vision API.
type visionRequest struct {
	Model     string          `json:"model"`
	Messages  []visionMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens"`
}

type visionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// visionScore sends actual images to a vision model for scoring.
func (s *Scorer) visionScore(title, tldr string, candidates []ImageCandidate) []ImageCandidate {
	for i, c := range candidates {
		prompt := fmt.Sprintf(
			`Look at this image. The article is titled: "%s". Summary: "%s".

Score this image 1-10 for how specifically it represents this topic.
9-10: Shows the exact brand, product, or place mentioned
7-8: Closely related (correct type of thing but not the specific one)
5-6: Generic but relevant category
1-4: Wrong or irrelevant

Reply ONLY with JSON: {"score": N, "reason": "brief reason"}`, title, tldr)

		contentParts := []interface{}{
			map[string]string{"type": "text", "text": prompt},
			map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]string{"url": c.Thumbnail},
			},
		}
		contentJSON, err := json.Marshal(contentParts)
		if err != nil {
			slog.Warn("vision content marshal failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}

		vReq := visionRequest{
			Model: s.visionModel,
			Messages: []visionMessage{
				{Role: "user", Content: json.RawMessage(contentJSON)},
			},
			MaxTokens: 200,
		}
		reqBody, err := json.Marshal(vReq)
		if err != nil {
			slog.Warn("vision request marshal failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}

		req, err := http.NewRequest("POST", s.visionURL+"/chat/completions", bytes.NewBuffer(reqBody))
		if err != nil {
			slog.Warn("vision request creation failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}
		req.Header.Set("Authorization", "Bearer "+s.visionToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			slog.Warn("vision request failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode != 200 {
			body := string(respBody)
			if len(body) > 200 {
				body = body[:200]
			}
			slog.Warn("vision API error", "index", i, "status", resp.StatusCode, "body", body)
			metrics.ImageScoringTotal.WithLabelValues("vision", "error").Inc()
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}

		var chatResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
			slog.Warn("vision response parse failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}

		content := chatResp.Choices[0].Message.Content
		content = strings.TrimSpace(content)
		if idx := strings.Index(content, "{"); idx != -1 {
			content = content[idx:]
		}
		if idx := strings.LastIndex(content, "}"); idx != -1 {
			content = content[:idx+1]
		}

		var result struct {
			Score  float64 `json:"score"`
			Reason string  `json:"reason"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			slog.Warn("vision score parse failed", "index", i, "response", content)
			candidates[i].Score = candidates[i].Score * 0.5
			continue
		}

		candidates[i].Score = result.Score
		slog.Info("image vision-scored", "index", i, "score", result.Score, "reason", result.Reason, "attribution", c.Attribution)
		metrics.ImageScoringTotal.WithLabelValues("vision", "success").Inc()
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}
