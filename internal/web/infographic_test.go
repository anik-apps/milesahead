package web

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/svg"
)

const sanitized = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360" width="560" height="360" color="` + svg.Ink + `" role="img"><title>Fee vs value</title><text x="1" y="1">$895</text></svg>`

// decodeSrc returns the SVG inside a data: URL the page would load.
func decodeSrc(t *testing.T, g *articleInfographic) string {
	t.Helper()
	const prefix = "data:image/svg+xml;base64,"
	src := string(g.Src)
	if !strings.HasPrefix(src, prefix) {
		t.Fatalf("src is not a base64 SVG data: URL: %.60q", src)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(src, prefix))
	if err != nil {
		t.Fatalf("decoding src: %v", err)
	}
	return string(raw)
}

func TestInfographicFor(t *testing.T) {
	g := infographicFor(sanitized)
	if g == nil {
		t.Fatal("sanitised markup was not shown")
	}
	if got := decodeSrc(t, g); got != sanitized {
		t.Errorf("image is not the stored chart:\n%s", got)
	}
	if g.Alt != "Fee vs value" {
		t.Errorf("alt = %q, want %q", g.Alt, "Fee vs value")
	}
	if g.Width != 560 || g.Height != 360 {
		t.Errorf("size = %dx%d, want 560x360", g.Width, g.Height)
	}
}

// Markup that cannot survive sanitisation is not shown. The write path
// sanitises too, so in normal operation none of these can be stored -- this
// covers a row edited by hand, restored from an old backup, or written by a
// code path that forgot.
func TestInfographicFor_RefusesUnsanitized(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"sentinel":              db.NoInfographic,
		"raw script":            `<svg onload="alert(1)"><script>alert(1)</script></svg>`,
		"plain html":            `<div onclick="alert(1)">hi</div>`,
		"right tag wrong attrs": `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)" viewBox="0 0 1 1"></svg>`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got := infographicFor(in); got != nil {
				t.Errorf("showed unsanitised markup: %+v", got)
			}
		})
	}
}

// Re-sanitising at render means junk around a valid chart is stripped rather
// than shown -- and rather than losing the chart.
func TestInfographicFor_StripsSurroundingJunk(t *testing.T) {
	g := infographicFor(`<p onclick="alert(1)">x</p>` + strings.Replace(sanitized, "<text", `<script>alert(1)</script><text`, 1))
	if g == nil {
		t.Fatal("a valid chart wrapped in junk was dropped entirely")
	}
	got := decodeSrc(t, g)
	for _, banned := range []string{"onclick", "alert", "<p", "<script"} {
		if strings.Contains(got, banned) {
			t.Errorf("image still contains %q:\n%s", banned, got)
		}
	}
}

// The title and description are stored XML-escaped. html/template escapes
// alt again on output, so they must be unescaped first or a reader hears
// "Fees &amp;amp; credits".
func TestInfographicAlt_UnescapesAndAddsDescription(t *testing.T) {
	g := infographicFor(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360">` +
		`<title>Fees &amp; credits</title><desc>Bar chart of the fee against the credits.</desc><text x="1" y="1">a</text></svg>`)
	if g == nil {
		t.Fatal("chart not shown")
	}
	if want := "Fees & credits. Bar chart of the fee against the credits."; g.Alt != want {
		t.Errorf("alt = %q, want %q", g.Alt, want)
	}
}

// Article pages show the chart only with ai.enrichment_infographics "on";
// "preview" is for the admin preview alone.
func TestPublicInfographic_FollowsTheMode(t *testing.T) {
	for mode, want := range map[string]bool{
		config.InfographicsOff: false, config.InfographicsPreview: false, config.InfographicsOn: true,
	} {
		app := &App{cfg: &config.Config{AI: config.AIConfig{EnrichmentInfographics: mode}}}
		if got := app.publicInfographic(sanitized) != nil; got != want {
			t.Errorf("mode %q: shown = %v, want %v", mode, got, want)
		}
	}
}

// infographicFor re-sanitises on every article page view. This keeps its
// "a few kilobytes" claim honest with a realistic chart.
func BenchmarkInfographicFor(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if infographicFor(realDiagram) == nil {
			b.Fatal("sanitisation failed")
		}
	}
}

// realDiagram is the shape of a real model chart: a horizontal bar chart of
// an Amex Platinum fee article, on the 560x360 canvas the prompt asks for.
const realDiagram = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360">` +
	`<title>Amex Platinum: credits vs the fee</title>` +
	`<desc>Horizontal bar chart comparing the advertised credits, a realistic value of them and the annual fee.</desc>` +
	`<g font-family="sans-serif">` +
	`<text x="280" y="32" font-size="22" font-weight="bold" text-anchor="middle" fill="currentColor">Paper credits vs real value</text>` +
	`<line x1="180" y1="56" x2="180" y2="290" stroke="currentColor" stroke-width="1"/>` +
	`<text x="170" y="96" font-size="18" text-anchor="end" fill="currentColor">Credits on paper</text>` +
	`<rect x="180" y="74" width="260" height="34" fill="#1f6feb"/>` +
	`<text x="448" y="96" font-size="18" font-weight="bold" fill="currentColor">$3,024</text>` +
	`<text x="170" y="166" font-size="18" text-anchor="end" fill="currentColor">Realistic value</text>` +
	`<rect x="180" y="144" width="104" height="34" fill="#2da44e"/>` +
	`<text x="292" y="166" font-size="18" font-weight="bold" fill="currentColor">$1,215</text>` +
	`<text x="170" y="236" font-size="18" text-anchor="end" fill="currentColor">Annual fee</text>` +
	`<rect x="180" y="214" width="77" height="34" fill="#cf222e"/>` +
	`<text x="265" y="236" font-size="18" font-weight="bold" fill="currentColor">$895</text>` +
	`<text x="280" y="336" font-size="16" text-anchor="middle" fill="currentColor">Real-world credits cover the fee with $320 to spare.</text>` +
	`</g></svg>`
