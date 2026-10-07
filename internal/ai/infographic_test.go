package ai

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/infographic"
)

// figuresChart is a one-figure chart showing label and value.
func figuresChart(label, value string) string {
	return `{"kind":"figures","headline":"The fee","rows":[{"label":"` + label + `","value":"` + value + `"},{"label":"Renewal","value":"yearly"}]}`
}

var (
	fitsChart = figuresChart("Annual fee", "$895")
	// Bars that would compare a percentage with a multiplier, as one of the
	// production models' charts did; the chart's reader refuses them.
	mixedUnitsChart = `{"kind":"compare","headline":"Capital One to BA","rows":[` +
		`{"label":"Bonus","value":"20%"},{"label":"Rate","value":"1.2x"}]}`
)

// A chart one model gets wrong is a chart the next model may get right, so a
// refusal moves down the chain within the call rather than waiting a pass.
func TestGenerateInfographic_RefusedChartTriesTheNextModel(t *testing.T) {
	cases := map[string]struct {
		first  string
		accept func(infographic.Chart) error
	}{
		"bars in two units": {first: mixedUnitsChart},
		"unparseable":       {first: `{"kind":"figures",`},
		"an SVG":            {first: `<svg><text>Annual fee $895</text></svg>`},
		"numbered source":   {first: figuresChart("Annual fee (Source 1)", "$895")},
		"refused by the caller": {
			first: figuresChart("Annual fee", "$995"),
			accept: func(c infographic.Chart) error {
				if strings.Contains(c.Text(), "$995") {
					return fmt.Errorf("invented number")
				}
				return nil
			},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mock := &perModelMockHFClient{responses: map[string]string{"first": c.first, "second": fitsChart}}
			got, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", "", c.accept)
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
	mock := &perModelMockHFClient{responses: map[string]string{"first": fitsChart, "second": mixedUnitsChart}}
	if _, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", "", nil); err != nil {
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
		responses: map[string]string{"first": mixedUnitsChart},
		errs:      map[string]error{"second": fmt.Errorf("http request: context deadline exceeded")},
	}
	_, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", "", nil)
	if err == nil {
		t.Fatal("want an error when no chart is usable")
	}
	for _, want := range []string{"2 model(s)", "first: infographic: compare rows are in different units", `"20%"`, "second: http request"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// NO_INFOGRAPHIC is the model's judgement that there is nothing to chart, not
// a failure, so the chain stops there.
func TestGenerateInfographic_NoInfographicEndsTheChain(t *testing.T) {
	mock := &perModelMockHFClient{responses: map[string]string{"first": "NO_INFOGRAPHIC", "second": fitsChart}}
	_, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", "", nil)
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
	if _, err := GenerateInfographic(mock, []string{"first", "second"}, "T", "tldr", "[]", "", nil); err == nil {
		t.Error("want the 400 returned")
	}
	if len(mock.callsOrder) != 1 {
		t.Errorf("calls = %v, want only the first", mock.callsOrder)
	}
}

// The note reaches the model in the prompt.
func TestGenerateInfographic_SendsTheNote(t *testing.T) {
	c := &mockHFClient{response: fitsChart}
	if _, err := GenerateInfographic(c, []string{"m"}, "T", "tldr", "[]", "Label the fee", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.lastPrompt, `wrote: "Label the fee"`) {
		t.Error("the editor's note was not in the prompt")
	}
}
