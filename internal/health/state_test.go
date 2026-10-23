package health

import (
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

var t0 = time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)

func call(o ai.Outcome, source string, at time.Time) ai.ModelCall {
	return ai.ModelCall{Step: StepCritic, Model: "m", Source: source, Outcome: o, At: at, Detail: string(o)}
}

func fold(calls ...ai.ModelCall) db.ModelHealth {
	h := db.ModelHealth{Step: StepCritic, Model: "m"}
	for _, c := range calls {
		h = Apply(h, c)
	}
	return h
}

func TestStateOf_Debounces(t *testing.T) {
	now := t0.Add(time.Hour)
	for _, tc := range []struct {
		name string
		h    db.ModelHealth
		want ModelState
	}{
		{"one failed check is not failing", fold(call(ai.OutcomeTimeout, ai.SourceCheck, t0)), ModelHealthy},
		{"two failed checks are", fold(call(ai.OutcomeTimeout, ai.SourceCheck, t0), call(ai.OutcomeOverloaded, ai.SourceCheck, t0)), ModelFailing},
		{"two failed calls are not", fold(call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0)), ModelHealthy},
		{"three failed calls are", fold(call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0)), ModelFailing},
		{"retired at once", fold(call(ai.OutcomeRetired, ai.SourceCall, t0)), ModelRetired},
		{"auth at once", fold(call(ai.OutcomeAuth, ai.SourceCheck, t0)), ModelAuth},
		{"a newer ok clears it", fold(call(ai.OutcomeRetired, ai.SourceCheck, t0), call(ai.OutcomeOK, ai.SourceCheck, t0)), ModelHealthy},
	} {
		if got := StateOf(tc.h, true, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	stale := fold(call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0))
	if got := StateOf(stale, true, t0.Add(7*time.Hour)); got != ModelHealthy {
		t.Errorf("3 call failures 7h ago: %q, want healthy", got)
	}
	if got := StateOf(db.ModelHealth{}, false, now); got != ModelUnknown {
		t.Errorf("no row: %q, want unknown", got)
	}
}

// Three failed calls 7 hours ago have expired. A failure now, a check's or a
// call's, starts afresh rather than reviving them: one blip is not failing.
func TestApply_ExpiredCallStreakIsNotRevived(t *testing.T) {
	old := []ai.ModelCall{
		call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0),
		call(ai.OutcomeTimeout, ai.SourceCall, t0),
	}
	later := t0.Add(7 * time.Hour)
	for _, src := range []string{ai.SourceCheck, ai.SourceCall} {
		h := fold(append(old, call(ai.OutcomeTimeout, src, later))...)
		if got := StateOf(h, true, later); got != ModelHealthy {
			t.Errorf("one failed %s 7h after an expired call streak: %q (call streak %d), want healthy",
				src, got, h.CallFailStreak)
		}
	}
	// Within the window the streak carries on.
	h := fold(call(ai.OutcomeTimeout, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0),
		call(ai.OutcomeTimeout, ai.SourceCall, t0.Add(5*time.Hour)))
	if got := StateOf(h, true, t0.Add(5*time.Hour)); got != ModelFailing {
		t.Errorf("3 failed calls within 6h: %q, want failing", got)
	}
}

func TestApply_TracksFailingSinceAndAnswered(t *testing.T) {
	h := fold(call(ai.OutcomeOK, ai.SourceCall, t0), call(ai.OutcomeTimeout, ai.SourceCall, t0.Add(time.Minute)),
		call(ai.OutcomeTimeout, ai.SourceCheck, t0.Add(2*time.Minute)))
	if !h.FailingSince.Equal(t0.Add(time.Minute)) || !h.LastAnswered.Equal(t0) || !h.LastCheck.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("row %+v", h)
	}
	if h.CallFailStreak != 1 || h.CheckFailStreak != 1 || h.LastError != "timeout" {
		t.Errorf("streaks %d/%d error %q", h.CallFailStreak, h.CheckFailStreak, h.LastError)
	}
}

func rows(states map[string]ai.Outcome) []db.ModelHealth {
	var out []db.ModelHealth
	for model, o := range states {
		out = append(out, Apply(db.ModelHealth{Step: StepCritic, Model: model},
			ai.ModelCall{Step: StepCritic, Model: model, Source: ai.SourceCall, Outcome: o, At: t0}))
	}
	return out
}

func TestEvaluate_StepStates(t *testing.T) {
	steps := []Step{{Name: StepCritic, Label: "Critic", Models: []string{"a", "b"}, StopsPublishing: true}}
	for _, tc := range []struct {
		name     string
		states   map[string]ai.Outcome
		want     StepState
		noBackup bool
	}{
		{"both fine", map[string]ai.Outcome{"a": ai.OutcomeOK, "b": ai.OutcomeOK}, StepOK, false},
		{"backup dead: the kimi-k3 case", map[string]ai.Outcome{"a": ai.OutcomeOK, "b": ai.OutcomeRetired}, StepOK, true},
		{"first dead", map[string]ai.Outcome{"a": ai.OutcomeRetired, "b": ai.OutcomeOK}, StepDegraded, false},
		{"first dead, backup never seen", map[string]ai.Outcome{"a": ai.OutcomeRetired}, StepDegraded, false},
		{"both dead", map[string]ai.Outcome{"a": ai.OutcomeRetired, "b": ai.OutcomeRetired}, StepDown, false},
	} {
		v := Evaluate(steps, rows(tc.states), t0.Add(time.Minute))[0]
		if v.State != tc.want || v.NoBackup != tc.noBackup {
			t.Errorf("%s: state %q noBackup %v, want %q %v", tc.name, v.State, v.NoBackup, tc.want, tc.noBackup)
		}
	}
}

func TestEvaluate_EmptyListIsDown(t *testing.T) {
	v := Evaluate([]Step{{Name: StepCritic, Label: "Critic", StopsPublishing: true}}, nil, t0)[0]
	if v.State != StepDown {
		t.Errorf("empty critic list: %q, want down", v.State)
	}
}

func TestEvaluate_SingleModelAndOff(t *testing.T) {
	steps := []Step{
		{Name: StepVision, Label: "Vision", Models: []string{"v"}},
		{Name: StepInfographic, Label: "Infographic", Models: []string{"i"}, Off: true},
	}
	views := Evaluate(steps, nil, t0)
	if !views[0].SingleModel || views[0].NoBackup || views[0].State != StepOK {
		t.Errorf("vision: %+v", views[0])
	}
	if views[1].State != StepOff {
		t.Errorf("infographic off: %q", views[1].State)
	}
}
