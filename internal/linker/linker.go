package linker

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// MatchedCard represents a card that was found and linked in the article content.
type MatchedCard struct {
	Slug  string
	Title string
}

// cardKeyword pairs a keyword (card title or alias from KeywordsJSON) with the
// card it identifies. Used internally by AutoLink and DetectMentions.
type cardKeyword struct {
	keyword string
	card    db.ReferencePage
}

// buildCardKeywords returns all (title + KeywordsJSON aliases) keyword/card
// pairs for the given cards, sorted by keyword length descending so longer
// keywords match before their suffixes (e.g. "Chase Sapphire Preferred"
// claims a span before "Sapphire Preferred" tries). Malformed KeywordsJSON
// is tolerated — the card's title still contributes a keyword.
func buildCardKeywords(cards []db.ReferencePage) []cardKeyword {
	var out []cardKeyword
	for _, card := range cards {
		out = append(out, cardKeyword{keyword: card.Title, card: card})
		var extraKW []string
		if card.KeywordsJSON != "" && card.KeywordsJSON != "[]" {
			_ = json.Unmarshal([]byte(card.KeywordsJSON), &extraKW)
		}
		for _, kw := range extraKW {
			kw = strings.TrimSpace(kw)
			if kw != "" {
				out = append(out, cardKeyword{keyword: kw, card: card})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return len(out[i].keyword) > len(out[j].keyword)
	})
	return out
}

// AutoLink scans content for card name mentions and replaces the first occurrence
// of each with a link to the card's reference page. It skips matches that are
// already inside <a> or <code> tags. Keywords are matched longest-first to avoid
// partial matches.
func AutoLink(content string, cards []db.ReferencePage) (string, []MatchedCard) {
	if len(cards) == 0 {
		return content, nil
	}

	keywords := buildCardKeywords(cards)

	var matched []MatchedCard
	seen := make(map[string]bool) // track by slug to link only first mention per card

	for _, kw := range keywords {
		if seen[kw.card.Slug] {
			continue
		}

		// Build a regex with word boundaries for case-insensitive matching.
		pattern := fmt.Sprintf(`(?i)\b%s\b`, regexp.QuoteMeta(kw.keyword))
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}

		loc := re.FindStringIndex(content)
		if loc == nil {
			continue
		}

		// Check if the match is inside an <a> or <code> tag.
		if isInsideTag(content, loc[0]) {
			continue
		}

		// Replace the first match with a link.
		original := content[loc[0]:loc[1]]
		link := fmt.Sprintf(`<a href="/cards/%s">%s</a>`, html.EscapeString(kw.card.Slug), original)
		content = content[:loc[0]] + link + content[loc[1]:]

		matched = append(matched, MatchedCard{Slug: kw.card.Slug, Title: kw.card.Title})
		seen[kw.card.Slug] = true
	}

	return content, matched
}

// DetectedCard reports a card whose name appears in article content, with the
// number of distinct keyword matches.
type DetectedCard struct {
	Slug  string
	Title string
	Count int
}

// DetectMentions scans content for card name and keyword mentions and returns
// a count per card, sorted by count descending then by slug ascending. It
// does NOT modify the content (unlike AutoLink). Used by the admin curation
// page to surface auto-detected cards beyond the auto-linked top set.
func DetectMentions(content string, cards []db.ReferencePage) []DetectedCard {
	if content == "" || len(cards) == 0 {
		return nil
	}

	keywords := buildCardKeywords(cards)

	// claimedRanges marks character ranges already attributed to a longer
	// keyword. A shorter keyword that overlaps a claimed range is skipped.
	type span struct{ start, end int }
	var claimed []span
	overlaps := func(s, e int) bool {
		for _, c := range claimed {
			if s < c.end && e > c.start {
				return true
			}
		}
		return false
	}

	// isWordChar / atWordBoundary mimic regexp \b semantics so a card titled
	// "Visa" doesn't match inside "Visage".
	isWordChar := func(b byte) bool {
		return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') ||
			(b >= '0' && b <= '9') || b == '_'
	}
	atWordBoundary := func(s string, p int) bool {
		if p == 0 || p == len(s) {
			return true
		}
		return isWordChar(s[p-1]) != isWordChar(s[p])
	}

	contentLower := strings.ToLower(content)
	counts := make(map[string]int)
	titles := make(map[string]string)
	for _, kw := range keywords {
		needle := strings.ToLower(kw.keyword)
		from := 0
		for {
			idx := strings.Index(contentLower[from:], needle)
			if idx < 0 {
				break
			}
			absStart := from + idx
			absEnd := absStart + len(needle)
			if atWordBoundary(contentLower, absStart) && atWordBoundary(contentLower, absEnd) &&
				!overlaps(absStart, absEnd) {
				counts[kw.card.Slug]++
				titles[kw.card.Slug] = kw.card.Title
				claimed = append(claimed, span{absStart, absEnd})
			}
			from = absEnd
		}
	}

	var out []DetectedCard
	for slug, count := range counts {
		out = append(out, DetectedCard{Slug: slug, Title: titles[slug], Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// isInsideTag returns true if the position is inside an <a>...</a> or <code>...</code> tag.
func isInsideTag(content string, pos int) bool {
	prefix := content[:pos]

	// Check <a> tags: find last <a or </a before pos.
	lastAOpen := strings.LastIndex(strings.ToLower(prefix), "<a ")
	if lastAOpen == -1 {
		lastAOpen = strings.LastIndex(strings.ToLower(prefix), "<a>")
	}
	lastAClose := strings.LastIndex(strings.ToLower(prefix), "</a>")
	if lastAOpen != -1 && lastAOpen > lastAClose {
		return true
	}

	// Check <code> tags.
	lastCodeOpen := strings.LastIndex(strings.ToLower(prefix), "<code")
	lastCodeClose := strings.LastIndex(strings.ToLower(prefix), "</code>")
	if lastCodeOpen != -1 && lastCodeOpen > lastCodeClose {
		return true
	}

	return false
}
