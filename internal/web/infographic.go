package web

import (
	"encoding/base64"
	"html"
	"html/template"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"

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
	clean, err := svg.Sanitize(s)
	if err != nil {
		slog.Warn("infographic: stored chart did not survive the sanitiser, not shown", "error", err)
		return nil
	}
	g := &articleInfographic{
		Src: template.URL("data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(clean))),
		Alt: infographicAlt(clean),
	}
	if m := rootSize.FindStringSubmatch(clean); m != nil {
		g.Width, g.Height = pixels(m[1]), pixels(m[2])
	}
	return g
}

// infographicAlt is the chart's title, followed by its description when it
// has one. Both are stored XML-escaped; html/template escapes again on
// output, so they are unescaped here to avoid "Fees &amp;amp; credits".
func infographicAlt(clean string) string {
	alt := html.UnescapeString(svg.Title(clean))
	if d := html.UnescapeString(svg.Desc(clean)); d != "" {
		alt = strings.TrimRight(alt, ". ") + ". " + d
	}
	return alt
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
	}
}
