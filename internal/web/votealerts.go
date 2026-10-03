package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// voteAlertRow is an open vote alert as the Published page lists it.
type voteAlertRow struct {
	db.VoteAlert
	// Age is how long ago the alert last changed: "3 h ago".
	Age string
	// Live reports whether the article is still published.
	Live bool
}

// openVoteAlerts lists the vote alerts not yet acknowledged, for the
// Published page's banner. Best effort: none on error.
func (app *App) openVoteAlerts(now time.Time) []voteAlertRow {
	alerts, err := app.db.ListOpenVoteAlerts()
	if err != nil {
		return nil
	}
	rows := make([]voteAlertRow, 0, len(alerts))
	for _, a := range alerts {
		rows = append(rows, voteAlertRow{VoteAlert: a, Age: ago(a.UpdatedAt, now), Live: a.Status == db.StatusApproved})
	}
	return rows
}

// voteAlertWindowLabel says which votes an alert counts, for the banner:
// "in the last 24 hours", or "in all" with no window.
func (app *App) voteAlertWindowLabel() string {
	switch h := app.cfg.Votes.AlertWindow; {
	case h == 1:
		return "in the hour before it was raised"
	case h > 1:
		return fmt.Sprintf("in the %d hours before it was raised", h)
	}
	return "in all"
}

// handleAcknowledgeVoteAlert marks one vote alert as seen. Its article can
// alert again, on votes cast after now.
func (app *App) handleAcknowledgeVoteAlert(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid alert ID", http.StatusBadRequest)
		return
	}
	if _, err := app.db.AcknowledgeVoteAlert(id, app.clock()); err != nil {
		app.serverError(w, r, "admin: acknowledging vote alert", err)
		return
	}
	// Already acknowledged (another tab, a double click) lands the same way.
	http.Redirect(w, r, withSort(r, "/admin/published?msg=alert_acknowledged", publishedSorts), http.StatusSeeOther)
}

// handleAcknowledgeAllVoteAlerts marks every open vote alert as seen.
func (app *App) handleAcknowledgeAllVoteAlerts(w http.ResponseWriter, r *http.Request) {
	if _, err := app.db.AcknowledgeAllVoteAlerts(app.clock()); err != nil {
		app.serverError(w, r, "admin: acknowledging vote alerts", err)
		return
	}
	http.Redirect(w, r, withSort(r, "/admin/published?msg=alert_acknowledged", publishedSorts), http.StatusSeeOther)
}
