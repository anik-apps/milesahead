package ai

import (
	"strings"
	"testing"
)

// The model returns a whole replacement section, so it has to see the whole
// section. It used to see the first 1,000 bytes, and every draft for a longer
// section dropped what came after.
func TestCardUpdateDetectionPrompt_ShowsTheWholeSection(t *testing.T) {
	section := "<ul>" + strings.Repeat("<li>An existing benefit, described in full.</li>", 100) + "<li>Last benefit</li></ul>"
	if len(section) < 4000 {
		t.Fatalf("fixture is %d bytes; it must be as long as the longest real section", len(section))
	}
	prompt := CardUpdateDetectionPrompt("Title", "Article body.", "Card", "Key Benefits", section)
	if !strings.Contains(prompt, section) {
		t.Fatal("the prompt does not carry the complete section")
	}
}

// Shown only the article, a model tends to rebuild the section from it. The
// prompt has to say that everything the article doesn't change stays.
func TestCardUpdateDetectionPrompt_SaysToKeepWhatTheArticleDoesNotChange(t *testing.T) {
	prompt := CardUpdateDetectionPrompt("Title", "Article body.", "Card", "Key Benefits", "<ul><li>A</li></ul>")
	for _, want := range []string{
		"keep every existing item",
		"Never drop a benefit because the article doesn't mention it",
		"Keep the section's existing HTML markup",
		"escape double quotes and newlines",
		"NO_UPDATE",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}
