package ai

import (
	"fmt"
	"strings"
)

// APIError is a structured failure from the inference API.
//
// Classification used to be done by substring-matching the formatted error
// string, which is unsound in both directions: a 400 whose response body merely
// mentions "status 404" was read as a retired model and burned the whole
// fallback chain, while a genuine retirement delivered as HTTP 200 with an
// error body carried no status token at all and was treated as unrecoverable.
// The status lives in a field now, so callers switch on data rather than prose.
type APIError struct {
	// StatusCode is the HTTP status, or 0 when the transport succeeded but the
	// body carried an error object (OpenAI-compatible gateways do this).
	StatusCode int
	Model      string
	Body       string
	Retries    int
	// Kind is how model health classifies the failure, set where Generate
	// returns it. It is empty on APIErrors built elsewhere (tests, fakes);
	// OutcomeOf falls back to the status code then.
	Kind Outcome
}

func (e *APIError) Error() string {
	switch {
	case e.StatusCode == 0:
		return fmt.Sprintf("api error (model %s): %s", e.Model, e.Body)
	case e.Retries > 0:
		return fmt.Sprintf("api error (status %d, model %s) after %d retries: %s",
			e.StatusCode, e.Model, e.Retries, e.Body)
	default:
		return fmt.Sprintf("api error (status %d, model %s): %s",
			e.StatusCode, e.Model, e.Body)
	}
}

// bodyNamesRetirement matches providers that describe a dead model in prose
// rather than with a status code. Deliberately narrow: it is only consulted
// when there is no status code to switch on.
func (e *APIError) bodyNamesRetirement() bool {
	b := strings.ToLower(e.Body)
	for _, phrase := range []string{
		"end of life", "no longer available", "has been retired",
		"model not found", "unknown model", "does not exist",
		"is not available", "deprecated and removed",
	} {
		if strings.Contains(b, phrase) {
			return true
		}
	}
	return false
}

// ModelGone reports that this specific model is unavailable, so another model
// in the chain may well succeed.
func (e *APIError) ModelGone() bool {
	switch e.StatusCode {
	case 404, 410:
		return true
	case 0:
		return e.bodyNamesRetirement()
	}
	return false
}

// Transient reports a fault likely to pass, or to be absent on another model.
//
// A status-less API error counts: we cannot tell what went wrong, and the cost
// of trying the next model is a few seconds, whereas the cost of being wrong in
// the other direction was 26 days of silence.
func (e *APIError) Transient() bool {
	switch {
	case e.StatusCode == 429:
		return true
	case e.StatusCode == 501 || e.StatusCode == 505:
		// Permanent by definition; every model behind this gateway will agree.
		return false
	case e.StatusCode >= 500:
		return true
	case e.StatusCode == 0:
		return !e.bodyNamesRetirement()
	}
	return false
}
