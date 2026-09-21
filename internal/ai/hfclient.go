package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/metrics"
)

const defaultBaseURL = "https://router.huggingface.co/v1"

// HFClientInterface allows mocking the HF client in tests.
type HFClientInterface interface {
	Generate(model, prompt string, maxTokens int) (string, error)
}

// HFClient is an HTTP client for the Hugging Face Inference API.
// Uses the OpenAI-compatible chat completions endpoint on router.huggingface.co.
type HFClient struct {
	token      string
	maxRetries int
	BaseURL    string
	httpClient *http.Client
}

// DefaultRequestTimeout bounds a single model call.
//
// Worth tuning with the model: generation latency varies by more than an order
// of magnitude between models (~10s for a small one, 105-510s for a 550B
// reasoning model), and a timeout below the primary's real latency is
// indistinguishable from the model being dead -- it would silently demote every
// article to the fallback.
const DefaultRequestTimeout = 300 * time.Second

// NewHFClient creates a new HFClient with the given token and retry limit.
func NewHFClient(token string, maxRetries int) *HFClient {
	return &HFClient{
		token:      token,
		maxRetries: maxRetries,
		BaseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: DefaultRequestTimeout},
	}
}

// SetRequestTimeout overrides the per-call timeout. A non-positive value keeps
// the current one.
func (c *HFClient) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		c.httpClient.Timeout = d
	}
}

// chatRequest is the OpenAI-compatible request format.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI-compatible response format.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// stripThinkingTags removes <think>...</think> blocks from model responses.
// Some reasoning models (DeepSeek R1, QwQ) include thinking chains in their output.
func stripThinkingTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start == -1 {
			break
		}
		end := strings.Index(s, "</think>")
		if end == -1 {
			// Unclosed think tag — remove everything from <think> onward
			s = strings.TrimSpace(s[:start])
			break
		}
		s = s[:start] + s[end+len("</think>"):]
	}
	return strings.TrimSpace(s)
}

// Generate sends a text generation request using the chat completions API.
func (c *HFClient) Generate(model, prompt string, maxTokens int) (string, error) {
	slog.Info("ai api call", "model", model, "max_tokens", maxTokens)
	start := time.Now()
	url := fmt.Sprintf("%s/chat/completions", c.BaseURL)

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   maxTokens,
		Temperature: 0.7,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		metrics.AIAPIErrorsTotal.Inc()
		metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
		return "", fmt.Errorf("marshal request: %w", err)
	}

	backoff := 100 * time.Millisecond

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", fmt.Errorf("http request: %w", err)
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", fmt.Errorf("read response: %w", err)
		}

		// Retry throttling and any 5xx. Previously only 429 and 503 were
		// retried, so a single transient 500 or 502 from the provider killed
		// the request outright even though the next attempt would have worked.
		// Retry throttling and transient 5xx. 501/505 are permanent by
		// definition, so retrying them only multiplies failed calls.
		if resp.StatusCode == 429 ||
			(resp.StatusCode >= 500 && resp.StatusCode != 501 && resp.StatusCode != 505) {
			if attempt < c.maxRetries {
				slog.Warn("ai api retry", "model", model, "attempt", attempt, "status", resp.StatusCode)
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", &APIError{StatusCode: resp.StatusCode, Model: model,
				Body: string(respBody), Retries: c.maxRetries}
		}

		if resp.StatusCode != http.StatusOK {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", &APIError{StatusCode: resp.StatusCode, Model: model, Body: string(respBody)}
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
		}

		// HTTP 200 carrying an error object. This is how an OpenAI-compatible
		// gateway can announce a retired model without ever using a 4xx, so it
		// must stay classifiable -- StatusCode 0 means "we do not know", and
		// APIError inspects the body for retirement wording.
		if chatResp.Error != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", &APIError{Model: model, Body: chatResp.Error.Message}
		}

		if len(chatResp.Choices) == 0 {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", &APIError{Model: model, Body: "no choices in response"}
		}

		content := chatResp.Choices[0].Message.Content
		// Strip <think>...</think> blocks from reasoning models (DeepSeek R1, etc.)
		content = stripThinkingTags(content)

		// A reasoning model can spend its whole token budget thinking and return
		// no answer. That arrives as HTTP 200 with empty content, so without
		// this it was a silent success: no fallback, and the empty string cached
		// against that prompt for the whole cache TTL.
		if strings.TrimSpace(content) == "" {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			return "", &APIError{Model: model,
				Body: "model returned empty content (token budget may have gone to reasoning)"}
		}

		metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
		return content, nil
	}

	slog.Error("ai api failed", "model", model, "error", "max retries exceeded")
	metrics.AIAPIErrorsTotal.Inc()
	metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
	return "", fmt.Errorf("max retries exceeded")
}
