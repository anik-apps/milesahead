package main

import (
	"reflect"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
)

func TestInfographicModels(t *testing.T) {
	enrich := []string{"e1", "e2"}
	cfg := &config.Config{}
	if got := infographicModels(cfg, enrich); !reflect.DeepEqual(got, enrich) {
		t.Errorf("unset: %v, want the enrichment chain", got)
	}
	cfg.AI.InfographicModel = "c1, c2"
	if got := infographicModels(cfg, enrich); !reflect.DeepEqual(got, []string{"c1", "c2"}) {
		t.Errorf("set: %v, want [c1 c2]", got)
	}
}

// Production draws charts with nemotron-3-super first and gpt-oss-20b behind
// it (see config.example.yaml), and shows them on article pages.
func TestDeployConfig_InfographicChain(t *testing.T) {
	stubReachable(t, false)
	cfg := deployConfig(t, nil)
	want := []string{"nvidia/nemotron-3-super-120b-a12b", "openai/gpt-oss-20b"}
	if got := infographicModels(cfg, enrichmentModels(cfg)); !reflect.DeepEqual(got, want) {
		t.Errorf("chart chain = %v, want %v", got, want)
	}
	if !cfg.AI.DrawInfographics() || !cfg.AI.ShowInfographics() {
		t.Errorf("enrichment_infographics = %q; the deploy config should draw charts and show them", cfg.AI.EnrichmentInfographics)
	}
}

// The boot preflight checks the chart chain too, once charts are drawn, and
// checks each model once.
func TestPreflightEnrichmentModels(t *testing.T) {
	enrich := []string{"e1", "e2"}
	cfg := &config.Config{}
	cfg.AI.InfographicModel = "c1,e2"
	cfg.AI.EnrichmentInfographics = config.InfographicsOff
	if got := preflightEnrichmentModels(cfg, enrich); !reflect.DeepEqual(got, enrich) {
		t.Errorf("charts off: %v, want only the enrichment chain", got)
	}
	cfg.AI.EnrichmentInfographics = config.InfographicsPreview
	if got := preflightEnrichmentModels(cfg, enrich); !reflect.DeepEqual(got, []string{"e1", "e2", "c1"}) {
		t.Errorf("charts drawn: %v, want [e1 e2 c1]", got)
	}
	if !reflect.DeepEqual(enrich, []string{"e1", "e2"}) {
		t.Errorf("the enrichment chain was modified: %v", enrich)
	}
}
