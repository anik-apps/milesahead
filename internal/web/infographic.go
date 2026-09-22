package web

import (
	"html"
	"html/template"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/svg"
)

// infographicHTML prepares a stored infographic for inlining, returning empty
// when there is nothing safe to show.
//
// The markup is re-sanitised here rather than trusted. It was already
// sanitised before it was written, so in the normal case this changes nothing
// -- but "was written by code that sanitises" is an assumption about history,
// and the value is about to be inlined into a page unescaped, where being
// wrong costs a same-origin XSS. A row edited by hand, restored from a backup
// taken before the sanitiser existed, or written by some future code path that
// forgets, all fail closed here instead of executing.
//
// The cost is parsing roughly two kilobytes of XML per rendered card:
// measured at 50us for a real diagram, so about 1ms for a full twenty-card
// homepage (see BenchmarkInfographicHTML). That is real but small next to the
// database round trips and template execution on the same request, and it
// buys a guarantee that holds regardless of what is in the column.
func infographicHTML(stored string) template.HTML {
	s := strings.TrimSpace(stored)
	if s == "" || s == db.NoInfographic {
		return ""
	}
	clean, err := svg.Sanitize(s)
	if err != nil {
		return ""
	}
	return template.HTML(clean)
}

// infographicTitle returns the diagram's accessible name, for the aria-label
// that makes it announce as one image rather than a loose pile of text nodes.
//
// The title is unescaped on the way out. It sits inside the SVG as XML, so an
// ampersand is stored as &amp;; handing that to html/template would escape it
// a second time and a reader would hear "Fee &amp;amp; value".
func infographicTitle(stored string) string {
	s := strings.TrimSpace(stored)
	if s == "" || s == db.NoInfographic {
		return ""
	}
	clean, err := svg.Sanitize(s)
	if err != nil {
		return ""
	}
	return html.UnescapeString(svg.Title(clean))
}
