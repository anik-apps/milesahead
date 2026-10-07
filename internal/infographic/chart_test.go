package infographic

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_ReadsTheJSONInAReply(t *testing.T) {
	reply := "Here is the chart:\n```json\n" +
		`{"kind":"change","headline":"  The IHG Select\nfee doubles ","rows":[{"label":"Old fee","value":"$49"},{"label":"New fee","value":"$99"}],"note":"From December 2, 2026","extra":"ignored"}` +
		"\n```"
	c, err := Parse(reply)
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindChange || c.Headline != "The IHG Select fee doubles" || len(c.Rows) != 2 || c.Note != "From December 2, 2026" {
		t.Errorf("parsed %+v", c)
	}
	// Stored and read back unchanged.
	back, err := Decode(c.JSON())
	if err != nil || back.JSON() != c.JSON() {
		t.Errorf("round trip: %v, %s vs %s", err, back.JSON(), c.JSON())
	}
	if strings.Contains(c.JSON(), "extra") {
		t.Error("an unknown field was stored")
	}
}

func TestParse_Refuses(t *testing.T) {
	two := `[{"label":"A","value":"$1"},{"label":"B","value":"$2"}]`
	cases := map[string]string{
		"no JSON":                 "NO_INFOGRAPHIC",
		"broken JSON":             `{"kind":"change",`,
		"unknown kind":            `{"kind":"pie","headline":"H","rows":` + two + `}`,
		"no headline":             `{"kind":"compare","headline":" ","rows":` + two + `}`,
		"empty value":             `{"kind":"compare","headline":"H","rows":[{"label":"A","value":"$1"},{"label":"B","value":""}]}`,
		"change with three rows":  `{"kind":"change","headline":"H","rows":[{"label":"A","value":"1"},{"label":"B","value":"2"},{"label":"C","value":"3"}]}`,
		"compare with one row":    `{"kind":"compare","headline":"H","rows":[{"label":"A","value":"$1"}]}`,
		"compare with six rows":   `{"kind":"compare","headline":"H","rows":[` + strings.Repeat(`{"label":"A","value":"$1"},`, 5) + `{"label":"A","value":"$1"}]}`,
		"figures with six rows":   `{"kind":"figures","headline":"H","rows":[` + strings.Repeat(`{"label":"A","value":"$1"},`, 5) + `{"label":"A","value":"$1"}]}`,
		"timeline with no date":   `{"kind":"timeline","headline":"H","rows":[{"label":"Jan 1","value":"Starts"},{"label":"","value":"Ends"}]}`,
		"timeline with one row":   `{"kind":"timeline","headline":"H","rows":[{"label":"Jan 1","value":"Starts"}]}`,
		"headline too long":       `{"kind":"compare","headline":"` + strings.Repeat("x", maxHeadline+1) + `","rows":` + two + `}`,
		"takeaway too long":       `{"kind":"compare","headline":"H","rows":` + two + `,"takeaway":"` + strings.Repeat("x", maxTakeaway+1) + `"}`,
		"compare ratio":           `{"kind":"compare","headline":"H","rows":[{"label":"Hilton","value":"1:3"},{"label":"Amtrak","value":"2:1"}]}`,
		"compare range":           `{"kind":"compare","headline":"H","rows":[{"label":"Tier 1","value":"7,000-24,000"},{"label":"Tier 2","value":"25,000"}]}`,
		"compare with no number":  `{"kind":"compare","headline":"H","rows":[{"label":"A","value":"High"},{"label":"B","value":"Low"}]}`,
		"compare percent and x":   `{"kind":"compare","headline":"H","rows":[{"label":"Bonus","value":"20%"},{"label":"Rate","value":"1.2x"}]}`,
		"compare dollars, points": `{"kind":"compare","headline":"H","rows":[{"label":"Fee","value":"$95"},{"label":"Bonus","value":"60,000 points"}]}`,
		"compare all zero":        `{"kind":"compare","headline":"H","rows":[{"label":"A","value":"0%"},{"label":"B","value":"0%"}]}`,
	}
	for name, reply := range cases {
		if _, err := Parse(reply); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse("NO_INFOGRAPHIC"); !errors.Is(err, ErrNoJSON) {
		t.Errorf("no JSON = %v, want ErrNoJSON", err)
	}
}

// A comparison's values must share a unit; how the unit is written does
// not matter, and neither do thousands separators.
func TestParse_CompareUnits(t *testing.T) {
	for name, rows := range map[string]string{
		"percent":         `[{"label":"JAL","value":"50%"},{"label":"Flying Blue","value":"40%"}]`,
		"dollars":         `[{"label":"Old","value":"$49"},{"label":"New","value":"$99"}]`,
		"miles, any case": `[{"label":"Base","value":"75,000 miles"},{"label":"Promo","value":"5,000 Miles"}]`,
		"bare numbers":    `[{"label":"A","value":"3"},{"label":"B","value":"36"}]`,
		"decimals":        `[{"label":"A","value":"1.5x"},{"label":"B","value":"1.35x"}]`,
	} {
		if _, err := Parse(`{"kind":"compare","headline":"H","rows":` + rows + `}`); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestView_CompareBars(t *testing.T) {
	c, err := Parse(`{"kind":"compare","headline":"Rove","rows":[` +
		`{"label":"Qatar","value":"35%"},{"label":"JAL","value":"50%"},{"label":"Tiny","value":"0.5%"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	v := c.View()
	if got := []int{v.Rows[0].Width, v.Rows[1].Width, v.Rows[2].Width}; got[0] != 70 || got[1] != 100 || got[2] != minBar {
		t.Errorf("widths = %v, want 70, 100 and the minimum", got)
	}
	if v.Rows[0].Key || !v.Rows[1].Key || v.Rows[2].Key {
		t.Errorf("only the longest bar is marked: %+v", v.Rows)
	}
	if v.First.Label != "Qatar" || len(v.Rest) != 2 {
		t.Errorf("First/Rest = %+v / %+v", v.First, v.Rest)
	}
}

func TestView_TimelineMarksTheLastStepUnlessTold(t *testing.T) {
	c, _ := Parse(`{"kind":"timeline","headline":"H","rows":[{"label":"Now","value":"1:1"},{"label":"Jan 1, 2027","value":"4:3"}]}`)
	if v := c.View(); v.Rows[0].Key || !v.Rows[1].Key {
		t.Errorf("unmarked timeline: %+v", v.Rows)
	}
	c, _ = Parse(`{"kind":"timeline","headline":"H","rows":[{"label":"Now","value":"1:1","key":true},{"label":"Jan 1, 2027","value":"4:3"}]}`)
	if v := c.View(); !v.Rows[0].Key || v.Rows[1].Key {
		t.Errorf("marked timeline: %+v", v.Rows)
	}
	// Key means nothing outside a timeline.
	c, _ = Parse(`{"kind":"figures","headline":"H","rows":[{"label":"a","value":"1","key":true}]}`)
	if v := c.View(); v.Rows[0].Key {
		t.Error("a figure was marked")
	}
}

func TestText_IsEveryWordShown(t *testing.T) {
	c, _ := Parse(`{"kind":"figures","headline":"United offer","rows":[{"label":"maximum total","value":"90,000 miles","detail":"with both bonuses"}],"note":"Ends soon","takeaway":"Apply before it ends."}`)
	want := "United offer\nmaximum total\n90,000 miles\nwith both bonuses\nEnds soon\nApply before it ends."
	if got := c.Text(); got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
}

func TestTidy_DropsControlCharacters(t *testing.T) {
	c, err := Parse("{\"kind\":\"figures\",\"headline\":\"Fee\\u0007 \\t now\",\"rows\":[{\"label\":\"a\",\"value\":\"$95\"}]}")
	if err != nil {
		t.Fatal(err)
	}
	if c.Headline != "Fee now" {
		t.Errorf("headline = %q", c.Headline)
	}
}
