package web

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/kumaranik/milesahead/internal/health"
)

// ModelHealth is what the admin Models tab reads; *health.Worker implements it.
type ModelHealth interface {
	Snapshot() health.Snapshot
	SendTestAlert() error
}

// SetModelHealth gives the admin the model health worker. Unset, the Models
// tab says model health is not running.
func (app *App) SetModelHealth(h ModelHealth) { app.modelHealth = h }

var modelMessages = map[string]string{
	"test_sent": "Test alert sent. Check the inbox, and the spam folder the first time.",
}

var modelErrors = map[string]string{
	"test_failed": "The test alert could not be sent. The error is shown under Alerts.",
	"not_emailed": "Alerts are not emailed, so there is nothing to test. See Alerts for why.",
}

type modelRow struct {
	Pos                              int
	Model, Status, Chip              string
	LastOK, Since, LastError, Advice string
	Answering                        bool
}

type stepRow struct {
	Label, Uses, State, Chip, Since string
	NoBackup, SingleModel           bool
	Models                          []modelRow
}

type modelsStatus struct {
	Stopped                           bool
	StoppedSince, Reason, LastArticle string
	Recipient, NotEmailed             string
	LastSent, LastSendError           string
	LastCheck, CheckEvery             string
	CheckOff, CheckLate               bool
}

func when(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("Jan 2 15:04 UTC")
}

func modelChip(s health.ModelState) string {
	switch s {
	case health.ModelHealthy:
		return "chip-ok"
	case health.ModelFailing:
		return "chip-warn"
	case health.ModelRetired, health.ModelAuth:
		return "chip-bad"
	}
	return "chip-mute"
}

func stepChip(s health.StepState) string {
	switch s {
	case health.StepOK:
		return "chip-ok"
	case health.StepDegraded:
		return "chip-warn"
	case health.StepDown:
		return "chip-bad"
	}
	return "chip-mute"
}

func modelsPage(s health.Snapshot) (modelsStatus, []stepRow) {
	st := modelsStatus{
		Stopped: s.Publishing.Stopped, StoppedSince: when(s.Publishing.Since), Reason: s.Publishing.Reason,
		LastArticle: when(s.Publishing.LastArticle), Recipient: s.Recipient, NotEmailed: s.NotEmailed,
		LastSendError: s.LastSendError, LastCheck: when(s.LastCheck),
		CheckOff: s.CheckEvery == 0, CheckLate: s.CheckLate,
	}
	if !s.LastSendAt.IsZero() {
		st.LastSent = when(s.LastSendAt)
	}
	if s.CheckEvery > 0 {
		st.CheckEvery = fmt.Sprintf("%d min", int(s.CheckEvery.Minutes()))
	}
	var rows []stepRow
	for _, v := range s.Steps {
		r := stepRow{Label: v.Label, Uses: v.Uses, State: string(v.State), Chip: stepChip(v.State),
			NoBackup: v.NoBackup, SingleModel: v.SingleModel}
		if !v.Since.IsZero() {
			r.Since = when(v.Since)
		}
		for i, m := range v.Models {
			mr := modelRow{Pos: i + 1, Model: m.Model, Status: string(m.State), Chip: modelChip(m.State),
				LastOK: when(m.LastOK), LastError: m.LastError, Advice: health.Advice(m.State), Answering: m.Answering}
			if !m.FailingSince.IsZero() && m.State.Dead() {
				mr.Since = when(m.FailingSince)
			}
			r.Models = append(r.Models, mr)
		}
		rows = append(rows, r)
	}
	return st, rows
}

func (app *App) handleAdminModels(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Title":     "Models",
		"CSRFToken": getCSRFToken(w, r),
		"Message":   modelMessages[r.URL.Query().Get("msg")],
		"Error":     modelErrors[r.URL.Query().Get("error")],
	}
	if app.modelHealth != nil {
		status, steps := modelsPage(app.modelHealth.Snapshot())
		data["Status"] = status
		data["Steps"] = steps
	}
	app.renderTemplate(w, "models.html", data)
}

func (app *App) handleSendTestAlert(w http.ResponseWriter, r *http.Request) {
	target := "/admin/models?msg=test_sent"
	switch {
	case app.modelHealth == nil:
		target = "/admin/models?error=not_emailed"
	default:
		if err := app.modelHealth.SendTestAlert(); errors.Is(err, health.ErrNotEmailed) {
			target = "/admin/models?error=not_emailed"
		} else if err != nil {
			slog.Warn("admin: test alert", "error", err)
			target = "/admin/models?error=test_failed"
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// modelBanner is the Review page's one-line warning, or "".
func (app *App) modelBanner() string {
	if app.modelHealth == nil {
		return ""
	}
	s := app.modelHealth.Snapshot()
	switch {
	case s.Publishing.Stopped:
		return "Publishing has stopped: " + s.Publishing.Reason + "."
	case s.NotEmailed != "":
		return "Model health alerts are not emailed: " + s.NotEmailed + "."
	}
	return ""
}
