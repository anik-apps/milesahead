package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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

// NewHFClient creates a new HFClient with the given token and retry limit.
func NewHFClient(token string, maxRetries int) *HFClient {
	return &HFClient{
		token:      token,
		maxRetries: maxRetries,
		BaseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 300 * time.Second},
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
		return "", fmt.Errorf("marshal request: %w", err)
	}

	backoff := 100 * time.Millisecond

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("http request: %w", err)
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("read response: %w", err)
		}

		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			if attempt < c.maxRetries {
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			return "", fmt.Errorf("rate limited after %d retries: %s", c.maxRetries, string(respBody))
		}

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HF API error (status %d): %s", resp.StatusCode, string(respBody))
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			return "", fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
		}

		if chatResp.Error != nil {
			return "", fmt.Errorf("HF API error: %s", chatResp.Error.Message)
		}

		if len(chatResp.Choices) == 0 {
			return "", fmt.Errorf("empty response from HF API")
		}

		content := chatResp.Choices[0].Message.Content
		// Strip <think>...</think> blocks from reasoning models (DeepSeek R1, etc.)
		content = stripThinkingTags(content)
		return content, nil
	}

	return "", fmt.Errorf("max retries exceeded")
}
