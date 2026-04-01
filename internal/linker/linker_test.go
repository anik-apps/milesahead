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
