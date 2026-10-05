package ai

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// numberToken matches a number as articles and charts write it: an optional
// currency sign, digits with or without thousands separators, an optional
// decimal part, and an optional unit or multiplier ("%", "x", "¢", "k",
// "M"). "$1,215.50", "80,000", "3x", "25%", "1.5¢" and "$3k" all match.
var numberToken = regexp.MustCompile(`([$€£]?)(\d{1,3}(?:,\d{3})+|\d+)(\.\d+)?\s?(%|¢|[xX]\b|[kK]\b|[mM]\b|[bB]\b)?`)

// chartNumber is one number in a chart's text.
type chartNumber struct {
	text  string  // as written, for the error message
	value float64 // with separators and currency dropped
	scale float64 // what a k/M/B suffix multiplies by, else 1
	bare  bool    // no currency, unit, separator, decimal or suffix
}

func numbersIn(text string) []chartNumber {
	var out []chartNumber
	for _, m := range numberToken.FindAllStringSubmatch(text, -1) {
		v, err := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", "")+m[3], 64)
		if err != nil {
			continue
		}
		n := chartNumber{text: strings.TrimSpace(m[0]), value: v, scale: 1}
		switch strings.ToLower(m[4]) {
		case "k":
			n.scale = 1e3
		case "m":
			n.scale = 1e6
		case "b":
			n.scale = 1e9
		}
		n.bare = m[1] == "" && m[3] == "" && m[4] == "" && !strings.Contains(m[2], ",")
		out = append(out, n)
	}
	return out
}

// tickLimit is the largest bare integer a chart may show without it
// appearing in the article. See UnsupportedChartNumbers.
const tickLimit = 10

// UnsupportedChartNumbers returns the numbers in a chart's text that appear
// nowhere in sources (the article's title, summary, key details and body).
//
// The chart is drawn by a model from those sources, and a number it made up
// or miscopied would be published as fact with nothing to revise it, so a
// chart showing one is refused and drawn again, as one naming a news site is.
//
// Numbers are compared by value, ignoring how they are written: "$3,024",
// "3,024" and "3024" are the same, as are "25%" and "25 percent", and a
// "$3k" matches a "3,000" (and a "3"). Units are not compared, so a chart
// cannot be caught writing "25%" for "25 points"; the check is for invented
// and miscopied figures, which is what models get wrong.
//
// Bare integers up to tickLimit (no currency, unit, separator or decimal)
// are exempt: they are the axis ticks, ranks and small counts a chart may add
// on its own ("0" at the base of an axis, "1" to "5" numbering rows). The
// prompt asks for no numbered axes at all; this keeps a stray "0" from
// costing a whole chart. A sum or difference the article does not state is
// refused like any other number: the prompt forbids derived figures too.
func UnsupportedChartNumbers(chart string, sources ...string) []string {
	// Rounded, so 1.2 x 1e6 and 1,200,000 meet despite float arithmetic.
	key := func(f float64) float64 { return math.Round(f*1000) / 1000 }
	known := map[float64]bool{}
	for _, src := range sources {
		for _, n := range numbersIn(src) {
			known[key(n.value)] = true
			known[key(n.value*n.scale)] = true
		}
	}
	var bad []string
	for _, n := range numbersIn(chart) {
		if n.bare && n.value <= tickLimit {
			continue
		}
		if known[key(n.value)] || known[key(n.value*n.scale)] {
			continue
		}
		bad = append(bad, n.text)
	}
	return bad
}
