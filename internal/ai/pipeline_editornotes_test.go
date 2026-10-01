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

// scriptedClient answers each call with the next scripted reply (repeating the
// last) and records every prompt.
type scriptedClient struct {
	replies []string
	prompts []string
}

func (c *scriptedClient) Generate(model, prompt string, maxTokens int) (string, error) {
	c.prompts = append(c.prompts, prompt)
	i := len(c.prompts) - 1
	if i >= len(c.replies) {
		i = len(c.replies) - 1
	}
	return c.replies[i], nil
}

// The critic grades the article only: no critic prompt, on generation, on
// the auto-revision's re-check or on an admin revision, carries an editor
// note. The notes that come back belong to the content that was kept.
func TestPipeline_CriticNeverSeesEditorNotes(t *testing.T) {
	const (
		firstNote   = "Could not find a link for: promotion-specific landing page"
		revisedNote = "Could not find a link for: the terms page from Source 2"
	)
	revisedOutput := strings.Replace(outputWithNote, firstNote, revisedNote, 1)
	withIssue := `{"score": 85, "issues": ["Missing end date"], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`
	clean := `{"score": 95, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`
	sources := []SourceArticle{
		{Name: "Upgraded Points", URL: "https://up.com/1", Title: "Wyndham", Content: "100% bonus"},
		{Name: "Loyalty Lobby", URL: "https://ll.com/1", Title: "Wyndham", Content: "100% bonus"},
	}

	gen := &scriptedClient{replies: []string{outputWithNote, revisedOutput}}
	critic := &scriptedClient{replies: []string{withIssue, clean}}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)
	generated, err := p.Process("deal", sources)
	if err != nil {
		t.Fatal(err)
	}
	if len(gen.prompts) != 2 {
		t.Fatalf("auto-revision did not run: %d generator calls", len(gen.prompts))
	}
	// The auto-revision rewrites the article without the note, and the
	// rewrite won, so its note is the one kept, with the outlet named.
	if strings.Contains(gen.prompts[1], firstNote) {
		t.Error("the auto-revision was shown the first draft's note")
	}
	if want := "Could not find a link for: the terms page from Loyalty Lobby"; generated.EditorNotes != want {
		t.Errorf("notes %q, want %q", generated.EditorNotes, want)
	}

	revGen := &scriptedClient{replies: []string{outputWithNote}}
	revCritic := &scriptedClient{replies: []string{clean}}
	rp := NewPipeline(NewGenerator(revGen, "gen", "", nil), NewCritic(revCritic, "critic"), 80)
	revised, err := rp.ProcessWithFeedback(sources, "<p>old</p>", "fix the link", "")
	if err != nil {
		t.Fatal(err)
	}
	if revised.EditorNotes != firstNote {
		t.Errorf("revision notes %q", revised.EditorNotes)
	}

	// Key details are parsed from the reply after the notes are out.
	for name, r := range map[string]*PipelineResult{"generation": generated, "revision": revised} {
		if len(r.KeyDetails) == 0 {
			t.Errorf("%s: key details not parsed from the reply", name)
		}
		for _, d := range r.KeyDetails {
			if strings.Contains(d.Label+d.Value, "Could not find") || strings.Contains(d.Label+d.Value, "editor-note") {
				t.Errorf("%s: note in key detail %+v", name, d)
			}
		}
	}

	// The critic client also serves the key-details call; every prompt it
	// got is checked, and the three verdicts must have seen the article.
	if len(critic.prompts) < 2 || len(revCritic.prompts) < 1 {
		t.Fatalf("critic calls: %d and %d", len(critic.prompts), len(revCritic.prompts))
	}
	for _, prompt := range []string{critic.prompts[0], critic.prompts[1], revCritic.prompts[0]} {
		if !strings.Contains(prompt, "Wyndham is selling points") {
			t.Errorf("critic prompt is missing the article")
		}
	}
	for i, prompt := range append(append([]string(nil), critic.prompts...), revCritic.prompts...) {
		for _, leak := range []string{"editor-note", "Could not find a link", firstNote, revisedNote} {
			if strings.Contains(prompt, leak) {
				t.Errorf("critic prompt %d contains %q", i, leak)
			}
		}
	}
}
