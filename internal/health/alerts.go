package health

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
)

// Severity orders alert events; the most severe sets the email's subject.
type Severity int

const (
	SeverityRecovered Severity = iota
	SeverityWarning
	SeverityUrgent
)

func (s Severity) String() string {
	switch s {
	case SeverityUrgent:
		return "URGENT"
	case SeverityWarning:
		return "Warning"
	}
	return "Recovered"
}

// Event is one change worth an email.
type Event struct {
	Key      string
	Severity Severity
	Title    string // one line; the email subject uses the most severe
	Detail   string
}

// repeatUrgentAfter: an urgent state is emailed again this long after the last time.
const repeatUrgentAfter = 24 * time.Hour

const (
	providerKey = "provider"
	droughtKey  = "drought"
	stateOK     = "ok"
)

func stepKey(step string) string          { return "step:" + step }
func backupKey(step, model string) string { return "backup:" + step + ":" + model }

// Advice is what to do about a model in state s.
func Advice(s ModelState) string {
	switch s {
	case ModelRetired:
		return "Pick a replacement with `ab-models -critic-candidates ...`, change the list in .github/workflows/deploy.yml and config.example.yaml, merge and deploy."
	case ModelAuth:
		return "Rotate the NVIDIA_API_KEY secret and redeploy."
	case ModelFailing:
		return "Usually passes. If it lasts over a day, move the model down its list."
	}
	return ""
}

// Plan compares the current views with the states last emailed (stored) and
// returns the events to email, and every alert row to save once the email has
// gone out. Keys for models no longer in a list are left alone.
func Plan(views []StepView, drought bool, stored map[string]db.ModelAlert, now time.Time) ([]Event, []db.ModelAlert) {
	p := &planner{stored: stored, now: now}
	if broken, kind := providerBroken(views); broken {
		p.urgent(providerKey, "broken", "Every model is failing: API key or provider problem", providerDetail(kind))
	} else {
		p.recovered(providerKey, "Models are answering again")
		for _, v := range views {
			p.step(v)
		}
	}
	if drought {
		p.urgent(droughtKey, "stopped", "No article in 24 hours",
			"No article has been generated in 24 hours while sources kept adding stories. Publishing has stopped. Open the Models tab to see which list is failing.")
	} else {
		p.recovered(droughtKey, "Articles are being generated again")
	}
	return p.events, p.next
}

type planner struct {
	stored map[string]db.ModelAlert
	now    time.Time
	events []Event
	next   []db.ModelAlert
}

func (p *planner) prev(key string) db.ModelAlert {
	if a, ok := p.stored[key]; ok {
		return a
	}
	return db.ModelAlert{Key: key, State: stateOK}
}

// save records key's new state, emailing ev if it is not nil.
func (p *planner) save(key, state string, ev *Event) {
	prev := p.prev(key)
	row := db.ModelAlert{Key: key, State: state, Since: prev.Since, NotifiedAt: prev.NotifiedAt}
	if state != prev.State {
		row.Since = p.now
	}
	if ev != nil {
		ev.Key = key
		p.events = append(p.events, *ev)
		row.NotifiedAt = p.now
	}
	p.next = append(p.next, row)
}

// urgent emails on entering state, and again every repeatUrgentAfter while it lasts.
func (p *planner) urgent(key, state, title, detail string) {
	prev := p.prev(key)
	switch {
	case prev.State != state:
		p.save(key, state, &Event{Severity: SeverityUrgent, Title: title, Detail: detail})
	case p.now.Sub(prev.NotifiedAt) >= repeatUrgentAfter:
		p.save(key, state, &Event{Severity: SeverityUrgent, Title: "Still: " + title, Detail: detail})
	default:
		p.save(key, state, nil)
	}
}

// warning emails on entering state only.
func (p *planner) warning(key, state, title, detail string) {
	if p.prev(key).State != state {
		p.save(key, state, &Event{Severity: SeverityWarning, Title: title, Detail: detail})
		return
	}
	p.save(key, state, nil)
}

// recovered returns key to ok, emailing if it was not ok.
func (p *planner) recovered(key, title string) {
	if p.prev(key).State != stateOK {
		p.save(key, stateOK, &Event{Severity: SeverityRecovered, Title: title})
		return
	}
	p.save(key, stateOK, nil)
}

func (p *planner) step(v StepView) {
	if v.State == StepOff {
		return
	}
	key := stepKey(v.Name)
	switch v.State {
	case StepDown:
		if v.StopsPublishing {
			p.urgent(key, string(StepDown), v.Label+" is down: publishing has stopped", downDetail(v))
		} else {
			p.warning(key, string(StepDown), v.Label+" is down: no model in its list works", downDetail(v))
		}
	case StepDegraded:
		p.warning(key, string(StepDegraded), v.Label+" is running on a backup", degradedDetail(v))
	default:
		p.recovered(key, v.Label+" is back on its first choice")
	}
	for i, m := range v.Models {
		if i == 0 {
			continue // the first choice failing is the degraded event
		}
		bk := backupKey(v.Name, m.Model)
		if !m.State.Dead() {
			p.save(bk, stateOK, nil) // a backup coming back is shown, not emailed
			continue
		}
		if v.State == StepOK {
			p.warning(bk, "dead", fmt.Sprintf("%s: backup %s is %s", v.Label, m.Model, m.State), backupDetail(v, m))
		} else {
			p.save(bk, "dead", nil) // the step's own event covers it
		}
	}
}

// providerBroken: every model seen, in every list, is retired, or every one
// is auth: the key or the base URL, not the models.
func providerBroken(views []StepView) (bool, ModelState) {
	var kind ModelState
	seen := 0
	for _, v := range views {
		if v.State == StepOff {
			continue
		}
		for _, m := range v.Models {
			if m.State == ModelUnknown {
				continue
			}
			if m.State != ModelAuth && m.State != ModelRetired {
				return false, ""
			}
			if kind == "" {
				kind = m.State
			} else if kind != m.State {
				return false, ""
			}
			seen++
		}
	}
	return seen >= 2, kind
}

func providerDetail(kind ModelState) string {
	if kind == ModelAuth {
		return "Every model refuses the API key (401/403). Rotate the NVIDIA_API_KEY secret and redeploy."
	}
	return "Every model answers 404/410. Check hf_base_url in deploy.yml; if it is right, the provider has retired every configured model."
}

func modelLine(m ModelView) string {
	s := fmt.Sprintf("%s: %s", m.Model, m.State)
	if !m.FailingSince.IsZero() {
		s += " since " + m.FailingSince.UTC().Format("Jan 2 15:04 MST")
	}
	if m.LastError != "" {
		e := m.LastError
		if len(e) > 160 {
			e = e[:160] + "..."
		}
		s += " (" + e + ")"
	}
	return s
}

func downDetail(v StepView) string {
	var lines []string
	for _, m := range v.Models {
		lines = append(lines, modelLine(m))
	}
	advice := ""
	if len(v.Models) > 0 {
		advice = "\n\n" + Advice(v.Models[0].State)
	}
	return "No model in the list answers:\n" + strings.Join(lines, "\n") + advice
}

func degradedDetail(v StepView) string {
	running := ""
	for _, m := range v.Models[1:] {
		if !m.State.Dead() {
			running = m.Model
			break
		}
	}
	return modelLine(v.Models[0]) + "\nRunning on " + running + ".\n\n" + Advice(v.Models[0].State)
}

func backupDetail(v StepView, m ModelView) string {
	s := modelLine(m)
	if v.NoBackup {
		s += "\n" + v.Label + " has no working backup left: if " + v.Models[0].Model + " fails too, the list is down."
	}
	return s + "\n\n" + Advice(m.State)
}

// Compose makes one email from every event of one evaluation, most severe first.
func Compose(events []Event, adminURL string) mail.ModelAlert {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Severity > sorted[j].Severity })
	top := sorted[0]
	subject := fmt.Sprintf("[Miles Ahead] %s: %s", top.Severity, top.Title)
	if len(sorted) > 1 {
		subject += fmt.Sprintf(" (+%d more)", len(sorted)-1)
	}
	a := mail.ModelAlert{Subject: subject, Urgent: top.Severity == SeverityUrgent, AdminURL: adminURL}
	for _, e := range sorted {
		a.Items = append(a.Items, mail.ModelAlertItem{Severity: e.Severity.String(), Title: e.Title, Detail: e.Detail})
	}
	return a
}
