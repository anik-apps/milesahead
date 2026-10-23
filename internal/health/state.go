package health

import (
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// ModelState is one model's health within one step.
type ModelState string

const (
	ModelUnknown ModelState = "unknown" // never checked or called yet
	ModelHealthy ModelState = "healthy"
	ModelFailing ModelState = "failing"
	ModelRetired ModelState = "retired"
	ModelAuth    ModelState = "auth"
)

// Dead reports whether the model cannot be counted on to answer.
func (s ModelState) Dead() bool { return s == ModelFailing || s == ModelRetired || s == ModelAuth }

// StepState is one model list's health.
type StepState string

const (
	StepOK       StepState = "ok"       // first choice answers
	StepDegraded StepState = "degraded" // first choice dead, a backup answers
	StepDown     StepState = "down"     // nothing in the list answers
	StepOff      StepState = "off"      // switched off in config
)

// Debounce: one blip is not a failure. Retired and auth take effect at once.
const (
	checkFailsToFail = 2
	callFailsToFail  = 3
	callFailWindow   = 6 * time.Hour
)

// Apply folds one model call or check into the model's row.
func Apply(h db.ModelHealth, c ai.ModelCall) db.ModelHealth {
	at := c.At.UTC().Truncate(time.Second)
	if c.Source == ai.SourceCheck {
		h.LastCheck = at
	}
	if c.Outcome == ai.OutcomeOK {
		h.LastOK = at
		h.FailingSince = time.Time{}
		h.CallFailStreak, h.CheckFailStreak = 0, 0
		h.LastOutcome, h.LastError = string(ai.OutcomeOK), ""
		if c.Source == ai.SourceCall {
			h.LastAnswered = at
		}
		return h
	}
	// A call streak lasts while failures keep coming within callFailWindow of
	// each other. Past that it has expired (StateOf ignores it), and the next
	// failure, a check's or a call's, must not revive it: LastFail is shared.
	if !h.LastFail.IsZero() && at.Sub(h.LastFail) > callFailWindow {
		h.CallFailStreak = 0
	}
	h.LastFail = at
	if h.FailingSince.IsZero() {
		h.FailingSince = at
	}
	if c.Source == ai.SourceCheck {
		h.CheckFailStreak++
	} else {
		h.CallFailStreak++
	}
	h.LastOutcome, h.LastError = string(c.Outcome), c.Detail
	return h
}

// StateOf judges a model from its row. found is false when it has none.
func StateOf(h db.ModelHealth, found bool, now time.Time) ModelState {
	if !found || h.LastOutcome == "" {
		return ModelUnknown
	}
	switch ai.Outcome(h.LastOutcome) {
	case ai.OutcomeRetired:
		return ModelRetired
	case ai.OutcomeAuth:
		return ModelAuth
	}
	if h.CheckFailStreak >= checkFailsToFail {
		return ModelFailing
	}
	if h.CallFailStreak >= callFailsToFail && now.Sub(h.LastFail) <= callFailWindow {
		return ModelFailing
	}
	return ModelHealthy
}

// ModelView is one model in one list, as the page and alerts see it.
type ModelView struct {
	Model                              string
	State                              ModelState
	LastOK, FailingSince, LastAnswered time.Time
	LastOutcome, LastError             string
	Answering                          bool // answered the list's most recent real call
}

// StepView is one list's state.
type StepView struct {
	Step
	State       StepState
	NoBackup    bool      // ok, but every backup in the list is dead
	SingleModel bool      // the list has no backup configured
	Since       time.Time // when it became degraded or down; zero when ok
	Models      []ModelView
}

// Evaluate judges every step from the stored rows. Rows for models no longer
// in a list are ignored.
func Evaluate(steps []Step, rows []db.ModelHealth, now time.Time) []StepView {
	byKey := map[[2]string]db.ModelHealth{}
	for _, r := range rows {
		byKey[[2]string{r.Step, r.Model}] = r
	}
	out := make([]StepView, 0, len(steps))
	for _, s := range steps {
		v := StepView{Step: s, SingleModel: len(s.Models) == 1}
		var latest time.Time
		answering := -1
		for i, m := range s.Models {
			r, found := byKey[[2]string{s.Name, m}]
			v.Models = append(v.Models, ModelView{
				Model: m, State: StateOf(r, found, now), LastOK: r.LastOK, FailingSince: r.FailingSince,
				LastAnswered: r.LastAnswered, LastOutcome: r.LastOutcome, LastError: r.LastError,
			})
			if r.LastAnswered.After(latest) {
				latest, answering = r.LastAnswered, i
			}
		}
		if answering >= 0 {
			v.Models[answering].Answering = true
		}
		v.State, v.NoBackup, v.Since = stepState(s, v.Models)
		out = append(out, v)
	}
	return out
}

func stepState(s Step, models []ModelView) (StepState, bool, time.Time) {
	if s.Off {
		return StepOff, false, time.Time{}
	}
	if len(models) == 0 {
		return StepDown, false, time.Time{}
	}
	var since time.Time
	alive := false
	for _, m := range models {
		if !m.State.Dead() {
			alive = true
			continue
		}
		if since.IsZero() || (!m.FailingSince.IsZero() && m.FailingSince.Before(since)) {
			since = m.FailingSince
		}
	}
	switch {
	case !alive:
		return StepDown, false, since
	case models[0].State.Dead():
		return StepDegraded, false, models[0].FailingSince
	}
	if len(models) == 1 {
		return StepOK, false, time.Time{}
	}
	for _, m := range models[1:] {
		if !m.State.Dead() {
			return StepOK, false, time.Time{}
		}
	}
	return StepOK, true, time.Time{}
}
