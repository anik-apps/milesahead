package ai

import (
	"reflect"
	"sort"
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

// TestPreflight_CriticChain: the critic is a chain, so one reachable critic is
// enough to publish, and the gauge counts how many answered. Critics are
// probed through the critic's own client (which may disable thinking), under
// the article-path timeout; nothing else is.
func TestPreflight_CriticChain(t *testing.T) {
	base := &budgetRecordingClient{}
	critics := &budgetRecordingClient{dead: map[string]bool{"busy-critic": true}}
	results := PreflightAllWithCriticClient(base, critics, "gen", []string{"fb"},
		[]string{"busy-critic", "live-critic"}, []string{"vision"}, []string{"enrich"})

	if !PublishingViable(results) {
		t.Errorf("one critic of two is alive; publishing must be viable: %s", PreflightSummary(results))
	}
	if got := preflightGauge(t, "critic"); got != 1 {
		t.Errorf("reachable{role=\"critic\"} = %v, want 1", got)
	}
	for _, m := range []string{"busy-critic", "live-critic"} {
		if critics.budget[m] != articleProbeTokens {
			t.Errorf("critic %s probed with %d tokens on the critic client; want %d",
				m, critics.budget[m], articleProbeTokens)
		}
		if _, onBase := base.budget[m]; onBase {
			t.Errorf("critic %s was probed through the base client", m)
		}
	}
	for _, m := range []string{"gen", "fb", "vision", "enrich"} {
		if _, onCritic := critics.budget[m]; onCritic {
			t.Errorf("%s was probed through the critic client", m)
		}
	}

	// Both dead: not viable, gauge 0.
	critics = &budgetRecordingClient{dead: map[string]bool{"busy-critic": true, "live-critic": true}}
	results = PreflightAllWithCriticClient(base, critics, "gen", nil,
		[]string{"busy-critic", "live-critic"}, nil, nil)
	if PublishingViable(results) {
		t.Error("every critic is dead; publishing must not be viable")
	}
	if got := preflightGauge(t, "critic"); got != 0 {
		t.Errorf("reachable{role=\"critic\"} = %v, want 0", got)
	}
}

// TestPreflight_EachRoleOnItsOwnClient: vision and enrichment models are
// probed through the clients their real calls use (thinking off, in
// production), and a role with no client of its own falls back to Default.
func TestPreflight_EachRoleOnItsOwnClient(t *testing.T) {
	base, vision, enrich := &budgetRecordingClient{}, &budgetRecordingClient{}, &budgetRecordingClient{}
	PreflightAllWithClients(PreflightClients{Default: base, Vision: vision, Enrichment: enrich},
		"gen", []string{"fb"}, []string{"critic"}, []string{"vis"}, []string{"enr"})

	for client, want := range map[*budgetRecordingClient][]string{
		base:   {"critic", "fb", "gen"},
		vision: {"vis"},
		enrich: {"enr"},
	} {
		var got []string
		for m := range client.budget {
			got = append(got, m)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("probed %v on one client; want %v", got, want)
		}
	}
	if enrich.budget["enr"] != preflightMaxTokens || vision.budget["vis"] != preflightMaxTokens {
		t.Errorf("budgets: enrichment %d, vision %d; want %d", enrich.budget["enr"], vision.budget["vis"], preflightMaxTokens)
	}
}

type sleepyClient struct{ d time.Duration }

func (c sleepyClient) Generate(string, string, int) (string, error) {
	time.Sleep(c.d)
	return "OK", nil
}

// A round lasts as long as its slowest model, not the sum: the hourly check
// probes every list, and article-path models get the full request timeout.
func TestPreflight_ProbesConcurrentlyInOrder(t *testing.T) {
	start := time.Now()
	res := PreflightAllWithCriticClient(sleepyClient{150 * time.Millisecond}, nil,
		"gen", []string{"fb"}, []string{"c1", "c2"}, nil, []string{"e"})
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("5 probes of 150ms took %v; they are not concurrent", el)
	}
	want := []string{"generator=gen", "fallback=fb", "critic=c1", "critic=c2", "enrichment=e"}
	if len(res) != len(want) {
		t.Fatalf("got %d results, want %d", len(res), len(want))
	}
	for i, r := range res {
		if got := r.Role + "=" + r.Model; got != want[i] || !r.OK {
			t.Errorf("result %d = %s ok=%v, want %s ok", i, got, r.OK, want[i])
		}
	}
}
