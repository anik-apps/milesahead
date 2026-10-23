package health

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

func view(name, label string, publishing bool, state StepState, models ...ModelView) StepView {
	v := StepView{Step: Step{Name: name, Label: label, StopsPublishing: publishing}, State: state, Models: models}
	if state == StepOK && len(models) > 1 {
		v.NoBackup = true
		for _, m := range models[1:] {
			if !m.State.Dead() {
				v.NoBackup = false
			}
		}
	}
	return v
}

func mv(model string, s ModelState) ModelView {
	return ModelView{Model: model, State: s, FailingSince: t0}
}

func asMap(rows []db.ModelAlert) map[string]db.ModelAlert {
	m := map[string]db.ModelAlert{}
	for _, r := range rows {
		m[r.Key] = r
	}
	return m
}

func TestPlan_DeadBackupWarnsOnce(t *testing.T) {
	views := []StepView{view(StepCritic, "Critic", true, StepOK, mv("super", ModelHealthy), mv("kimi", ModelRetired))}
	events, next := Plan(views, false, nil, t0)
	if len(events) != 1 || events[0].Severity != SeverityWarning || events[0].Key != "backup:critic:kimi" {
		t.Fatalf("events %+v", events)
	}
	if again, _ := Plan(views, false, asMap(next), t0.Add(time.Hour)); len(again) != 0 {
		t.Errorf("warned again: %+v", again)
	}
}

func TestPlan_PublishingStepDownIsUrgentAndRepeatsDaily(t *testing.T) {
	// The generator still answers, so this is the critic's problem, not the provider's.
	views := []StepView{
		view(StepGenerator, "Generator", true, StepOK, mv("gen", ModelHealthy)),
		view(StepCritic, "Critic", true, StepDown, mv("super", ModelRetired), mv("kimi", ModelRetired)),
	}
	events, next := Plan(views, false, nil, t0)
	if len(events) != 1 || events[0].Severity != SeverityUrgent || !strings.Contains(events[0].Title, "publishing has stopped") {
		t.Fatalf("events %+v", events)
	}
	if e, _ := Plan(views, false, asMap(next), t0.Add(23*time.Hour)); len(e) != 0 {
		t.Errorf("repeated within a day: %+v", e)
	}
	e, _ := Plan(views, false, asMap(next), t0.Add(25*time.Hour))
	if len(e) != 1 || !strings.HasPrefix(e[0].Title, "Still: ") {
		t.Errorf("no daily repeat: %+v", e)
	}
}

func TestPlan_OtherStepDownIsAWarning(t *testing.T) {
	views := []StepView{view(StepEnrichment, "Enrichment", false, StepDown, mv("a", ModelRetired))}
	events, _ := Plan(views, false, nil, t0)
	if len(events) != 1 || events[0].Severity != SeverityWarning {
		t.Errorf("events %+v", events)
	}
}

// A list back on its first choice is shown on the tab, not emailed.
func TestPlan_DegradedToOKIsNotEmailed(t *testing.T) {
	degraded := []StepView{view(StepInfographic, "Infographic", false, StepDegraded, mv("a", ModelRetired), mv("b", ModelHealthy))}
	_, next := Plan(degraded, false, nil, t0)
	ok := []StepView{view(StepInfographic, "Infographic", false, StepOK, mv("a2", ModelHealthy), mv("b", ModelHealthy))}
	events, next := Plan(ok, false, asMap(next), t0.Add(time.Hour))
	if len(events) != 0 {
		t.Errorf("degraded -> ok emailed %+v", events)
	}
	if got := asMap(next)["step:infographic"].State; got != stateOK {
		t.Errorf("saved state %q, want ok", got)
	}
}

// A list flapping between degraded and ok warns once, then not again for a day.
func TestPlan_FlappingSendsAtMostOneWarningADay(t *testing.T) {
	degraded := []StepView{view(StepInfographic, "Infographic", false, StepDegraded, mv("a", ModelFailing), mv("b", ModelHealthy))}
	ok := []StepView{view(StepInfographic, "Infographic", false, StepOK, mv("a", ModelHealthy), mv("b", ModelHealthy))}

	var stored map[string]db.ModelAlert
	emails := 0
	for i := range 6 {
		views := degraded
		if i%2 == 1 {
			views = ok
		}
		events, next := Plan(views, false, stored, t0.Add(time.Duration(i)*time.Hour))
		if len(events) > 0 {
			emails++
		}
		stored = asMap(next)
	}
	if emails != 1 {
		t.Fatalf("emails over 6 flapping evaluations = %d, want 1", emails)
	}

	// Ok at hour 5; degraded again within a day of the warning is still quiet.
	if events, _ := Plan(degraded, false, stored, t0.Add(23*time.Hour)); len(events) != 0 {
		t.Errorf("warned again within a day: %+v", events)
	}
	events, _ := Plan(degraded, false, stored, t0.Add(25*time.Hour))
	if len(events) != 1 || events[0].Severity != SeverityWarning {
		t.Errorf("a day later: events %+v, want one warning", events)
	}
}

// A publishing step leaving down is emailed, whatever it comes back as.
func TestPlan_PublishingResumedIsEmailed(t *testing.T) {
	gen := view(StepGenerator, "Generator", true, StepOK, mv("gen", ModelHealthy))
	down := []StepView{gen, view(StepCritic, "Critic", true, StepDown, mv("super", ModelRetired), mv("kimi", ModelRetired))}
	_, next := Plan(down, false, nil, t0)
	stored := asMap(next)

	for _, tc := range []struct {
		name  string
		after StepView
		title string
	}{
		{"ok", view(StepCritic, "Critic", true, StepOK, mv("super", ModelHealthy), mv("kimi", ModelHealthy)),
			"Critic is back: publishing has resumed"},
		{"backup", view(StepCritic, "Critic", true, StepDegraded, mv("super", ModelRetired), mv("kimi", ModelHealthy)),
			"Critic answers again on a backup: publishing has resumed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events, _ := Plan([]StepView{gen, tc.after}, false, stored, t0.Add(time.Hour))
			if len(events) != 1 || events[0].Severity != SeverityRecovered || events[0].Title != tc.title {
				t.Fatalf("events %+v, want one recovered %q", events, tc.title)
			}
			if tc.after.State == StepDegraded && !strings.Contains(events[0].Detail, "super: retired") {
				t.Errorf("detail %q, want the degraded detail", events[0].Detail)
			}
		})
	}
}

// The degraded email names the model that answered, never one that may not
// have been called.
func TestPlan_DegradedDetailNamesTheModelAnswering(t *testing.T) {
	answering := mv("c", ModelHealthy)
	answering.Answering = true
	withCall := view(StepEnrichment, "Enrichment", false, StepDegraded, mv("a", ModelRetired), mv("b", ModelHealthy), answering)
	if d := degradedDetail(withCall); !strings.Contains(d, "Running on c.") {
		t.Errorf("detail %q, want it running on the model that answered", d)
	}

	// The last call answered went to the first choice, before it died: no
	// backup has been called yet.
	first := mv("a", ModelRetired)
	first.Answering = true
	noCall := view(StepEnrichment, "Enrichment", false, StepDegraded, first, mv("b", ModelFailing), mv("c", ModelHealthy), mv("d", ModelUnknown))
	d := degradedDetail(noCall)
	if strings.Contains(d, "Running on") || !strings.Contains(d, "Backups configured: c, d.") {
		t.Errorf("detail %q, want the working backups listed and no claim one is running", d)
	}
}

// One retired model listed in every step is one model, not a provider problem.
func TestPlan_OneModelInManyListsIsNotAProviderProblem(t *testing.T) {
	var views []StepView
	for _, s := range []string{StepGenerator, StepCritic, StepEnrichment, StepInfographic} {
		views = append(views, view(s, s, s == StepGenerator || s == StepCritic, StepDegraded,
			mv("shared", ModelRetired), mv("other-"+s, ModelUnknown)))
	}
	events, _ := Plan(views, false, nil, t0)
	for _, e := range events {
		if e.Key == providerKey {
			t.Fatalf("one retired model raised %+v", e)
		}
	}
}

func TestPlan_ProviderProblemIsOneEmail(t *testing.T) {
	views := []StepView{
		view(StepGenerator, "Generator", true, StepDown, mv("g", ModelAuth)),
		view(StepCritic, "Critic", true, StepDown, mv("c1", ModelAuth), mv("c2", ModelAuth)),
	}
	events, next := Plan(views, false, nil, t0)
	if len(events) != 1 || events[0].Key != "provider" || events[0].Severity != SeverityUrgent {
		t.Fatalf("events %+v", events)
	}
	healthy := []StepView{
		view(StepGenerator, "Generator", true, StepOK, mv("g", ModelHealthy)),
		view(StepCritic, "Critic", true, StepOK, mv("c1", ModelHealthy), mv("c2", ModelHealthy)),
	}
	events, _ = Plan(healthy, false, asMap(next), t0.Add(time.Hour))
	if len(events) != 1 || events[0].Key != "provider" || events[0].Severity != SeverityRecovered {
		t.Errorf("recovery events %+v", events)
	}
}

func TestPlan_Drought(t *testing.T) {
	events, next := Plan(nil, true, nil, t0)
	if len(events) != 1 || events[0].Key != "drought" || events[0].Severity != SeverityUrgent {
		t.Fatalf("events %+v", events)
	}
	events, _ = Plan(nil, false, asMap(next), t0.Add(time.Hour))
	if len(events) != 1 || events[0].Severity != SeverityRecovered {
		t.Errorf("events %+v", events)
	}
}

func TestPlan_IgnoresModelsNoLongerConfigured(t *testing.T) {
	stored := map[string]db.ModelAlert{
		"backup:critic:kimi": {Key: "backup:critic:kimi", State: "dead", Since: t0, NotifiedAt: t0},
	}
	views := []StepView{view(StepCritic, "Critic", true, StepOK, mv("vl", ModelHealthy), mv("lightning", ModelHealthy))}
	events, _ := Plan(views, false, stored, t0.Add(48*time.Hour))
	if len(events) != 0 {
		t.Errorf("a model removed from the list produced %+v", events)
	}
}

func TestCompose_SubjectFromTheMostSevere(t *testing.T) {
	a := Compose([]Event{
		{Key: "backup:enrichment:x", Severity: SeverityWarning, Title: "Enrichment: backup x is retired"},
		{Key: "step:critic", Severity: SeverityUrgent, Title: "Critic is down: publishing has stopped"},
	}, "https://milesahead.example/admin/models")
	if a.Subject != "[Miles Ahead] URGENT: Critic is down: publishing has stopped (+1 more)" || !a.Urgent {
		t.Errorf("subject %q urgent %v", a.Subject, a.Urgent)
	}
	if a.Items[0].Severity != "URGENT" || a.AdminURL == "" {
		t.Errorf("items %+v", a.Items)
	}
}
