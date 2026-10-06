package enrich

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// designerPrompt identifies the infographic call among a pass's prompts.
const designerPrompt = "information designer"

func drawn(label string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360">` +
		`<title>Fee vs value</title>` +
		`<rect x="10" y="10" width="300" height="24" fill="#1f6feb"/>` +
		`<text x="10" y="60" font-size="18">` + label + `</text></svg>`
}

var drawnSVG = drawn("Annual fee $895")

func readInfographic(t *testing.T, d *db.Database, id int64) (string, int) {
	t.Helper()
	var svg string
	var attempts int
	if err := d.DB().QueryRow(
		`SELECT COALESCE(infographic_svg,''), enrichment_attempts FROM generated_articles WHERE id = ?`,
		id).Scan(&svg, &attempts); err != nil {
		t.Fatalf("read back: %v", err)
	}
	return svg, attempts
}

func fullReplies(extra map[string]string) map[string]string {
	r := map[string]string{
		"Scout":                "Worth it only if you fly JAL.",
		"Extract key details":  `{"key_details":[{"label":"Fee","value":"$895"}],"links":[]}`,
		"topic tags":           `["transfer-bonuses"]`,
		"image search queries": `["jal plane"]`,
	}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

// newInfographicService is a reconciler with infographics on.
func newInfographicService(d *db.Database, client ai.HFClientInterface) *Service {
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Infographics = true
	return s
}

func TestInfographic_StoresSanitizedMarkup(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{
		designerPrompt: "Here you go:\n```svg\n" + drawnSVG + "\n```",
	})}
	newInfographicService(d, client).Run()

	got, _ := readInfographic(t, d, id)
	if !strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360"`) {
		t.Fatalf("stored markup is not the sanitiser's output:\n%s", got)
	}
	if !strings.Contains(got, "Annual fee $895") {
		t.Errorf("stored markup lost its labels:\n%s", got)
	}
	// The prose and fence the model wrapped it in must not survive.
	if strings.Contains(got, "```") || strings.Contains(got, "Here you go") {
		t.Errorf("wrapper text was stored:\n%s", got)
	}
}

// Off -- the default -- the step does not exist: no model call, nothing
// stored, and an article missing only its chart is not picked up at all.
func TestInfographic_OffMeansNoCallsAndNoBacklog(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{designerPrompt: drawnSVG})}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	if n := client.promptCalls(designerPrompt); n != 0 {
		t.Errorf("infographic requested %d time(s) with infographics off", n)
	}
	if got, _ := readInfographic(t, d, id); got != "" {
		t.Errorf("infographic stored with infographics off: %q", got)
	}

	// A finished article is not listed for its missing chart.
	done := seedArticle(t, d)
	if _, err := d.DB().Exec(`UPDATE generated_articles SET scouts_take = 't',
		key_details = '[{"label":"Fee","value":"$95"}]', image_url = 'u', tags_json = '["deals"]' WHERE id = ?`, done); err != nil {
		t.Fatal(err)
	}
	arts, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.ID == done || a.MissingInfographic {
			t.Errorf("listed with infographics off: %+v", a)
		}
	}
	arts, err = d.ListArticlesNeedingEnrichmentWithInfographics(3, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range arts {
		if a.ID == done && a.MissingInfographic && a.Missing() == "infographic" {
			found = true
		}
	}
	if !found {
		t.Error("with infographics on, a finished article without a chart should be listed for it alone")
	}
}

// An article with nothing to chart must be asked exactly once. Leaving the
// column empty would re-ask on every pass and spend the article's whole
// attempt budget on the one field it can never have.
func TestInfographic_NoChartableNumbersIsRecordedNotRetried(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{designerPrompt: "NO_INFOGRAPHIC"})}
	s := newInfographicService(d, client)
	s.Run()

	if got, _ := readInfographic(t, d, id); got != db.NoInfographic {
		t.Fatalf("infographic_svg = %q, want the sentinel %q", got, db.NoInfographic)
	}
	before := client.promptCalls(designerPrompt)
	s.Run()
	if after := client.promptCalls(designerPrompt); after != before {
		t.Errorf("infographic was requested %d more time(s) after the sentinel was written", after-before)
	}
	if got, _ := readInfographic(t, d, id); got != db.NoInfographic {
		t.Errorf("sentinel was overwritten: %q", got)
	}
}

// Markup the sanitiser rejects, or whose words break the outlet or
// source-numbering rules, is not stored: the column stays empty so a later
// pass can try again, and the failure is charged like any other.
func TestInfographic_RefusedChartsAreNotStored(t *testing.T) {
	cases := map[string]string{
		"script only":               `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360"><script>alert(1)</script></svg>`,
		"names an outlet":           drawn("According to The Points Guy, $895"),
		"names a configured source": drawn("Some Travel Blog says $895"),
		"numbered source":           drawn("Source 1: $895"),
		"outlet split over tspans":  drawn("Per One Mile at a <tspan>Time</tspan>"),
		"truncated":                 strings.TrimSuffix(drawnSVG, "</svg>"),
		"invented number":           drawn("Annual fee $995"),
		"derived number":            drawn("Fee over five years $4,475"),
		"text past the edge":        drawn("Annual fee $895, against the airline credit, the hotel credit and lounge access"),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			d := testDB(t)
			id := seedArticle(t, d)
			client := &scriptedClient{reply: fullReplies(map[string]string{designerPrompt: reply})}
			s := newInfographicService(d, client)
			s.Outlets = func() []ai.Outlet { return []ai.Outlet{{Name: "Some Travel Blog", URL: "https://sometravelblog.net"}} }
			s.Run()

			got, attempts := readInfographic(t, d, id)
			if got != "" {
				t.Errorf("refused chart was stored:\n%s", got)
			}
			if attempts != 1 {
				t.Errorf("attempts = %d, want 1", attempts)
			}
			arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(s.MaxAttempts, 10)
			if err != nil {
				t.Fatal(err)
			}
			var eligible bool
			for _, a := range arts {
				eligible = eligible || (a.ID == id && a.MissingInfographic)
			}
			if !eligible {
				t.Error("article should stay eligible for another try")
			}
		})
	}
}

// The outlet a story is about may be named, as in prose.
func TestInfographic_AllowsTheSubjectOutlet(t *testing.T) {
	d := testDB(t)
	id, err := d.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Thrifty Traveler Premium Is 30% Off", Slug: "ttp-sale", Content: "Body.",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{reply: fullReplies(map[string]string{
		designerPrompt: drawn("Thrifty Traveler Premium: 30% off"),
	})}
	newInfographicService(d, client).Run()
	if got, _ := readInfographic(t, d, id); !strings.Contains(got, "Thrifty Traveler Premium") {
		t.Errorf("a chart naming the story's own subject was refused: %q", got)
	}
}

// The chart's numbers come from key_details, so an article that has none yet
// must not be charted -- and must not get the sentinel either, or it would
// never be charted once the details arrive.
func TestInfographic_WaitsForKeyDetails(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{
		fail: map[string]error{"Extract key details": errFake},
		reply: map[string]string{
			"Scout":                "take",
			"topic tags":           `["deals"]`,
			"image search queries": `["plane"]`,
			designerPrompt:         drawnSVG,
		},
	}
	s := newInfographicService(d, client)
	s.Run()

	if got, _ := readInfographic(t, d, id); got != "" {
		t.Errorf("infographic_svg = %q, want empty while key details are missing", got)
	}
	if n := client.promptCalls(designerPrompt); n != 0 {
		t.Errorf("infographic was requested %d time(s) with no key details to chart", n)
	}
}

// The call goes through InfographicClient -- the bounded, no-retry client
// main builds -- and never through the enrichment client.
func TestInfographic_UsesItsOwnClient(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	shared := &scriptedClient{reply: fullReplies(nil)}
	own := &scriptedClient{reply: map[string]string{designerPrompt: drawnSVG}}
	s := newInfographicService(d, shared)
	s.InfographicClient = own
	s.Run()

	if n := shared.promptCalls(designerPrompt); n != 0 {
		t.Errorf("the enrichment client was asked for the chart %d time(s)", n)
	}
	if n := own.promptCalls(designerPrompt); n != 1 {
		t.Errorf("the infographic client was asked %d time(s), want 1", n)
	}
	if got, _ := readInfographic(t, d, id); got == "" {
		t.Error("nothing stored")
	}
}

// Past the pass deadline the chart is put off, not tried and not charged,
// exactly as an image search is.
func TestInfographic_PutOffPastTheDeadline(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)
	if _, err := d.DB().Exec(`UPDATE generated_articles SET scouts_take = 't',
		key_details = '[{"label":"Fee","value":"$95"}]', image_url = 'u', tags_json = '["deals"]' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{reply: map[string]string{designerPrompt: drawnSVG}}
	s := newInfographicService(d, client)

	arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(s.MaxAttempts, 10)
	if err != nil || len(arts) != 1 {
		t.Fatalf("listed %d, err %v; want the one article", len(arts), err)
	}
	filled, failed, putOff := s.enrichOne(arts[0], time.Now().Add(-time.Second))
	if filled != 0 || failed != 0 || !putOff {
		t.Errorf("enrichOne = %d filled, %d failed, put off %v; want 0, 0, true", filled, failed, putOff)
	}
	if n := client.promptCalls(designerPrompt); n != 0 {
		t.Errorf("chart requested %d time(s) past the deadline", n)
	}
	if got, attempts := readInfographic(t, d, id); got != "" || attempts != 0 {
		t.Errorf("stored %q with %d attempts; want nothing and no attempt charged", got, attempts)
	}
}

var errFake = fakeErr("provider exploded")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// With less of the pass left than the chain could take, the chart is put off
// without ending the pass: the article's other fields are saved, uncharged,
// and the chart waits for the next pass.
func TestInfographic_PutOffWhenThePassCannotFitTheChain(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)
	client := &scriptedClient{reply: fullReplies(map[string]string{designerPrompt: drawnSVG})}
	s := newInfographicService(d, client)

	arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(s.MaxAttempts, 10)
	if err != nil || len(arts) != 1 {
		t.Fatalf("listed %d, err %v", len(arts), err)
	}
	_, _, putOff := s.enrichOne(arts[0], time.Now().Add(s.infographicBudget()/2))
	if putOff {
		t.Error("a chart put off for want of time ended the pass")
	}
	if n := client.promptCalls(designerPrompt); n != 0 {
		t.Errorf("chart requested %d time(s) with too little of the pass left", n)
	}
	var take string
	if err := d.DB().QueryRow(`SELECT COALESCE(scouts_take,'') FROM generated_articles WHERE id = ?`, id).Scan(&take); err != nil {
		t.Fatal(err)
	}
	if take == "" {
		t.Error("the article's other fields were not saved")
	}
	if got, _ := readInfographic(t, d, id); got != "" {
		t.Errorf("chart stored: %q", got)
	}
}

// An admin waiting on a new image does not wait on a chart too: the chart
// waits a pass.
func TestInfographic_WaitsWhileAnImageIsRequested(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)
	client := &scriptedClient{reply: fullReplies(map[string]string{designerPrompt: drawnSVG})}
	s := newInfographicService(d, client)

	arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(s.MaxAttempts, 10)
	if err != nil || len(arts) != 1 {
		t.Fatalf("listed %d, err %v", len(arts), err)
	}
	a := arts[0]
	a.ImageRequestSeq = 1 // as listed with a pending request
	s.enrichOne(a, time.Now().Add(time.Hour))
	if n := client.promptCalls(designerPrompt); n != 0 {
		t.Errorf("chart requested %d time(s) while an image request was pending", n)
	}
	if got, _ := readInfographic(t, d, id); got != "" {
		t.Errorf("chart stored: %q", got)
	}
}

// chartsByModel answers the chart prompt per model, and every other prompt
// as its scriptedClient does.
type chartsByModel struct {
	scriptedClient
	charts map[string]string
	order  []string // models asked for a chart, in order
}

func (c *chartsByModel) Generate(model, prompt string, maxTokens int) (string, error) {
	if strings.Contains(prompt, designerPrompt) {
		c.order = append(c.order, model)
		if out, ok := c.charts[model]; ok {
			return out, nil
		}
		return "", fmt.Errorf("no chart scripted for %s", model)
	}
	return c.scriptedClient.Generate(model, prompt, maxTokens)
}

// Charts are drawn with their own chain when one is set, and a chart the
// first model gets wrong goes to the next in the same pass.
func TestInfographic_OwnChainAndFallbackOnRefusal(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &chartsByModel{
		scriptedClient: scriptedClient{reply: fullReplies(nil)},
		charts: map[string]string{
			"chart-a": drawn("Annual fee $995"), // not in the article
			"chart-b": drawnSVG,
		},
	}
	s := newInfographicService(d, client)
	s.InfographicModels = []string{"chart-a", "chart-b"}
	s.Run()

	if got := strings.Join(client.order, ","); got != "chart-a,chart-b" {
		t.Errorf("chart asked of %q, want chart-a then chart-b", got)
	}
	if got, _ := readInfographic(t, d, id); !strings.Contains(got, "Annual fee $895") {
		t.Errorf("stored %q, want chart-b's chart", got)
	}
}

// The pass reserves time for the chart's own chain, not the enrichment one.
func TestInfographic_BudgetIsTheChartChain(t *testing.T) {
	s := New(testDB(t), &scriptedClient{}, []string{"m1"}, "Scout", nil, nil)
	if got := s.infographicBudget(); got != ai.InfographicCallTimeout {
		t.Errorf("budget with the enrichment chain of one = %v", got)
	}
	s.InfographicModels = []string{"a", "b", "c"}
	if got := s.infographicBudget(); got != 3*ai.InfographicCallTimeout {
		t.Errorf("budget with a chart chain of three = %v, want %v", got, 3*ai.InfographicCallTimeout)
	}
}
