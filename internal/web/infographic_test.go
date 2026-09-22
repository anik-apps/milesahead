package web

import (
	"strings"
	"testing"
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

// Anything that did not come out of internal/svg is refused. This is not the
// security boundary -- sanitisation happens at write -- but it is what stops a
// row edited by hand or restored from an older backup being inlined raw.
func TestInfographicHTML_RefusesUnsanitized(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"sentinel":              "none",
		"raw script":            `<svg onload="alert(1)"><script>alert(1)</script></svg>`,
		"plain html":            `<div onclick="alert(1)">hi</div>`,
		"right tag wrong attrs": `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)" viewBox="0 0 1 1"></svg>`,
		"leading junk":          `<p>x</p>` + sanitized,
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
