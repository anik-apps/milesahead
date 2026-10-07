package ai

import (
	"strings"
	"testing"
)

// sampleSources returns a small set of sources used by multiple tests.
func sampleSources() []SourceArticle {
	return []SourceArticle{
		{
			Name:    "The Points Guy",
			Title:   "Hilton 100% Bonus",
			URL:     "https://thepointsguy.com/news/hilton-buy-points-bonus/",
			Content: "Hilton is offering 100% bonus through May 29, 2026.",
		},
		{
			Name:    "Loyalty Lobby",
			Title:   "Hilton Honors Buy Points 100%",
			URL:     "https://loyaltylobby.com/2026/04/hilton-buy-points/",
			Content: "Maximum purchase raised to 160,000 points during this sale.",
		},
	}
}

// TestRevisionPrompt_PassesFeedbackThrough verifies the admin's feedback text
// appears verbatim inside the assembled prompt — the model can't act on
// feedback it doesn't see.
func TestRevisionPrompt_PassesFeedbackThrough(t *testing.T) {
	feedback := "Where is the link to the claim?"
	prompt := RevisionPrompt(sampleSources(), "<p>previous article body</p>", feedback, "")

	if !strings.Contains(prompt, feedback) {
		t.Errorf("RevisionPrompt does not contain feedback text %q", feedback)
	}
	// The feedback must appear inside the marked block so the model recognizes it
	// as the admin's instruction, not just incidental text.
	if !strings.Contains(prompt, "=== ADMIN FEEDBACK ===") || !strings.Contains(prompt, "=== END FEEDBACK ===") {
		t.Error("RevisionPrompt missing ADMIN FEEDBACK markers")
	}
}

// TestRevisionPrompt_FormatsSourcesWithNames guards the loadSourceArticles bug:
// if Name is dropped on the source, the model loses the outlet label. Both
// source names should be present in the rendered prompt.
func TestRevisionPrompt_FormatsSourcesWithNames(t *testing.T) {
	sources := sampleSources()
	prompt := RevisionPrompt(sources, "<p>previous</p>", "fix typo", "")

	for _, s := range sources {
		marker := "=== From " + s.Name + " ==="
		if !strings.Contains(prompt, marker) {
			t.Errorf("RevisionPrompt missing source label %q", marker)
		}
	}
	if strings.Contains(prompt, "(outlet name missing)") {
		t.Error("RevisionPrompt rendered a missing outlet name — Name field not populated")
	}
}

// TestRevisionPrompt_OmitsSourceURLBlock guards the bug where RevisionPrompt
// dumped source-news URLs into a block that contradicted the writing-style
// rule forbidding links to those domains. The block should stay deleted.
func TestRevisionPrompt_OmitsSourceURLBlock(t *testing.T) {
	prompt := RevisionPrompt(sampleSources(), "<p>previous</p>", "fix typo", "")

	bannedSubstrings := []string{
		"Source article URLs (can be used as links",
		"Source article URLs (can be used",
	}
	for _, s := range bannedSubstrings {
		if strings.Contains(prompt, s) {
			t.Errorf("RevisionPrompt should NOT contain conflicting block %q", s)
		}
	}
}

// TestRevisionPrompt_IncludesBannedDomainList verifies the writing-style rules
// (which RevisionPrompt embeds) name the source-news domains explicitly so the
// model has a concrete list rather than a vague hint.
func TestRevisionPrompt_IncludesBannedDomainList(t *testing.T) {
	prompt := RevisionPrompt(sampleSources(), "<p>previous</p>", "add a link", "")

	requiredDomains := []string{
		"thepointsguy.com",
		"upgradedpoints.com",
		"doctorofcredit.com",
		"onemileatatime.com",
		"loyaltylobby.com",
	}
	for _, d := range requiredDomains {
		if !strings.Contains(prompt, d) {
			t.Errorf("RevisionPrompt missing banned-domain entry %q", d)
		}
	}
}

// TestRevisionPrompt_AllowsAdjacentFixes verifies the softened wording so
// feedback like "paragraph structuring is wrong" can be applied throughout
// the article rather than to a single paragraph.
func TestRevisionPrompt_AllowsAdjacentFixes(t *testing.T) {
	prompt := RevisionPrompt(sampleSources(), "<p>previous</p>", "rewrite intro", "")

	if !strings.Contains(prompt, "obvious adjacent fixes implied by the feedback") {
		t.Error("RevisionPrompt missing softened-revision wording")
	}
	// Old rigid wording must not survive.
	if strings.Contains(prompt, "EXACTLY what they asked — nothing more, nothing less") {
		t.Error("RevisionPrompt still contains old rigid wording")
	}
}

// TestFormatSources_RendersName is a focused regression test for the helper
// the revision flow depends on. If Name is empty, the label says so visibly
// rather than rendering an empty label.
func TestFormatSources_RendersName(t *testing.T) {
	got := formatSources([]SourceArticle{{Name: "Upgraded Points", Title: "T", URL: "u", Content: "c"}})
	if !strings.Contains(got, "=== From Upgraded Points ===") {
		t.Errorf("formatSources missing outlet label; got: %s", got)
	}

	gotEmpty := formatSources([]SourceArticle{{Title: "T", URL: "u", Content: "c"}})
	if !strings.Contains(gotEmpty, "(outlet name missing)") {
		t.Errorf("formatSources should flag an empty Name visibly; got: %s", gotEmpty)
	}
}

// TestFormatSources_NoNumbering guards the "Source 1 notes this offer..." leak:
// the sources were labelled "--- Source 1: [Name] ---" and the generator
// copied the label into published copy. The labels must carry no number or
// position for a model to repeat.
func TestFormatSources_NoNumbering(t *testing.T) {
	sources := append(sampleSources(), SourceArticle{Name: "Doctor of Credit", Title: "T3", URL: "u3", Content: "c3"})
	got := formatSources(sources)
	if refs := FindSourceRefs(got); len(refs) > 0 {
		t.Errorf("formatSources output contains numbered references %v:\n%s", refs, got)
	}
	for _, banned := range []string{"Source 1", "Source 2", "Source 3", "[1]", "#1", "--- Source"} {
		if strings.Contains(got, banned) {
			t.Errorf("formatSources output contains %q:\n%s", banned, got)
		}
	}
}

// TestSourcePrompts_LabelByOutletAndForbidNumbering covers every prompt that
// hands the model source material: the sources are labelled by outlet, and
// the writing prompts carry the rule against numbered references.
func TestSourcePrompts_LabelByOutletAndForbidNumbering(t *testing.T) {
	src := sampleSources()
	writers := map[string]string{
		"deal":     DealAlertPrompt(src),
		"roundup":  RoundupPrompt(src),
		"summary":  SummaryPrompt(src),
		"revision": RevisionPrompt(src, "<p>Source 1 notes the cap.</p>", "fix", ""),
	}
	all := map[string]string{
		"critic":  CriticPrompt("<p>article</p>", src),
		"critic2": Critic2Prompt("<p>article</p>", src),
	}
	for k, v := range writers {
		all[k] = v
	}

	for name, prompt := range all {
		for _, s := range src {
			if !strings.Contains(prompt, "=== From "+s.Name+" ===") {
				t.Errorf("%s prompt does not label %q by outlet", name, s.Name)
			}
		}
		if strings.Contains(prompt, "--- Source ") {
			t.Errorf("%s prompt still numbers its sources", name)
		}
	}
	for name, prompt := range writers {
		if !strings.Contains(prompt, sourceReferenceRule) {
			t.Errorf("%s prompt missing the source reference rule", name)
		}
		for _, want := range []string{"NEVER refer to a source by number or position", `"Source 1"`, `"the first source"`} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
	}
	if !strings.Contains(writers["revision"], "or refers to a source by number or position") {
		t.Error("revision prompt does not ask for existing numbered references to be fixed")
	}
}

// TestPrompts_NeverNameOutlets: every prompt that writes or checks article
// text carries the owner's rule -- never name or refer to the news sites and
// blogs -- with both exceptions verbatim, and none still tells the model to
// name the outlet when attributing.
func TestPrompts_NeverNameOutlets(t *testing.T) {
	src := sampleSources()
	prompts := map[string]string{
		"deal":        DealAlertPrompt(src),
		"roundup":     RoundupPrompt(src),
		"summary":     SummaryPrompt(src),
		"revision":    RevisionPrompt(src, "<p>previous</p>", "fix", "issue"),
		"critic":      CriticPrompt("<p>article</p>", src),
		"critic2":     Critic2Prompt("<p>article</p>", src),
		"scouts_take": ScoutsTakePrompt("Scout", "T", "tldr", "<p>body</p>"),
		"details":     ExtractDetailsPrompt("T", "<p>body</p>"),
		"infographic": InfographicPrompt("T", "tldr", `[{"label":"Fee","value":"$95"}]`),
	}
	exception1 := "1. The outlet is itself the subject of the story (e.g. a sale on Thrifty Traveler Premium)."
	exception2 := `2. An unconfirmed report whose only source is that outlet — then one mention, phrased as a report ("reportedly, according to X").`
	for name, p := range prompts {
		for _, want := range []string{
			"NEVER name or refer to the news sites and blogs you were given",
			"State facts plainly",
			"Issuers, airlines, hotel chains and their official pages are NOT news sites",
			exception1, exception2,
		} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
		for _, old := range []string{
			"name the outlet:", "name the outlet instead", "name the outlet or drop", "name the outlet only if",
			"Doctor of Credit is reporting", "One Mile at a Time notes...",
			"link the source name to their article", "The Points Guy is reporting</a>",
			"Do not mention source site names UNLESS",
		} {
			if strings.Contains(p, old) {
				t.Errorf("%s prompt still carries the old guidance %q", name, old)
			}
		}
	}
	for _, name := range []string{"critic", "critic2"} {
		if !strings.Contains(prompts[name], "set attribution_check to false and add an issue that quotes the phrase and says to remove the mention") {
			t.Errorf("%s prompt does not fail attribution on a news-site mention", name)
		}
	}
	if !strings.Contains(prompts["revision"], "If the previous article names or refers to a news site or blog") {
		t.Error("revision prompt does not ask for existing news-site mentions to be removed")
	}
}

// TestCriticPrompt_TreatsNumberedSourcesAsAttributionFailure: the critic must
// fail attribution on "Source N" phrasing so the auto-revision fixes it.
func TestCriticPrompt_TreatsNumberedSourcesAsAttributionFailure(t *testing.T) {
	p := CriticPrompt("<p>Source 1 notes the bonus.</p>", sampleSources())
	for _, want := range []string{"SOURCE NUMBERING", `"Source 1 notes"`, "ATTRIBUTION FAILURE", "attribution_check to false"} {
		if !strings.Contains(p, want) {
			t.Errorf("CriticPrompt missing %q", want)
		}
	}
}

// TestEnrichmentPrompts_ForbidNumberedSources: enrichment reads the article,
// not the sources, but older articles can still contain "Source 1".
func TestEnrichmentPrompts_ForbidNumberedSources(t *testing.T) {
	for name, p := range map[string]string{
		"scouts_take": ScoutsTakePrompt("Scout", "T", "tldr", "<p>Source 1 notes it.</p>"),
		"details":     ExtractDetailsPrompt("T", "<p>Source 1 notes it.</p>"),
		"infographic": InfographicPrompt("T", "Source 1 notes it.", "[]"),
	} {
		if !strings.Contains(p, "Never refer to sources by number or position") {
			t.Errorf("%s prompt missing the numbered-source rule", name)
		}
	}
}

// TestWritingStyleAndHTMLFormat_ForbidMarkdown verifies the format/style
// constants explicitly forbid Markdown in the body so models don't emit
// **bold** or ## headers.
func TestWritingStyleAndHTMLFormat_ForbidMarkdown(t *testing.T) {
	combined := writingStyle + htmlFormat

	for _, marker := range []string{"NEVER", "Markdown"} {
		if !strings.Contains(combined, marker) {
			t.Errorf("writingStyle+htmlFormat missing %q signal — Markdown rule may be missing", marker)
		}
	}
	if !strings.Contains(combined, "<strong>") {
		t.Error("writingStyle+htmlFormat missing <strong> example — Markdown-vs-HTML contrast not shown")
	}
	if !strings.Contains(combined, "ASCII hyphens") {
		t.Error("writingStyle+htmlFormat missing ASCII-hyphens rule")
	}
}

// The infographic prompt states the rules the checks enforce, so a model is
// not refused for something it was never told, and asks for JSON only.
func TestInfographicPrompt_StatesTheRules(t *testing.T) {
	p := InfographicPrompt("T", "tldr", "[]")
	for _, want := range []string{
		"Every number must appear in the article above",
		"add up or subtract",
		// What infographic.Chart.Validate enforces.
		`"change"`, `"compare"`, `"figures"`, `"timeline"`,
		"in the same unit",
		"Never put a total beside the parts",
		"Do not repeat the value in it",
		`never a ratio ("1:3"), a range ("7,000-24,000") or two numbers`,
		// What no check can catch.
		"order of steps exactly as the article puts it",
		"Only the JSON object",
		"NO_INFOGRAPHIC",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("infographic prompt missing %q", want)
		}
	}
	for _, gone := range []string{"<svg", "viewBox", "font-size"} {
		if strings.Contains(p, gone) {
			t.Errorf("infographic prompt still asks about drawing: %q", gone)
		}
	}
}
