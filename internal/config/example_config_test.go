package config

import (
	"strings"
	"testing"
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
	// not a quality gate.
	if cfg.AI.GeneratorModel == cfg.AI.CriticModel {
		t.Error("generator and critic must be different models (no self-grading)")
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
	if cfg.AI.RequestTimeout <= 0 {
		t.Error("request_timeout must be set explicitly; the default can be " +
			"shorter than the primary model's real latency")
	}
}
