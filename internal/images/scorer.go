package images

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
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
	textModels  []string
	visionURL   string // e.g., "https://integrate.api.nvidia.com/v1"
	visionToken string
	// visionModels are tried in order, e.g. "meta/llama-3.2-90b-vision-instruct";
	// see visionScore.
	visionModels []string
	recorder     ai.Recorder // model health; nil records nothing
	httpClient   *http.Client
	// imageClient downloads the thumbnails sent to the vision model; see
	// inlineImage.
	imageClient *http.Client
	// warmer, if set, is given the thumbnails about to go to the vision
	// model before any of them is sent. See SetVisionModel.
	warmer Warmer
}

// VisionStep is the model-health step vision calls are recorded under
// (health.StepVision; a test in cmd/milesahead keeps the two equal).
const VisionStep = "vision"

// Warmer has an image host prepare files before another client fetches them.
// *Searcher is one: its Warm has Wikimedia render new thumbnail sizes.
type Warmer interface {
	Warm(urls []string)
}

// NewScorer creates a Scorer with a chain of text models for pre-filtering,
// tried in order through ai.TryModels.
//
// A chain rather than one model: this used to be the critic model alone, so
// when that model was retired (410 on every call) every image lost its text
// score. It is a short call bounded by ai.ShortCallTimeout, so it belongs on a
// fast model -- production passes the enrichment chain, not the critic, whose
// replies can take minutes.
func NewScorer(textClient ai.HFClientInterface, textModels []string) *Scorer {
	return &Scorer{
		textClient:  textClient,
		textModels:  textModels,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		imageClient: &http.Client{Timeout: renderTimeout},
	}
}

// SetImageTransport routes the scorer's thumbnail downloads through rt. It
// exists so tests can stand in for the image hosts without the network.
func (s *Scorer) SetImageTransport(rt http.RoundTripper) {
	s.imageClient.Transport = rt
}

// SetVisionModel enables vision-based scoring for the final selection. model
// is one model or a comma-separated list, tried in order.
//
// warmer is handed the thumbnails Score is about to send, before any is sent
// (the Searcher that found the candidates, in production). For a sized
// Wikimedia URL the scorer's download is usually the first request for that
// size, and it met a slow render or a 429 -- and the candidate took the x0.6
// failure penalty however good the image was. It is an argument here, not an
// option, so that vision scoring cannot be switched on without it by
// accident; nil means no warming.
func (s *Scorer) SetVisionModel(baseURL, token, model string, warmer Warmer) {
	s.visionURL = baseURL
	s.visionToken = token
	s.visionModels = ai.SplitModels(model)
	s.warmer = warmer
}

// SetRecorder reports every vision call's outcome to model health.
func (s *Scorer) SetRecorder(r ai.Recorder) { s.recorder = r }

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
	if len(s.visionModels) > 0 && s.visionURL != "" {
		top := 5
		if len(candidates) < top {
			top = len(candidates)
		}
		if s.warmer != nil {
			thumbs := make([]string, top)
			for i, c := range candidates[:top] {
				thumbs[i] = c.Thumbnail
			}
			s.warmer.Warm(thumbs)
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
	response, err := ai.TryModels(s.textClient, s.textModels, prompt, 2048)
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
	// ChatTemplateKwargs carries enable_thinking=false; see visionCall.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type visionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// visionScorePattern finds a score stated in prose, for models that will not
// hold to the JSON contract.
//
// Anchored deliberately: an unanchored "first number" would read "Japan
// Airlines 787" as a score of 787. Only forms that actually assert a score
// count, and the value is range-checked afterwards.
var visionScorePattern = regexp.MustCompile(
	`(?i)(?:"?score"?\s*[:=]\s*|\bscore\s+(?:of\s+|is\s+)?|\brate[ds]?\s+(?:it\s+)?(?:a[ns]?\s+)?)(\d{1,2}(?:\.\d)?)|\b(\d{1,2}(?:\.\d)?)\s*(?:/|out of)\s*10\b`)

// parseVisionScore reads a 1-10 score from a model reply, preferring the JSON
// contract and falling back to prose.
//
// The fallback exists because a model that narrates its judgement is still
// giving a usable judgement; discarding it meant the image took a penalty for
// the model's verbosity rather than for being a bad image.
func parseVisionScore(content string) (score float64, reason string, ok bool) {
	content = strings.TrimSpace(content)

	if start := strings.Index(content, "{"); start != -1 {
		if end := strings.LastIndex(content, "}"); end > start {
			var parsed struct {
				Score  float64 `json:"score"`
				Reason string  `json:"reason"`
			}
			if err := json.Unmarshal([]byte(content[start:end+1]), &parsed); err == nil {
				if parsed.Score >= 1 && parsed.Score <= 10 {
					return parsed.Score, parsed.Reason, true
				}
			}
		}
	}

	m := visionScorePattern.FindStringSubmatch(content)
	if m == nil {
		return 0, "", false
	}
	raw := m[1]
	if raw == "" {
		raw = m[2]
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 1 || v > 10 {
		return 0, "", false
	}
	return v, truncateForLog(content), true
}

// truncateForLog keeps a model reply readable in a log line.
func truncateForLog(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}

// visionMaxTokens bounds a vision reply.
//
// Was 200, which truncated any model that narrates before answering -- the
// reply got cut off mid-explanation, the JSON was never reached, and the
// candidate took a scoring penalty for the model's verbosity rather than the
// image's relevance. Observed with meta/llama-3.2-11b-vision-instruct.
const visionMaxTokens = 512

// maxInlineImage is the largest thumbnail sent to the vision model inline; a
// larger one is sent by URL. Both vision models accepted a 309 KB image
// inline (412 KB as base64) on 2026-10-29, and the thumbnails of the 40
// newest articles were 13-83 KB.
const maxInlineImage = 300 << 10

// inlineImage downloads a thumbnail and returns it as a data URI, so the
// vision model is sent the image itself rather than a URL to fetch.
//
// The provider used to fetch every thumbnail itself, and Wikimedia refuses
// its fetcher: on 2026-10-29 each Wikimedia thumbnail came back 403 from
// nemotron-3.5-super-vl and 500 from llama-3.2-11b-vision, while the same
// files sent inline scored in 1-17s. Both statuses were recorded against the
// model, and a 500 also moved the call off it, so a host refusing a fetch
// read as the vision model failing. This download carries the searcher's
// userAgent, which Wikimedia accepts.
//
// A thumbnail over maxInlineImage is returned as its URL. An error means the
// thumbnail could not be downloaded, which a reader's browser would not
// manage either.
func (s *Scorer) inlineImage(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.imageClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "image/") {
		return "", fmt.Errorf("content type %q is not an image", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInlineImage+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxInlineImage {
		return rawURL, nil
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(body), nil
}

// visionScore sends actual images to a vision model for scoring.
// If ALL vision calls fail, restores original text scores (no penalty).
func (s *Scorer) visionScore(title, tldr string, candidates []ImageCandidate) []ImageCandidate {
	// Save original text scores in case all vision calls fail
	textScores := make([]float64, len(candidates))
	for i, c := range candidates {
		textScores[i] = c.Score
	}
	visionSuccesses := 0
	defer func() {
		if visionSuccesses == 0 {
			slog.Warn("all vision scoring failed, restoring text scores")
			for i := range candidates {
				candidates[i].Score = textScores[i]
			}
		}
	}()
	mi := 0 // the vision model in use; moves down the list when one is down
	for i, c := range candidates {
		prompt := fmt.Sprintf(
			`Reply with ONLY this JSON object and nothing before or after it:
{"score": N, "reason": "brief reason"}

Do not explain your reasoning before the JSON. Start your reply with "{".

Look at this image. The article is titled: "%s". Summary: "%s".

Score this image 1-10 for how specifically it represents this topic.
9-10: Shows the exact brand, product, or place mentioned
7-8: Closely related (correct type of thing but not the specific one)
5-6: Generic but relevant category
1-4: Wrong or irrelevant`, title, tldr)

		image, err := s.inlineImage(c.Thumbnail)
		if err != nil {
			// Not the model's failure, so no model is asked or blamed.
			slog.Warn("vision scoring skipped: thumbnail could not be downloaded",
				"index", i, "url", c.Thumbnail, "error", err)
			metrics.ImageScoringTotal.WithLabelValues("vision", "error").Inc()
			candidates[i].Score = candidates[i].Score * 0.6
			continue
		}
		contentParts := []interface{}{
			map[string]string{"type": "text", "text": prompt},
			map[string]interface{}{
				"type":      "image_url",
				"image_url": map[string]string{"url": image},
			},
		}
		contentJSON, err := json.Marshal(contentParts)
		if err != nil {
			slog.Warn("vision content marshal failed", "index", i, "error", err)
			candidates[i].Score = candidates[i].Score * 0.6
			continue
		}

		for {
			if mi >= len(s.visionModels) {
				// Every vision model failed earlier in this call.
				candidates[i].Score = candidates[i].Score * 0.6
				break
			}
			model := s.visionModels[mi]
			score, reason, outcome, err := s.visionCall(model, contentJSON)
			if err == nil {
				candidates[i].Score = score
				visionSuccesses++
				slog.Info("image vision-scored", "index", i, "model", model, "score", score,
					"reason", reason, "attribution", c.Attribution)
				metrics.ImageScoringTotal.WithLabelValues("vision", "success").Inc()
				break
			}
			slog.Warn("vision scoring failed", "index", i, "model", model, "outcome", outcome, "error", err)
			if outcome == ai.OutcomeRetired || outcome == ai.OutcomeTimeout || outcome == ai.OutcomeOverloaded {
				mi++ // this model is down for the rest of the call; retry the candidate on the next
				continue
			}
			candidates[i].Score = candidates[i].Score * 0.6
			break
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}

// visionCall asks one vision model to score one image.
func (s *Scorer) visionCall(model string, contentJSON []byte) (float64, string, ai.Outcome, error) {
	start := time.Now()
	fail := func(o ai.Outcome, status int, err error) (float64, string, ai.Outcome, error) {
		s.recordVision(model, start, o, status, err)
		return 0, "", o, err
	}
	// Thinking off: the reply is one line of JSON, and with thinking on
	// nemotron-3.5-super-vl spent visionMaxTokens reasoning and returned an
	// empty reply in 9 of 18 calls (2026-10-29). llama-3.2-11b-vision does
	// not reason; it accepts the field.
	reqBody, err := json.Marshal(visionRequest{
		Model:              model,
		Messages:           []visionMessage{{Role: "user", Content: json.RawMessage(contentJSON)}},
		MaxTokens:          visionMaxTokens,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	})
	if err != nil {
		return fail(ai.OutcomeError, 0, err)
	}
	req, err := http.NewRequest("POST", s.visionURL+"/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return fail(ai.OutcomeError, 0, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.visionToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		e := fmt.Errorf("http request: %w", err)
		return fail(ai.OutcomeOf(e), 0, e)
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	if resp.StatusCode != 200 {
		body := string(respBody)
		if len(body) > 200 {
			body = body[:200]
		}
		metrics.ImageScoringTotal.WithLabelValues("vision", "error").Inc()
		apiErr := &ai.APIError{StatusCode: resp.StatusCode, Model: model, Body: body}
		o := ai.OutcomeOf(apiErr)
		if o == ai.OutcomeError {
			// A 4xx about the image (a thumbnail the provider could not fetch)
			// is not the model's fault: not recorded against it.
			return 0, "", o, apiErr
		}
		return fail(o, resp.StatusCode, apiErr)
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return fail(ai.OutcomeBadReply, resp.StatusCode, fmt.Errorf("vision response did not parse: %v", err))
	}
	score, reason, ok := parseVisionScore(chatResp.Choices[0].Message.Content)
	if !ok {
		return fail(ai.OutcomeBadReply, resp.StatusCode,
			fmt.Errorf("vision score did not parse: %s", truncateForLog(chatResp.Choices[0].Message.Content)))
	}
	s.recordVision(model, start, ai.OutcomeOK, resp.StatusCode, nil)
	return score, reason, ai.OutcomeOK, nil
}

func (s *Scorer) recordVision(model string, start time.Time, o ai.Outcome, status int, err error) {
	if s.recorder == nil {
		return
	}
	c := ai.ModelCall{Step: VisionStep, Model: model, Source: ai.SourceCall, Outcome: o,
		HTTPStatus: status, At: time.Now().UTC(), Duration: time.Since(start)}
	if err != nil {
		c.Detail = err.Error()
		if len(c.Detail) > 300 {
			c.Detail = c.Detail[:300]
		}
	}
	s.recorder.RecordModelCall(c)
}
