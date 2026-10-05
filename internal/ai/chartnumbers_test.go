package ai

import (
	"reflect"
	"testing"
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
