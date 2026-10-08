package config

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
)

// TestExampleConfigLoads keeps config.example.yaml honest: it is the documented
// reference and the template deploy.yml mirrors, so a typo there is shipped
// straight to production. It also pins the three model roles to non-empty
// values, since an empty generator or critic silently stops publication.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml must parse: %v", err)
	}
	for _, f := range []struct{ name, val string }{
		{"generator_model", cfg.AI.GeneratorModel},
		{"critic_model", cfg.AI.CriticModel},
		{"vision_model", cfg.AI.VisionModel},
		{"fallback_generator_model", cfg.AI.FallbackGeneratorModel},
	} {
		if strings.TrimSpace(f.val) == "" {
			t.Errorf("%s must not be empty in the example config", f.name)
		}
	}
	// The generator and critic must differ: a model grading its own output is
	// not a quality gate. critic_model is a chain, so no critic in it may be
	// the primary generator. (The generator FALLBACK chain does share
	// nemotron-3-super with the critic chain; that caveat is documented in
	// config.example.yaml rather than forbidden, because no other measured
	// critic scores usable articles above the threshold.)
	critics := ai.SplitModels(cfg.AI.CriticModel)
	for _, c := range critics {
		if c == cfg.AI.GeneratorModel {
			t.Errorf("critic %q is the generator (no self-grading)", c)
		}
	}
	// The critic was a single model twice in two days (retired 2026-09-28,
	// overloaded 2026-09-30), and a critic error drops the article outright.
	if len(critics) < 2 {
		t.Errorf("critic chain is %d deep; configure at least two", len(critics))
	}
	// Every critic score in the example's rationale was measured with thinking
	// off; with it on, nemotron-3-super scored 45 and leaked reasoning.
	if !cfg.AI.CriticDisableThinking {
		t.Error("critic_disable_thinking must be true: the documented critic " +
			"measurements were all taken with thinking off")
	}
	if cfg.AI.CriticThreshold <= 0 || cfg.AI.CriticThreshold > 100 {
		t.Errorf("critic_threshold %d out of range", cfg.AI.CriticThreshold)
	}
	// The incident was the primary AND its only fallback dying together, so a
	// one-deep chain is no more resilient than the config that failed.
	if n := len(strings.Split(cfg.AI.FallbackGeneratorModel, ",")); n < 2 {
		t.Errorf("fallback chain is %d deep; configure at least two so one "+
			"retirement cannot stop publication", n)
	}
	// A pass with no bound can outlast its own interval, at which point every
	// later tick is skipped and the reconciler looks idle rather than stuck.
	if cfg.AI.EnrichmentMaxDuration <= 0 {
		t.Error("enrichment_max_duration must be set; an unbounded pass cannot " +
			"be distinguished from a stalled one")
	}
	if cfg.AI.EnrichmentInterval > 0 &&
		cfg.AI.EnrichmentMaxDuration > cfg.AI.EnrichmentInterval {
		t.Errorf("enrichment_max_duration (%dm) exceeds enrichment_interval (%dm); "+
			"passes would routinely overlap and be skipped",
			cfg.AI.EnrichmentMaxDuration, cfg.AI.EnrichmentInterval)
	}
	// The short-call chain must not lean on the critic: the critic's
	// retirement on 2026-09-28 took down every side job that shared it.
	//
	// This used to forbid the critic anywhere in enrichment_model. Both are
	// chains now, and nvidia/nemotron-3-super-120b-a12b is legitimately in
	// both: first critic (fast with thinking off) and enrichment's second
	// model. What the rule protects is still enforced -- enrichment must not
	// be LED by the primary critic, so when the critic's backend degrades,
	// enrichment's first choice is unaffected and the side jobs only lose a
	// fallback. (Enrichment requests also never carry the critic's
	// thinking-off setting.)
	if enrich := ai.SplitModels(cfg.AI.EnrichmentModel); len(enrich) > 0 && len(critics) > 0 &&
		enrich[0] == critics[0] {
		t.Errorf("enrichment_model is led by the primary critic %q; short calls "+
			"must lead with a model of their own", enrich[0])
	}
	// The story check ships observing; blocking is switched on only after
	// its shadow decisions have been reviewed.
	if cfg.Scanner.StoryDedup != StoryDedupShadow {
		t.Errorf("scanner.story_dedup = %q, want %q", cfg.Scanner.StoryDedup, StoryDedupShadow)
	}
	if cfg.AI.RequestTimeout <= 0 {
		t.Error("request_timeout must be set explicitly; the default can be " +
			"shorter than the primary model's real latency")
	}
}

// TestDeployConfigMirrorsCritic pins the production critic settings to the
// documented ones. deploy.yml writes its own config heredoc, so a change to
// config.example.yaml alone would ship nothing.
func TestDeployConfigMirrorsCritic(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../.github/workflows/deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	deploy := string(raw)
	want := []string{
		fmt.Sprintf("critic_model: %q", cfg.AI.CriticModel),
		fmt.Sprintf("critic_disable_thinking: %v", cfg.AI.CriticDisableThinking),
		fmt.Sprintf("story_dedup: %q", cfg.Scanner.StoryDedup),
		// Infographics are set the same in both: changing one alone fails here.
		fmt.Sprintf("enrichment_infographics: %q", cfg.AI.EnrichmentInfographics),
		fmt.Sprintf("infographic_model: %q", cfg.AI.InfographicModel),
	}
	for _, w := range want {
		if !strings.Contains(deploy, w) {
			t.Errorf("deploy.yml does not contain %s (from config.example.yaml)", w)
		}
	}
}
