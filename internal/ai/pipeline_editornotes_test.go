package ai

import (
	"strings"
	"testing"
)

// The model's output with a note to the admin in it, as the revision prompt
// once asked for.
const outputWithNote = `Wyndham Rewards 100% Points Bonus

TLDR: Buy points at a 100% bonus through May 1.

KEY_DETAILS_START
- Bonus: 100%
KEY_DETAILS_END

<p>Wyndham is selling points with a 100% bonus.</p>
<div class="editor-note">Could not find a link for: promotion-specific landing page</div>`

func TestPipeline_SplitsEditorNotes(t *testing.T) {
	critic := `{"score": 90, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`
	newPipeline := func() *Pipeline {
		return NewPipeline(NewGenerator(&mockHFClient{response: outputWithNote}, "gen", "", nil),
			NewCritic(&mockHFClient{response: critic}, "critic"), 80)
	}
	sources := []SourceArticle{
		{Name: "Upgraded Points", URL: "https://up.com/1", Title: "Wyndham", Content: "100% bonus"},
		{Name: "Loyalty Lobby", URL: "https://ll.com/1", Title: "Wyndham", Content: "100% bonus"},
	}

	generated, err := newPipeline().Process("deal", sources)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := newPipeline().ProcessWithFeedback(sources, "<p>old</p>", "fix the link", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*PipelineResult{"generation": generated, "revision": revised} {
		if strings.Contains(r.Content, "editor-note") || strings.Contains(r.Content, "Could not find") {
			t.Errorf("%s: note left in content: %q", name, r.Content)
		}
		if !strings.Contains(r.Content, "Wyndham is selling points") {
			t.Errorf("%s: body lost: %q", name, r.Content)
		}
		if r.EditorNotes != "Could not find a link for: promotion-specific landing page" {
			t.Errorf("%s: notes %q", name, r.EditorNotes)
		}
	}
}
