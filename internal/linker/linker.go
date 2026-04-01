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

// AutoLink scans content for card name mentions and replaces the first occurrence
// of each with a link to the card's reference page. It skips matches that are
// already inside <a> or <code> tags. Keywords are matched longest-first to avoid
// partial matches.
func AutoLink(content string, cards []db.ReferencePage) (string, []MatchedCard) {
	if len(cards) == 0 {
		return content, nil
	}

	// Build keyword -> card mapping. Each card contributes its title plus any
	// keywords stored in keywords_json.
	type cardKeyword struct {
		keyword string
		card    db.ReferencePage
	}

	var keywords []cardKeyword
	for _, card := range cards {
		keywords = append(keywords, cardKeyword{keyword: card.Title, card: card})

		// Parse additional keywords from keywords_json.
		var extraKW []string
		if card.KeywordsJSON != "" && card.KeywordsJSON != "[]" {
			json.Unmarshal([]byte(card.KeywordsJSON), &extraKW)
		}
		for _, kw := range extraKW {
			kw = strings.TrimSpace(kw)
			if kw != "" {
				keywords = append(keywords, cardKeyword{keyword: kw, card: card})
			}
		}
	}

	// Sort by keyword length descending so longer matches take priority.
	sort.Slice(keywords, func(i, j int) bool {
		return len(keywords[i].keyword) > len(keywords[j].keyword)
	})

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
