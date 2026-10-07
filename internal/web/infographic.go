package web

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/infographic"
)

// infographicFor prepares a stored chart for the "At a glance" panel
// (templates/_infographic.html), or returns nil when there is nothing to
// show.
//
// The stored JSON is checked again here rather than trusted. It was checked
// before it was written, so in the normal case this changes nothing; but a
// row edited by hand, restored from an old backup or written by a future
// code path still has to be a chart the templates can draw. Its words reach
// the page only as escaped text.
func infographicFor(stored string) *infographic.View {
	s := strings.TrimSpace(stored)
	if s == "" || s == db.NoInfographic {
		return nil
	}
	chart, err := infographic.Decode(s)
	if err != nil {
		warnUnshowable(s, err)
		return nil
	}
	v := chart.View()
	return &v
}

// unshowable remembers the stored charts already reported as unreadable, by
// hash, so a bad row is logged once rather than on every view of its page.
var unshowable sync.Map

func warnUnshowable(stored string, err error) {
	sum := sha256.Sum256([]byte(stored))
	if _, seen := unshowable.LoadOrStore(sum, true); !seen {
		slog.Warn("infographic: stored chart is not one the site can draw, not shown (logged once per chart; Redraw chart replaces it)",
			"error", err)
	}
}

// publicInfographic is the chart an article page shows: none unless
// ai.enrichment_infographics is "on".
func (app *App) publicInfographic(stored string) *infographic.View {
	if !app.cfg.AI.ShowInfographics() {
		return nil
	}
	return infographicFor(stored)
}

// infographicsState is ai.enrichment_infographics as the admin preview
// needs it: whether charts are being drawn, and whether the site shows them.
type infographicsState struct{ Drawn, Shown bool }

// attachInfographics loads the review queue rows' charts in one query.
func (app *App) attachInfographics(rows []adminArticleRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	stored, err := app.db.InfographicsFor(ids)
	if err != nil {
		slog.Warn("admin: could not load infographics", "error", err)
		return
	}
	for i := range rows {
		s, ok := stored[rows[i].ID]
		if !ok {
			continue
		}
		if s == db.NoInfographic {
			rows[i].InfographicNone = true
			continue
		}
		rows[i].Infographic = infographicFor(s)
		rows[i].InfographicBroken = rows[i].Infographic == nil
	}
}

// Chart actions, from the review queue preview and the Published row menu.
// Both are POSTs behind the admin CSRF check; neither touches the article's
// content.
const (
	chartRemovedMessage = "Chart removed. It is not shown or drawn again unless you choose Redraw chart."
	chartRedrawMessage  = "Chart cleared. Enrichment draws a new one on a coming pass (every 15 minutes) while ai.enrichment_infographics is preview or on."
)

// handleRemoveChart stores the "nothing to chart" sentinel, so a chart the
// admin judged wrong comes off the page at once and is not drawn again.
func (app *App) handleRemoveChart(w http.ResponseWriter, r *http.Request) {
	app.chartAction(w, r, app.db.RemoveInfographic, "chart_removed")
}

// handleRedrawChart clears the chart so the reconciler draws a new one, with
// a fresh attempt budget. Until it does, the article has no chart.
func (app *App) handleRedrawChart(w http.ResponseWriter, r *http.Request) {
	app.chartAction(w, r, app.db.RedrawInfographic, "chart_redraw")
}

func (app *App) chartAction(w http.ResponseWriter, r *http.Request, act func(int64) error, msg string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}
	if err := act(id); err != nil {
		if errors.Is(err, db.ErrNoChart) {
			http.Error(w, "This article does not exist or was rejected", http.StatusBadRequest)
			return
		}
		app.serverError(w, r, "admin: chart action failed", err)
		return
	}
	if r.FormValue("return") == "published" {
		http.Redirect(w, r, withSort(r, fmt.Sprintf("/admin/published?msg=%s&id=%d", msg, id), publishedSorts), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, withSort(r, fmt.Sprintf("/admin/dashboard?msg=%s&id=%d&open=%d", msg, id, id), queueSorts), http.StatusSeeOther)
}
