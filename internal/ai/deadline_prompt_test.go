package ai

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/offer"
)

// The site marks an article's offer as ended from its "Deadline" key detail.
// Both prompts that produce key details must ask for that line in a form the
// parser reads.
func TestPrompts_AskForAParseableDeadline(t *testing.T) {
	gen := DealAlertPrompt([]SourceArticle{{Name: "A", Title: "T", Content: "C"}})
	extract := ExtractDetailsPrompt("T", "C")
	for name, p := range map[string]string{"generator": gen, "extract details": extract} {
		if !strings.Contains(p, `labelled "Deadline"`) && !strings.Contains(p, `label "Deadline"`) {
			t.Errorf("%s prompt does not ask for a Deadline key detail", name)
		}
		if !strings.Contains(p, "Month D, YYYY") {
			t.Errorf("%s prompt does not fix the Deadline date format", name)
		}
	}
}

// The worked example in the generator prompt, run through the real parsing
// and storage path, must come out with its end date: if the example and the
// parser ever drift apart, the model is being taught a format the site
// cannot read.
func TestPromptExample_DeadlineParsesToAnEndDate(t *testing.T) {
	i := strings.Index(htmlFormat, "Example:\n")
	if i < 0 {
		t.Fatal("htmlFormat has no worked example")
	}
	_, _, keyDetails, _, _ := parseStructuredContent(htmlFormat[i+len("Example:\n"):])
	if len(keyDetails) == 0 {
		t.Fatal("the example yields no key details")
	}
	stored, err := json.Marshal(keyDetails) // as article.Manager stores them
	if err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	if got := offer.EndDate(string(stored), written); got != "2026-05-01" {
		t.Errorf("example Deadline parsed to %q, want 2026-05-01 (key details %s)", got, stored)
	}
}
