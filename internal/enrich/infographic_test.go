package enrich

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

const drawnSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450">` +
	`<title>Fee vs value</title>` +
	`<rect x="10" y="10" width="300" height="24" fill="#1f6feb"/>` +
	`<text x="10" y="60" font-size="14">Annual fee $895</text></svg>`

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

func TestInfographic_StoresSanitizedMarkup(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{
		"information designer": "Here you go:\n```svg\n" + drawnSVG + "\n```",
	})}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	got, _ := readInfographic(t, d, id)
	if !strings.HasPrefix(got, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450"`) {
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

// An article with nothing to chart must be asked exactly once. Leaving the
// column empty would re-ask on every pass and, worse, would spend the article's
// whole attempt budget on the one field it can never have.
func TestInfographic_NoChartableNumbersIsRecordedNotRetried(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{
		"information designer": "NO_INFOGRAPHIC",
	})}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	got, _ := readInfographic(t, d, id)
	if got != db.NoInfographic {
		t.Fatalf("infographic_svg = %q, want the sentinel %q", got, db.NoInfographic)
	}

	// A second pass must not ask again -- that is the whole point of the
	// sentinel, and the cost of getting it wrong is a model call per pass
	// forever.
	before := client.promptCalls("information designer")
	s.Run()
	if after := client.promptCalls("information designer"); after != before {
		t.Errorf("infographic was requested %d more time(s) after the sentinel was written",
			after-before)
	}
	if got, _ := readInfographic(t, d, id); got != db.NoInfographic {
		t.Errorf("sentinel was overwritten: %q", got)
	}
	arts, err := d.ListArticlesNeedingEnrichment(s.MaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.ID == id && a.MissingInfographic {
			t.Error("article is still listed as needing an infographic after the sentinel was written")
		}
	}
}

// Markup the sanitiser rejects must leave the column empty rather than storing
// something the page would refuse to render anyway -- the article stays
// eligible so a later pass can try again.
func TestInfographic_UnsafeMarkupIsNotStored(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{reply: fullReplies(map[string]string{
		"information designer": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 450">` +
			`<title>x</title><script>alert(1)</script><text x="1" y="1">hi</text></svg>`,
	})}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	got, _ := readInfographic(t, d, id)
	if strings.Contains(got, "script") || strings.Contains(got, "alert") {
		t.Fatalf("executable markup reached the database:\n%s", got)
	}
}

// The chart's numbers come from key_details, so an article that has none yet
// must not be charted -- and must not have the sentinel written either, or it
// would never be charted once the details arrive.
func TestInfographic_WaitsForKeyDetails(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	client := &scriptedClient{
		fail: map[string]error{"Extract key details": errFake},
		reply: map[string]string{
			"Scout":                "take",
			"topic tags":           `["deals"]`,
			"image search queries": `["plane"]`,
			"information designer": drawnSVG,
		},
	}
	s := New(d, client, []string{"m1"}, "Scout", nil, nil)
	s.Pace = 0
	s.Run()

	if got, _ := readInfographic(t, d, id); got != "" {
		t.Errorf("infographic_svg = %q, want empty while key details are missing", got)
	}
	// Asking a model to chart numbers we do not have is a wasted call, so the
	// step must not run at all until key details exist.
	if n := client.promptCalls("information designer"); n != 0 {
		t.Errorf("infographic was requested %d time(s) with no key details to chart", n)
	}
	arts, err := d.ListArticlesNeedingEnrichment(s.MaxAttempts, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, a := range arts {
		if a.ID == id && a.MissingInfographic {
			found = true
		}
	}
	if !found {
		t.Error("article should still be eligible for an infographic once key details arrive")
	}
}

var errFake = fakeErr("provider exploded")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
