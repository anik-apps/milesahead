package svg

import (
	"encoding/xml"
	"io"
	"regexp"
	"strings"
	"testing"
)

const validWrapper = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360">` +
	`<title>Fee vs value</title>%s<text x="10" y="20">$895</text></svg>`

func sanitize(t *testing.T, inner string) string {
	t.Helper()
	got, err := Sanitize(strings.Replace(validWrapper, "%s", inner, 1))
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	return got
}

// Each case below is a distinct way to smuggle execution, a fetch or page
// styling past a naive filter. The page shows the result as an image, which
// would neutralise most of these anyway; the sanitiser must not rely on that.
func TestSanitize_StripsExecution(t *testing.T) {
	cases := []struct {
		name   string
		inner  string
		banned []string
	}{
		{"script element", `<script>alert(1)</script>`, []string{"alert", "script"}},
		{"script uppercase", `<SCRIPT>alert(1)</SCRIPT>`, []string{"alert", "script"}},
		{"script with cdata", `<script><![CDATA[alert(1)]]></script>`, []string{"alert"}},
		{"svg-prefixed script", `<s:script xmlns:s="http://www.w3.org/2000/svg">alert(1)</s:script>`, []string{"alert", "script"}},
		{"onload handler", `<rect x="1" y="1" width="2" height="2" onload="alert(1)"/>`, []string{"onload", "alert"}},
		{"onclick handler", `<rect x="1" y="1" width="2" height="2" onclick="alert(1)"/>`, []string{"onclick", "alert"}},
		{"mixed-case handler", `<rect x="1" y="1" width="2" height="2" OnMouseOver="alert(1)"/>`, []string{"alert"}},
		// Dropping the tag but keeping children would promote raw HTML into
		// the output -- the children must go with it.
		{"foreignObject html", `<foreignObject width="9" height="9"><body xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(1)"/></body></foreignObject>`, []string{"foreignObject", "onerror", "alert", "<img"}},
		{"anchor javascript uri", `<a xlink:href="javascript:alert(1)" xmlns:xlink="http://www.w3.org/1999/xlink"><text x="1" y="1">go</text></a>`, []string{"javascript", "xlink", "<a "}},
		{"xlink href on allowed element", `<rect xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="https://evil.test/" x="1" y="1" width="2" height="2"/>`, []string{"evil.test", "href"}},
		{"plain href on allowed element", `<rect href="https://evil.test/" x="1" y="1" width="2" height="2"/>`, []string{"evil.test", "href"}},
		{"external image", `<image href="https://evil.test/track.png" width="9" height="9"/>`, []string{"evil.test", "<image"}},
		{"use external ref", `<use href="https://evil.test/x.svg#a"/>`, []string{"evil.test", "<use"}},
		{"style element", `<style>@import url(https://evil.test/x.css);</style>`, []string{"evil.test", "@import", "<style"}},
		{"style attribute", `<rect x="1" y="1" width="2" height="2" style="background:url(https://evil.test/x)"/>`, []string{"evil.test", "style="}},
		{"url fill", `<rect x="1" y="1" width="2" height="2" fill="url(https://evil.test/x)"/>`, []string{"evil.test", "url("}},
		{"url fill same-document", `<rect x="1" y="1" width="2" height="2" fill="url(#g)"/>`, []string{"url("}},
		{"data uri fill", `<rect x="1" y="1" width="2" height="2" fill="data:image/svg+xml;base64,AAAA"/>`, []string{"data:"}},
		{"url in transform", `<g transform="url(https://evil.test/)"><text x="1" y="1">a</text></g>`, []string{"evil.test", "url("}},
		{"css in font-family", `<text x="1" y="1" font-family="x;background:url(https://evil.test/)">a</text>`, []string{"evil.test", "url("}},
		{"entity-encoded scheme", `<rect x="1" y="1" width="2" height="2" fill="&#x6A;avascript:alert(1)"/>`, []string{"javascript", "alert"}},
		{"double-encoded entity", `<rect x="1" y="1" width="2" height="2" fill="&amp;#106;avascript:alert(1)"/>`, []string{"javascript", "alert", "&#"}},
		{"backslash css escape", `<text x="1" y="1" font-family="\75 rl(x)">a</text>`, []string{`\75`}},
		{"animate retarget", `<rect x="1" y="1" width="2" height="2"><animate attributeName="onload" to="alert(1)"/></rect>`, []string{"animate", "alert", "onload"}},
		{"set element", `<set attributeName="onload" to="alert(1)"/>`, []string{"<set", "alert"}},
		{"handler element", `<handler type="text/javascript">alert(1)</handler>`, []string{"handler", "alert"}},
		{"nested svg viewport escape", `<svg viewBox="0 0 1 1" width="99999"><text x="1" y="1">x</text></svg>`, []string{"99999"}},
		// class would let the chart pick up the page's own styles if it were
		// ever inlined; id is only ever the target of a reference.
		{"class attribute", `<rect class="admin-overlay" x="1" y="1" width="2" height="2"/>`, []string{"class=", "admin-overlay"}},
		{"id attribute", `<rect id="target" x="1" y="1" width="2" height="2"/>`, []string{"id=", "target"}},
		{"stylesheet processing instruction", `<?xml-stylesheet href="https://evil.test/x.css"?>`, []string{"evil.test", "stylesheet"}},
		{"comment", `<!-- <script>alert(1)</script> -->`, []string{"alert", "<!--"}},
		{"defs and gradient", `<defs><linearGradient id="g"><stop offset="0"/></linearGradient></defs>`, []string{"defs", "gradient", "stop"}},
		{"filter element", `<filter id="f"><feImage href="https://evil.test/"/></filter>`, []string{"filter", "feImage", "evil.test"}},
		{"xml:base", `<g xml:base="https://evil.test/"><text x="1" y="1">a</text></g>`, []string{"evil.test", "base"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitize(t, tc.inner)
			low := strings.ToLower(got)
			for _, b := range tc.banned {
				if strings.Contains(low, strings.ToLower(b)) {
					t.Errorf("output still contains %q:\n%s", b, got)
				}
			}
		})
	}
}

// Text is escaped on the way out, so markup spelt in a label stays a label.
func TestSanitize_TextIsEscaped(t *testing.T) {
	for _, inner := range []string{
		`<text x="1" y="1">Fee &amp; value &lt;b&gt;</text>`,
		`<text x="1" y="1"><![CDATA[<script>alert(1)</script>]]></text>`,
		`<text x="1" y="1">&lt;/svg&gt;&lt;script&gt;alert(1)&lt;/script&gt;</text>`,
	} {
		got := sanitize(t, inner)
		if strings.Contains(got, "<b>") || strings.Contains(got, "<script") || strings.Count(got, "</svg>") != 1 {
			t.Errorf("text content was not escaped:\n%s", got)
		}
	}
	if got := sanitize(t, `<text x="1" y="1">Fee &amp; value</text>`); !strings.Contains(got, "Fee &amp; value") {
		t.Errorf("ampersand lost its escaping:\n%s", got)
	}
}

// Anything after the root closes is outside the document. It used to be
// written after </svg>, where an inlined page would have shown it as page
// text.
func TestSanitize_DropsContentAfterRoot(t *testing.T) {
	got, err := Sanitize(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">a</text></svg>` +
		`<text x="1" y="1">outside</text><rect x="1" y="1" width="1" height="1"/>` +
		`<svg viewBox="0 0 8 4"><text x="1" y="1">second</text></svg>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "outside") || strings.Contains(got, "second") || !strings.HasSuffix(got, "</svg>") || strings.Count(got, "</svg>") != 1 {
		t.Errorf("content after the root survived:\n%s", got)
	}
}

func TestSanitize_KeepsDrawing(t *testing.T) {
	got := sanitize(t, `<g transform="translate(10,10)">`+
		`<rect x="0" y="0" width="300" height="24" fill="#4c8bf5" stroke="none"/>`+
		`<line x1="0" y1="0" x2="300" y2="0" stroke="#999" stroke-width="2"/>`+
		`<path d="M0 0 L10 10 Z" fill="currentColor"/>`+
		`<text x="4" y="18" font-size="18" font-family="'Helvetica Neue', sans-serif" text-anchor="start">Credits on paper</text>`+
		`<circle cx="5" cy="5" r="3" fill="hsl(210, 80%, 50%)" opacity="0.5"/>`+
		`</g>`)

	for _, want := range []string{
		`viewBox="0 0 560 360"`, `width="560"`, `height="360"`, `color="` + Ink + `"`, `role="img"`, `<title>`,
		`<rect`, `width="300"`, `fill="#4c8bf5"`,
		`<line`, `stroke-width="2"`, `<path`, `d="M0 0 L10 10 Z"`,
		`fill="currentColor"`, `transform="translate(10,10)"`,
		`text-anchor="start"`, `Credits on paper`, `font-family="&#39;Helvetica Neue&#39;, sans-serif"`,
		`fill="hsl(210, 80%, 50%)"`, `opacity="0.5"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output dropped %q:\n%s", want, got)
		}
	}
}

// The model sizes its own canvas; the page decides how big it renders.
func TestSanitize_RootSizeComesFromTheViewBox(t *testing.T) {
	got, err := Sanitize(`<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="900" viewBox="0 0 800 450">` +
		`<title>t</title><text x="1" y="1">a</text></svg>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "1600") || strings.Contains(got, `height="900"`) {
		t.Errorf("root kept its pixel size:\n%s", got)
	}
	if !strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450" width="800" height="450"`) {
		t.Errorf("root is not the canonical form:\n%s", got)
	}
}

func TestSanitize_Rejects(t *testing.T) {
	deep := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title>` +
		strings.Repeat("<g>", MaxDepth) + `<text x="1" y="1">a</text>` + strings.Repeat("</g>", MaxDepth) + `</svg>`
	deepDropped := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">a</text>` +
		strings.Repeat("<x>", 5000) + strings.Repeat("</x>", 5000) + `</svg>`
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"prose only", "I cannot create images."},
		{"no svg root", `<div><p>hello</p></div>`},
		{"truncated", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450"><title>t</title><rect x="1"`},
		{"unclosed element", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">a</svg>`},
		{"unquoted attribute", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x=1 y="1">a</text></svg>`},
		{"missing viewBox", `<svg xmlns="http://www.w3.org/2000/svg"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"non numeric viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 auto 450"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"dots for a viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox=". . . ."><title>t</title><text x="1" y="1">a</text></svg>`},
		{"zero-size viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 0 450"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"huge viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 99999 450"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"no title", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><text x="1" y="1">a</text></svg>`},
		{"no text", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><rect x="1" y="1" width="2" height="2"/></svg>`},
		{"only whitespace text", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">  </text></svg>`},
		{"oversized", `<svg viewBox="0 0 1 1">` + strings.Repeat("x", MaxInputBytes) + `</svg>`},
		{"too deep", deep},
		{"too deep inside a dropped element", deepDropped},
		// A DTD-declared entity is never expanded: there is no DTD support,
		// and the reference is an error rather than an empty string.
		{"custom entity", `<!DOCTYPE svg [<!ENTITY x "<script>alert(1)</script>">]><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">&x;</text></svg>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Sanitize(tc.in); err == nil {
				t.Errorf("Sanitize accepted %s: %q", tc.name, got)
			}
		})
	}
}

func TestSanitize_ElementCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">a</text>`)
	for i := 0; i <= MaxElements; i++ {
		b.WriteString(`<rect x="1" y="1" width="1" height="1"/>`)
	}
	b.WriteString(`</svg>`)
	if _, err := Sanitize(b.String()); err == nil {
		t.Error("Sanitize accepted an SVG over the element cap")
	}
}

// Models wrap output in fences and prose however they like.
func TestSanitize_ExtractsFromWrapping(t *testing.T) {
	inner := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text x="1" y="1">a</text></svg>`
	for _, wrapper := range []string{
		"Here you go:\n```svg\n" + inner + "\n```\nHope that helps!",
		"<?xml version=\"1.0\"?>\n" + inner,
		inner,
	} {
		if _, err := Sanitize(wrapper); err != nil {
			t.Errorf("Sanitize(%.30q…) = %v", wrapper, err)
		}
	}
}

// Sanitising the output again changes nothing: the web layer re-sanitises at
// render, and must show what was stored.
func TestSanitize_Idempotent(t *testing.T) {
	once := sanitize(t, `<g transform="translate(1,2)"><rect x="0" y="0" width="3" height="4" fill="#123"/>`+
		`<text x="1" y="1" font-family="'A B', sans-serif">Fees &amp; credits <tspan font-weight="bold">$95</tspan></text></g>`+
		`<desc>A bar chart.</desc>`)
	twice, err := Sanitize(once)
	if err != nil {
		t.Fatal(err)
	}
	if once != twice {
		t.Errorf("not idempotent:\n%s\n%s", once, twice)
	}
}

func TestTitleAndDesc(t *testing.T) {
	got := sanitize(t, `<desc>Bars.</desc>`)
	if Title(got) != "Fee vs value" {
		t.Errorf("Title = %q, want %q", Title(got), "Fee vs value")
	}
	if Desc(got) != "Bars." {
		t.Errorf("Desc = %q, want %q", Desc(got), "Bars.")
	}
	if Title("<svg></svg>") != "" || Desc("<svg><title>x</title></svg>") != "" {
		t.Error("found a title or description where there is none")
	}
}

// Text is what the outlet guards read, so it must see every word a reader
// does, and see a phrase split across <tspan>s as one phrase.
func TestText_EveryVisibleWord(t *testing.T) {
	got, err := Text(sanitize(t, `<desc>Compared by fee.</desc>`+
		`<text x="1" y="40">According to The Points <tspan>Guy</tspan></text>`+
		`<text x="1" y="60"><tspan>Source</tspan> <tspan>2</tspan></text>`+
		`<rect x="1" y="1" width="2" height="2"/>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Fee vs value", "Compared by fee.", "According to The Points Guy", "Source 2", "$895"} {
		if !strings.Contains(got, want) {
			t.Errorf("Text missing %q:\n%s", want, got)
		}
	}
	if _, err := Text("<svg><text>broken</svg>"); err == nil {
		t.Error("Text accepted malformed markup")
	}
}

// allowedOutput is the shape every sanitised document must have: only
// allowlisted element names, only allowlisted attributes plus the root's own,
// and no value spelling a scheme, url() or entity.
func checkOutput(t *testing.T, out string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(out))
	depth, roots := 0, 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("output is not well-formed: %v\n%s", err, out)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
			if !allowedElements[e.Name.Local] {
				t.Fatalf("output has element %q:\n%s", e.Name.Local, out)
			}
			for _, a := range e.Attr {
				root := e.Name.Local == "svg" && depth == 1
				if !allowedAttrs[a.Name.Local] && !(root && rootAttrs[a.Name.Local]) {
					t.Fatalf("output has attribute %q:\n%s", a.Name.Local, out)
				}
				if dangerousValue.MatchString(a.Value) {
					t.Fatalf("output has a dangerous value %q:\n%s", a.Value, out)
				}
			}
		case xml.EndElement:
			depth--
		case xml.ProcInst, xml.Directive, xml.Comment:
			t.Fatalf("output has a %T:\n%s", tok, out)
		}
	}
	if roots != 1 {
		t.Fatalf("output has %d root elements:\n%s", roots, out)
	}
	if onAttr.MatchString(out) {
		t.Fatalf("output has an event handler:\n%s", out)
	}
}

var (
	rootAttrs = map[string]bool{"xmlns": true, "viewBox": true, "width": true, "height": true, "color": true, "role": true}
	onAttr    = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
)

// FuzzSanitize: whatever goes in, what comes out is allowlisted, well-formed,
// a single document and stable under a second pass. The seeds run with every
// `go test`; `go test -fuzz=FuzzSanitize ./internal/svg` explores further.
func FuzzSanitize(f *testing.F) {
	for _, seed := range []string{
		strings.Replace(validWrapper, "%s", "", 1),
		strings.Replace(validWrapper, "%s", `<script>alert(1)</script><rect onload="x" x="1" y="1" width="1" height="1"/>`, 1),
		strings.Replace(validWrapper, "%s", `<foreignObject><div xmlns="http://www.w3.org/1999/xhtml">x</div></foreignObject>`, 1),
		strings.Replace(validWrapper, "%s", `<g transform="rotate(5)"><text x="1" y="1">a<tspan>b</tspan></text></g>`, 1),
		strings.Replace(validWrapper, "%s", `<rect fill="&#x6A;avascript:x" x="1" y="1" width="1" height="1"/>`, 1),
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><text>a</text></svg><text>after</text>`,
		"```svg\n<svg viewBox='0 0 1 1'><title>x</title><text>y</text></svg>\n```",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Sanitize(in)
		if err != nil {
			return
		}
		checkOutput(t, out)
		again, err := Sanitize(out)
		if err != nil || again != out {
			t.Fatalf("second pass changed or rejected the output (%v):\n%s\n%s", err, out, again)
		}
	})
}
