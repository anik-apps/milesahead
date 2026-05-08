package linker

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func makeCard(slug, title, keywordsJSON string) db.ReferencePage {
	return db.ReferencePage{
		Slug:         slug,
		Title:        title,
		KeywordsJSON: keywordsJSON,
	}
}

func TestAutoLink_BasicMatch(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "Amex Gold Card", `[]`),
	}
	content := "<p>The Amex Gold Card now offers 4x points on dining.</p>"
	result, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card, got %d", len(matched))
	}
	if matched[0].Slug != "amex-gold" {
		t.Errorf("expected slug amex-gold, got %s", matched[0].Slug)
	}
	if !strings.Contains(result, `<a href="/cards/amex-gold">Amex Gold Card</a>`) {
		t.Errorf("expected link in result, got: %s", result)
	}
}

func TestAutoLink_SkipExistingLinks(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "Amex Gold Card", `[]`),
	}
	content := `<p>Check out the <a href="/other">Amex Gold Card</a> for details.</p>`
	result, matched := AutoLink(content, cards)

	if len(matched) != 0 {
		t.Fatalf("expected 0 matched cards (inside existing link), got %d", len(matched))
	}
	if result != content {
		t.Errorf("content should be unchanged, got: %s", result)
	}
}

func TestAutoLink_SkipCodeBlocks(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "Amex Gold Card", `[]`),
	}
	content := `<p>Example: <code>Amex Gold Card</code> is referenced here.</p>`
	result, matched := AutoLink(content, cards)

	if len(matched) != 0 {
		t.Fatalf("expected 0 matched cards (inside code), got %d", len(matched))
	}
	if result != content {
		t.Errorf("content should be unchanged, got: %s", result)
	}
}

func TestAutoLink_OnlyFirstMention(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "Amex Gold Card", `[]`),
	}
	content := "<p>The Amex Gold Card is great. The Amex Gold Card has benefits.</p>"
	result, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card, got %d", len(matched))
	}
	// Count occurrences of the link tag.
	linkCount := strings.Count(result, `<a href="/cards/amex-gold">`)
	if linkCount != 1 {
		t.Errorf("expected exactly 1 link, got %d in: %s", linkCount, result)
	}
}

func TestAutoLink_LongerKeywordFirst(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("sapphire-preferred", "Chase Sapphire Preferred", `[]`),
		makeCard("sapphire-reserve", "Chase Sapphire Reserve", `[]`),
	}
	content := "<p>The Chase Sapphire Preferred beats the Chase Sapphire Reserve.</p>"
	result, matched := AutoLink(content, cards)

	if len(matched) != 2 {
		t.Fatalf("expected 2 matched cards, got %d", len(matched))
	}
	if !strings.Contains(result, `<a href="/cards/sapphire-preferred">Chase Sapphire Preferred</a>`) {
		t.Errorf("expected Preferred link, got: %s", result)
	}
	if !strings.Contains(result, `<a href="/cards/sapphire-reserve">Chase Sapphire Reserve</a>`) {
		t.Errorf("expected Reserve link, got: %s", result)
	}
}

func TestAutoLink_NoCards(t *testing.T) {
	content := "<p>Some article content.</p>"
	result, matched := AutoLink(content, nil)

	if len(matched) != 0 {
		t.Fatalf("expected 0 matched cards, got %d", len(matched))
	}
	if result != content {
		t.Errorf("content should be unchanged")
	}
}

func TestAutoLink_CaseInsensitive(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "Amex Gold Card", `[]`),
	}
	content := "<p>The amex gold card is popular.</p>"
	result, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card, got %d", len(matched))
	}
	// The link should preserve the original case from the content.
	if !strings.Contains(result, `<a href="/cards/amex-gold">amex gold card</a>`) {
		t.Errorf("expected case-preserved link, got: %s", result)
	}
}

func TestAutoLink_Keywords(t *testing.T) {
	cards := []db.ReferencePage{
		makeCard("amex-gold", "American Express Gold Card", `["Amex Gold"]`),
	}
	content := "<p>The Amex Gold is a great card.</p>"
	result, matched := AutoLink(content, cards)

	if len(matched) != 1 {
		t.Fatalf("expected 1 matched card via keyword, got %d", len(matched))
	}
	if !strings.Contains(result, `<a href="/cards/amex-gold">Amex Gold</a>`) {
		t.Errorf("expected keyword link, got: %s", result)
	}
}

func TestDetectMentions_Empty(t *testing.T) {
	got := DetectMentions("", nil)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d", len(got))
	}
}

func TestDetectMentions_BasicCount(t *testing.T) {
	cards := []db.ReferencePage{
		{Slug: "chase-sapphire-preferred", Title: "Chase Sapphire Preferred", KeywordsJSON: `["CSP","Sapphire Preferred"]`},
		{Slug: "amex-gold", Title: "American Express Gold", KeywordsJSON: `["Amex Gold"]`},
	}
	body := "<p>The Chase Sapphire Preferred is the benchmark. The Sapphire Preferred edges out competitors. " +
		"Compare to the American Express Gold for dining.</p>"

	got := DetectMentions(body, cards)
	want := map[string]int{
		"chase-sapphire-preferred": 2, // matched as "Chase Sapphire Preferred" + "Sapphire Preferred"
		"amex-gold":                1,
	}
	if len(got) != 2 {
		t.Fatalf("got %d cards, want 2: %+v", len(got), got)
	}
	for _, dc := range got {
		if want[dc.Slug] != dc.Count {
			t.Errorf("%s: got count %d, want %d", dc.Slug, dc.Count, want[dc.Slug])
		}
	}
	// Ordering is part of the contract: count desc, slug asc on ties.
	if got[0].Slug != "chase-sapphire-preferred" {
		t.Errorf("first result: got %s, want chase-sapphire-preferred", got[0].Slug)
	}
	if got[1].Slug != "amex-gold" {
		t.Errorf("second result: got %s, want amex-gold", got[1].Slug)
	}
}

func TestDetectMentions_OrderedByCountDescending(t *testing.T) {
	cards := []db.ReferencePage{
		{Slug: "a", Title: "Card A"},
		{Slug: "b", Title: "Card B"},
		{Slug: "c", Title: "Card C"},
	}
	body := "Card A. Card B Card B Card B. Card C Card C."
	got := DetectMentions(body, cards)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Slug != "b" || got[1].Slug != "c" || got[2].Slug != "a" {
		t.Errorf("expected order b,c,a; got %s,%s,%s", got[0].Slug, got[1].Slug, got[2].Slug)
	}
	if got[0].Count != 3 || got[1].Count != 2 || got[2].Count != 1 {
		t.Errorf("counts wrong: %+v", got)
	}
}

func TestDetectMentions_CaseInsensitive(t *testing.T) {
	cards := []db.ReferencePage{{Slug: "csp", Title: "Chase Sapphire Preferred"}}
	body := "the chase sapphire preferred is great. CHASE SAPPHIRE PREFERRED again."
	got := DetectMentions(body, cards)
	if len(got) != 1 || got[0].Count != 2 {
		t.Errorf("expected 1 card with 2 mentions, got %+v", got)
	}
}

func TestDetectMentions_LongerKeywordWinsFromOverlap(t *testing.T) {
	// Longer keyword "Chase Sapphire Preferred" claims one span; a separate
	// later occurrence of just "Sapphire Preferred" with no longer match
	// nearby should still be counted. Total: 2 mentions for the same card.
	cards := []db.ReferencePage{
		{Slug: "csp", Title: "Chase Sapphire Preferred", KeywordsJSON: `["Sapphire Preferred"]`},
	}
	body := "The Chase Sapphire Preferred is excellent. Later, the Sapphire Preferred shines on dining."
	got := DetectMentions(body, cards)
	if len(got) != 1 {
		t.Fatalf("expected 1 card, got %d: %+v", len(got), got)
	}
	if got[0].Count != 2 {
		t.Errorf("expected 2 mentions (1 long + 1 short non-overlapping), got %d", got[0].Count)
	}
}

// TestDetectMentions_WordBoundary ensures "Visa" doesn't match inside "Visage".
func TestDetectMentions_WordBoundary(t *testing.T) {
	cards := []db.ReferencePage{{Slug: "visa", Title: "Visa"}}
	body := "She had a serene Visage. The Visa card is great."
	got := DetectMentions(body, cards)
	if len(got) != 1 || got[0].Count != 1 {
		t.Errorf("expected 1 mention (only the standalone 'Visa'), got %+v", got)
	}
}

// TestDetectMentions_AfterAutoLink simulates content that AutoLink already processed.
func TestDetectMentions_AfterAutoLink(t *testing.T) {
	cards := []db.ReferencePage{{Slug: "csp", Title: "Chase Sapphire Preferred"}}
	body := `<p>The <a href="/cards/csp">Chase Sapphire Preferred</a> is great. ` +
		`The Chase Sapphire Preferred has perks.</p>`
	got := DetectMentions(body, cards)
	if len(got) != 1 || got[0].Count != 2 {
		t.Errorf("expected 2 mentions counting both wrapped and bare, got %+v", got)
	}
}
