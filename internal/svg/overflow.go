package svg

import (
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Overflow returns the text in a chart that runs past the edges of its
// viewBox, one description per run of text. The chart is shown as an image,
// which clips at the viewBox, so a label that runs past it loses whatever
// falls outside -- usually the airline name or the number the bar is for.
//
// Measured on the production models' first charts, five of nine had clipped
// text: row labels far longer than the prompt's limit, right-aligned so they
// ran off the left edge. Nothing else catches it; every number is still in
// the markup, so the number guard is satisfied.
//
// Widths are estimated, not rendered: each character's advance width in
// Helvetica (Arial has the same metrics), which is what sans-serif resolves
// to on macOS, iOS and Windows, scaled by font size, with bold about 7%
// wider. Android's Roboto is a little narrower. Linux's DejaVu Sans is about
// a tenth wider, so text that only just fits may clip there; the check is for
// text that plainly does not fit.
//
// It reads font-size, font-weight, letter-spacing, text-anchor and
// dominant-baseline as the chart inherits them, x, y, dx and dy on <text> and
// <tspan>, and transform on every enclosing element. A <tspan> with its own x
// or y starts a new run, positioned and anchored on its own, as in SVG.
//
// It parses the input itself, and returns an error when it cannot.
func Overflow(doc string) ([]string, error) {
	dec := xml.NewDecoder(strings.NewReader(doc))
	dec.Strict = true
	dec.Entity = xml.HTMLEntity

	var (
		stack []textStyle
		frame box // the viewBox, once the root is read
		run   *textRun
		pen   struct{ x, y float64 }
		out   []string
	)
	finish := func() {
		if run == nil {
			return
		}
		if msg := run.overflow(frame); msg != "" {
			out = append(out, msg)
		}
		run = nil
	}
	start := func(st textStyle) {
		finish()
		run = &textRun{style: st, x: pen.x, y: pen.y, top: math.Inf(1), bottom: math.Inf(-1)}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading svg: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				m := viewBoxRe.FindStringSubmatch(strings.TrimSpace(attrValue(t, "viewBox")))
				if m == nil {
					return nil, fmt.Errorf("svg root needs a numeric viewBox")
				}
				frame = box{num(m[1]), num(m[2]), num(m[1]) + num(m[3]), num(m[2]) + num(m[4])}
				stack = append(stack, textStyle{fontSize: defaultFontSize, ctm: identity})
				continue
			}
			parent := stack[len(stack)-1]
			st := parent.inherit(t, frame)
			stack = append(stack, st)

			name := t.Name.Local
			inText := parent.element == "text" || parent.element == "tspan"
			switch {
			case name == "text" && !inText:
				pen.x, pen.y = 0, 0
				st.position(t, frame, &pen.x, &pen.y)
				start(st)
			case name == "tspan" && inText && run != nil:
				_, hasX := attr(t, "x")
				_, hasY := attr(t, "y")
				if hasX || hasY {
					// An absolute position starts a new chunk of text, which
					// SVG anchors on its own. Without an x it carries on from
					// where the last chunk ended.
					pen.x = run.end()
					st.position(t, frame, &pen.x, &pen.y)
					start(st)
					break
				}
				// A relative shift stays in the run: dx widens it, dy moves
				// what follows up or down.
				run.width += st.length(t, "dx", frame.width())
				pen.y += st.length(t, "dy", frame.height())
			}
			stack[len(stack)-1].element = name

		case xml.EndElement:
			if len(stack) > 0 {
				if stack[len(stack)-1].element == "text" {
					finish()
				}
				stack = stack[:len(stack)-1]
			}

		case xml.CharData:
			if run == nil || len(stack) == 0 {
				continue
			}
			st := stack[len(stack)-1]
			if st.element != "text" && st.element != "tspan" {
				continue
			}
			run.add(string(t), st, pen.y)
		}
	}
	finish()
	return out, nil
}

// defaultFontSize is what SVG text is set at when nothing says otherwise.
const defaultFontSize = 16

// overflowSlack is how far past an edge an estimate may fall before it
// counts: the widths are a model of the font, not a measurement.
const overflowSlack = 2

// boldWidth is how much wider bold Helvetica sets than regular, averaged over
// mixed-case text.
const boldWidth = 1.07

// textStyle is what a text run inherits from the elements around it.
type textStyle struct {
	element       string
	fontSize      float64
	bold          bool
	letterSpacing float64
	anchor        string // start, middle or end
	baseline      string // dominant-baseline
	ctm           matrix // user space to viewBox space
}

func (p textStyle) inherit(t xml.StartElement, frame box) textStyle {
	st := p
	st.element = ""
	if v, ok := attr(t, "font-size"); ok {
		if f, ok := fontSize(v, p.fontSize); ok {
			st.fontSize = f
		}
	}
	if v, ok := attr(t, "font-weight"); ok {
		switch v {
		case "bold", "bolder":
			st.bold = true
		case "normal", "lighter":
			st.bold = false
		default:
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				st.bold = n >= 600
			}
		}
	}
	if v, ok := attr(t, "letter-spacing"); ok {
		if v == "normal" {
			st.letterSpacing = 0
		} else if f, ok := lengthValue(v, st.fontSize, frame.width()); ok {
			st.letterSpacing = f
		}
	}
	if v, ok := attr(t, "text-anchor"); ok && (v == "start" || v == "middle" || v == "end") {
		st.anchor = v
	}
	if v, ok := attr(t, "dominant-baseline"); ok {
		st.baseline = v
	}
	if v, ok := attr(t, "transform"); ok && t.Name.Local != "tspan" {
		// An invalid transform is ignored by browsers, so it is here too.
		// A <tspan> cannot be transformed; its text is set in the <text>'s
		// coordinates.
		if m, ok := parseTransform(v); ok {
			st.ctm = p.ctm.mul(m)
		}
	}
	return st
}

// position sets the pen from an element's x and y, where it has them, then
// shifts it by dx and dy.
func (p textStyle) position(t xml.StartElement, frame box, x, y *float64) {
	if v, ok := attr(t, "x"); ok {
		if f, ok := lengthValue(v, p.fontSize, frame.width()); ok {
			*x = f
		}
	}
	if v, ok := attr(t, "y"); ok {
		if f, ok := lengthValue(v, p.fontSize, frame.height()); ok {
			*y = f
		}
	}
	*x += p.length(t, "dx", frame.width())
	*y += p.length(t, "dy", frame.height())
}

func (p textStyle) length(t xml.StartElement, name string, ref float64) float64 {
	v, ok := attr(t, name)
	if !ok {
		return 0
	}
	f, _ := lengthValue(v, p.fontSize, ref)
	return f
}

// textRun is one chunk of text: set from one position and anchored as one.
type textRun struct {
	style       textStyle // of the element that started it
	x, y        float64   // where it starts
	text        strings.Builder
	width       float64
	lastSpace   bool
	spaceWidth  float64 // of the last space added
	top, bottom float64 // vertical extent, in user space
}

// left is where the run's first character is drawn, were it width wide.
func (r *textRun) left(width float64) float64 {
	switch r.style.anchor {
	case "middle":
		return r.x - width/2
	case "end":
		return r.x - width
	}
	return r.x
}

// end is where the next character after the run would be set.
func (r *textRun) end() float64 { return r.left(r.width) + r.width }

// add sets s at st, collapsing whitespace as SVG does by default.
func (r *textRun) add(s string, st textStyle, baseline float64) {
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if r.text.Len() == 0 || r.lastSpace {
				continue
			}
			c, r.lastSpace = ' ', true
		} else {
			r.lastSpace = false
		}
		w := charWidth(c) * st.fontSize
		if st.bold {
			w *= boldWidth
		}
		w += st.letterSpacing
		if c == ' ' {
			r.spaceWidth = w
		}
		r.width += w
		r.text.WriteRune(c)
		if c != ' ' {
			top, bottom := lineBox(baseline, st)
			r.top, r.bottom = math.Min(r.top, top), math.Max(r.bottom, bottom)
		}
	}
}

// lineBox is the vertical extent of a glyph set on baseline y: Helvetica's
// cap height above it and descender below, moved as dominant-baseline asks.
func lineBox(y float64, st textStyle) (top, bottom float64) {
	const ascent, descent = 0.72, 0.21
	fs := st.fontSize
	switch st.baseline {
	case "middle", "central", "mathematical":
		return y - (ascent+descent)/2*fs, y + (ascent+descent)/2*fs
	case "hanging", "text-before-edge":
		return y, y + (ascent+descent)*fs
	case "text-after-edge", "ideographic":
		return y - (ascent+descent)*fs, y
	}
	return y - ascent*fs, y + descent*fs
}

// overflow describes how far the run falls outside frame, or returns "".
func (r *textRun) overflow(frame box) string {
	text := strings.TrimRight(r.text.String(), " ")
	if text == "" {
		return ""
	}
	width := r.width
	if r.lastSpace {
		// A trailing space is collapsed away, not drawn.
		width -= r.spaceWidth
	}
	x0 := r.left(width)
	b := r.style.ctm.bounds(box{x0, r.top, x0 + width, r.bottom})

	var edges []string
	if d := frame.x0 - b.x0; d > overflowSlack {
		edges = append(edges, fmt.Sprintf("%.0f past the left edge", d))
	}
	if d := b.x1 - frame.x1; d > overflowSlack {
		edges = append(edges, fmt.Sprintf("%.0f past the right edge", d))
	}
	if d := frame.y0 - b.y0; d > overflowSlack {
		edges = append(edges, fmt.Sprintf("%.0f past the top", d))
	}
	if d := b.y1 - frame.y1; d > overflowSlack {
		edges = append(edges, fmt.Sprintf("%.0f past the bottom", d))
	}
	if len(edges) == 0 {
		return ""
	}
	if runes := []rune(text); len(runes) > 48 {
		text = string(runes[:47]) + "…"
	}
	return fmt.Sprintf("%q runs about %s", text, strings.Join(edges, " and "))
}

// helvetica is the advance width of each printable ASCII character, from ' '
// to '~', in thousandths of an em: Helvetica's metrics, which Arial shares.
var helvetica = [95]float64{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // space to /
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, // 0 to 9
	278, 278, 584, 584, 584, 556, 1015, // : to @
	667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, // A to M
	722, 778, 667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, // N to Z
	278, 278, 278, 469, 556, 333, // [ to `
	556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, // a to m
	556, 556, 556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, // n to z
	334, 260, 334, 584, // { to ~
}

// wideChars are the non-ASCII characters charts use, in the same units.
var wideChars = map[rune]float64{
	'–': 556, '—': 1000, '‘': 222, '’': 222, '“': 333, '”': 333, '…': 1000,
	'•': 350, '·': 278, '€': 556, '£': 556, '¢': 556, '×': 584, '÷': 584,
	'±': 584, '≈': 584, '°': 400, '→': 1000, '←': 1000, ' ': 278,
}

// charWidth is c's advance width in ems. Characters outside the tables are
// taken as slightly wider than average.
func charWidth(c rune) float64 {
	if c >= ' ' && c <= '~' {
		return helvetica[c-' '] / 1000
	}
	if w, ok := wideChars[c]; ok {
		return w / 1000
	}
	return 0.6
}

// fontSize reads a font-size against the inherited one.
func fontSize(v string, inherited float64) (float64, bool) {
	switch v {
	case "xx-small":
		return 9, true
	case "x-small":
		return 10, true
	case "small":
		return 13, true
	case "medium":
		return 16, true
	case "large":
		return 18, true
	case "x-large":
		return 24, true
	case "xx-large":
		return 32, true
	case "smaller":
		return inherited / 1.2, true
	case "larger":
		return inherited * 1.2, true
	}
	f, ok := lengthValue(v, inherited, inherited)
	return f, ok && f > 0
}

var lengthRe = regexp.MustCompile(`^(-?[0-9]*\.?[0-9]+(?:e-?[0-9]+)?)(px|pt|pc|em|ex|in|cm|mm|%)?$`)

// lengthValue reads a length in user units. em and ex are of fontSize; a
// percentage is of ref. Of a list of positions ("10 20 30") only the first is
// read: the rest place single characters, and the run is measured as a whole.
func lengthValue(v string, fontSize, ref float64) (float64, bool) {
	if f := strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' }); len(f) > 0 {
		v = f[0]
	}
	m := lengthRe.FindStringSubmatch(v)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "pt":
		n *= 4.0 / 3
	case "pc":
		n *= 16
	case "em":
		n *= fontSize
	case "ex":
		n *= fontSize / 2
	case "in":
		n *= 96
	case "cm":
		n *= 96 / 2.54
	case "mm":
		n *= 96 / 25.4
	case "%":
		n *= ref / 100
	}
	return n, true
}

func attr(t xml.StartElement, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return strings.TrimSpace(a.Value), true
		}
	}
	return "", false
}

func num(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// box is an axis-aligned rectangle.
type box struct{ x0, y0, x1, y1 float64 }

func (b box) width() float64  { return b.x1 - b.x0 }
func (b box) height() float64 { return b.y1 - b.y0 }

// matrix is an SVG transform: [a b c d e f], mapping (x, y) to
// (a·x + c·y + e, b·x + d·y + f).
type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul returns m followed by n: n applies first, as in transform="m n".
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[2]*n[1],
		m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3],
		m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4],
		m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

// bounds is the axis-aligned box around b once transformed.
func (m matrix) bounds(b box) box {
	out := box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range [4][2]float64{{b.x0, b.y0}, {b.x1, b.y0}, {b.x0, b.y1}, {b.x1, b.y1}} {
		x := m[0]*p[0] + m[2]*p[1] + m[4]
		y := m[1]*p[0] + m[3]*p[1] + m[5]
		out = box{math.Min(out.x0, x), math.Min(out.y0, y), math.Max(out.x1, x), math.Max(out.y1, y)}
	}
	return out
}

var (
	transformRe    = regexp.MustCompile(`\s*([a-zA-Z]+)\s*\(([^()]*)\)\s*,?`)
	transformArgRe = regexp.MustCompile(`[\s,]+`)
)

// parseTransform reads an SVG transform list, reporting false for one a
// browser would reject.
func parseTransform(v string) (matrix, bool) {
	m := identity
	rest := v
	for strings.TrimSpace(rest) != "" {
		loc := transformRe.FindStringSubmatchIndex(rest)
		if loc == nil || loc[0] != 0 {
			return identity, false
		}
		name := rest[loc[2]:loc[3]]
		var args []float64
		for _, s := range transformArgRe.Split(strings.TrimSpace(rest[loc[4]:loc[5]]), -1) {
			if s == "" {
				continue
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return identity, false
			}
			args = append(args, f)
		}
		rest = rest[loc[1]:]

		var t matrix
		switch {
		case name == "matrix" && len(args) == 6:
			t = matrix{args[0], args[1], args[2], args[3], args[4], args[5]}
		case name == "translate" && len(args) == 1:
			t = matrix{1, 0, 0, 1, args[0], 0}
		case name == "translate" && len(args) == 2:
			t = matrix{1, 0, 0, 1, args[0], args[1]}
		case name == "scale" && len(args) == 1:
			t = matrix{args[0], 0, 0, args[0], 0, 0}
		case name == "scale" && len(args) == 2:
			t = matrix{args[0], 0, 0, args[1], 0, 0}
		case name == "rotate" && (len(args) == 1 || len(args) == 3):
			sin, cos := math.Sincos(args[0] * math.Pi / 180)
			t = matrix{cos, sin, -sin, cos, 0, 0}
			if len(args) == 3 {
				cx, cy := args[1], args[2]
				t = matrix{1, 0, 0, 1, cx, cy}.mul(t).mul(matrix{1, 0, 0, 1, -cx, -cy})
			}
		case name == "skewX" && len(args) == 1:
			t = matrix{1, 0, math.Tan(args[0] * math.Pi / 180), 1, 0, 0}
		case name == "skewY" && len(args) == 1:
			t = matrix{1, math.Tan(args[0] * math.Pi / 180), 0, 1, 0, 0}
		default:
			return identity, false
		}
		m = m.mul(t)
	}
	return m, true
}
