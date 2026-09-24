package web

import (
	"html"
	"strings"
)

// scrollTables wraps each top-level <table> in its own horizontally scrolling,
// labelled region. Card sections and articles carry wide comparison tables; left
// bare, a 5-column table made the whole page wider than a phone screen, so the
// page slid sideways and the sticky header scrolled away. Wrapping the table,
// rather than making it display:block, keeps its table semantics for screen
// readers. Nested tables stay inside their parent's wrapper.
func scrollTables(label, content string) string {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "<table") {
		return content
	}
	open := `<div class="table-scroll" role="region" tabindex="0" aria-label="` + html.EscapeString(label) + `">`
	var b strings.Builder
	depth, last := 0, 0
	for i := 0; i < len(lower); {
		switch {
		case strings.HasPrefix(lower[i:], "<table") && isTagBoundary(lower, i+len("<table")):
			if depth == 0 {
				b.WriteString(content[last:i])
				b.WriteString(open)
				last = i
			}
			depth++
			i += len("<table")
		case strings.HasPrefix(lower[i:], "</table>") && depth > 0:
			depth--
			i += len("</table>")
			if depth == 0 {
				b.WriteString(content[last:i])
				b.WriteString("</div>")
				last = i
			}
		default:
			i++
		}
	}
	if depth > 0 {
		// Unclosed table: leave the markup as it was rather than guess.
		return content
	}
	b.WriteString(content[last:])
	return b.String()
}

// isTagBoundary reports whether the tag name that ends at i really ends there
// (so "<table" matches <table> and <table class=...>, but not <tablet>).
func isTagBoundary(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	switch s[i] {
	case '>', ' ', '\t', '\n', '\r', '/':
		return true
	}
	return false
}
