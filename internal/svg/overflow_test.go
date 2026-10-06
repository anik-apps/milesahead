package svg

import (
	"strings"
	"testing"
)

func chart(body string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360"><title>T</title>` + body + `</svg>`
}

// Two charts the production models drew: one that fits, and one whose
// right-aligned row labels ran off the left edge, losing the airline names.
func TestOverflow_ProductionCharts(t *testing.T) {
	fits := chart(`<text x="280" y="50" text-anchor="middle" font-size="22" font-family="sans-serif" fill="currentColor">Chase IHG Select Card Fee Increase</text>` +
		`<rect x="180" y="110" width="129" height="30" fill="#2da44e"></rect>` +
		`<text x="170" y="130" text-anchor="end" font-size="18" font-family="sans-serif" fill="currentColor">Old fee</text>` +
		`<text x="319" y="130" text-anchor="start" font-size="18" font-family="sans-serif" fill="currentColor">$49</text>` +
		`<rect x="180" y="170" width="260" height="30" fill="#1f6feb"></rect>` +
		`<text x="170" y="190" text-anchor="end" font-size="18" font-family="sans-serif" fill="currentColor">New fee</text>` +
		`<text x="450" y="190" text-anchor="start" font-size="18" font-family="sans-serif" fill="currentColor">$99</text>` +
		`<text x="280" y="320" text-anchor="middle" font-size="18" font-family="sans-serif" fill="currentColor">New fee $99 effective Dec 2, 2026; one more $49 cycle.</text>`)
	if got, err := Overflow(fits); err != nil || len(got) != 0 {
		t.Errorf("chart that fits: %q, %v", got, err)
	}

	clipped := chart(`<text x="280" y="40" font-family="sans-serif" font-size="22" font-weight="bold" text-anchor="middle" fill="currentColor">Rove Transfer Bonuses</text>` +
		`<rect x="180" y="80" width="260" height="20" fill="#1f6feb"></rect>` +
		`<text x="170" y="90" font-family="sans-serif" font-size="18" text-anchor="end" fill="currentColor">JAL 50% – 1.5 miles per point</text>` +
		`<text x="450" y="90" font-family="sans-serif" font-size="18" fill="currentColor">50%</text>` +
		`<text x="170" y="170" font-family="sans-serif" font-size="18" text-anchor="end" fill="currentColor">Qatar Airways Privilege Club 35% – 1.35 miles per point</text>` +
		`<text x="280" y="330" font-family="sans-serif" font-size="18" font-weight="bold" text-anchor="middle" fill="currentColor">Bonus ends October 31, 2026</text>`)
	got, err := Overflow(clipped)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d overflows, want the two long row labels: %q", len(got), got)
	}
	if !strings.Contains(got[0], `"JAL 50%`) || !strings.Contains(got[0], "past the left edge") {
		t.Errorf("first = %q", got[0])
	}
	if !strings.Contains(got[1], "Qatar Airways") || !strings.Contains(got[1], "…") {
		t.Errorf("second = %q, want the label, shortened", got[1])
	}
}

func TestOverflow_Anchors(t *testing.T) {
	// "Annual fee" at 18 is about 86 wide.
	cases := []struct {
		name, text string
		want       string // "" for none
	}{
		{"start fits", `<text x="400" y="50" font-size="18">Annual fee</text>`, ""},
		{"start runs right", `<text x="500" y="50" font-size="18">Annual fee</text>`, "right edge"},
		{"end fits", `<text x="100" y="50" font-size="18" text-anchor="end">Annual fee</text>`, ""},
		{"end runs left", `<text x="60" y="50" font-size="18" text-anchor="end">Annual fee</text>`, "left edge"},
		{"middle fits", `<text x="280" y="50" font-size="18" text-anchor="middle">Annual fee</text>`, ""},
		{"middle runs both ways", `<text x="280" y="50" font-size="40" text-anchor="middle">Annual fee for the Platinum Card</text>`, "left edge and"},
		{"anchor inherited from g", `<g text-anchor="end"><text x="60" y="50" font-size="18">Annual fee</text></g>`, "left edge"},
		{"font-size inherited from g", `<g font-size="36"><text x="400" y="50">Annual fee</text></g>`, "right edge"},
		{"bold is wider", `<text x="473" y="50" font-size="18" font-weight="bold">Annual fee</text>`, "right edge"},
		{"regular just fits", `<text x="473" y="50" font-size="18">Annual fee</text>`, ""},
		{"default font size fits", `<text x="480" y="50">Annual fee</text>`, ""},
		{"default font size runs right", `<text x="490" y="50">Annual fee</text>`, "right edge"},
		{"em font size", `<g font-size="18"><text x="400" y="50" font-size="2em">Annual fee</text></g>`, "right edge"},
		{"percent x", `<text x="90%" y="50" font-size="18">Annual fee</text>`, "right edge"},
		{"letter-spacing", `<text x="440" y="50" font-size="18" letter-spacing="4">Annual fee</text>`, "right edge"},
		{"above the top", `<text x="10" y="5" font-size="18">Annual fee</text>`, "past the top"},
		{"below the bottom", `<text x="10" y="372" font-size="18">Annual fee</text>`, "past the bottom"},
		{"hanging baseline at the top fits", `<text x="10" y="2" font-size="18" dominant-baseline="hanging">Annual fee</text>`, ""},
		{"whitespace collapses", "<text x=\"470\" y=\"50\" font-size=\"18\">\n   Annual    fee   \n</text>", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Overflow(chart(c.text))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case c.want == "" && len(got) != 0:
				t.Errorf("want no overflow, got %q", got)
			case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
				t.Errorf("want one overflow mentioning %q, got %q", c.want, got)
			}
		})
	}
}

func TestOverflow_Transforms(t *testing.T) {
	cases := []struct {
		name, text string
		want       string
	}{
		{"translate moves it off", `<g transform="translate(470,0)"><text x="10" y="50" font-size="18">Annual fee</text></g>`, "right edge"},
		{"translate back on", `<g transform="translate(-100 0)"><text x="550" y="50" font-size="18">Annual fee</text></g>`, ""},
		{"nested translates add", `<g transform="translate(200,0)"><g transform="translate(270,0)"><text x="10" y="50" font-size="18">Annual fee</text></g></g>`, "right edge"},
		{"scale", `<g transform="scale(2)"><text x="200" y="50" font-size="18">Annual fee</text></g>`, "right edge"},
		{"transform on the text", `<text x="10" y="50" font-size="18" transform="translate(500)">Annual fee</text>`, "right edge"},
		{"rotated label fits", `<text x="30" y="200" font-size="18" transform="rotate(-90 30 200)">Annual fee</text>`, ""},
		{"rotated label runs off the top", `<text x="30" y="60" font-size="18" transform="rotate(-90 30 60)">Annual fee</text>`, "past the top"},
		{"invalid transform is ignored", `<g transform="translate(470,0) bogus"><text x="10" y="50" font-size="18">Annual fee</text></g>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Overflow(chart(c.text))
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case c.want == "" && len(got) != 0:
				t.Errorf("want no overflow, got %q", got)
			case c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)):
				t.Errorf("want one overflow mentioning %q, got %q", c.want, got)
			}
		})
	}
}

func TestOverflow_Tspans(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		// A row label and its value as two positioned chunks: each is
		// measured from its own x.
		{"positioned chunks fit", `<text y="50" font-size="18"><tspan x="10">Annual fee</tspan><tspan x="470">$895</tspan></text>`, nil},
		{"second chunk runs off", `<text y="50" font-size="18"><tspan x="10">Annual fee</tspan><tspan x="530">$895</tspan></text>`, []string{`"$895"`}},
		// Unpositioned tspans continue the line, so the run is their sum.
		{"continued line runs off", `<text x="320" y="50" font-size="18">Annual fee <tspan font-weight="bold">$895</tspan> and the credits</text>`, []string{"Annual fee $895 and the credits"}},
		{"dx widens the run", `<text x="400" y="50" font-size="18">Annual<tspan dx="100">fee</tspan></text>`, []string{"Annualfee"}},
		// A second line below, set by y alone, starts where the first ended.
		{"y-only tspan continues from the end", `<text x="440" y="50" font-size="18">Annual fee<tspan y="80">$895</tspan></text>`, []string{`"$895"`}},
		{"dy moves the rest down", `<text x="10" y="340" font-size="18">Fee<tspan dy="30">$895</tspan></text>`, []string{"past the bottom"}},
		{"title and desc are not drawn", `<desc>` + strings.Repeat("A very long description ", 20) + `</desc><text x="10" y="50" font-size="18">Fee</text>`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Overflow(chart(c.text))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %q, want %d overflow(s)", got, len(c.want))
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("overflow %d = %q, want it to mention %q", i, got[i], w)
				}
			}
		})
	}
}

func TestOverflow_ViewBoxOrigin(t *testing.T) {
	doc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="100 0 560 360"><title>T</title>` +
		`<text x="20" y="50" font-size="18">Annual fee</text>` +
		`<text x="600" y="50" font-size="18">Fee</text></svg>`
	got, err := Overflow(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0], `"Annual fee"`) || !strings.Contains(got[0], "left edge") {
		t.Errorf("got %q; want only the text left of x=100", got)
	}
}

func TestOverflow_Errors(t *testing.T) {
	if _, err := Overflow(`<svg xmlns="http://www.w3.org/2000/svg"><text>x</text></svg>`); err == nil {
		t.Error("no viewBox: want an error")
	}
	if _, err := Overflow(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><text>`); err == nil {
		t.Error("unterminated: want an error")
	}
}
