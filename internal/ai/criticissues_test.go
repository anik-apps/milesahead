package ai

import (
	"strings"
	"testing"
)

// Issue strings below are verbatim from production verification_results
// (2026-09-21 to 2026-10-01) unless marked otherwise.

func TestIsPassedCheck_DropsConfirmations(t *testing.T) {
	for _, issue := range []string{
		"No issues detected. The article is accurate, complete, and well-written.",
		"Deadline of December 31, 2026 matches both sources.",
		"Registration required and offer codes 39002 and 64062 are correct per Loyalty Lobby source.",
		"Links point to ihg.com (official), not source news sites — good.",
		"Tone is natural, engaging, and flows well — readability is strong.",
		"HTML appears valid: no Markdown, tags properly opened and closed.",
		"No links to source news sites (TPG, OMAAT, etc.) — all links are to ihg.com — good.",
		"No contradictions or illogical passages — coherence is strong.",
		"HTML is clean — no Markdown artifacts, tags properly closed; template blocks (KEY_DETAILS, LINKS) appear intact.",
		"No hallucinated deals, programs, or numbers detected; all specific figures (2%, 2.5%, 3.5%, $295, $500, $5,000, 150 days, 12 billing cycles) match the sources.",
		"Article says 'Paid stays only — award nights and Points & Cash don't count' which is correct per both sources.",
		"The article's valuation of '~0.5¢ each ($1,000 total value)' matches Source 1's statement: 'We value IHG points at 0.5 cents per point, making 200,000 bonus points worth $1,000'.",
		"The article's tone is natural, engaging, and flows well — READABILITY check passes.",
		"No major fabricated claims: all core elements (200K bonus after $5K spend, November 18, 2026 deadline, 0.5¢ valuation, refreshed lineup with 125K–200K bonuses) are directly supported by the sources.",
		"Article claims Premier Select offers $50 United TravelBank annually, which matches Source 1, so not hallucinated.",
		"Article links to ihg.com and creditcards.chase.com in LINKS_START and in-body links, which are official pages, not source news sites, so no source leakage.",
		"The article does not link to any source news sites (UpgradedPoints, AwardWallet, etc.) in the body — links are to Chase and IHG official pages, which is permitted.",
		"The article links to Chase product pages (Sapphire Preferred, Sapphire Reserve, Ultimate Rewards Portal), which are official pages and permitted per guidelines, so no link check failure.",
		// Closing no-change verdicts.
		"The offer code 39002 is correctly cited and matches both sources. However, the article presents it as a single code for both offers, which is accurate per sources, but the links use the same offerCode parameter — no issue there.",
		"Upon recheck: The article's KEY_DETAILS_START says 'Stay window: September 28, 2026 – March 28, 2027' — this matches the source (09/28/2026 to 03/28/2027). TLDR also says same. So no error.",
		// From a critic run on the new prompt (2026-10-01).
		"The article's TLDR says 'through December 31, 2026' for the offer period, which matches the source. However, the article later says 'through the end of 2027' for Diamond status validity, which is correct per source. No issue.",
		// Reported from a critic run on the numbered-source prompt.
		"No attribution to sources by number or outlet — good.",
		"The bonus amount is verified.",
		"",
	} {
		if !isPassedCheck(issue) {
			t.Errorf("kept a passed check: %q", issue)
		}
	}
}

func TestIsPassedCheck_KeepsProblems(t *testing.T) {
	for _, issue := range []string{
		// Real errors, some of them phrased around a confirmation.
		"The article incorrectly states that Aer Lingus is part of the transatlantic joint venture.",
		"Article claims 'The standard rate is $12.50 per 1,000 points' which matches sources, but the calculation 'roughly $8.06 per 1,000' at 55% bonus is mathematically incorrect: $12.50 * (1 - 0.55) = $5.625 per 1,000 points, not $8.06.",
		"The article states the deadline is 'December 15, 202026' but sources say 'mid-December' or do not specify the exact day; however, the registration URL and context support this date, so it is acceptable.",
		"Article claims Premier Card fourth-night-free benefit requires $5,000 annual spend starting in 2027, which matches Source 2's statement about no-fee card, but incorrectly attributes it to Premier Card",
		"Article mentions Premier Business Card and Traveler Business Card details (fees, bonuses, credits) not found in either source; these are hallucinated details.",
		"HTML validity: <li> tags in KEY_DETAILS and LINKS sections are not wrapped in a <ul> or <ol> parent element, which is technically invalid HTML",
		"Article contains multiple links to source news sites (Doctor of Credit, Loyalty Lobby, Frequent Miler) in the body, violating SOURCE LEAKAGE and LINK CHECK rules.",
		"Title says '36 Times in 11 Weeks' but should be '36 Nights' — 36 nights could be fewer than 36 separate stays, a minor imprecision",
		"The article mentions specific quarterly rotating categories (gas stations, Amazon) that aren't listed in the sources",
		"Minor inconsistency: Source 2 mentions the bonus ends on April 18, 2026, but the article and other sources correctly state October 24, 2026.",
		"Source leakage in LINKS_START section (mentions American Airlines app URL directly)",
		"The TLDR could be more concise.",
		"Article links to source news sites: '<li>IHG One Rewards Credit Cards | https://creditcards.chase.com/rewards-credit-cards/ihg</li>'",
		"Article states 'Chase Sapphire Reserve and Reserve for Business retain 1:1 Hyatt transfers' which matches Source 2, but then incorrectly applies the same retention to Reserve for Business in the Venture comparison.",
		// From a critic run on the new prompt (2026-10-01): "only" keeps it.
		"The article does not link to any source news sites (e.g., upgradingpoints.com, onemileatatime.com) in the body — the links are to Marriott.com only. Good.",
		// Constructed: a confirmation next to a bare statement of the problem.
		"The deadline in the TLDR is correct; the key details say September 29.",
		"The bonus is accurate. The key details list 75,000 points.",
		"No attribution to sources by number, but the TLDR credits a blog by name.",
		"The TLDR says the bonus ends October 30; the sources say October 31. Otherwise no issues.",
		"The deadline is wrong in the key details. No other issue.",
		"Refers to source material by number or position (\"Source 5\"). Readers never see numbered sources: name the outlet instead (One Mile at a Time, Upgraded Points), or state the fact without attributing it.",
	} {
		if isPassedCheck(issue) {
			t.Errorf("dropped a problem: %q", issue)
		}
	}
}

func TestDropPassedChecks(t *testing.T) {
	v := &VerificationResult{
		Issues: []string{
			"Deadline of December 31, 2026 matches both sources.",
			"The TLDR could be more concise.",
			"HTML appears valid: no Markdown, tags properly opened and closed.",
		},
		FactualCheck: true, AttributionCheck: true, CompletenessCheck: true, HallucinationCheck: true,
	}
	dropped := dropPassedChecks(v)
	if len(dropped) != 2 || len(v.Issues) != 1 || v.Issues[0] != "The TLDR could be more concise." {
		t.Errorf("issues = %q, dropped = %q", v.Issues, dropped)
	}

	// All confirmations on a passing verdict: nothing left to fix.
	v = &VerificationResult{
		Issues:       []string{"No contradictions or illogical passages — coherence is strong."},
		FactualCheck: true, AttributionCheck: true, CompletenessCheck: true, HallucinationCheck: true,
	}
	if dropPassedChecks(v); v.Issues == nil || len(v.Issues) != 0 {
		t.Errorf("issues = %#v; want an empty, non-nil list", v.Issues)
	}
}

// TestDropPassedChecks_NeverClearsAFailingVerdict: a failed check with only
// badly worded issues keeps them, so the revision still runs.
func TestDropPassedChecks_NeverClearsAFailingVerdict(t *testing.T) {
	issues := []string{"Deadline of December 31, 2026 matches both sources."}
	v := &VerificationResult{
		Issues:       append([]string(nil), issues...),
		FactualCheck: false, AttributionCheck: true, CompletenessCheck: true, HallucinationCheck: true,
	}
	if dropped := dropPassedChecks(v); dropped != nil || len(v.Issues) != 1 {
		t.Errorf("issues = %q, dropped = %q; want the issues kept on a failed check", v.Issues, dropped)
	}
}

func TestCritic_VerifyDropsPassedChecks(t *testing.T) {
	c := NewCritic(&mockHFClient{response: `{"score": 92, "issues": [
		"Deadline of December 31, 2026 matches both sources.",
		"The article incorrectly states that Aer Lingus is part of the transatlantic joint venture."
	], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`}, "critic")
	v, err := c.Verify("<p>a</p>", sampleSources())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(v.Issues) != 1 || !strings.Contains(v.Issues[0], "Aer Lingus") {
		t.Errorf("issues = %q; want only the Aer Lingus error", v.Issues)
	}
}

func TestCriticPrompts_AskForActionableIssuesOnly(t *testing.T) {
	for name, p := range map[string]string{
		"critic":  CriticPrompt("<p>a</p>", sampleSources()),
		"critic2": Critic2Prompt("<p>a</p>", sampleSources()),
	} {
		for _, want := range []string{
			"Include ONLY problems that require changing the article",
			"Do NOT list checks that passed",
			`return "issues": []`,
			`"issues": [<problems that need a change in the article; [] if none>]`,
			`"factual_check": <bool>`,
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
	}
}

// criticCalls counts verification prompts; the same client also answers the
// key-details call.
func criticCalls(c *recordingClient) int {
	n := 0
	for _, p := range c.prompts {
		if strings.Contains(p, "VERIFICATION CHECKLIST") {
			n++
		}
	}
	return n
}

// positiveOnlyVerdict is a production-style reply whose issues are all passed
// checks (verdict 1233, abridged).
const positiveOnlyVerdict = `{"score": 92, "issues": [
	"Deadline of December 31, 2026 matches both sources.",
	"Links point to ihg.com (official), not source news sites — good.",
	"HTML appears valid: no Markdown, tags properly opened and closed.",
	"No contradictions or illogical passages — coherence is strong."
], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`

func TestPipeline_Process_PassedChecksDoNotRevise(t *testing.T) {
	gen := &recordingClient{reply: "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\n<p>The 20% bonus runs through October 31.</p>"}
	critic := &recordingClient{reply: positiveOnlyVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 75)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(gen.prompts) != 1 || criticCalls(critic) != 1 {
		t.Errorf("generator called %d times, critic %d; want one each, no auto-revision",
			len(gen.prompts), criticCalls(critic))
	}
	if len(res.Verification.Issues) != 0 || res.Verification.Score != 92 {
		t.Errorf("verification = %+v; want score 92 and no issues", res.Verification)
	}
}

func TestPipeline_Process_RealIssueStillRevises(t *testing.T) {
	gen := &recordingClient{reply: "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\n<p>The 20% bonus runs through October 30.</p>"}
	critic := &recordingClient{reply: `{"score": 85, "issues": [
		"Deadline of December 31, 2026 matches both sources.",
		"The article says the bonus ends October 30; both sources say October 31."
	], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 75)

	if _, err := p.Process("deal", numberedRefSources()); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(gen.prompts) != 2 || criticCalls(critic) != 2 {
		t.Fatalf("generator called %d times, critic %d; want an auto-revision", len(gen.prompts), criticCalls(critic))
	}
	revision := gen.prompts[1]
	if !strings.Contains(revision, "both sources say October 31") {
		t.Errorf("auto-revision prompt does not carry the real issue:\n%s", revision)
	}
	if strings.Contains(revision, "matches both sources") {
		t.Errorf("auto-revision prompt asks the generator to fix a passed check:\n%s", revision)
	}
}

// TestPipeline_Process_SourceRefIssueRevisesDespitePassedChecks: an
// unmappable "Source N" forces the revision even when the critic's own issues
// are all confirmations and get dropped.
func TestPipeline_Process_SourceRefIssueRevisesDespitePassedChecks(t *testing.T) {
	gen := &recordingClient{reply: "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\n<p>Source 5 notes this offer is rare.</p>"}
	critic := &recordingClient{reply: positiveOnlyVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 90)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(gen.prompts) != 2 {
		t.Fatalf("generator called %d times; want generation then auto-revision", len(gen.prompts))
	}
	revision := gen.prompts[1]
	if !strings.Contains(revision, `"Source 5"`) {
		t.Errorf("auto-revision prompt does not carry the source-reference issue:\n%s", revision)
	}
	if strings.Contains(revision, "coherence is strong") {
		t.Errorf("auto-revision prompt carries a passed check:\n%s", revision)
	}
	v := res.Verification
	if v.AttributionCheck || len(v.Issues) != 1 || !strings.Contains(v.Issues[0], `"Source 5"`) {
		t.Errorf("verification = %+v; want only the source-reference issue and a failed attribution check", v)
	}
}

// TestCritic_VerifyAcceptsObjectIssues: asked what to fix, a production critic
// once answered with {"problem", "fix"} objects. That must not become a parse
// error, which would drop the article from the scan.
func TestCritic_VerifyAcceptsObjectIssues(t *testing.T) {
	c := NewCritic(&mockHFClient{response: `{"score": 85, "issues": [
		{"problem": "The article says the Premier fee was $99; no source gives the prior fee.", "fix": "Drop the $99."},
		"The TLDR omits the November 18 deadline.",
		{"severity": "high", "detail": "Traveler Business Card is not in either source."},
		42
	], "factual_check": false, "attribution_check": true, "completeness_check": false, "hallucination_check": true}`}, "critic")
	v, err := c.Verify("<p>a</p>", sampleSources())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	want := []string{
		"The article says the Premier fee was $99; no source gives the prior fee. Fix: Drop the $99.",
		"The TLDR omits the November 18 deadline.",
		"Traveler Business Card is not in either source. high",
		"42",
	}
	if strings.Join(v.Issues, "\n") != strings.Join(want, "\n") {
		t.Errorf("issues = %q\nwant %q", v.Issues, want)
	}
	if v.FactualCheck || v.Score != 85 {
		t.Errorf("verdict fields lost: %+v", v)
	}
}
