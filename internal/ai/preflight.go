package ai

import (
	"fmt"
	"log/slog"
	"strings"
)

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
	var out []PreflightResult

	check := func(model, role string) {
		if strings.TrimSpace(model) == "" {
			return
		}
		// One token is enough: we are asking "does this model exist", not
		// "is it any good".
		_, err := client.Generate(model, "Reply with the single word: OK", 16)
		r := PreflightResult{Model: model, Role: role, OK: err == nil, Err: err}
		switch {
		case err == nil:
			slog.Info("preflight ok", "role", role, "model", model)
		case isModelUnavailableError(err):
			slog.Error("preflight FAILED: model no longer exists",
				"role", role, "model", model, "err", err)
		default:
			slog.Warn("preflight inconclusive (model may be temporarily unavailable)",
				"role", role, "model", model, "err", err)
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
