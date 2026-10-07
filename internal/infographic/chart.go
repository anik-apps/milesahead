// Package infographic is an article's "At a glance" chart: a few numbers
// from its key details, chosen by a model and drawn by the site.
//
// The model chooses only the kind of chart, its words and its numbers. It
// used to write the chart as SVG, and every chart came out different --
// colours picked from a list, filled boxes, a layout of its own, text cut off
// at the edge, bars of the wrong length -- and needed a sanitiser between it
// and the page. Now the site draws each chart from one of four fixed designs
// (templates/_infographic.html) in its own type and colours, the bar lengths
// are computed here from the values, and nothing the model writes reaches the
// page except as escaped text.
package infographic

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The four kinds of chart.
const (
	// KindChange is one number that moved: a fee, a ratio, a price. Two
	// rows, before and after.
	KindChange = "change"
	// KindCompare is two to five amounts of the same kind in the same unit,
	// drawn as bars.
	KindCompare = "compare"
	// KindFigures is numbers that do not compare: a headline figure and one
	// to four more.
	KindFigures = "figures"
	// KindTimeline is two to five dated steps.
	KindTimeline = "timeline"
)

// Row is one line of a chart. What each field holds depends on the kind:
// for a timeline Label is the date and Value what happens then.
type Row struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	// Key marks the step that matters in a timeline.
	Key bool `json:"key,omitempty"`
}

// Chart is what the model returns and what is stored, as JSON.
type Chart struct {
	Kind     string `json:"kind"`
	Headline string `json:"headline"`
	Rows     []Row  `json:"rows"`
	// Note is a short date or condition shown under the chart.
	Note string `json:"note,omitempty"`
	// Takeaway is one sentence the reader should act on.
	Takeaway string `json:"takeaway,omitempty"`
}

// Length limits, in characters. The prompt asks for less; these leave room
// for a model that runs a little over, since the panel wraps rather than
// clips. They keep a runaway answer from filling the sidebar.
const (
	maxHeadline = 80
	maxLabel    = 40
	maxValue    = 32
	maxDetail   = 80
	maxNote     = 60
	maxTakeaway = 160
)

// ErrNoJSON means the reply held no JSON object.
var ErrNoJSON = errors.New("no JSON object in the reply")

// Parse reads a chart from a model's reply -- the JSON object in it, with
// any prose or code fence around it ignored -- tidies its text and checks it.
func Parse(reply string) (Chart, error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start == -1 || end < start {
		return Chart{}, ErrNoJSON
	}
	var c Chart
	if err := json.Unmarshal([]byte(reply[start:end+1]), &c); err != nil {
		return Chart{}, fmt.Errorf("reading the chart: %w", err)
	}
	c.tidy()
	if err := c.Validate(); err != nil {
		return Chart{}, err
	}
	return c, nil
}

// Decode reads a stored chart, written by JSON.
func Decode(stored string) (Chart, error) {
	var c Chart
	if err := json.Unmarshal([]byte(stored), &c); err != nil {
		return Chart{}, fmt.Errorf("reading the stored chart: %w", err)
	}
	c.tidy()
	if err := c.Validate(); err != nil {
		return Chart{}, err
	}
	return c, nil
}

// JSON is the chart as stored.
func (c Chart) JSON() string {
	b, err := json.Marshal(c)
	if err != nil {
		// A struct of strings and bools always marshals.
		panic(err)
	}
	return string(b)
}

// Text is every word the chart shows, one field per line, for the checks a
// chart's words must pass: no source by number, no news site, no number the
// article does not state.
func (c Chart) Text() string {
	parts := []string{c.Headline}
	for _, r := range c.Rows {
		parts = append(parts, r.Label, r.Value, r.Detail)
	}
	parts = append(parts, c.Note, c.Takeaway)
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n")
}

// tidy collapses whitespace and drops control characters in every field, so
// a stray newline cannot reach the page and lengths count what is shown.
func (c *Chart) tidy() {
	c.Kind = strings.ToLower(clean(c.Kind))
	c.Headline = clean(c.Headline)
	c.Note = clean(c.Note)
	c.Takeaway = clean(c.Takeaway)
	for i := range c.Rows {
		r := &c.Rows[i]
		r.Label, r.Value, r.Detail = clean(r.Label), clean(r.Value), clean(r.Detail)
	}
}

// clean drops control and invisible format characters (zero-width spaces,
// soft hyphens, direction overrides), which would let a chart show an
// outlet's name or a number that the checks read differently, and collapses
// whitespace.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if (unicode.IsControl(r) && !unicode.IsSpace(r)) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// Validate checks a chart can be drawn as its kind says.
func (c Chart) Validate() error {
	if c.Headline == "" {
		return errors.New("chart has no headline")
	}
	if err := within("headline", c.Headline, maxHeadline); err != nil {
		return err
	}
	if err := within("note", c.Note, maxNote); err != nil {
		return err
	}
	if err := within("takeaway", c.Takeaway, maxTakeaway); err != nil {
		return err
	}
	for i, r := range c.Rows {
		if r.Value == "" {
			return fmt.Errorf("row %d has no value", i+1)
		}
		if err := within("label", r.Label, maxLabel); err != nil {
			return err
		}
		if err := within("value", r.Value, maxValue); err != nil {
			return err
		}
		if err := within("detail", r.Detail, maxDetail); err != nil {
			return err
		}
	}

	n := len(c.Rows)
	switch c.Kind {
	case KindChange:
		if n != 2 {
			return fmt.Errorf("a change has two rows, before and after; this has %d", n)
		}
		if strings.EqualFold(c.Rows[0].Value, c.Rows[1].Value) {
			return fmt.Errorf("a change from %q to the same value is not a change", c.Rows[0].Value)
		}
		if err := c.labelled(); err != nil {
			return err
		}
	case KindCompare:
		if n < 2 || n > 5 {
			return fmt.Errorf("a comparison has two to five rows; this has %d", n)
		}
		if err := c.labelled(); err != nil {
			return err
		}
		if _, err := c.amounts(); err != nil {
			return err
		}
	case KindFigures:
		// One number alone is not worth a chart; the prompt asks for
		// NO_INFOGRAPHIC then.
		if n < 2 || n > 5 {
			return fmt.Errorf("figures has two to five rows; this has %d", n)
		}
	case KindTimeline:
		if n < 2 || n > 5 {
			return fmt.Errorf("a timeline has two to five rows; this has %d", n)
		}
		for i, r := range c.Rows {
			if r.Label == "" {
				return fmt.Errorf("timeline row %d has no date", i+1)
			}
		}
	default:
		return fmt.Errorf("unknown kind of chart %q", c.Kind)
	}
	return nil
}

// labelled checks every row names what its value is.
func (c Chart) labelled() error {
	for i, r := range c.Rows {
		if r.Label == "" {
			return fmt.Errorf("%s row %d has no label", c.Kind, i+1)
		}
	}
	return nil
}

func within(field, s string, limit int) error {
	if n := utf8.RuneCountInString(s); n > limit {
		return fmt.Errorf("%s is %d characters, over %d: %q", field, n, limit, s)
	}
	return nil
}

var (
	// number is one figure as articles write it: "50", "1.35", "75,000".
	number = regexp.MustCompile(`\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?`)
	// ratio is "1:3" or "4:3", which is not a length.
	ratio = regexp.MustCompile(`\d\s*:\s*\d`)
)

// amounts reads each comparison row's value as a bar length.
//
// Each value must be exactly one number, and every row the same unit -- what
// is left of the value once the number is taken out: "%", "$", "miles". That
// is the rule against bars that claim a comparison the numbers do not make:
// a percentage beside a multiplier, two programs' points, a ratio drawn as a
// length. A range ("7,000-24,000") has two numbers and is refused too; it
// belongs in a row's label or detail.
func (c Chart) amounts() ([]float64, error) {
	out := make([]float64, len(c.Rows))
	unit := ""
	for i, r := range c.Rows {
		if ratio.MatchString(r.Value) {
			return nil, fmt.Errorf("compare row %d is a ratio (%q), which cannot be a bar", i+1, r.Value)
		}
		nums := number.FindAllString(r.Value, -1)
		if len(nums) != 1 {
			return nil, fmt.Errorf("compare row %d needs exactly one number, got %q", i+1, r.Value)
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(nums[0], ",", ""), 64)
		if err != nil {
			return nil, fmt.Errorf("compare row %d: %w", i+1, err)
		}
		u := strings.ToLower(strings.Join(strings.Fields(strings.Replace(r.Value, nums[0], "", 1)), " "))
		if i == 0 {
			unit = u
		} else if u != unit {
			return nil, fmt.Errorf("compare rows are in different units (%q and %q), so their bars would not compare", c.Rows[0].Value, r.Value)
		}
		out[i] = f
	}
	if largest(out...) <= 0 {
		return nil, errors.New("compare rows are all zero")
	}
	return out, nil
}

func largest(fs ...float64) float64 {
	m := math.Inf(-1)
	for _, f := range fs {
		m = math.Max(m, f)
	}
	return m
}

// View is a chart as the page draws it.
type View struct {
	Kind     string
	Headline string
	// First is the before value of a change, the headline figure of
	// figures, and the first row of the others; Rest is what follows.
	First    ViewRow
	Rest     []ViewRow
	Rows     []ViewRow
	Note     string
	Takeaway string
	// Stacked sets a change's before and after one above the other: either
	// value is too long to sit beside the other in the sidebar's width
	// without breaking a number in two.
	Stacked bool
}

// sideBySide is the longest change value, in characters, that still fits
// beside the other at the change's size in the sidebar: "$1,250", "4:3".
const sideBySide = 6

// ViewRow is one row as drawn.
type ViewRow struct {
	Label, Value, Detail string
	// Key is the row the accent colour marks: the longest bar of a
	// comparison, the step that matters in a timeline.
	Key bool
	// Width is a comparison bar's length, in percent of the longest.
	Width int
}

// minBar keeps the shortest bar visible beside a much longer one.
const minBar = 3

// View lays a valid chart out for the page.
func (c Chart) View() View {
	v := View{Kind: c.Kind, Headline: c.Headline, Note: c.Note, Takeaway: c.Takeaway}
	for _, r := range c.Rows {
		v.Rows = append(v.Rows, ViewRow{Label: r.Label, Value: r.Value, Detail: r.Detail, Key: r.Key})
	}
	switch c.Kind {
	case KindCompare:
		amounts, err := c.amounts()
		if err != nil {
			break
		}
		top := largest(amounts...)
		for i, a := range amounts {
			// Zero gets no bar at all; anything else at least a stub.
			if a > 0 {
				v.Rows[i].Width = int(math.Max(minBar, math.Round(a/top*100)))
			}
			v.Rows[i].Key = a == top
		}
	case KindTimeline:
		key := false
		for _, r := range v.Rows {
			key = key || r.Key
		}
		if !key {
			v.Rows[len(v.Rows)-1].Key = true
		}
	default:
		for i := range v.Rows {
			v.Rows[i].Key = false
		}
		if c.Kind == KindChange {
			for _, r := range c.Rows {
				v.Stacked = v.Stacked || utf8.RuneCountInString(r.Value) > sideBySide
			}
		}
	}
	if len(v.Rows) > 0 {
		v.First, v.Rest = v.Rows[0], v.Rows[1:]
	}
	return v
}
