package ai

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Numbered source references.
//
// The prompts used to label each source "--- Source 1: [The Points Guy] ---",
// and the generator echoed the label into published copy: "Source 1 notes this
// offer doesn't come around often". Readers never see the source list, so the
// phrase is meaningless to them. The prompts now label sources by outlet only
// and forbid positional references, but a prompt is a request, not a
// guarantee, so generated text is also checked here.
//
// A reference is never rewritten to the outlet's name: articles must not name
// the sites they were written from at all (see outlets.go). Citation markers
// ("[1]") carry no wording and are removed; any other reference is reported, so
// the caller fails the attribution check and lets the revision pass restate the
// fact without attribution.
//
// "Article N" is deliberately NOT treated as a source reference: in this domain
// it is far more likely to be "Article 7 of EU261" than a leaked label.
var (
	// "[1]", "[2, 3]", "[Source 2]" used as citation markers. The trailing
	// group keeps whatever followed the marker and requires it to be a word
	// boundary of the prose kind, so "?a[1]=x" inside a URL is not touched.
	bracketCiteRE = regexp.MustCompile(`[ \t]*\[(?i:sources?\s*#?\s*)?\d{1,2}(?:\s*[,-]\s*\d{1,2})*\]([\s.,;:!?)<]|$)`)

	// "Sources 1 and 2", "sources 1, 2 and 3".
	sourceListRE = regexp.MustCompile(`(?i)\bsources?\s*#?\s*\d{1,2}(?:\s*(?:,\s*and|,|and|&)\s*#?\s*\d{1,2})+\b`)

	// "Source 1", "source #2", "Source2". The number is one or two digits
	// followed by a boundary, so years ("source 2026") never match.
	numberedSourceRE = regexp.MustCompile(`(?i)\bsources?\s*#?\s*(\d{1,2})\b`)

	// "the first source", "the 2nd source". Not "the first source of income".
	ordinalSourceRE = regexp.MustCompile(`(?i)\bthe\s+(first|second|third|fourth|fifth|sixth|1st|2nd|3rd|4th|5th|6th)\s+source\b`)
)

// StripCitationMarkers removes bracketed citation markers ("[1]", "[Source
// 2]"), returning the markers it removed. They are banned outright and carry
// no wording, so removing them never changes what a sentence says.
func StripCitationMarkers(text string) (string, []string) {
	if !strings.Contains(text, "[") {
		return text, nil
	}
	var removed []string
	out := replaceEach(bracketCiteRE, text, func(g []string, _, _ string) string {
		removed = append(removed, strings.TrimSpace(strings.TrimSuffix(g[0], g[1])))
		return g[1]
	})
	return out, removed
}

// FindSourceRefs returns the numbered source references in text, in order,
// without changing anything. Citation markers count.
func FindSourceRefs(text string) []string {
	if !mayContainSourceRef(text) {
		return nil
	}
	type span struct{ start, end int }
	var spans []span
	for _, re := range []*regexp.Regexp{bracketCiteRE, sourceListRE, numberedSourceRE} {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			end := m[1]
			if re == bracketCiteRE {
				end = m[2] // not the character after the marker
			}
			if re == numberedSourceRE && notASourceRef(text[:m[0]], text[m[1]:]) {
				continue
			}
			spans = append(spans, span{m[0], end})
		}
	}
	for _, m := range ordinalSourceRE.FindAllStringIndex(text, -1) {
		rest := strings.TrimLeft(text[m[1]:], " \t")
		if len(rest) >= 3 && strings.EqualFold(rest[:2], "of") && !isWordByte(rest[2]) {
			continue // "the first source of points"
		}
		spans = append(spans, span{m[0], m[1]})
	}
	// "Sources 1 and 2" also matches as "Sources 1": report the longest
	// match at each place once.
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	var found []string
	lastEnd := -1
	for _, s := range spans {
		if s.start < lastEnd {
			continue
		}
		lastEnd = s.end
		found = append(found, strings.TrimSpace(text[s.start:s.end]))
	}
	return found
}

// SourceRefIssue is the critic issue recorded for numbered references, worded
// so the revision pass knows what to do with it.
func SourceRefIssue(refs []string) string {
	var quoted []string
	seen := map[string]bool{}
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			quoted = append(quoted, strconv.Quote(r))
		}
	}
	return fmt.Sprintf("Refers to source material by number or position (%s). Readers never see the sources: "+
		"remove the reference and state the fact without attribution.", strings.Join(quoted, ", "))
}

// mayContainSourceRef is a cheap prefilter: every pattern needs "source" or
// a bracketed digit.
func mayContainSourceRef(text string) bool {
	return strings.Contains(strings.ToLower(text), "source") || strings.Contains(text, "[")
}

// notASourceRef rules out the few legitimate phrasings the numbered pattern
// would otherwise catch: "open source 2", and version-like "source 2.0".
func notASourceRef(before, after string) bool {
	if strings.HasSuffix(strings.ToLower(before), "open ") {
		return true
	}
	return len(after) >= 2 && (after[0] == '.' || after[0] == ',') && after[1] >= '0' && after[1] <= '9'
}

// normalizeOutlet keeps only letters and digits, lower-cased, so
// "LoyaltyLobby" and "Loyalty Lobby" compare equal.
func normalizeOutlet(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// replaceEach is regexp.ReplaceAllStringFunc with the submatches and the text
// on either side of the match, which RE2's lack of lookaround otherwise makes
// unavailable.
func replaceEach(re *regexp.Regexp, text string, fn func(groups []string, before, after string) string) string {
	matches := re.FindAllStringSubmatchIndex(text, -1)
	if matches == nil {
		return text
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		groups := make([]string, len(m)/2)
		for i := range groups {
			if m[2*i] >= 0 {
				groups[i] = text[m[2*i]:m[2*i+1]]
			}
		}
		b.WriteString(text[last:m[0]])
		b.WriteString(fn(groups, text[:m[0]], text[m[1]:]))
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
