package ai

import (
	"strings"
	"testing"
)

func refSources() []SourceArticle {
	return []SourceArticle{
		{Name: "One Mile at a Time"},
		{Name: "Upgraded Points"},
		{Name: "Loyalty Lobby"},
	}
}

func TestFixSourceRefs_RewritesToOutletName(t *testing.T) {
	cases := []struct{ in, want string }{
		// The production example (article 1136).
		{`Source 1 notes this offer "doesn't come around often," and`,
			`One Mile at a Time notes this offer "doesn't come around often," and`},
		{"as discussed in Source 2.</li>", "as discussed in Upgraded Points.</li>"},
		{"Source 2 also notes the card", "Upgraded Points also notes the card"},
		{"according to source 3, the cap", "according to Loyalty Lobby, the cap"},
		{"Source #2 says", "Upgraded Points says"},
		{"Source1 says", "One Mile at a Time says"},
		{"Source 2's numbers", "Upgraded Points's numbers"},
		{"Sources 1 and 2 agree", "One Mile at a Time and Upgraded Points agree"},
		{"sources 1, 2 and 3 agree", "One Mile at a Time, Upgraded Points and Loyalty Lobby agree"},
		{"Source 1 & 3 agree", "One Mile at a Time and Loyalty Lobby agree"},
		{"Sources 2 and 2 agree", "Upgraded Points agree"},
		{"The first source says May 13", "One Mile at a Time says May 13"},
		{"while the second source says May 31", "while Upgraded Points says May 31"},
		{"image from Source 2 (LoyaltyLobby)", "image from Loyalty Lobby"},
		// The outlet named in the text wins over the number.
		{"image from Source 1 (Loyalty Lobby)", "image from Loyalty Lobby"},
		// Citation markers are banned outright and removed.
		{"a 20% bonus [1].", "a 20% bonus."},
		{"a 20% bonus [Source 2] through May", "a 20% bonus through May"},
		{"a 20% bonus [1, 3]</p>", "a 20% bonus</p>"},
	}
	for _, c := range cases {
		got, rep := FixSourceRefs(c.in, refSources())
		if got != c.want {
			t.Errorf("FixSourceRefs(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
		if len(rep.Unresolved) != 0 {
			t.Errorf("FixSourceRefs(%q) left unresolved %v", c.in, rep.Unresolved)
		}
		if !rep.Changed() {
			t.Errorf("FixSourceRefs(%q) reported no change", c.in)
		}
		if refs := FindSourceRefs(got); len(refs) != 0 {
			t.Errorf("rewritten text %q still has references %v", got, refs)
		}
	}
}

func TestFixSourceRefs_UnmappableIsFlaggedAndLeftInPlace(t *testing.T) {
	cases := []struct {
		in      string
		sources []SourceArticle
	}{
		{"Source 4 notes the cap", refSources()},                                           // out of range
		{"Source 0 notes the cap", refSources()},                                           // not a position
		{"the sixth source says", refSources()},                                            // out of range
		{"Sources 1 and 7 agree", refSources()},                                            // one of the list is out of range
		{"Sources 1-3 agree", refSources()},                                                // a range is not half-rewritten
		{"Source 2 notes", []SourceArticle{{Name: "Doctor of Credit"}, {Name: "Unknown"}}}, // no usable name
		{"Source 1 notes", []SourceArticle{{Name: ""}, {Name: "Doctor of Credit"}}},
	}
	for _, c := range cases {
		got, rep := FixSourceRefs(c.in, c.sources)
		if got != c.in {
			t.Errorf("FixSourceRefs(%q) = %q, want it left unchanged", c.in, got)
		}
		if len(rep.Unresolved) == 0 {
			t.Errorf("FixSourceRefs(%q) did not flag the reference", c.in)
		}
	}
}

func TestFixSourceRefs_NoFalsePositives(t *testing.T) {
	legit := []string{
		"Sources say the devaluation is coming.",
		"Multiple sources confirm the bonus; one source lists May 13.",
		"It is built on open source software.",
		"It runs on open source 2.0 tooling.",
		"Source: The Points Guy",
		"Source: 10xTravel",
		"<h3>Sources</h3>",
		"Chase is the first source of points for most people.",
		"The source of the confusion is the fine print.",
		"Compensation is set by Article 7 of EU261.",
		"Outsource 2 tasks; resource 3 is fine.",
		"The 2026 source data shows 3x points.",
		"Book via https://example.com/search?a[1]=x&b=2 today.",
		`Check your email for a subject line like "[Name], earn 75,000 points"`,
		"Miles4Migrants and 10xTravel both covered it.",
		"Up to 160,000 points (source 2026 terms).",
	}
	for _, s := range legit {
		got, rep := FixSourceRefs(s, refSources())
		if got != s || rep.Changed() || len(rep.Unresolved) != 0 {
			t.Errorf("FixSourceRefs(%q) = %q (%+v), want untouched", s, got, rep)
		}
		if refs := FindSourceRefs(s); len(refs) != 0 {
			t.Errorf("FindSourceRefs(%q) = %v, want none", s, refs)
		}
	}
}

func TestFindSourceRefs(t *testing.T) {
	got := FindSourceRefs("Source 1 notes it. The second source disagrees [2].")
	want := []string{"[2]", "Source 1", "The second source"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("FindSourceRefs = %q, want %q", got, want)
	}
}

func TestSourceRefIssue_NamesOutlets(t *testing.T) {
	issue := SourceRefIssue([]string{"Source 4"}, refSources())
	for _, want := range []string{`"Source 4"`, "One Mile at a Time", "Upgraded Points", "Loyalty Lobby"} {
		if !strings.Contains(issue, want) {
			t.Errorf("issue %q missing %q", issue, want)
		}
	}
}
