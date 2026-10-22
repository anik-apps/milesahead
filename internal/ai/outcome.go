package ai

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Outcome is how one model call ended, as model health records it.
type Outcome string

const (
	OutcomeOK         Outcome = "ok"
	OutcomeRetired    Outcome = "retired"    // 404, 410, or a body naming a retirement
	OutcomeAuth       Outcome = "auth"       // 401, 402, 403: the key, not the model
	OutcomeTimeout    Outcome = "timeout"    // no answer in time, or a gateway 504
	OutcomeOverloaded Outcome = "overloaded" // 429 or another 5xx
	OutcomeBadReply   Outcome = "bad_reply"  // empty, cut off, or unparseable
	OutcomeError      Outcome = "error"      // anything else
)

// Where a ModelCall came from.
const (
	SourceCall  = "call"  // a real call made by a job
	SourceCheck = "check" // the model health check
)

// ModelCall is one finished model call, after the client's own retries, or
// one health check of a model.
type ModelCall struct {
	Step       string
	Model      string
	Source     string
	Outcome    Outcome
	HTTPStatus int    // 0 when there was no response
	Detail     string // the error, first 300 bytes; empty on ok
	At         time.Time
	Duration   time.Duration
}

// Recorder receives finished model calls. RecordModelCall runs on the
// caller's goroutine, inside an AI call, so it must not block.
type Recorder interface {
	RecordModelCall(ModelCall)
}

// classifyStatus is the outcome for an HTTP status other than 200.
func classifyStatus(status int) Outcome {
	switch {
	case status == 404 || status == 410:
		return OutcomeRetired
	case status == 401 || status == 402 || status == 403:
		return OutcomeAuth
	case status == 504:
		return OutcomeTimeout
	case status == 429 || status >= 500:
		return OutcomeOverloaded
	}
	return OutcomeError
}

// OutcomeOf classifies an error returned by HFClient.Generate, or by anything
// that wraps one. A nil error is OutcomeOK.
func OutcomeOf(err error) Outcome {
	if err == nil {
		return OutcomeOK
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Kind != "":
			return apiErr.Kind
		case apiErr.ModelGone():
			return OutcomeRetired
		case apiErr.StatusCode != 0:
			return classifyStatus(apiErr.StatusCode)
		}
		return OutcomeError
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
		return OutcomeTimeout
	}
	if strings.Contains(err.Error(), "unmarshal response") {
		return OutcomeBadReply
	}
	return OutcomeError
}

// outcomeDetail is err's text, cut to what model health stores.
func outcomeDetail(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
