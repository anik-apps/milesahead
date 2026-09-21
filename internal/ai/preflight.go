package ai

import (
	"fmt"
	"log/slog"
	"strings"
)

// preflightMaxTokens bounds the probe response. See the note in Preflight's
// check function: too small a budget makes reasoning models look dead.
const preflightMaxTokens = 512

// PreflightResult reports whether one configured model actually answers.
type PreflightResult struct {
	Model string
	Role  string // "generator", "fallback", "critic"
	OK    bool
	Err   error
}

// Preflight verifies that every configured model still exists before the
// scheduler starts calling them.
//
// This exists because the provider's own /v1/models catalogue is not a liveness
// signal: of 81 models it listed, 49 looked chat-capable and only 9 answered --
// the rest returned 404, timed out, or were resource-exhausted. A model can also
// be retired mid-deployment and answer 410 Gone forever after. Discovering that
// from article-generation logs took 26 days; discovering it at boot takes one
// round trip per model.
//
// It never blocks startup. The website serving cards and existing articles is
// independent of generation, and refusing to boot would turn a content outage
// into a site outage.
func Preflight(client HFClientInterface, generator string, fallbacks []string, critics ...string) []PreflightResult {
	return PreflightAll(client, generator, fallbacks, critics, nil)
}

// PreflightAll additionally probes auxiliary models such as the vision model
// used for image scoring. That model was hardcoded and silently dead, timing
// out on every call -- the one failure the original preflight would have missed.
func PreflightAll(client HFClientInterface, generator string, fallbacks, critics, others []string) []PreflightResult {
	var out []PreflightResult

	check := func(model, role string) {
		if strings.TrimSpace(model) == "" {
			return
		}
		// The budget has to leave room for a reasoning model to think before it
		// answers. At 16 tokens openai/gpt-oss-20b spends the whole allowance
		// reasoning and returns empty content, which this check then reports as
		// a dead model -- a false alarm on a model that generates articles
		// perfectly well. Measured on that model: 16 tokens empty, 128 and
		// above returns "OK". 512 leaves a wide margin without being a
		// meaningful cost once per boot.
		_, err := client.Generate(model, "Reply with the single word: OK", preflightMaxTokens)
		r := PreflightResult{Model: model, Role: role, OK: err == nil, Err: err}
		switch {
		case err == nil:
			slog.Info("preflight ok", "role", role, "model", model)
		default:
			// Every configured model is load-bearing: a dead critic stops
			// publication just as completely as a dead generator, because
			// Pipeline.Process returns an error when verification fails. So
			// this is ERROR regardless of role and regardless of whether the
			// cause looks permanent -- WARN-level reporting is what let the
			// previous outage run for 26 days.
			slog.Error("preflight FAILED: configured model did not answer",
				"role", role, "model", model, "permanent", isModelUnavailableError(err),
				"err", err)
		}
		out = append(out, r)
	}

	check(generator, "generator")
	for _, f := range fallbacks {
		check(f, "fallback")
	}
	for _, c := range critics {
		check(c, "critic")
	}
	for _, o := range others {
		check(o, "vision")
	}
	return out
}

// GenerationViable reports whether at least one generator-capable model
// answered, i.e. whether the pipeline can produce anything at all.
func GenerationViable(results []PreflightResult) bool {
	for _, r := range results {
		if r.OK && (r.Role == "generator" || r.Role == "fallback") {
			return true
		}
	}
	return false
}

// PublishingViable reports whether the pipeline can complete end to end.
//
// Generation alone is not enough: Pipeline.Process verifies every article and
// returns an error if the critic call fails, so a live generator with a dead
// critic publishes exactly nothing. The critic is also the one role with no
// fallback chain, which makes it the most fragile link, and it was one of the
// three models retired in the incident this check exists to catch.
func PublishingViable(results []PreflightResult) bool {
	critic := false
	for _, r := range results {
		if r.OK && r.Role == "critic" {
			critic = true
		}
	}
	return GenerationViable(results) && critic
}

// PreflightSummary renders results for a single log line.
func PreflightSummary(results []PreflightResult) string {
	var ok, dead []string
	for _, r := range results {
		if r.OK {
			ok = append(ok, r.Role+"="+r.Model)
		} else {
			dead = append(dead, r.Role+"="+r.Model)
		}
	}
	return fmt.Sprintf("%d/%d models reachable; ok=[%s] unreachable=[%s]",
		len(ok), len(results), strings.Join(ok, " "), strings.Join(dead, " "))
}
