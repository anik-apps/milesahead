package ai

import (
	"errors"
	"strings"
	"testing"
)

// recordingClient returns a fixed reply and keeps every prompt it was sent.
type recordingClient struct {
	reply   string
	prompts []string
}

func (c *recordingClient) Generate(model, prompt string, maxTokens int) (string, error) {
	c.prompts = append(c.prompts, prompt)
	return c.reply, nil
}

const cleanVerdict = `{"score": 95, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`

// Presented in this order, so a positional rewrite would turn Source 1 into
// One Mile at a Time and Source 2 into Upgraded Points.
func numberedRefSources() []SourceArticle {
	return []SourceArticle{
		{Name: "One Mile at a Time", URL: "https://onemileatatime.com/a", Title: "Capital One to BA 20%", Content: "This doesn't come around often. 20% bonus through October 31."},
		{Name: "Upgraded Points", URL: "https://upgradedpoints.com/b", Title: "20% bonus to Avios", Content: "Capital One 20% transfer bonus to British Airways through October 31."},
	}
}

const numberedRefReply = `Source 1 Says Capital One's BA Bonus Is Rare

TLDR: Source 2 confirms a 20% bonus through October 31.

KEY_DETAILS_START
- Bonus: 20% (per Source 2)
- Deadline: October 31, 2026
KEY_DETAILS_END

LINKS_START
LINKS_END

<p>Source 1 notes this offer "doesn't come around often," and the 20% bump runs through October 31 [1].</p>`

// outletNames are the outlets numberedRefSources names; none may appear in
// generated copy.
var outletNames = []string{"One Mile at a Time", "Upgraded Points"}

func assertNoOutletNames(t *testing.T, fields ...string) {
	t.Helper()
	for _, f := range fields {
		for _, name := range outletNames {
			if strings.Contains(f, name) {
				t.Errorf("%q was written into %q", name, f)
			}
		}
	}
}

// TestPipeline_Process_NumberedSourceRefsAreFlaggedNotRewritten: "Source 1"
// is never turned into the outlet's name, in any field. It fails attribution
// and goes to the auto-revision; only citation markers are removed outright.
func TestPipeline_Process_NumberedSourceRefsAreFlaggedNotRewritten(t *testing.T) {
	gen := &recordingClient{reply: numberedRefReply}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	var details []string
	for _, d := range res.KeyDetails {
		details = append(details, d.Label+": "+d.Value)
	}
	// The critic is shown the article between these two headings.
	shown := critic.prompts[0]
	shown = shown[strings.Index(shown, "Generated Article:"):strings.Index(shown, "Original Sources:")]
	assertNoOutletNames(t, append([]string{res.Title, res.TLDR, res.Content, shown}, details...)...)
	if strings.Contains(res.Content, "[1]") {
		t.Errorf("citation marker left in %q", res.Content)
	}

	if len(gen.prompts) != 2 {
		t.Fatalf("generator called %d times; want generation then auto-revision", len(gen.prompts))
	}
	if !strings.Contains(gen.prompts[1], `"Source 1"`) || !strings.Contains(gen.prompts[1], "state the fact without attribution") {
		t.Errorf("auto-revision prompt does not carry the source-reference issue:\n%s", gen.prompts[1])
	}
	// The fake writer repeats itself, so the reference survives the one
	// revision: the article is kept, failing attribution.
	v := res.Verification
	if v.AttributionCheck || len(v.Issues) == 0 || !strings.Contains(v.Issues[len(v.Issues)-1], `"Source 1"`) {
		t.Errorf("verification = %+v; want a failed attribution check with the issue", v)
	}
	// Key details are parsed from the reply: the one citing "Source 2" is
	// dropped, not rewritten to the outlet's name.
	if len(res.KeyDetails) != 1 || res.KeyDetails[0].Label != "Deadline" {
		t.Errorf("key details = %+v; want the Source 2 detail dropped and the deadline kept", res.KeyDetails)
	}
}

// TestPipeline_Process_FlagsUnmappableSourceRefs: a number that maps to no
// source is treated the same way, and keeps the article below the publish
// threshold if it survives.
func TestPipeline_Process_FlagsUnmappableSourceRefs(t *testing.T) {
	gen := &recordingClient{reply: "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\n<p>Source 5 notes this offer is rare.</p>"}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 90)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(gen.prompts) != 2 {
		t.Fatalf("generator called %d times; want generation then auto-revision", len(gen.prompts))
	}
	if !strings.Contains(gen.prompts[1], `"Source 5"`) {
		t.Errorf("auto-revision prompt does not carry the source-reference issue:\n%s", gen.prompts[1])
	}
	v := res.Verification
	if v.AttributionCheck {
		t.Error("attribution check passed with a numbered source reference")
	}
	if v.Score > 85 || res.PassedThreshold {
		t.Errorf("score %d passed=%v; a failed attribution check must cost the score", v.Score, res.PassedThreshold)
	}
}

// TestPipeline_ProcessWithFeedback_FlagsSourceRefs covers the admin revision
// path: references are flagged, not rewritten.
func TestPipeline_ProcessWithFeedback_FlagsSourceRefs(t *testing.T) {
	gen := &recordingClient{reply: "Title\n\nTLDR: Source 2 says 20%.\n\n<p>Source 1 notes it is rare. Source 9 disagrees.</p>"}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.ProcessWithFeedback(numberedRefSources(), "<p>old</p>", "tighten it", "")
	if err != nil {
		t.Fatalf("ProcessWithFeedback: %v", err)
	}
	assertNoOutletNames(t, res.TLDR, res.Content)
	if res.Verification.AttributionCheck {
		t.Error("attribution check passed with numbered references left in the article")
	}
}

// TestPipeline_InlineDetails_DropNumberedRefs: details from the separate
// extraction call have no revision pass after them, so any detail with a
// numbered reference is dropped.
func TestPipeline_InlineDetails_DropNumberedRefs(t *testing.T) {
	gen := &recordingClient{reply: "Title\n\n<p>Body.</p>"}
	client := &perModelMockHFClient{responses: map[string]string{
		"critic": cleanVerdict,
		"details": `{"key_details": [
			{"Label": "Bonus", "Value": "20% per Source 2"},
			{"Label": "Note", "Value": "Source 7 says more"},
			{"Label": "Cap", "Value": "160,000 points [1]"},
			{"Label": "Deadline", "Value": "October 31, 2026"}], "links": []}`,
	}}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(client, "critic"), 80)
	p.SetDetailModels([]string{"details"})

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(res.KeyDetails) != 2 || res.KeyDetails[0].Value != "160,000 points" || res.KeyDetails[1].Label != "Deadline" {
		t.Errorf("key details = %+v; want both numbered-reference details dropped and the marker removed", res.KeyDetails)
	}
}

func TestGenerateScoutsTake_RefusesNumberedSourceRefs(t *testing.T) {
	c := &mockHFClient{response: "Source 1 is right: transfer now."}
	if _, err := GenerateScoutsTake(c, []string{"m"}, "Scout", "T", "tldr", "body"); err == nil {
		t.Error("a take that refers to Source 1 was accepted")
	}
	c.response = "Transfer now if you fly BA; the 20% bonus is rare."
	if got, err := GenerateScoutsTake(c, []string{"m"}, "Scout", "T", "tldr", "body"); err != nil || got != c.response {
		t.Errorf("clean take = %q, %v", got, err)
	}
}

func TestExtractDetails_DropsNumberedSourceRefs(t *testing.T) {
	c := &mockHFClient{response: `{"key_details": [{"Label": "Bonus", "Value": "20% (Source 1)"}, {"Label": "Deadline", "Value": "October 31, 2026"}], "links": []}`}
	details, _, err := ExtractDetails(c, []string{"m"}, "T", "body")
	if err != nil {
		t.Fatalf("ExtractDetails: %v", err)
	}
	if len(details) != 1 || details[0].Label != "Deadline" {
		t.Errorf("details = %+v; want the Source 1 detail dropped", details)
	}

	c.response = `{"key_details": [{"Label": "Bonus", "Value": "per Source 2"}], "links": []}`
	if _, _, err := ExtractDetails(c, []string{"m"}, "T", "body"); err == nil {
		t.Error("want an error when every detail refers to a numbered source, so the field is retried")
	}
}

func TestGenerateInfographic_RefusesNumberedSourceRefs(t *testing.T) {
	chart := func(label string) string {
		return `{"kind":"figures","headline":"Fee","rows":[{"label":"` + label + `","value":"$895"}]}`
	}
	c := &mockHFClient{response: chart("Fee (Source 1)")}
	if _, err := GenerateInfographic(c, []string{"m"}, "T", "tldr", "[]", nil); err == nil {
		t.Error("a chart that refers to Source 1 was accepted")
	}
	c.response = chart("Annual fee")
	if got, err := GenerateInfographic(c, []string{"m"}, "T", "tldr", "[]", nil); err != nil || !strings.Contains(got, `"label":"Annual fee"`) {
		t.Errorf("clean chart = %q, %v", got, err)
	}
	c.response = "NO_INFOGRAPHIC"
	if _, err := GenerateInfographic(c, []string{"m"}, "T", "tldr", "[]", nil); !errors.Is(err, ErrNoInfographic) {
		t.Errorf("NO_INFOGRAPHIC = %v, want ErrNoInfographic", err)
	}
}
