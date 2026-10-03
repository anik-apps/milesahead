// Package svg turns model-authored SVG markup into a small, predictable SVG
// document that is safe to show to readers.
//
// The model that writes this markup is prompted with article text that was
// itself written from third-party sites, so its output is treated as
// attacker-influenced rather than merely untidy.
//
// The page never inlines the result. It is shown through an <img> whose src is
// a data: URL (see internal/web/infographic.go), and an SVG loaded as an image
// runs no script, loads no external resource and gets an opaque origin, so a
// gap in this package cannot become same-origin script on the site. This
// package is the second layer, and still a strict one: it bounds size and
// shape, keeps the document to drawing primitives and text, and gives the
// outlet guards one place to read every word the chart shows (Text).
//
// Two rules do the work. Everything is allowlisted -- an element or attribute
// not named below is dropped rather than inspected -- and nothing from the
// input is ever copied through verbatim: what survives is re-serialised by
// this package, so the output can only contain constructs it knows how to
// emit.
package svg

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const (
	// MaxInputBytes bounds what is parsed at all. A labelled bar chart is a
	// few kilobytes; anything near this is a runaway generation, not a
	// diagram.
	MaxInputBytes = 64 << 10

	// MaxElements bounds the cost of rendering the result. The prompt asks
	// for under 60.
	MaxElements = 400

	// MaxDepth bounds nesting. A chart needs two or three levels of <g>;
	// thousands of nested groups are a resource attack on the renderer, not
	// a layout.
	MaxDepth = 16

	// maxAttrLen bounds a single attribute value. Long path data is the only
	// legitimate reason for a long value, and a chart does not need 2 KB of it.
	maxAttrLen = 2048

	// maxViewBox bounds the canvas the model may declare.
	maxViewBox = 4000

	// svgNS is the only namespace whose elements are kept. Anything else --
	// XHTML smuggled through <foreignObject>, xlink references -- is dropped.
	svgNS = "http://www.w3.org/2000/svg"

	// Ink is the colour currentColor resolves to inside the chart. An SVG
	// shown as an image cannot inherit the page's text colour, so the root
	// sets it; the page darkens or inverts the whole image for the dark theme.
	Ink = "#1a1a2e"
)

// allowedElements is drawing primitives and text only.
//
// The notable absences are deliberate: <script> and <handler> execute;
// <foreignObject> embeds arbitrary HTML; <use> and <image> dereference other
// documents; <a> navigates; <style> brings CSS with its own url() fetches and
// selectors; <animate*> and <set> can retarget attributes after sanitisation
// has run; and <defs>, gradients, <filter>, <clipPath> and <mask> are left out
// because they only work through url(#id) references, which are refused.
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
// fetch, a navigation, or script execution. "class" and "id" are absent on
// purpose: there is no stylesheet for a class to select, and an id is only
// useful as the target of a reference.
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
	"text-anchor": true, "dominant-baseline": true,
}

// colorAttrs are matched against safeColor. url(#…) would be same-document and
// harmless, but url(http://…) is an outbound fetch, so no url() is allowed.
var colorAttrs = map[string]bool{"fill": true, "stroke": true}

var opacityAttrs = map[string]bool{"opacity": true, "fill-opacity": true, "stroke-opacity": true}

var (
	safeColor  = regexp.MustCompile(`^(none|currentColor|transparent|#[0-9a-fA-F]{3,8}|rgba?\([0-9.,%\s]+\)|hsla?\([0-9.,%\sdegra]+\)|[a-zA-Z]{3,20})$`)
	safeNumber = regexp.MustCompile(`^-?[0-9]*\.?[0-9]+(e-?[0-9]+)?%?$`)
	viewBoxRe  = regexp.MustCompile(`^(-?[0-9]*\.?[0-9]+)[\s,]+(-?[0-9]*\.?[0-9]+)[\s,]+([0-9]*\.?[0-9]+)[\s,]+([0-9]*\.?[0-9]+)$`)

	// safeValue is the whole alphabet an attribute value other than
	// font-family may use: digits, letters, the punctuation of numbers, path
	// data, transforms and colour functions. No colon, slash, quote,
	// semicolon, ampersand or angle bracket, so no URL scheme, CSS
	// declaration or markup can be spelt in it.
	safeValue = regexp.MustCompile(`^[A-Za-z0-9#.,%()+\-\s]*$`)
	// safeFontFamily also allows the quotes and spaces of a font list.
	safeFontFamily = regexp.MustCompile(`^[A-Za-z0-9 ,'"\-]*$`)
	// dangerousValue is checked as well, so a future widening of the
	// alphabets above cannot quietly let one of these through.
	dangerousValue = regexp.MustCompile(`(?i)(javascript|data:|url\s*\(|expression\s*\(|<|&#|\\)`)
)

// ErrNoRoot means the input held no <svg> document at all.
var ErrNoRoot = errors.New("no <svg> root element")

// Sanitize extracts the SVG document from a model response and rewrites it
// using only constructs on the allowlist.
//
// An error means nothing usable survived -- callers should treat that as "no
// infographic this time" rather than substituting the raw input.
func Sanitize(raw string) (string, error) {
	doc, err := extractDocument(raw)
	if err != nil {
		return "", err
	}

	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = true
	// A fixed table of HTML entities, not a document-supplied one: models emit
	// &nbsp; and &amp; freely, and there is no DTD to expand, so this adds
	// tolerance without adding an entity-expansion surface.
	dec.Entity = xml.HTMLEntity

	var (
		out        strings.Builder
		open       []string // allowed elements currently open, for closing tags
		skipDepth  int      // >0 while inside a dropped element
		elements   int
		sawRoot    bool
		rootClosed bool
		title      string
		hasText    bool
	)

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("parsing svg: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if skipDepth > 0 {
				skipDepth++
				if skipDepth+len(open) > MaxDepth*4 {
					return "", fmt.Errorf("svg nests deeper than %d", MaxDepth*4)
				}
				continue
			}
			name := t.Name.Local
			if rootClosed || !allowedElements[name] || (t.Name.Space != "" && t.Name.Space != svgNS) {
				// Skip the element and everything inside it. Dropping only the
				// tag would promote its children into the output, which for
				// <foreignObject> means promoting raw HTML. Anything after the
				// root has closed is outside the document and goes too.
				skipDepth = 1
				continue
			}
			if !sawRoot && name != "svg" {
				skipDepth = 1
				continue
			}
			if name == "svg" && sawRoot {
				// A nested <svg> establishes a new viewport and coordinate
				// system the page did not size for.
				skipDepth = 1
				continue
			}
			if elements++; elements > MaxElements {
				return "", fmt.Errorf("svg has more than %d elements", MaxElements)
			}
			if len(open) >= MaxDepth {
				return "", fmt.Errorf("svg nests deeper than %d", MaxDepth)
			}

			if name == "svg" {
				sawRoot = true
				w, h, vb, err := parseViewBox(attrValue(t, "viewBox"))
				if err != nil {
					return "", err
				}
				// The model's own width/height are dropped: the canvas is the
				// viewBox, and the page decides how large it is shown.
				fmt.Fprintf(&out, `<svg xmlns="%s" viewBox="%s" width="%s" height="%s" color="%s" role="img">`,
					svgNS, vb, w, h, Ink)
				open = append(open, name)
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
			// Strict parsing guarantees end tags match their start tags, so
			// the element closing here is always the top of the stack.
			if n := len(open); n > 0 {
				out.WriteString("</" + open[n-1] + ">")
				open = open[:n-1]
				if n == 1 {
					rootClosed = true
				}
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
			switch cur {
			case "title":
				title += text
			case "text", "tspan":
				if strings.TrimSpace(text) != "" {
					hasText = true
				}
			}
			xml.EscapeText(&out, []byte(text))
		}
		// Comments, processing instructions and directives are dropped: none of
		// them render, and a directive is where a DTD would arrive.
	}

	if !sawRoot {
		return "", ErrNoRoot
	}
	if !rootClosed {
		// Strict parsing already fails on an unterminated document; this is
		// the belt to that brace.
		return "", fmt.Errorf("svg is unterminated")
	}
	if strings.TrimSpace(title) == "" {
		return "", fmt.Errorf("svg has no <title>, so it has no accessible name")
	}
	if !hasText {
		// A chart with no labels is decoration, and this one exists to carry
		// the article's numbers.
		return "", fmt.Errorf("svg contains no text")
	}
	return out.String(), nil
}

// parseViewBox validates a viewBox and returns its width, height and a
// normalised form. Only plain numbers are accepted, and the canvas must have a
// positive, bounded size.
func parseViewBox(raw string) (w, h, normalised string, err error) {
	m := viewBoxRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", "", "", fmt.Errorf("svg root needs a numeric viewBox, got %q", raw)
	}
	for _, s := range m[3:5] {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 || f > maxViewBox {
			return "", "", "", fmt.Errorf("svg viewBox size %q is outside 0-%d", s, maxViewBox)
		}
	}
	return m[3], m[4], strings.Join(m[1:5], " "), nil
}

// Title returns the accessible name of a sanitised SVG, still XML-escaped.
func Title(sanitized string) string {
	return firstElementText(sanitized, "title")
}

// Desc returns the description of a sanitised SVG, still XML-escaped, or "".
func Desc(sanitized string) string {
	return firstElementText(sanitized, "desc")
}

func firstElementText(sanitized, name string) string {
	open, end := "<"+name+">", "</"+name+">"
	start := strings.Index(sanitized, open)
	if start == -1 {
		return ""
	}
	stop := strings.Index(sanitized[start:], end)
	if stop == -1 {
		return ""
	}
	return strings.TrimSpace(sanitized[start+len(open) : start+stop])
}

// Text returns every word a reader can see or hear in an SVG -- its title,
// description and all text -- one run per line, unescaped. It is what the
// outlet and source-reference guards check, so a chart cannot name a news
// site that the same guards would have refused in prose.
//
// It parses the input itself rather than trusting it to be sanitised, and
// returns an error when it cannot.
func Text(doc string) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity
	var (
		b     strings.Builder
		stack []string
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("reading svg text: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			// A <tspan> continues its line, so a name split across two
			// of them is still read as one phrase.
			if t.Name.Local == "tspan" {
				b.WriteString(" ")
			} else {
				b.WriteString("\n")
			}
		case xml.CharData:
			if len(stack) > 0 && textElements[stack[len(stack)-1]] {
				b.Write(t)
			}
		}
	}
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n"), nil
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
		return "", ErrNoRoot
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
		if a.Name.Space == "" && a.Name.Local == name {
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
	if v == "" || len(v) > maxAttrLen || dangerousValue.MatchString(v) {
		return "", false
	}
	alphabet := safeValue
	if name == "font-family" {
		alphabet = safeFontFamily
	}
	if !alphabet.MatchString(v) {
		return "", false
	}
	if colorAttrs[name] && !safeColor.MatchString(v) {
		return "", false
	}
	if opacityAttrs[name] && !safeNumber.MatchString(v) {
		return "", false
	}
	return v, true
}
