package ai

import (
	"strings"
	"testing"
)

func TestStripCitationMarkers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a 20% bonus [1].", "a 20% bonus."},
		{"a 20% bonus [Source 2] through May", "a 20% bonus through May"},
		{"a 20% bonus [1, 3]</p>", "a 20% bonus</p>"},
	}
	for _, c := range cases {
		got, removed := StripCitationMarkers(c.in)
		if got != c.want || len(removed) == 0 {
			t.Errorf("StripCitationMarkers(%q) = %q, %v; want %q", c.in, got, removed, c.want)
		}
	}
	for _, s := range []string{"Book via https://example.com/search?a[1]=x&b=2 today.", `"[Name], earn 75,000 points"`} {
		if got, removed := StripCitationMarkers(s); got != s || len(removed) != 0 {
			t.Errorf("StripCitationMarkers(%q) = %q, %v; want untouched", s, got, removed)
		}
	}
}

// TestFindSourceRefs_EveryForm: every numbered or positional reference is
// found, including the ones the pipeline used to rewrite to an outlet name.
func TestFindSourceRefs_EveryForm(t *testing.T) {
	for _, s := range []string{
		`Source 1 notes this offer "doesn't come around often," and`,
		"as discussed in Source 2.</li>",
		"according to source 3, the cap",
		"Source #2 says",
		"Source1 says",
		"Source 2's numbers",
		"Sources 1 and 2 agree",
		"sources 1, 2 and 3 agree",
		"Sources 1-3 agree",
		"The first source says May 13",
		"while the second source says May 31",
		"image from Source 2 (LoyaltyLobby)",
		"Source 0 notes the cap",
		"a 20% bonus [1].",
	} {
		if refs := FindSourceRefs(s); len(refs) == 0 {
			t.Errorf("FindSourceRefs(%q) found nothing", s)
		}
	}
}

func TestFindSourceRefs_NoFalsePositives(t *testing.T) {
	legit := []string{
		"Sources say the devaluation is coming.",
		"Multiple sources confirm the bonus; one source lists May 13.",
		"It is built on open source software.",
		"It runs on open source 2.0 tooling.",
		"<h3>Sources</h3>",
		"Chase is the first source of points for most people.",
		"The source of the confusion is the fine print.",
		"Compensation is set by Article 7 of EU261.",
		"Outsource 2 tasks; resource 3 is fine.",
		"The 2026 source data shows 3x points.",
		"Book via https://example.com/search?a[1]=x&b=2 today.",
		`Check your email for a subject line like "[Name], earn 75,000 points"`,
		"Up to 160,000 points (source 2026 terms).",
	}
	for _, s := range legit {
		if refs := FindSourceRefs(s); len(refs) != 0 {
			t.Errorf("FindSourceRefs(%q) = %v, want none", s, refs)
		}
	}
}

func TestFindSourceRefs_InOrderOncePerPlace(t *testing.T) {
	got := FindSourceRefs("Source 1 notes it. Sources 2 and 3 agree. The second source disagrees [2].")
	want := []string{"Source 1", "Sources 2 and 3", "The second source", "[2]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("FindSourceRefs = %q, want %q", got, want)
	}
}

// TestSourceRefIssue_NamesNoOutlet: the issue tells the writer to drop the
// attribution, never to put an outlet's name in its place.
func TestSourceRefIssue_NamesNoOutlet(t *testing.T) {
	issue := SourceRefIssue([]string{"Source 4", "Source 4", "the first source"})
	if strings.Count(issue, `"Source 4"`) != 1 || !strings.Contains(issue, `"the first source"`) {
		t.Errorf("issue %q should quote each reference once", issue)
	}
	if !strings.Contains(issue, "state the fact without attribution") {
		t.Errorf("issue %q does not say to drop the attribution", issue)
	}
	if strings.Contains(issue, "name the outlet") {
		t.Errorf("issue %q still asks for the outlet's name", issue)
	}
}
