package web

import (
	"html/template"
	"strings"
)

// sanitizedSVGPrefix is the exact opening tag internal/svg emits. Nothing else
// produces this byte-for-byte, so matching on it is a cheap way to be sure the
// markup about to be inlined came from the sanitiser.
const sanitizedSVGPrefix = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="`

// infographicHTML prepares a stored infographic for inlining, returning empty
// when there is nothing to show.
//
// The value is already sanitised -- internal/svg runs before it is ever
// written -- so this is not the security boundary. It is the check that the
// boundary was actually crossed: a row edited by hand, restored from an older
// backup, or written by some future code path that forgot to sanitise would
// otherwise be inlined into the page unescaped. Refusing anything that does
// not carry the sanitiser's own signature costs one prefix comparison.
func infographicHTML(stored string) template.HTML {
	s := strings.TrimSpace(stored)
	// The sentinel meaning "a model looked and found nothing to chart".
	if s == "" || s == "none" {
		return ""
	}
	if !strings.HasPrefix(s, sanitizedSVGPrefix) {
		return ""
	}
	return template.HTML(s)
}

// infographicTitle pulls the accessible name out for use in aria-label, so the
// diagram is announced as one image rather than a loose pile of text nodes.
func infographicTitle(stored string) string {
	s := strings.TrimSpace(stored)
	if !strings.HasPrefix(s, sanitizedSVGPrefix) {
		return ""
	}
	start := strings.Index(s, "<title>")
	end := strings.Index(s, "</title>")
	if start == -1 || end <= start {
		return ""
	}
	return strings.TrimSpace(s[start+len("<title>") : end])
}
