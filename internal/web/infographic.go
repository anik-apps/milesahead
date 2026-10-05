package web

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/svg"
)

// articleInfographic is a model-drawn chart, ready for an <img>.
//
// The chart is shown as an image, never inlined. Its src is a data: URL, so
// the SVG is parsed as an image document: browsers run no script in it, load
// nothing it references and give it an opaque origin, and there is no URL on
// this site that serves it as a document someone could be sent to. Even a
// bug in internal/svg therefore cannot become script running on the site, and
// the sanitiser is the second line rather than the only one.
//
// The cost is theming: an image cannot inherit the page's text colour. The
// chart's text is currentColor, which the sanitiser pins to a dark ink, and
// the dark theme inverts the image in CSS (see .article-infographic img).
type articleInfographic struct {
	Src template.URL
	// Alt is the chart's title and description. The numbers it shows are the
	// key details, which the page lists as text beside it.
	Alt           string
	Width, Height int
}

// rootSize reads the width and height the sanitiser writes on its root.
var rootSize = regexp.MustCompile(`^<svg [^>]*\bwidth="([0-9.]+)" height="([0-9.]+)"`)

// infographicFor prepares a stored infographic for display, or returns nil
// when there is nothing to show.
//
// The markup is re-sanitised here rather than trusted. It was sanitised
// before it was written, so in the normal case this changes nothing; but a
// row edited by hand, restored from an old backup or written by a future code
// path that forgot still goes through the allowlist before reaching a page.
// The cost is parsing a few kilobytes on an article page.
func infographicFor(stored string) *articleInfographic {
	s := strings.TrimSpace(stored)
	if s == "" || s == db.NoInfographic {
		return nil
	}
	doc, err := svg.SanitizeDoc(s)
	if err != nil {
		warnUnshowable(s, err)
		return nil
	}
	g := &articleInfographic{
		Src: template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(doc.SVG))),
		Alt: infographicAlt(doc),
	}
	if m := rootSize.FindStringSubmatch(doc.SVG); m != nil {
		g.Width, g.Height = pixels(m[1]), pixels(m[2])
	}
	return g
}

// infographicAlt is the chart's title, followed by its description when it
// has one: the root's own <title> and <desc>, as plain text (html/template
// escapes them on output).
func infographicAlt(doc svg.Doc) string {
	alt := doc.Title
	if doc.Desc != "" {
		alt = strings.TrimRight(alt, ". ") + ". " + doc.Desc
	}
	return alt
}

// unshowable remembers the stored charts already reported as failing the
// sanitiser, by hash, so a bad row is logged once rather than on every view
// of its page.
var unshowable sync.Map

func warnUnshowable(stored string, err error) {
	sum := sha256.Sum256([]byte(stored))
	if _, seen := unshowable.LoadOrStore(sum, true); !seen {
		slog.Warn("infographic: stored chart did not survive the sanitiser, not shown (logged once per chart; Redraw chart replaces it)",
			"error", err)
	}
}

// pixels rounds a viewBox dimension to the whole number an <img> attribute
// takes; it only sets the aspect ratio, since CSS decides the size.
func pixels(s string) int {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 1 {
		return 0
	}
	return int(math.Round(f))
}

// publicInfographic is the chart an article page shows: none unless
// ai.enrichment_infographics is "on".
func (app *App) publicInfographic(stored string) *articleInfographic {
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
