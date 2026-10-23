// Package health tracks whether each AI step's models answer: real calls and
// hourly checks fold into one row per (step, model), lists are judged ok,
// degraded or down, and the admin is emailed when that changes.
package health

import (
	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
)

// Step names, as stored in model_health and shown in the admin.
const (
	StepGenerator   = "generator"
	StepCritic      = "critic"
	StepEnrichment  = "enrichment"
	StepInfographic = "infographic"
	StepVision      = "vision"
)

// DefaultVisionModel is used when vision_model is unset.
const DefaultVisionModel = "meta/llama-3.2-11b-vision-instruct"

// Step is one model list and the jobs that use it.
type Step struct {
	Name, Label string
	Uses        string   // the jobs that call this list, for the admin page
	Models      []string // tried in order
	// Roles are the preflight roles whose results are this step's checks.
	Roles []string
	// Off: switched off in config, so not checked and shown as off.
	Off bool
	// StopsPublishing: nothing is published while this step is down.
	StopsPublishing bool
}

// EnrichmentModels is the chain for every short AI call: enrichment, image
// text-scoring, the newsletter pick, card-update detection and the pipeline's
// inline key-details call. It is enrichment_model, else the generator
// fallbacks then the critics.
//
// Those calls asked for 512-2048 tokens, so HFClient bounds them at
// ai.ShortCallTimeout (30s). Card-update detection now returns a whole section
// and asks for more, so it goes through newCardUpdateClient in cmd/milesahead
// (120s) instead.
// Most of them used to go to the critic model, which failed them all when
// mistralai/mistral-nemotron was retired on 2026-09-28; its first replacement
// took minutes per reply and would have timed out every one. The critic is for
// verification only.
//
// Unset, it falls back to the generator fallback chain rather than silently
// doing nothing. Those models are the fast ones; the critic chain goes last.
func EnrichmentModels(cfg *config.Config) []string {
	if models := ai.SplitModels(cfg.AI.EnrichmentModel); len(models) > 0 {
		return models
	}
	return append(ai.SplitModels(cfg.AI.FallbackGeneratorModel), ai.SplitModels(cfg.AI.CriticModel)...)
}

// InfographicModels is the chart chain: infographic_model, else enrichment's.
func InfographicModels(cfg *config.Config) []string {
	if models := ai.SplitModels(cfg.AI.InfographicModel); len(models) > 0 {
		return models
	}
	return EnrichmentModels(cfg)
}

// VisionModels is the image-scoring chain: vision_model, else the default.
func VisionModels(cfg *config.Config) []string {
	if models := ai.SplitModels(cfg.AI.VisionModel); len(models) > 0 {
		return models
	}
	return []string{DefaultVisionModel}
}

// Steps is every model list the app calls, in the order the admin shows
// them when all are healthy. It is the one definition the wiring, the check,
// the alerts and the page use.
func Steps(cfg *config.Config) []Step {
	generator := append(ai.SplitModels(cfg.AI.GeneratorModel), ai.SplitModels(cfg.AI.FallbackGeneratorModel)...)
	return []Step{
		{Name: StepGenerator, Label: "Generator", Uses: "Articles and revisions",
			Models: generator, Roles: []string{"generator", "fallback"}, StopsPublishing: true},
		{Name: StepCritic, Label: "Critic", Uses: "Verifying every article",
			Models: ai.SplitModels(cfg.AI.CriticModel), Roles: []string{"critic"}, StopsPublishing: true},
		{Name: StepEnrichment, Label: "Enrichment",
			Uses:   "Scout's Take, key details, tags, image queries and scoring, newsletter pick, card updates, story dedup",
			Models: EnrichmentModels(cfg), Roles: []string{"enrichment"}},
		{Name: StepInfographic, Label: "Infographic", Uses: "Charts",
			Models: InfographicModels(cfg), Roles: []string{"enrichment"}, Off: !cfg.AI.DrawInfographics()},
		{Name: StepVision, Label: "Vision", Uses: "Image scoring",
			Models: VisionModels(cfg), Roles: []string{"vision"}, Off: cfg.AI.HFBaseURL == ""},
	}
}
