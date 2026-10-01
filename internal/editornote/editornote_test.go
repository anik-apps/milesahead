package editornote

import (
	"reflect"
	"testing"
)

func TestSplit(t *testing.T) {
	tests := []struct {
		name, in, clean, notes string
	}{
		{"no note", "<p>Plain article.</p>", "<p>Plain article.</p>", ""},
		{"word only in text", "<p>An editor-note is not a class here.</p>",
			"<p>An editor-note is not a class here.</p>", ""},
		{"similar class", `<div class="editor-notes">kept</div>`, `<div class="editor-notes">kept</div>`, ""},
		{
			"production shape (article 57)",
			"<p>Body.</p>\n<div class=\"editor-note\">\nCould not find a link for: EVA Air promotional image from Source 2 (LoyaltyLobby)\n</div>",
			"<p>Body.</p>",
			"Could not find a link for: EVA Air promotional image from Source 2 (LoyaltyLobby)",
		},
		{
			"note in the middle, nested markup, entities",
			`<p>One.</p><div class="x editor-note"><div>Could not find</div> a <a href="u">link</a> &amp; more</div><p>Two &lt;3</p>`,
			`<p>One.</p><p>Two &lt;3</p>`,
			"Could not find a link & more",
		},
		{
			"two notes, one repeated",
			`<div class="editor-note">A</div><p>Body</p><span class='editor-note'>B</span><div class="editor-note">A</div>`,
			`<p>Body</p>`,
			"A\nB",
		},
		{"unclosed note runs to the end", `<p>Body</p><div class="editor-note">Note <p>tail`, `<p>Body</p>`, "Note tail"},
		{"empty void note", `<p>a</p><hr class="editor-note"><p>b</p>`, `<p>a</p><p>b</p>`, ""},
		{
			"content outside notes kept byte for byte",
			"<p>Caf&eacute; <b>bold</b><!-- c --></p>\n<div class=\"editor-note\">n</div>\n<p>after</p>",
			"<p>Caf&eacute; <b>bold</b><!-- c --></p>\n\n<p>after</p>",
			"n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clean, notes := Split(tt.in)
			if clean != tt.clean {
				t.Errorf("clean = %q, want %q", clean, tt.clean)
			}
			if notes != tt.notes {
				t.Errorf("notes = %q, want %q", notes, tt.notes)
			}
			if Strip(tt.in) != tt.clean {
				t.Errorf("Strip disagrees with Split")
			}
			// Splitting is idempotent: the clean content has nothing left.
			if again, n := Split(clean); again != clean || n != "" {
				t.Errorf("second split changed %q to %q, notes %q", clean, again, n)
			}
		})
	}
}

func TestJoinAndLines(t *testing.T) {
	if got := Join("a\nb", "", " b ", "c"); got != "a\nb\nc" {
		t.Errorf("Join = %q", got)
	}
	if got := Lines(""); got != nil {
		t.Errorf("Lines(\"\") = %v", got)
	}
	if got := Lines("a\n\nb"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Lines = %v", got)
	}
}
