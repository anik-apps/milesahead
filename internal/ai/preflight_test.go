package ai

import (
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// budgetRecordingClient records the max_tokens each model was probed with and
// fails the models in dead.
type budgetRecordingClient struct {
	mu     sync.Mutex
	budget map[string]int
	dead   map[string]bool
}

func (c *budgetRecordingClient) Generate(model, _ string, maxTokens int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.budget == nil {
		c.budget = map[string]int{}
	}
	c.budget[model] = maxTokens
	if c.dead[model] {
		return "", &APIError{StatusCode: 410, Model: model, Body: "end of life"}
	}
	return "OK", nil
}

// TestPreflight_ProbesEachModelUnderItsRealTimeout guards against a false
// "no CRITIC is reachable". deepseek-ai/deepseek-v4.1-flash took 4m56s to
// answer the probe; probed at 512 tokens it got the 30s short-call timeout and
// would have been reported dead at every boot while verifying articles fine
// under its real 900s budget. Each role has to be probed in the timeout class
// its real calls get: article-path models long, short-call models short.
func TestPreflight_ProbesEachModelUnderItsRealTimeout(t *testing.T) {
	client := &budgetRecordingClient{}
	PreflightAll(client, "gen", []string{"fb"}, []string{"critic"},
		[]string{"vision"}, []string{"enrich-a", "enrich-b"})

	hf := NewHFClient("token", 0)
	hf.SetRequestTimeout(15 * time.Minute)
	for model, wantShort := range map[string]bool{
		"gen": false, "fb": false, "critic": false,
		"vision": true, "enrich-a": true, "enrich-b": true,
	} {
		budget, probed := client.budget[model]
		if !probed {
			t.Errorf("%s was not probed", model)
			continue
		}
		if short := hf.timeoutFor(budget) == ShortCallTimeout; short != wantShort {
			t.Errorf("%s probed with max_tokens=%d (short timeout=%v); want short=%v",
				model, budget, short, wantShort)
		}
	}
}

// TestPreflight_ExportsReachableModelsByRole: the retired critic logged an
// ERROR at every boot for two days and nobody saw it. The gauge makes
// role="critic" == 0 alertable.
func TestPreflight_ExportsReachableModelsByRole(t *testing.T) {
	client := &budgetRecordingClient{dead: map[string]bool{"old-critic": true, "enrich-a": true}}
	results := PreflightAll(client, "gen", nil, []string{"old-critic"},
		[]string{"vision"}, []string{"enrich-a", "enrich-b"})

	if PublishingViable(results) {
		t.Error("the only critic is dead; publishing must not be reported viable")
	}
	for role, want := range map[string]float64{
		"generator": 1, "critic": 0, "vision": 1, "enrichment": 1,
	} {
		if got := preflightGauge(t, role); got != want {
			t.Errorf("reachable{role=%q} = %v, want %v", role, got, want)
		}
	}
}

func preflightGauge(t *testing.T, role string) float64 {
	t.Helper()
	g, err := metrics.AIPreflightModelsReachable.GetMetricWithLabelValues(role)
	if err != nil {
		t.Fatalf("gauge lookup: %v", err)
	}
	var m dto.Metric
	if err := g.Write(&m); err != nil {
		t.Fatalf("gauge read: %v", err)
	}
	return m.GetGauge().GetValue()
}
