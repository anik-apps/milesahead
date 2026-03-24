package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultBaseURL = "https://api-inference.huggingface.co"

// HFClientInterface allows mocking the HF client in tests.
type HFClientInterface interface {
	Generate(model, prompt string, maxTokens int) (string, error)
}

// HFClient is an HTTP client for the Hugging Face Inference API.
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
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

type generateRequest struct {
	Inputs     string            `json:"inputs"`
	Parameters map[string]int    `json:"parameters"`
}

// Generate sends a text generation request to the HF Inference API.
func (c *HFClient) Generate(model, prompt string, maxTokens int) (string, error) {
	url := fmt.Sprintf("%s/models/%s", c.BaseURL, model)

	reqBody := generateRequest{
		Inputs:     prompt,
		Parameters: map[string]int{"max_new_tokens": maxTokens},
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

		var results []map[string]string
		if err := json.Unmarshal(respBody, &results); err != nil {
			return "", fmt.Errorf("unmarshal response: %w", err)
		}
		if len(results) == 0 {
			return "", fmt.Errorf("empty response from HF API")
		}
		text, ok := results[0]["generated_text"]
		if !ok {
			return "", fmt.Errorf("no generated_text in response")
		}
		return text, nil
	}

	return "", fmt.Errorf("max retries exceeded")
}
