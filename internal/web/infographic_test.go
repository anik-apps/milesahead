package web

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// storedChart is a chart as enrichment stores it: a comparison of two
// transfer bonuses.
const storedChart = `{"kind":"compare","headline":"Rove transfer bonuses","rows":[` +
	`{"label":"Japan Airlines","value":"50%","detail":"1.5 miles per point"},` +
	`{"label":"Flying Blue","value":"40%"}],"note":"Through October 31, 2026"}`

func TestInfographicFor(t *testing.T) {
	v := infographicFor(storedChart)
	if v == nil {
		t.Fatal("a stored chart was not prepared")
	}
	if v.Kind != "compare" || v.Headline != "Rove transfer bonuses" || len(v.Rows) != 2 {
		t.Fatalf("view = %+v", v)
	}
	if v.Rows[0].Width != 100 || v.Rows[1].Width != 80 || !v.Rows[0].Key || v.Rows[1].Key {
		t.Errorf("bars = %+v; want 100%% (marked) and 80%%", v.Rows)
	}

	for name, stored := range map[string]string{
		"empty":    "",
		"sentinel": db.NoInfographic,
		"not JSON": "<svg><text>$895</text></svg>",
		// Read back, a stored chart is checked like a fresh one: these bars
		// would compare a ratio with a percentage.
		"not drawable": `{"kind":"compare","headline":"Transfers","rows":[{"label":"Hilton","value":"1:3"},{"label":"Amtrak","value":"40%"}]}`,
		"unknown kind": `{"kind":"pie","headline":"Fees","rows":[{"label":"Fee","value":"$95"}]}`,
	} {
		if v := infographicFor(stored); v != nil {
			t.Errorf("%s: prepared %+v, want nothing", name, v)
		}
	}
}

// Article pages show the chart only with ai.enrichment_infographics "on";
// "preview" is for the admin alone.
func TestPublicInfographic_FollowsTheMode(t *testing.T) {
	for mode, want := range map[string]bool{
		config.InfographicsOff: false, config.InfographicsPreview: false, config.InfographicsOn: true,
	} {
		app := &App{cfg: &config.Config{AI: config.AIConfig{EnrichmentInfographics: mode}}}
		if got := app.publicInfographic(storedChart) != nil; got != want {
			t.Errorf("mode %q: shown = %v, want %v", mode, got, want)
		}
	}
}
