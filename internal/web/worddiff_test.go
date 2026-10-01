package web

import (
	"strings"
	"testing"
)

func TestDiffWords(t *testing.T) {
	d := diffWords("<p>The annual fee is $695.</p><p>Lounges are unchanged.</p>",
		"<p>The annual fee is <strong>$895</strong>.</p><p>Lounges are unchanged.</p>")
	if d.Added != 1 || d.Removed != 1 {
		t.Errorf("added %d removed %d, want 1 and 1", d.Added, d.Removed)
	}
	got := string(d.HTML)
	for _, want := range []string{"<del>$695. </del>", "<ins>$895. </ins>", "<p>Lounges are unchanged. </p>"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "<strong>") {
		t.Error("tags from the content must not reach the diff")
	}
}

func TestDiffWords_EscapesText(t *testing.T) {
	d := diffWords("<p>a</p>", "<p>a &lt;script&gt;alert(1)&lt;/script&gt;</p>")
	if strings.Contains(string(d.HTML), "<script>") {
		t.Fatalf("unescaped text in diff: %s", d.HTML)
	}
	if !strings.Contains(string(d.HTML), "&lt;script&gt;") {
		t.Errorf("escaped text missing: %s", d.HTML)
	}
}

func TestDiffWords_SameAndListItems(t *testing.T) {
	d := diffWords("<ul><li>One</li><li>Two</li></ul>", "<ul><li>One</li><li>Two</li></ul>")
	if !d.Same {
		t.Error("identical content should be Same")
	}
	if strings.Count(string(d.HTML), "<p>") != 2 {
		t.Errorf("list items should be paragraphs: %s", d.HTML)
	}
}

func TestDiffWords_TooLong(t *testing.T) {
	long := strings.Repeat("word ", 2100)
	if d := diffWords(long, long+"x"); !d.TooLong || d.HTML != "" {
		t.Error("very long sections should fall back")
	}
}
