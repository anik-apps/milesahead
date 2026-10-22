package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// backoff is the first retry delay, doubling per attempt. Injectable so
	// tests exercise the retry path without sleeping for real.
	backoff time.Duration
	// requestTimeout bounds one attempt; see timeoutFor.
	requestTimeout time.Duration
	// disableThinking asks the model to answer without a reasoning phase; see
	// WithThinkingDisabled.
	disableThinking bool
	// step and recorder label the client for model health; see WithStep.
	step     string
	recorder Recorder
}

// initialBackoff is the first retry delay; it doubles per attempt.
const initialBackoff = 2 * time.Second

// shortCallTokens separates short, interactive calls from article generation.
//
// Every short call in the codebase asks for 2048 or fewer; generation and
// criticism ask for 4096. The threshold sits between them with a 2x margin on
// each side, rather than on top of a call site.
//
// It was 512, which is exactly what Scout's Take, tags and image queries ask
// for -- so those three were capped only by an <= comparison, and one extra
// token would silently have restored the 30x-longer generation timeout. Worse,
// the three calls it did NOT cover are the expensive ones: key details (1024),
// image text-scoring (2048) and card-update detection (1024) all inherited the
// 900s generation timeout, so the "fail over faster" intent missed its biggest
// targets entirely. TestTimeoutFor_ClassifiesEveryRealCallSite pins this.
// (Card-update detection has since outgrown the class: its reply is a whole
// section, ai.CardUpdateTokens, bounded by its own client instead.)
const shortCallTokens = 2048

// ShortCallTimeout bounds those short calls.
//
// Was 90s, which is ~7x the slowest healthy response measured (2.0-12.5s) and
// far too patient once a fallback chain exists. With 3 retries that is 6.2
// minutes of waiting on a degraded model BEFORE the chain even tries a healthy
// one -- observed in production on 2026-09-21, where a single enrichment pass
// spent minutes per field retrying a model that was timing out while a working
// alternative sat unused.
//
// Retrying hard is right when there is no alternative. When there is one,
// failing over quickly beats waiting. 30s is still 2.4x the slowest healthy
// response.
const ShortCallTimeout = 30 * time.Second

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
		token:          token,
		maxRetries:     maxRetries,
		BaseURL:        defaultBaseURL,
		httpClient:     &http.Client{},
		backoff:        initialBackoff,
		requestTimeout: DefaultRequestTimeout,
	}
}

// SetBackoff overrides the first retry delay. A non-positive value is ignored.
func (c *HFClient) SetBackoff(d time.Duration) {
	if d > 0 {
		c.backoff = d
	}
}

// timeoutFor sizes a call's deadline to the work it asks for.
//
// One timeout for every call cannot fit both: the generator writes 4096 tokens
// and legitimately takes minutes, while enrichment calls ask for 256 and should
// answer in seconds. Sharing the generator's budget means a hung 256-token call
// sits there for the full generator timeout before failing -- and since the
// scheduler runs articles back to back, that stalls everything behind it.
func (c *HFClient) timeoutFor(maxTokens int) time.Duration {
	t := c.requestTimeout
	if t <= 0 {
		t = DefaultRequestTimeout
	}
	if maxTokens <= shortCallTokens && t > ShortCallTimeout {
		return ShortCallTimeout
	}
	return t
}

// SetRequestTimeout overrides the per-call timeout. A non-positive value keeps
// the current one.
func (c *HFClient) SetRequestTimeout(d time.Duration) {
	if d > 0 {
		c.requestTimeout = d
	}
}

// WithThinkingDisabled returns a copy of the client whose requests carry
// chat_template_kwargs {"enable_thinking": false}, telling a hybrid reasoning
// model to answer directly. The receiver is not modified, so the copy can be
// handed to one role (the critic) while every other call keeps the default.
//
// It is per client rather than per model because the same model can serve
// several roles: nvidia/nemotron-3.5-super-vl-120b-a12b is at once the first
// critic, an enrichment model and a generator fallback, and only the critic
// call was measured with thinking off. With thinking on, its predecessor
// nemotron-3-super (retired 2026-10-20) truncated or leaked its reasoning into
// the critic's JSON and scored a real article 45; with it off it scored the
// same article 75, and a clean one 95, in 2-28s. On deepseek-v4.1-flash the
// same switch removed the reasoning output entirely (a 36-token reply). Both
// critics in production, nemotron-3.5-super-vl and nemotron-3.5-lightning,
// were measured with the field set on 2026-10-22.
func (c *HFClient) WithThinkingDisabled() *HFClient {
	cp := *c
	cp.disableThinking = true
	return &cp
}

// WithMaxRetries returns a copy of the client that retries a failed call at
// most n times. The original client is unchanged.
func (c *HFClient) WithMaxRetries(n int) *HFClient {
	cp := *c
	cp.maxRetries = n
	return &cp
}

// WithRequestTimeout returns a copy of the client whose calls above the
// short-call threshold are bounded by d rather than the client's own timeout.
// Short calls keep ShortCallTimeout as their ceiling. A non-positive d
// returns an unchanged copy. The original client is unchanged.
func (c *HFClient) WithRequestTimeout(d time.Duration) *HFClient {
	cp := *c
	if d > 0 {
		cp.requestTimeout = d
	}
	return &cp
}

// WithStep returns a copy of the client that reports every finished call to
// rec under the given model-health step (generator, critic, enrichment,
// infographic). The receiver is not modified, so one base client can be
// labelled once per step; copies made from a labelled client keep the label.
// A client without a step reports nothing, which is what tests, ab-models and
// the health check's own probes want.
func (c *HFClient) WithStep(step string, rec Recorder) *HFClient {
	cp := *c
	cp.step = step
	cp.recorder = rec
	return &cp
}

// ReportBadReply records that a call this client made answered, but with a
// reply its caller could not use: a critic's JSON that will not parse.
// Generate has already recorded the call itself as ok.
func (c *HFClient) ReportBadReply(model, detail string) {
	c.record(model, time.Now(), OutcomeBadReply, http.StatusOK, errors.New(detail))
}

// record reports one finished call to the client's recorder, if it has one.
func (c *HFClient) record(model string, start time.Time, o Outcome, status int, err error) {
	if c.recorder == nil || c.step == "" {
		return
	}
	c.recorder.RecordModelCall(ModelCall{
		Step: c.step, Model: model, Source: SourceCall, Outcome: o, HTTPStatus: status,
		Detail: outcomeDetail(err), At: time.Now().UTC(), Duration: time.Since(start),
	})
}

// chatRequest is the OpenAI-compatible request format.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature,omitempty"`
	// ChatTemplateKwargs is passed through to the model's chat template. Nil,
	// and so absent from the body, unless the client disables thinking.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the OpenAI-compatible response format.
// chatChoice is named rather than anonymous so tests construct it the same way
// production parses it. As an inline literal, every test that built a response
// had to mirror the field list exactly, and adding a field broke them all.
type chatChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	// FinishReason is "length" when max_tokens cut the answer off. Without
	// reading it, a half-sentence is indistinguishable from a short answer.
	FinishReason string `json:"finish_reason"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// stripThinkingTags removes <think>...</think> blocks from model responses.
// Some reasoning models (DeepSeek R1, QwQ) include thinking chains in their output.
func stripThinkingTags(s string) string {
	const open, close = "<think>", "</think>"
	for {
		start := strings.Index(s, open)
		if start == -1 {
			break
		}
		// Search for the closer AFTER the opener. Searching the whole string
		// finds a stray "</think>" that precedes it, giving end < start, and
		// then s[:start] + s[end+8:] reassembles the identical string -- the
		// loop makes no progress and spins forever at 100% CPU.
		//
		// That is not hypothetical: the caller holds the enrichment pass guard,
		// so a single such reply permanently wedges enrichment (every later
		// pass logs "skipped: previous pass still running") and blocks graceful
		// shutdown until systemd SIGKILLs the process.
		rel := strings.Index(s[start:], close)
		if rel == -1 {
			// Unclosed think tag — remove everything from <think> onward.
			s = strings.TrimSpace(s[:start])
			break
		}
		end := start + rel
		s = s[:start] + s[end+len(close):]
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
	if c.disableThinking {
		reqBody.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		metrics.AIAPIErrorsTotal.Inc()
		metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
		err = fmt.Errorf("marshal request: %w", err)
		c.record(model, start, OutcomeError, 0, err)
		return "", err
	}

	// Retries have to outlast a provider hiccup, not just a packet loss. The
	// old schedule was 100ms/200ms/400ms -- 700ms of total patience across all
	// three attempts -- and a degraded window of a few seconds on the provider
	// side blew straight through it. Observed 2026-09-21: four enrichment calls
	// (key details, Scout's Take, image query, image scoring) all returned
	// "500 after 3 retries" inside one 52-second window, and the identical
	// prompts succeeded on the same model minutes later. The article was still
	// published, silently missing its key-details table, Scout's Take and image.
	//
	// 2s/4s/8s is 14s of patience, which is nothing against generation runs
	// measured in minutes.
	backoff := c.backoff
	if backoff <= 0 {
		backoff = initialBackoff
	}

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeoutFor(maxTokens))
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			cancel()
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			err = fmt.Errorf("create request: %w", err)
			c.record(model, start, OutcomeError, 0, err)
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			cancel()
			// A timeout or a dropped connection is exactly what a retry is for,
			// yet this returned immediately for years: the loop below only ever
			// retried HTTP STATUS CODES, so a transport failure never got a
			// second attempt and the caller saw a hard error on the first blip.
			//
			// That is what broke article enrichment for two and a half months.
			// From 2026-07-01 the provider's latency crossed the client timeout
			// on roughly a quarter of calls; every one of those failed as
			// "context deadline exceeded", was never retried, and silently cost
			// the article its Scout's Take, key details or image.
			//
			// Context cancellation is excluded: that is our own shutdown, and
			// retrying it would just delay the exit.
			if attempt < c.maxRetries && !errors.Is(err, context.Canceled) {
				slog.Warn("ai api transport error, retrying",
					"model", model, "attempt", attempt, "err", err)
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			e := fmt.Errorf("http request: %w", err)
			c.record(model, start, OutcomeOf(e), 0, e)
			return "", e
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			e := fmt.Errorf("read response: %w", err)
			c.record(model, start, OutcomeOf(e), resp.StatusCode, e)
			return "", e
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
			apiErr := &APIError{StatusCode: resp.StatusCode, Model: model,
				Body: string(respBody), Retries: c.maxRetries, Kind: classifyStatus(resp.StatusCode)}
			c.record(model, start, apiErr.Kind, resp.StatusCode, apiErr)
			return "", apiErr
		}

		if resp.StatusCode != http.StatusOK {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			apiErr := &APIError{StatusCode: resp.StatusCode, Model: model,
				Body: string(respBody), Kind: classifyStatus(resp.StatusCode)}
			c.record(model, start, apiErr.Kind, resp.StatusCode, apiErr)
			return "", apiErr
		}

		var chatResp chatResponse
		if err := json.Unmarshal(respBody, &chatResp); err != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			e := fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
			c.record(model, start, OutcomeBadReply, resp.StatusCode, e)
			return "", e
		}

		// HTTP 200 carrying an error object. This is how an OpenAI-compatible
		// gateway can announce a retired model without ever using a 4xx, so it
		// must stay classifiable -- StatusCode 0 means "we do not know", and
		// APIError inspects the body for retirement wording.
		if chatResp.Error != nil {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			apiErr := &APIError{Model: model, Body: chatResp.Error.Message, Kind: OutcomeError}
			if apiErr.ModelGone() {
				apiErr.Kind = OutcomeRetired
			}
			c.record(model, start, apiErr.Kind, resp.StatusCode, apiErr)
			return "", apiErr
		}

		if len(chatResp.Choices) == 0 {
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			apiErr := &APIError{Model: model, Body: "no choices in response", Kind: OutcomeBadReply}
			c.record(model, start, OutcomeBadReply, resp.StatusCode, apiErr)
			return "", apiErr
		}

		content := chatResp.Choices[0].Message.Content
		// Strip <think>...</think> blocks from reasoning models (DeepSeek R1, etc.)
		content = stripThinkingTags(content)

		// Two ways a 200 can still be a failure, both worth another attempt.
		//
		// Empty content: a reasoning model can spend the whole token budget
		// thinking and return no answer. Measured on this provider at 256
		// tokens, openai/gpt-oss-20b does exactly that.
		//
		// Truncated content: finish_reason "length" means max_tokens cut the
		// answer off mid-sentence. That is the more dangerous of the two,
		// because the result is NON-empty and would otherwise be stored
		// verbatim -- nvidia/nemotron-3-super-120b-a12b at 256 tokens returns
		// things like "...if you carry one of those cards" and nothing
		// downstream would ever notice.
		finish := chatResp.Choices[0].FinishReason
		if strings.TrimSpace(content) == "" || finish == "length" {
			why := "model returned empty content (token budget may have gone to reasoning)"
			if finish == "length" {
				why = "model output truncated at max_tokens (finish_reason=length)"
			}
			if attempt < c.maxRetries {
				slog.Warn("ai api unusable response, retrying",
					"model", model, "attempt", attempt, "reason", why)
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			metrics.AIAPIErrorsTotal.Inc()
			metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
			apiErr := &APIError{Model: model, Body: why, Kind: OutcomeBadReply}
			c.record(model, start, OutcomeBadReply, resp.StatusCode, apiErr)
			return "", apiErr
		}

		metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
		c.record(model, start, OutcomeOK, resp.StatusCode, nil)
		return content, nil
	}

	slog.Error("ai api failed", "model", model, "error", "max retries exceeded")
	metrics.AIAPIErrorsTotal.Inc()
	metrics.AIAPIDuration.Observe(time.Since(start).Seconds())
	err = fmt.Errorf("max retries exceeded")
	c.record(model, start, OutcomeError, 0, err)
	return "", err
}
