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
// if Name is dropped on the source, the model sees `--- Source 1: [] ---` with
// empty brackets. Both source names should be present in the rendered prompt.
func TestRevisionPrompt_FormatsSourcesWithNames(t *testing.T) {
	sources := sampleSources()
	prompt := RevisionPrompt(sources, "<p>previous</p>", "fix typo", "")

	for _, s := range sources {
		marker := "[" + s.Name + "]"
		if !strings.Contains(prompt, marker) {
			t.Errorf("RevisionPrompt missing source name marker %q", marker)
		}
	}
	if strings.Contains(prompt, "[]") {
		t.Error("RevisionPrompt contains empty source-name brackets [] — Name field not populated")
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
// the revision flow depends on. If Name is empty, formatSources renders empty
// brackets and the prompt loses publisher context.
func TestFormatSources_RendersName(t *testing.T) {
	got := formatSources([]SourceArticle{{Name: "Upgraded Points", Title: "T", URL: "u", Content: "c"}})
	if !strings.Contains(got, "[Upgraded Points]") {
		t.Errorf("formatSources missing name marker; got: %s", got)
	}

	gotEmpty := formatSources([]SourceArticle{{Title: "T", URL: "u", Content: "c"}})
	if !strings.Contains(gotEmpty, "[]") {
		t.Errorf("formatSources should render [] for empty Name as a visible signal; got: %s", gotEmpty)
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
