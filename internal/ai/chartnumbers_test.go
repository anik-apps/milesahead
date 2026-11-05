package ai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/infographic"
)

func TestUnsupportedChartNumbers(t *testing.T) {
	const (
		title   = "Amex Platinum Fee Rises to $895"
		tldr    = "The advertised credits add up to $3,024; most cardholders use about $1,215."
		details = `[{"Label":"Annual fee","Value":"$895 (was $695)"},{"Label":"Bonus","Value":"80,000 points"},` +
			`{"Label":"Dining","Value":"3x points, 25% back"},{"Label":"Value","Value":"1.5 cents a point"},` +
			`{"Label":"Credit","Value":"$3k a year"},{"Label":"Members","Value":"1.2 million"},{"Label":"Deadline","Value":"October 30, 2026"}]`
		body = "<p>Use about $1,215 of credits and the card nets roughly $320.</p>"
	)
	cases := []struct {
		name  string
		chart string
		want  []string
	}{
		{"every figure from the article", "Credits on paper $3,024\nTypically used ~$1,215\nNew fee $895\nOld fee $695\nNets +$320", nil},
		{"separators, currency and units are normalised", "80000 points\n3X dining\n25 % back\n1.5¢ a point\n$3,000 credit\n$3K credit\n1.2M members\nOct 30, 2026", nil},
		{"trailing zeros", "Fee $895.00\nValue 1.50¢", nil},
		{"invented figure", "Credits $3,024\nTypically used $1,400", []string{"$1,400"}},
		{"miscopied figure", "Bonus 8,000 points", []string{"8,000"}},
		{"wrong percent", "35% back", []string{"35%"}},
		{"derived sum the article does not state", "Fee plus old fee $1,590", []string{"$1,590"}},
		// Bare small integers are axis ticks and row numbers.
		{"axis ticks and ranks", "0\n5\n10\n1. Hotel\n2. Resy", nil},
		{"a bare integer above the tick limit", "0\n500\n1000", []string{"500", "1000"}},
		{"a small number with a unit is still checked", "7x points\n$5", []string{"7x", "$5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnsupportedChartNumbers(tc.chart, title, tldr, details, body)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("UnsupportedChartNumbers = %q, want %q", got, tc.want)
			}
		})
	}
}

// Values drawn as bars get no small-number exemption: an invented "3" beside
// a stated "8" would be a bar of the wrong length.
func TestUnsupportedValues_NoSmallNumberExemption(t *testing.T) {
	src := "Eight nights earn the bonus; 8 nights is the minimum. Lounge visits: 10."
	if bad := UnsupportedChartNumbers("3 nights", src); len(bad) != 0 {
		t.Errorf("chart text: %q; small counts in words are exempt", bad)
	}
	if bad := UnsupportedValues([]string{"3", "8"}, src); len(bad) != 1 || bad[0] != "3" {
		t.Errorf("bar values = %q, want [3]", bad)
	}
	if bad := UnsupportedValues([]string{"8", "10"}, src); len(bad) != 0 {
		t.Errorf("stated bar values refused: %q", bad)
	}
}

// CheckChartNumbers is what production refuses charts with: invented numbers
// in a chart's words, and, for a comparison only, any unstated bar value --
// small ones included.
func TestCheckChartNumbers(t *testing.T) {
	src := "Eight nights earn the bonus; 8 nights is the minimum. The fee is $95."
	cases := []struct {
		name    string
		chart   infographic.Chart
		wantErr string
	}{
		{"stated numbers pass", infographic.Chart{Kind: infographic.KindFigures, Headline: "$95 fee",
			Rows: []infographic.Row{{Label: "Minimum stay", Value: "8 nights"}}}, ""},
		{"invented number in the words", infographic.Chart{Kind: infographic.KindFigures, Headline: "$120 fee",
			Rows: []infographic.Row{{Label: "Minimum stay", Value: "8 nights"}}}, "shows numbers"},
		{"small unstated value outside a comparison is a tick", infographic.Chart{Kind: infographic.KindFigures,
			Rows: []infographic.Row{{Label: "Stay", Value: "3"}}}, ""},
		{"small unstated bar in a comparison", infographic.Chart{Kind: infographic.KindCompare,
			Rows: []infographic.Row{{Label: "Bonus", Value: "8"}, {Label: "Base", Value: "3"}}}, "draws bars"},
	}
	for _, c := range cases {
		err := CheckChartNumbers(c.chart, src)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want one containing %q", c.name, err, c.wantErr)
		}
	}
}
