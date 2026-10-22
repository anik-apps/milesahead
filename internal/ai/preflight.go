package ai

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// preflightMaxTokens bounds the probe response for models the app calls with
// a short budget (vision, the enrichment chain). See the note on probe in
// PreflightAllWithCriticClient: too small a budget makes reasoning models look dead.
const preflightMaxTokens = 512

// articleProbeTokens is the probe budget for models on the article path
// (generator, fallbacks, critic). Those are called with 4096 tokens and so get
// the full request timeout; probing them with 512 capped the probe at
// ShortCallTimeout instead.
//
// That mismatch reported a working critic as dead. deepseek-ai/deepseek-v4.1-flash
// took 4m56s to answer "OK" on 2026-09-29 -- well inside its real 900s budget,
// far outside 30s -- so a 512-token probe would have logged "no CRITIC is
// reachable" at every boot while articles verified fine. A probe has to run
// under the same timeout the model will actually get. max_tokens is only a
// ceiling, so the larger budget costs nothing on a model that answers "OK".
const articleProbeTokens = 4096

// PreflightResult reports whether one configured model actually answers.
type PreflightResult struct {
	Model string
	Role  string // "generator", "fallback", "critic", "vision", "enrichment"
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
	return PreflightAll(client, generator, fallbacks, critics, nil, nil)
}

// PreflightAll additionally probes auxiliary models such as the vision model
// used for image scoring. That model was hardcoded and silently dead, timing
// out on every call -- the one failure the original preflight would have missed.
//
// enrichment is the short-call chain (enrichment, image text-scoring, the
// newsletter pick, card-update detection). Its models are probed under the
// short-call timeout they run with; a dead one is logged like any other, but
// it does not affect GenerationViable or PublishingViable.
//
// The results are also exported as milesahead_ai_preflight_models_reachable.
func PreflightAll(client HFClientInterface, generator string, fallbacks, critics, others, enrichment []string) []PreflightResult {
	return PreflightAllWithCriticClient(client, client, generator, fallbacks, critics, others, enrichment)
}

// PreflightAllWithCriticClient is PreflightAll with the critics probed through
// their own client -- the one the Critic really calls them with, which may
// disable thinking (see HFClient.WithThinkingDisabled). A probe has to send the
// request shape the role will send, or it can pass a model the real call fails.
//
// critics is the whole critic chain; one reachable critic is enough for
// PublishingViable, and the role="critic" gauge counts how many answered.
func PreflightAllWithCriticClient(client, criticClient HFClientInterface, generator string,
	fallbacks, critics, others, enrichment []string) []PreflightResult {
	type probe struct {
		client      HFClientInterface
		model, role string
		maxTokens   int
	}
	var probes []probe
	add := func(c HFClientInterface, model, role string, maxTokens int) {
		if strings.TrimSpace(model) != "" {
			probes = append(probes, probe{c, model, role, maxTokens})
		}
	}
	if criticClient == nil {
		criticClient = client
	}
	add(client, generator, "generator", articleProbeTokens)
	for _, f := range fallbacks {
		add(client, f, "fallback", articleProbeTokens)
	}
	for _, c := range critics {
		add(criticClient, c, "critic", articleProbeTokens)
	}
	for _, o := range others {
		add(client, o, "vision", preflightMaxTokens)
	}
	for _, e := range enrichment {
		add(client, e, "enrichment", preflightMaxTokens)
	}

	// Concurrently: the health check runs this every hour, and an article-path
	// probe can take the full request timeout, so a round lasts as long as its
	// slowest model rather than the sum of them. Results keep probe order.
	out := make([]PreflightResult, len(probes))
	var wg sync.WaitGroup
	for i, p := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// The budget has to leave room for a reasoning model to think before it
			// answers. At 16 tokens openai/gpt-oss-20b spends the whole allowance
			// reasoning and returns empty content, which this check then reports as
			// a dead model -- a false alarm on a model that generates articles
			// perfectly well. Measured on that model: 16 tokens empty, 128 and
			// above returns "OK". 512 leaves a wide margin without being a
			// meaningful cost once per boot. Article-path models get more; see
			// articleProbeTokens.
			_, err := p.client.Generate(p.model, "Reply with the single word: OK", p.maxTokens)
			out[i] = PreflightResult{Model: p.model, Role: p.role, OK: err == nil, Err: err}
			if err == nil {
				slog.Info("preflight ok", "role", p.role, "model", p.model)
				return
			}
			// Every configured model is load-bearing: a dead critic stops
			// publication just as completely as a dead generator, because
			// Pipeline.Process returns an error when verification fails. So
			// this is ERROR regardless of role and regardless of whether the
			// cause looks permanent -- WARN-level reporting is what let the
			// previous outage run for 26 days.
			slog.Error("preflight FAILED: configured model did not answer",
				"role", p.role, "model", p.model, "permanent", isModelUnavailableError(err),
				"err", err)
		}()
	}
	wg.Wait()
	recordPreflight(out)
	return out
}

// recordPreflight exports how many models answered per role, so a dead critic
// is alertable rather than one ERROR line at boot: the 2026-09-28 critic
// retirement logged that line at every startup for two days without anyone
// seeing it. role="critic" == 0 is the condition to alert on.
//
// Only configured roles are set, and the gauge is absent until the preflight
// finishes -- with a slow critic, minutes after boot.
func recordPreflight(results []PreflightResult) {
	reachable := map[string]int{}
	for _, r := range results {
		n := reachable[r.Role] // a role whose models all failed must still read 0
		if r.OK {
			n++
		}
		reachable[r.Role] = n
	}
	metrics.AIPreflightModelsReachable.Reset()
	for role, n := range reachable {
		metrics.AIPreflightModelsReachable.WithLabelValues(role).Set(float64(n))
	}
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
// critic publishes exactly nothing. The critic is a chain now, so any one
// reachable critic is enough; it was a single model until that model was
// retired (2026-09-28) and its replacement overloaded (2026-09-30).
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
