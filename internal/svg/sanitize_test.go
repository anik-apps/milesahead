package svg

import (
	"strings"
	"testing"
)

const validWrapper = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450">` +
	`<title>Fee vs value</title>%s<text x="10" y="20">$895</text></svg>`

func sanitize(t *testing.T, inner string) string {
	t.Helper()
	got, err := Sanitize(strings.Replace(validWrapper, "%s", inner, 1))
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	return got
}

// The output of this package is inlined into a page unescaped, so anything
// that executes is a same-origin XSS. Each case below is a distinct way to
// smuggle execution past a naive filter.
func TestSanitize_StripsExecution(t *testing.T) {
	cases := []struct {
		name   string
		inner  string
		banned []string
	}{
		{"script element", `<script>alert(1)</script>`, []string{"alert", "script"}},
		{"script with cdata", `<script><![CDATA[alert(1)]]></script>`, []string{"alert"}},
		{"onload handler", `<rect x="1" y="1" width="2" height="2" onload="alert(1)"/>`, []string{"onload", "alert"}},
		{"onclick handler", `<rect x="1" y="1" width="2" height="2" onclick="alert(1)"/>`, []string{"onclick", "alert"}},
		{"mixed-case handler", `<rect x="1" y="1" width="2" height="2" OnMouseOver="alert(1)"/>`, []string{"alert"}},
		// Dropping the tag but keeping children would promote raw HTML into
		// the page -- the children must go with it.
		{"foreignObject html", `<foreignObject width="9" height="9"><body xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(1)"/></body></foreignObject>`, []string{"foreignObject", "onerror", "alert", "<img"}},
		{"anchor javascript uri", `<a xlink:href="javascript:alert(1)" xmlns:xlink="http://www.w3.org/1999/xlink"><text x="1" y="1">go</text></a>`, []string{"javascript", "xlink", "<a "}},
		{"external image", `<image href="https://evil.test/track.png" width="9" height="9"/>`, []string{"evil.test", "<image"}},
		{"use external ref", `<use href="https://evil.test/x.svg#a"/>`, []string{"evil.test", "<use"}},
		{"style element", `<style>@import url(https://evil.test/x.css);</style>`, []string{"evil.test", "@import", "<style"}},
		{"style attribute", `<rect x="1" y="1" width="2" height="2" style="background:url(https://evil.test/x)"/>`, []string{"evil.test", "style="}},
		{"url fill", `<rect x="1" y="1" width="2" height="2" fill="url(https://evil.test/x)"/>`, []string{"evil.test", "url("}},
		{"data uri fill", `<rect x="1" y="1" width="2" height="2" fill="data:image/svg+xml;base64,AAAA"/>`, []string{"data:"}},
		{"animate retarget", `<rect x="1" y="1" width="2" height="2"><animate attributeName="onload" to="alert(1)"/></rect>`, []string{"animate", "alert", "onload"}},
		{"set element", `<set attributeName="onload" to="alert(1)"/>`, []string{"<set", "alert"}},
		{"handler element", `<handler type="text/javascript">alert(1)</handler>`, []string{"handler", "alert"}},
		{"nested svg viewport escape", `<svg viewBox="0 0 1 1" width="99999"><text x="1" y="1">x</text></svg>`, []string{"99999"}},
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

func TestSanitize_KeepsDrawing(t *testing.T) {
	got := sanitize(t, `<g transform="translate(10,10)">`+
		`<rect x="0" y="0" width="300" height="24" fill="#4c8bf5" stroke="none"/>`+
		`<line x1="0" y1="0" x2="300" y2="0" stroke="#999" stroke-width="2"/>`+
		`<path d="M0 0 L10 10 Z" fill="currentColor"/>`+
		`<text x="4" y="18" font-size="14" text-anchor="start">Credits on paper</text>`+
		`</g>`)

	for _, want := range []string{
		`viewBox="0 0 800 450"`, `role="img"`, `<title>`,
		`<rect`, `width="300"`, `fill="#4c8bf5"`,
		`<line`, `stroke-width="2"`, `<path`, `d="M0 0 L10 10 Z"`,
		`fill="currentColor"`, `transform="translate(10,10)"`,
		`text-anchor="start"`, `Credits on paper`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output dropped %q:\n%s", want, got)
		}
	}
}

// The model sizes its own canvas; the page decides how big it renders. A
// model-chosen pixel width would overflow the homepage card it sits in.
func TestSanitize_DropsRootWidthHeight(t *testing.T) {
	got, err := Sanitize(`<svg xmlns="http://www.w3.org/2000/svg" width="1600" height="900" viewBox="0 0 800 450">` +
		`<title>t</title><text x="1" y="1">a</text></svg>`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "1600") || strings.Contains(got, `height="900"`) {
		t.Errorf("root kept its pixel size:\n%s", got)
	}
	if !strings.Contains(got, `viewBox="0 0 800 450"`) {
		t.Errorf("root lost its viewBox:\n%s", got)
	}
}

func TestSanitize_TextIsEscaped(t *testing.T) {
	got := sanitize(t, `<text x="1" y="1">Fee &amp; value &lt;b&gt;</text>`)
	if strings.Contains(got, "<b>") {
		t.Errorf("text content was not escaped:\n%s", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("ampersand lost its escaping:\n%s", got)
	}
}

func TestSanitize_Rejects(t *testing.T) {
	cases := []struct{ name, in string }{
		{"empty", ""},
		{"prose only", "I cannot create images."},
		{"no svg root", `<div><p>hello</p></div>`},
		{"truncated", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450"><title>t</title><rect x="1"`},
		{"missing viewBox", `<svg xmlns="http://www.w3.org/2000/svg"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"non numeric viewBox", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 auto 450"><title>t</title><text x="1" y="1">a</text></svg>`},
		{"no title", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><text x="1" y="1">a</text></svg>`},
		{"no text", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 4"><title>t</title><rect x="1" y="1" width="2" height="2"/></svg>`},
		{"oversized", `<svg viewBox="0 0 1 1">` + strings.Repeat("x", MaxInputBytes) + `</svg>`},
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

func TestTitle(t *testing.T) {
	got := sanitize(t, `<text x="1" y="1">a</text>`)
	if Title(got) != "Fee vs value" {
		t.Errorf("Title = %q, want %q", Title(got), "Fee vs value")
	}
	if Title("<svg></svg>") != "" {
		t.Error("Title found a name where there is none")
	}
}
