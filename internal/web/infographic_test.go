package web

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

const sanitized = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450" class="infographic-svg" role="img" preserveAspectRatio="xMidYMid meet"><title>Fee vs value</title><text x="1" y="1">$895</text></svg>`

func TestInfographicHTML(t *testing.T) {
	if got := infographicHTML(sanitized); string(got) != sanitized {
		t.Errorf("sanitised markup was not passed through:\n%s", got)
	}
	if got := infographicTitle(sanitized); got != "Fee vs value" {
		t.Errorf("title = %q, want %q", got, "Fee vs value")
	}
}

// Markup that cannot survive sanitisation must not reach the page. The write
// path sanitises too, so in normal operation none of these can be stored --
// this covers the row that was edited by hand, restored from a backup taken
// before the sanitiser existed, or written by a code path that forgot.
func TestInfographicHTML_RefusesUnsanitized(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"sentinel":              db.NoInfographic,
		"raw script":            `<svg onload="alert(1)"><script>alert(1)</script></svg>`,
		"plain html":            `<div onclick="alert(1)">hi</div>`,
		"right tag wrong attrs": `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)" viewBox="0 0 1 1"></svg>`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := infographicHTML(in); got != "" {
				t.Errorf("inlined unsanitised markup: %s", got)
			}
			if got := infographicTitle(in); got != "" {
				t.Errorf("title = %q, want empty", got)
			}
		})
	}
}

// Leading or trailing whitespace from the column must not defeat the check.
func TestInfographicHTML_TrimsWhitespace(t *testing.T) {
	if got := infographicHTML("\n  " + sanitized + "  \n"); !strings.HasPrefix(string(got), "<svg") {
		t.Errorf("whitespace-padded markup was rejected: %q", got)
	}
}

// Re-sanitising at render means junk around a valid diagram is stripped rather
// than causing the whole thing to be dropped -- the failure mode is "show the
// safe part", not "show nothing".
func TestInfographicHTML_StripsSurroundingJunk(t *testing.T) {
	got := string(infographicHTML(`<p onclick="alert(1)">x</p>` + sanitized + `<script>alert(1)</script>`))
	if got == "" {
		t.Fatal("a valid diagram wrapped in junk was dropped entirely")
	}
	for _, banned := range []string{"onclick", "alert", "<p", "<script"} {
		if strings.Contains(got, banned) {
			t.Errorf("output still contains %q:\n%s", banned, got)
		}
	}
	if !strings.Contains(got, "Fee vs value") {
		t.Errorf("the diagram itself was lost:\n%s", got)
	}
}

// An ampersand is stored XML-escaped inside the SVG. Handed to html/template
// as-is it would be escaped a second time and read aloud as "&amp;amp;".
func TestInfographicTitle_UnescapesEntities(t *testing.T) {
	markup := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450">` +
		`<title>Fees &amp; credits</title><text x="1" y="1">a</text></svg>`
	if got := infographicTitle(markup); got != "Fees & credits" {
		t.Errorf("title = %q, want %q", got, "Fees & credits")
	}
}

// infographicHTML re-sanitises on every render, and the homepage renders up to
// twenty cards. This exists to keep the justification in that function honest:
// it cites ~50us per diagram, and if that ever drifts the comment is wrong.
func BenchmarkInfographicHTML(b *testing.B) {
	// A real diagram from the production model, not the short fixture above:
	// cost scales with the markup, so benchmarking the toy version would
	// understate it.
	for i := 0; i < b.N; i++ {
		if infographicHTML(realDiagram) == "" {
			b.Fatal("sanitisation failed")
		}
	}
}

// realDiagram is verbatim output from nvidia/nemotron-3-ultra-550b-a55b for an
// Amex Platinum fee article, after sanitisation.
const realDiagram = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450" class="infographic-svg" role="img" preserveAspectRatio="xMidYMid meet"><title>Amex Platinum credit value comparison</title><desc>Horizontal bar chart comparing Amex Platinum annual fee, advertised credits, realistic value, and net result.</desc><g font-family="sans-serif"><text x="400" y="40" font-size="24" font-weight="bold" text-anchor="middle">Amex Platinum: Paper Credits vs. Real Value</text><line x1="200" y1="60" x2="200" y2="380" stroke="currentColor" stroke-width="1"></line><rect x="200" y="80" width="500" height="50" fill="#1f6feb"></rect><text x="190" y="105" font-size="14" text-anchor="end">Credits on paper</text><text x="710" y="105" font-size="14" font-weight="bold" text-anchor="start">$3,024</text><rect x="200" y="160" width="200.9" height="50" fill="#28a745"></rect><text x="190" y="185" font-size="14" text-anchor="end">Realistic value</text><text x="410.9" y="185" font-size="14" font-weight="bold" text-anchor="start">$1,215</text><rect x="200" y="240" width="148.0" height="50" fill="#dc3545"></rect><text x="190" y="265" font-size="14" text-anchor="end">Annual fee</text><text x="358" y="265" font-size="14" font-weight="bold" text-anchor="start">$895</text><rect x="200" y="320" width="52.9" height="50" fill="#6f42c1"></rect><text x="190" y="345" font-size="14" text-anchor="end">Net result</text><text x="262.9" y="345" font-size="14" font-weight="bold" text-anchor="start">+$320</text><text x="400" y="420" font-size="16" text-anchor="middle">Real-world credits cover the fee with $320 to spare, but fall far short of the $3,024 advertised.</text></g></svg>`
