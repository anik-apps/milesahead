package ai

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func infographicSVG(text string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360"><title>Fee</title>` + text + `</svg>`
}

var (
	fitsChart = infographicSVG(`<text x="170" y="50" font-size="18" text-anchor="end">Annual fee</text>` +
		`<text x="450" y="50" font-size="18">$895</text>`)
	// The row label is right-aligned at x=170 and far too long, so it runs
	// off the left edge, as the production models' labels did.
	clippedChart = infographicSVG(`<text x="170" y="50" font-size="18" text-anchor="end">Qatar Airways Privilege Club 35% bonus</text>` +
		`<text x="450" y="50" font-size="18">35%</text>`)
)

// A chart one model gets wrong is a chart the next model may get right, so a
// refusal moves down the chain within the call rather than waiting a pass.
func TestGenerateInfographic_RefusedChartTriesTheNextModel(t *testing.T) {
	cases := map[string]struct {
		first  string
		accept func(string) error
	}{
		"text past the edge": {first: clippedChart},
		"unparseable":        {first: "<svg><rect></svg>"},
		"numbered source":    {first: infographicSVG(`<text x="10" y="50" font-size="18">$895 (Source 1)</text>`)},
		"refused by the caller": {
			first: infographicSVG(`<text x="10" y="50" font-size="18">Annual fee $995</text>`),
			accept: func(words string) error {
				if strings.Contains(words, "$995") {
					return fmt.Errorf("invented number")
				}
				return nil
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mock := &perModelMockHFClient{responses: map[string]string{"first": c.first, "second": fitsChart}}
			got, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", c.accept)
			if err != nil {
				t.Fatalf("err = %v, want the second model's chart", err)
			}
			if !strings.Contains(got, "Annual fee") {
				t.Errorf("got %q, want the second model's chart", got)
			}
			if strings.Join(mock.callsOrder, ",") != "first,second" {
				t.Errorf("calls = %v", mock.callsOrder)
			}
		})
	}
}

func TestGenerateInfographic_FirstUsableChartWins(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{"first": fitsChart, "second": clippedChart}}
	if _, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", nil); err != nil {
		t.Fatal(err)
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("calls = %v; a usable first chart should end the chain", mock.callsOrder)
	}
}

// When every model's chart is refused, the error says why for each, so the
// log shows what to fix in the prompt.
func TestGenerateInfographic_EveryChartRefused(t *testing.T) {
	mock := &perModelMockHFClient{
		responses: map[string]string{"first": clippedChart},
		errs:      map[string]error{"second": fmt.Errorf("http request: context deadline exceeded")},
	}
	_, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", nil)
	if err == nil {
		t.Fatal("want an error when no chart is usable")
	}
	for _, want := range []string{"2 model(s)", "first: infographic text runs past its edges", "Qatar Airways", "left edge", "second: http request"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// NO_INFOGRAPHIC is the model's judgement that there is nothing to chart, not
// a failure, so the chain stops there.
func TestGenerateInfographic_NoInfographicEndsTheChain(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{"first": "NO_INFOGRAPHIC", "second": fitsChart}}
	_, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", nil)
	if !errors.Is(err, ErrNoInfographic) {
		t.Errorf("err = %v, want ErrNoInfographic", err)
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("calls = %v, want only the first", mock.callsOrder)
	}
}

// A request the provider rejects outright would be rejected for every model,
// so it ends the chain as it does in TryModels.
func TestGenerateInfographic_BadRequestEndsTheChain(t *testing.T) {
	mock := &perModelMockHFClient{
		errs:      map[string]error{"first": &APIError{StatusCode: 400, Body: "bad request"}},
		responses: map[string]string{"second": fitsChart},
	}
	if _, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", nil); err == nil {
		t.Error("want the 400 returned")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("calls = %v, want only the first", mock.callsOrder)
	}
}
