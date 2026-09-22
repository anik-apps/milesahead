// Package svg turns model-authored SVG markup into markup that is safe to
// inline into a page.
//
// Inlining is what makes this mandatory rather than defensive. An
// <img src="…svg"> is sandboxed by the browser; an inline <svg> executes in the
// page's own origin, so one <script> or onload= inside it is same-origin XSS
// with reach into the admin session. The model that writes this markup is
// prompted with article text scraped from third-party sites, so its output has
// to be treated as attacker-influenced rather than merely untidy.
//
// Two rules do the work. Everything is allowlisted -- an element or attribute
// not named below is dropped rather than inspected -- and nothing from the
// input is ever copied through verbatim: what survives is re-serialised by this
// package, so the output can only contain constructs it knows how to emit.
package svg

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
)

const (
	// MaxInputBytes bounds what is parsed at all. A page of bar chart is a few
	// kilobytes; anything near this is a runaway generation, not a diagram.
	MaxInputBytes = 64 << 10

	// MaxElements bounds the cost of rendering the result in a browser.
	MaxElements = 2000

	// svgNS is the only namespace whose elements are kept. Anything else --
	// XHTML smuggled through <foreignObject>, xlink references -- is dropped.
	svgNS = "http://www.w3.org/2000/svg"
)

// allowedElements is drawing primitives and text only.
//
// The notable absences are deliberate: <script> and <handler> execute;
// <foreignObject> embeds arbitrary HTML; <use> and <image> dereference
// external documents; <a> navigates; <style> reintroduces CSS with its own
// url() fetches; <animate*> and <set> can retarget attributes after
// sanitisation has already run; and <defs>/gradients are omitted because
// their ids collide when several of these are rendered on one page.
var allowedElements = map[string]bool{
	"svg": true, "g": true, "title": true, "desc": true,
	"rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "path": true,
	"text": true, "tspan": true,
}

// textElements may carry character data. Text anywhere else is whitespace that
// SVG does not render, so it is dropped rather than escaped.
var textElements = map[string]bool{
	"text": true, "tspan": true, "title": true, "desc": true,
}

// allowedAttrs is geometry and presentation. No attribute here can cause a
// fetch, a navigation, or script execution.
var allowedAttrs = map[string]bool{
	"x": true, "y": true, "width": true, "height": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"x1": true, "y1": true, "x2": true, "y2": true,
	"points": true, "d": true, "dx": true, "dy": true, "transform": true,
	"fill": true, "fill-opacity": true, "fill-rule": true,
	"stroke": true, "stroke-width": true, "stroke-opacity": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-dasharray": true,
	"opacity": true, "font-family": true, "font-size": true,
	"font-weight": true, "font-style": true, "letter-spacing": true,
	"text-anchor": true, "dominant-baseline": true, "class": true,
}

// colorAttrs are matched against safeColor. url(#…) would be same-document and
// harmless, but url(http://…) is an outbound fetch that leaks the reader's IP
// and referer from inside a page they think is static, so no url() is allowed
// at all.
var colorAttrs = map[string]bool{"fill": true, "stroke": true}

var (
	safeColor  = regexp.MustCompile(`^(none|currentColor|transparent|#[0-9a-fA-F]{3,8}|rgba?\([0-9.,%\s]+\)|hsla?\([0-9.,%\sdegra]+\)|[a-zA-Z]{3,20})$`)
	safeNumber = regexp.MustCompile(`^-?[0-9.]+(e-?[0-9]+)?$`)
	viewBoxRe  = regexp.MustCompile(`^-?[0-9.]+\s+-?[0-9.]+\s+[0-9.]+\s+[0-9.]+$`)
	// Attribute values are re-escaped on output, so the only characters that
	// could still mean something are the ones a CSS or URL parser would act
	// on. Rejecting the value outright is cheaper than reasoning about it.
	dangerousValue = regexp.MustCompile(`(?i)(javascript:|data:|url\s*\(|expression\s*\(|<|&#)`)
)

// Sanitize extracts the SVG document from a model response and rewrites it
// using only constructs on the allowlist.
//
// The returned markup is safe to inline without escaping. An error means
// nothing usable survived -- callers should treat that as "no infographic this
// time" rather than substituting the raw input.
func Sanitize(raw string) (string, error) {
	doc, err := extractDocument(raw)
	if err != nil {
		return "", err
	}

	dec := xml.NewDecoder(strings.NewReader(doc))
	// A fixed table of HTML entities, not a document-supplied one: models emit
	// &nbsp; and &amp; freely, and there is no DTD to expand, so this adds
	// tolerance without adding an entity-expansion surface.
	dec.Entity = xml.HTMLEntity

	var (
		out       strings.Builder
		open      []string // allowed elements currently open, for closing tags
		skipDepth int      // >0 while inside a dropped element
		elements  int
		sawRoot   bool
		title     string
	)

	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return "", fmt.Errorf("parsing svg: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			name := t.Name.Local
			if !allowedElements[name] || (t.Name.Space != "" && t.Name.Space != svgNS) {
				// Skip the element and everything inside it. Dropping only the
				// tag would promote its children into the output, which for
				// <foreignObject> means promoting raw HTML.
				skipDepth = 1
				continue
			}
			if elements++; elements > MaxElements {
				return "", fmt.Errorf("svg has more than %d elements", MaxElements)
			}

			if name == "svg" {
				if sawRoot {
					// A nested <svg> establishes a new viewport and can escape
					// the layout the page reserved for it.
					skipDepth = 1
					continue
				}
				sawRoot = true
				vb := attrValue(t, "viewBox")
				if !viewBoxRe.MatchString(strings.TrimSpace(vb)) {
					return "", fmt.Errorf("svg root needs a numeric viewBox, got %q", vb)
				}
				// width/height are dropped on purpose so CSS controls the size;
				// a model-chosen pixel width would overflow the card it sits in.
				out.WriteString(`<svg xmlns="` + svgNS + `" viewBox="` + vb +
					`" class="infographic-svg" role="img" preserveAspectRatio="xMidYMid meet">`)
				open = append(open, name)
				continue
			}
			if !sawRoot {
				skipDepth = 1
				continue
			}

			out.WriteString("<" + name)
			for _, a := range t.Attr {
				if v, ok := cleanAttr(a); ok {
					out.WriteString(" " + a.Name.Local + `="`)
					xml.EscapeText(&out, []byte(v))
					out.WriteString(`"`)
				}
			}
			out.WriteString(">")
			open = append(open, name)

		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if n := len(open); n > 0 && open[n-1] == t.Name.Local {
				out.WriteString("</" + t.Name.Local + ">")
				open = open[:n-1]
			}

		case xml.CharData:
			if skipDepth > 0 || len(open) == 0 {
				continue
			}
			cur := open[len(open)-1]
			if !textElements[cur] {
				continue
			}
			text := string(t)
			if cur == "title" {
				title += text
			}
			xml.EscapeText(&out, []byte(text))
		}
		// Comments, processing instructions and directives are dropped: none of
		// them render, and a directive is where a DTD would arrive.
	}

	if !sawRoot {
		return "", fmt.Errorf("no <svg> root element")
	}
	// Close anything the model left open rather than emitting invalid markup
	// that the browser would repair by adopting following page content.
	for i := len(open) - 1; i >= 0; i-- {
		out.WriteString("</" + open[i] + ">")
	}

	result := out.String()
	if strings.TrimSpace(title) == "" {
		return "", fmt.Errorf("svg has no <title>, so it has no accessible name")
	}
	if !strings.Contains(result, "<text") {
		// A chart with no labels is decoration, and this one exists to carry
		// the article's numbers.
		return "", fmt.Errorf("svg contains no text")
	}
	return result, nil
}

// Title returns the accessible name of a sanitised SVG, for use as alt text
// where the markup itself cannot be inlined.
func Title(sanitized string) string {
	start := strings.Index(sanitized, "<title>")
	end := strings.Index(sanitized, "</title>")
	if start == -1 || end <= start {
		return ""
	}
	return strings.TrimSpace(sanitized[start+len("<title>") : end])
}

// extractDocument finds the SVG inside whatever the model wrapped it in --
// prose, a ```svg fence, an XML declaration -- and rejects anything oversized
// before it reaches the parser.
func extractDocument(raw string) (string, error) {
	if len(raw) > MaxInputBytes {
		return "", fmt.Errorf("response is %d bytes, over the %d limit", len(raw), MaxInputBytes)
	}
	start := strings.Index(raw, "<svg")
	if start == -1 {
		return "", fmt.Errorf("response contains no <svg element")
	}
	end := strings.LastIndex(raw, "</svg>")
	if end == -1 || end < start {
		// Truncated generation: the model ran out of tokens mid-diagram.
		return "", fmt.Errorf("svg is unterminated")
	}
	return raw[start : end+len("</svg>")], nil
}

func attrValue(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// cleanAttr reports whether an attribute may be emitted, and with what value.
func cleanAttr(a xml.Attr) (string, bool) {
	// Namespaced attributes are rejected wholesale. This is what removes
	// xlink:href without needing to enumerate it, and it costs nothing:
	// no presentation attribute is namespaced.
	if a.Name.Space != "" {
		return "", false
	}
	name := a.Name.Local
	// Event handlers all start with "on" and none are on the allowlist, but
	// checking explicitly means a future allowlist edit cannot let one in.
	if strings.HasPrefix(strings.ToLower(name), "on") || !allowedAttrs[name] {
		return "", false
	}
	v := strings.TrimSpace(a.Value)
	if v == "" || len(v) > 2048 || dangerousValue.MatchString(v) {
		return "", false
	}
	if colorAttrs[name] && !safeColor.MatchString(v) {
		return "", false
	}
	if name == "opacity" || name == "fill-opacity" || name == "stroke-opacity" {
		if !safeNumber.MatchString(v) {
			return "", false
		}
	}
	return v, true
}
