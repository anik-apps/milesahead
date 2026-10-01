package ai

import (
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

// Presented in this order: Source 1 is One Mile at a Time, Source 2 is
// Upgraded Points.
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

// TestPipeline_Process_RewritesNumberedSourceRefs: a mappable "Source N" never
// reaches the article, in any field, and the critic reviews the rewritten text.
func TestPipeline_Process_RewritesNumberedSourceRefs(t *testing.T) {
	gen := &recordingClient{reply: numberedRefReply}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if res.Title != "One Mile at a Time Says Capital One's BA Bonus Is Rare" {
		t.Errorf("title = %q", res.Title)
	}
	if res.TLDR != "Upgraded Points confirms a 20% bonus through October 31." {
		t.Errorf("tldr = %q", res.TLDR)
	}
	if !strings.Contains(res.Content, `<p>One Mile at a Time notes this offer "doesn't come around often," and the 20% bump runs through October 31.</p>`) {
		t.Errorf("content = %q", res.Content)
	}
	for _, field := range []string{res.Title, res.TLDR, res.Content} {
		if refs := FindSourceRefs(field); len(refs) > 0 {
			t.Errorf("field still has numbered references %v: %q", refs, field)
		}
	}

	// Mapped references are not the critic's problem: no issue, no revision.
	if len(gen.prompts) != 1 {
		t.Errorf("generator called %d times; a fully mapped article needs no revision", len(gen.prompts))
	}
	if !res.Verification.AttributionCheck || len(res.Verification.Issues) != 0 {
		t.Errorf("verification = %+v; want a clean attribution check", res.Verification)
	}
	// The structured key details are part of the same reply, so they are
	// rewritten with it, both in the critic's copy and in the parsed details.
	if strings.Contains(critic.prompts[0], "Source 1 notes this offer") ||
		!strings.Contains(critic.prompts[0], "20% (per Upgraded Points)") {
		t.Error("the critic was shown the unrewritten article")
	}
	if len(res.KeyDetails) != 2 || res.KeyDetails[0].Value != "20% (per Upgraded Points)" || res.KeyDetails[1].Label != "Deadline" {
		t.Errorf("key details = %+v; want the generator's, with Source 2 rewritten", res.KeyDetails)
	}
}

// TestPipeline_Process_FlagsUnmappableSourceRefs: a number that maps to no
// source fails the attribution check, is handed to the auto-revision as an
// issue, and keeps the article below the publish threshold if it survives.
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
	revision := gen.prompts[1]
	if !strings.Contains(revision, `"Source 5"`) || !strings.Contains(revision, "name the outlet instead (One Mile at a Time, Upgraded Points)") {
		t.Errorf("auto-revision prompt does not carry the source-reference issue:\n%s", revision)
	}

	v := res.Verification
	if v.AttributionCheck {
		t.Error("attribution check passed with an unmappable source reference")
	}
	if len(v.Issues) == 0 || !strings.Contains(v.Issues[len(v.Issues)-1], `"Source 5"`) {
		t.Errorf("issues = %v; want the source-reference issue", v.Issues)
	}
	if v.Score > 85 || res.PassedThreshold {
		t.Errorf("score %d passed=%v; a failed attribution check must cost the score", v.Score, res.PassedThreshold)
	}
}

// TestPipeline_ProcessWithFeedback_RewritesAndFlags covers the admin revision
// path: mapped references are rewritten, unmapped ones fail attribution.
func TestPipeline_ProcessWithFeedback_RewritesAndFlags(t *testing.T) {
	gen := &recordingClient{reply: "Title\n\nTLDR: Source 2 says 20%.\n\n<p>Source 1 notes it is rare. Source 9 disagrees.</p>"}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.ProcessWithFeedback(numberedRefSources(), "<p>old</p>", "tighten it", "")
	if err != nil {
		t.Fatalf("ProcessWithFeedback: %v", err)
	}
	if res.TLDR != "Upgraded Points says 20%." {
		t.Errorf("tldr = %q", res.TLDR)
	}
	if !strings.Contains(res.Content, "One Mile at a Time notes it is rare. Source 9 disagrees.") {
		t.Errorf("content = %q", res.Content)
	}
	if res.Verification.AttributionCheck {
		t.Error("attribution check passed with Source 9 left in the article")
	}
}

// TestPipeline_InlineDetails_RewriteOrDropNumberedRefs: details from the
// separate extraction call get the same treatment.
func TestPipeline_InlineDetails_RewriteOrDropNumberedRefs(t *testing.T) {
	gen := &recordingClient{reply: "Title\n\n<p>Body.</p>"}
	client := &perModelMockHFClient{responses: map[string]string{
		"critic": cleanVerdict,
		"details": `{"key_details": [
			{"Label": "Bonus", "Value": "20% per Source 2"},
			{"Label": "Note", "Value": "Source 7 says more"},
			{"Label": "Deadline", "Value": "October 31, 2026"}], "links": []}`,
	}}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(client, "critic"), 80)
	p.SetDetailModels([]string{"details"})

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(res.KeyDetails) != 2 || res.KeyDetails[0].Value != "20% per Upgraded Points" || res.KeyDetails[1].Label != "Deadline" {
		t.Errorf("key details = %+v; want Source 2 rewritten and the Source 7 detail dropped", res.KeyDetails)
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
