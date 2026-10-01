package ai

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Numbered source references.
//
// The prompts used to label each source "--- Source 1: [The Points Guy] ---",
// and the generator echoed the label into published copy: "Source 1 notes this
// offer doesn't come around often". Readers never see the source list in that
// order (or at all, in the body), so the phrase is meaningless to them. The
// prompts now label sources by outlet only and forbid positional references,
// but a prompt is a request, not a guarantee, so generated text is also checked
// here: a reference that can be mapped to an outlet is rewritten to the
// outlet's name, and one that cannot is reported so the caller can fail the
// critic's attribution check and let the revision pass fix it.
//
// "Article N" is deliberately NOT treated as a source reference: in this domain
// it is far more likely to be "Article 7 of EU261" than a leaked label.
var (
	// "[1]", "[2, 3]", "[Source 2]" used as citation markers. They are banned
	// outright, so they are removed rather than mapped. The trailing group keeps
	// whatever followed the marker and requires it to be a word boundary of the
	// prose kind, so "?a[1]=x" inside a URL is not touched.
	bracketCiteRE = regexp.MustCompile(`[ \t]*\[(?i:sources?\s*#?\s*)?\d{1,2}(?:\s*[,-]\s*\d{1,2})*\]([\s.,;:!?)<]|$)`)

	// "Source 2 (LoyaltyLobby)": the model named the outlet itself.
	explicitSourceRE = regexp.MustCompile(`(?i)\bsources?\s*#?\s*(\d{1,2})\s*\(([^()<>\n]{2,40})\)`)

	// "Sources 1 and 2", "sources 1, 2 and 3".
	sourceListRE = regexp.MustCompile(`(?i)\bsources?\s*#?\s*\d{1,2}(?:\s*(?:,\s*and|,|and|&)\s*#?\s*\d{1,2})+\b`)

	// "Source 1", "source #2", "Source2". The number is one or two digits
	// followed by a boundary, so years ("source 2026") never match.
	numberedSourceRE = regexp.MustCompile(`(?i)\bsources?\s*#?\s*(\d{1,2})\b`)

	// "the first source", "the 2nd source". Not "the first source of income".
	ordinalSourceRE = regexp.MustCompile(`(?i)\bthe\s+(first|second|third|fourth|fifth|sixth|1st|2nd|3rd|4th|5th|6th)\s+source\b`)

	digitsRE = regexp.MustCompile(`\d{1,2}`)
)

var ordinals = map[string]int{
	"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6,
	"1st": 1, "2nd": 2, "3rd": 3, "4th": 4, "5th": 5, "6th": 6,
}

// SourceRefReport describes what RewriteSourceRefs did.
type SourceRefReport struct {
	// Rewritten lists each change as "old -> new" (citation markers that were
	// removed show an empty new side).
	Rewritten []string
	// Unresolved lists references left in place because they could not be
	// mapped to an outlet safely.
	Unresolved []string
}

// Changed reports whether any reference was rewritten or removed.
func (r SourceRefReport) Changed() bool { return len(r.Rewritten) > 0 }

// RewriteSourceRefs rewrites numbered references to source material.
//
// known is every outlet the text was written from; an outlet the text names
// itself in parentheses ("Source 2 (LoyaltyLobby)") is matched against it,
// ignoring case, spaces and punctuation, and wins over the number. byNumber
// maps a 1-based source number to an outlet name, or reports false when the
// number cannot be mapped unambiguously; such references are left in place and
// listed in Unresolved.
func RewriteSourceRefs(text string, known []string, byNumber func(n int) (string, bool)) (string, SourceRefReport) {
	var rep SourceRefReport
	if !mayContainSourceRef(text) {
		return text, rep
	}

	text = replaceEach(bracketCiteRE, text, func(g []string, _, _ string) string {
		rep.Rewritten = append(rep.Rewritten, strings.TrimSpace(strings.TrimSuffix(g[0], g[1]))+" -> ")
		return g[1]
	})

	// An explicit form whose outlet is not one of the sources is left alone
	// and reported: the text names an outlet the article was not written
	// from, so mapping the number by position would print a clean-looking
	// but possibly wrong name ("Loyalty Lobby (Some Blog)"). The span is
	// swapped for a placeholder so the passes below cannot rewrite its
	// "Source N", and restored at the end.
	var held []string
	text = replaceEach(explicitSourceRE, text, func(g []string, _, _ string) string {
		name, ok := matchOutlet(g[2], known)
		if !ok {
			rep.Unresolved = append(rep.Unresolved, g[0])
			held = append(held, g[0])
			return heldPlaceholder(len(held) - 1)
		}
		rep.Rewritten = append(rep.Rewritten, g[0]+" -> "+name)
		return name
	})

	text = replaceEach(sourceListRE, text, func(g []string, _, _ string) string {
		var names []string
		seen := map[string]bool{}
		for _, d := range digitsRE.FindAllString(g[0], -1) {
			n, _ := strconv.Atoi(d)
			name, ok := byNumber(n)
			if !ok {
				rep.Unresolved = append(rep.Unresolved, g[0])
				return g[0]
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		joined := joinNames(names)
		rep.Rewritten = append(rep.Rewritten, g[0]+" -> "+joined)
		return joined
	})

	text = replaceEach(numberedSourceRE, text, func(g []string, before, after string) string {
		if notASourceRef(before, after) {
			return g[0]
		}
		n, _ := strconv.Atoi(g[1])
		name, ok := byNumber(n)
		if !ok || rangeFollows(after) {
			rep.Unresolved = append(rep.Unresolved, g[0])
			return g[0]
		}
		rep.Rewritten = append(rep.Rewritten, g[0]+" -> "+name)
		return name
	})

	text = replaceEach(ordinalSourceRE, text, func(g []string, _, after string) string {
		if rest := strings.TrimLeft(after, " \t"); len(rest) >= 3 && strings.EqualFold(rest[:2], "of") && !isWordByte(rest[2]) {
			return g[0] // "the first source of points"
		}
		name, ok := byNumber(ordinals[strings.ToLower(g[1])])
		if !ok {
			rep.Unresolved = append(rep.Unresolved, g[0])
			return g[0]
		}
		rep.Rewritten = append(rep.Rewritten, g[0]+" -> "+name)
		return name
	})

	for i, h := range held {
		text = strings.Replace(text, heldPlaceholder(i), h, 1)
	}
	return text, rep
}

// heldPlaceholder marks a span the rewrite passes must not touch. It is built
// from private-use code points, which never occur in generated prose and
// contain nothing the patterns match.
func heldPlaceholder(i int) string {
	return "\uE000" + strconv.Itoa(i) + "\uE001"
}

// FixSourceRefs rewrites numbered references using the order the sources were
// presented in: "Source N" is the Nth source.
func FixSourceRefs(text string, sources []SourceArticle) (string, SourceRefReport) {
	names := SourceNames(sources)
	return RewriteSourceRefs(text, names, ByPosition(names))
}

// FindSourceRefs returns the numbered source references in text, without
// changing anything. Citation markers count.
func FindSourceRefs(text string) []string {
	if !mayContainSourceRef(text) {
		return nil
	}
	var found []string
	for _, re := range []*regexp.Regexp{bracketCiteRE, sourceListRE, numberedSourceRE} {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			end := m[1]
			if re == bracketCiteRE {
				end = m[2] // not the character after the marker
			}
			s := text[m[0]:end]
			if re == numberedSourceRE && notASourceRef(text[:m[0]], text[m[1]:]) {
				continue
			}
			found = append(found, strings.TrimSpace(s))
		}
	}
	for _, m := range ordinalSourceRE.FindAllStringIndex(text, -1) {
		rest := strings.TrimLeft(text[m[1]:], " \t")
		if len(rest) >= 3 && strings.EqualFold(rest[:2], "of") && !isWordByte(rest[2]) {
			continue
		}
		found = append(found, text[m[0]:m[1]])
	}
	return found
}

// SourceNames returns the outlet name of each source, in order.
func SourceNames(sources []SourceArticle) []string {
	names := make([]string, len(sources))
	for i, s := range sources {
		names[i] = s.Name
	}
	return names
}

// ByPosition maps "Source N" to names[N-1]. A number out of range, or a source
// with no usable outlet name, cannot be mapped.
func ByPosition(names []string) func(int) (string, bool) {
	return func(n int) (string, bool) {
		if n < 1 || n > len(names) || !usableOutlet(names[n-1]) {
			return "", false
		}
		return names[n-1], true
	}
}

// SourceRefIssue is the critic issue recorded for references that could not be
// rewritten, worded so the revision pass knows what to do with it.
func SourceRefIssue(unresolved []string, sources []SourceArticle) string {
	var outlets []string
	seen := map[string]bool{}
	for _, n := range SourceNames(sources) {
		if usableOutlet(n) && !seen[n] {
			seen[n] = true
			outlets = append(outlets, n)
		}
	}
	quoted := make([]string, len(unresolved))
	for i, u := range unresolved {
		quoted[i] = strconv.Quote(u)
	}
	return fmt.Sprintf("Refers to source material by number or position (%s). Readers never see numbered sources: "+
		"name the outlet instead (%s), or state the fact without attributing it.",
		strings.Join(quoted, ", "), strings.Join(outlets, ", "))
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

// moreNumbersRE matches the rest of a "Sources 1-3" range, or of a
// "Sources 1 and 7" list the list pass declined to rewrite.
var moreNumbersRE = regexp.MustCompile(`^\s*(?:,\s*and|,|and|&|to|-|–|—)\s*#?\s*\d`)

// rangeFollows reports that the number is the start of a range or list, which
// is left for the critic rather than half-rewritten ("Doctor of Credit-3").
func rangeFollows(after string) bool {
	return moreNumbersRE.MatchString(after)
}

func usableOutlet(name string) bool {
	n := strings.TrimSpace(name)
	return n != "" && !strings.EqualFold(n, "unknown") && !strings.ContainsAny(n, "\"\\<>")
}

// matchOutlet finds the known outlet a free-text mention refers to, comparing
// letters and digits only ("LoyaltyLobby" is "Loyalty Lobby").
func matchOutlet(mention string, known []string) (string, bool) {
	want := normalizeOutlet(mention)
	if want == "" {
		return "", false
	}
	for _, k := range known {
		if usableOutlet(k) && normalizeOutlet(k) == want {
			return k, true
		}
	}
	return "", false
}

func normalizeOutlet(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
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
