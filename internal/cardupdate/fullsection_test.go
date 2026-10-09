package cardupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// sectionEditor stands in for a model that does exactly what it is asked: it
// takes the section as the prompt shows it and returns that section with one
// change applied. If edit is nil it adds one bullet at the end of the list.
type sectionEditor struct {
	maxTokens int
	edit      func(shown string) string
}

func (c *sectionEditor) Generate(_, prompt string, maxTokens int) (string, error) {
	c.maxTokens = maxTokens
	start := strings.Index(prompt, "<ul>")
	if start < 0 {
		return "NO_UPDATE", nil
	}
	shown := prompt[start:]
	if end := strings.Index(shown, "\n\n"); end >= 0 {
		shown = shown[:end]
	}
	var updated string
	if c.edit != nil {
		updated = c.edit(shown)
	} else if i := strings.LastIndex(shown, "</ul>"); i >= 0 {
		updated = shown[:i] + "<li>Monthly DoorDash credit</li></ul>"
	} else {
		updated = shown + "<li>Monthly DoorDash credit</li>"
	}
	out, _ := json.Marshal(map[string]string{"updated_content": updated, "summary": "Added the DoorDash credit."})
	return string(out), nil
}

// longBenefits is a Key Benefits list well past 1,000 bytes, like most real
// cards' (43 of 53 seed cards). Its last item is the one a cut-off prompt hides.
func longBenefits() string {
	var b strings.Builder
	b.WriteString("<ul>")
	for i := 1; i <= 14; i++ {
		fmt.Fprintf(&b, "<li><strong>Benefit %d</strong> — a perk described at the length real card pages use.</li>", i)
	}
	b.WriteString("<li><strong>Last benefit</strong> — no foreign transaction fees.</li></ul>")
	return b.String()
}

// draftFor runs detection for one card whose key_benefits section is content
// and returns the pending updates it saved.
func draftFor(t *testing.T, content string, client *sectionEditor) []db.PendingUpdate {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	sections, _ := json.Marshal([]db.ReferenceSection{{Key: "key_benefits", Title: "Key Benefits", Content: content}})
	pageID, err := database.CreateReferencePage(&db.ReferencePage{
		Slug: "sapphire-reserve", Title: "Sapphire Reserve", Category: "travel", Issuer: "Chase",
		SectionsJSON: string(sections), RecentChangesJSON: "[]", PendingUpdatesJSON: "[]",
		KeywordsJSON: `["sapphire reserve"]`, Status: "published",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Sapphire Reserve adds a monthly DoorDash credit", Slug: "csr-doordash",
		Content: "The Sapphire Reserve now includes a monthly DoorDash credit.", Type: "news",
		Status: db.StatusDraft, SourcesJSON: "[]",
	})
	if err != nil {
		t.Fatalf("create article: %v", err)
	}
	if err := NewDetector(database, client, []string{"m"}).DetectAndDraft(context.Background(), articleID); err != nil {
		t.Fatalf("DetectAndDraft: %v", err)
	}
	page, err := database.GetReferencePage(pageID)
	if err != nil {
		t.Fatalf("get page: %v", err)
	}
	var pending []db.PendingUpdate
	if err := json.Unmarshal([]byte(page.PendingUpdatesJSON), &pending); err != nil {
		t.Fatalf("pending updates: %v", err)
	}
	return pending
}

// The prompt used to show only the first 1,000 bytes of the section while
// asking for "the full updated section", and approving a draft replaces the
// whole section. So a draft that only added one bullet deleted every benefit
// past byte 1,000.
func TestDetectAndDraft_KeepsTheWholeSection(t *testing.T) {
	content := longBenefits()
	if len(content) <= 1000 {
		t.Fatalf("fixture is %d bytes; it must be longer than the old 1,000-byte cut", len(content))
	}
	client := &sectionEditor{}
	pending := draftFor(t, content, client)

	if len(pending) != 1 {
		t.Fatalf("pending updates = %d, want 1", len(pending))
	}
	got := pending[0].ProposedContent
	for _, want := range []string{"Benefit 14", "Last benefit", "Monthly DoorDash credit"} {
		if !strings.Contains(got, want) {
			t.Errorf("draft lost %q; a draft must keep everything the article does not change", want)
		}
	}
	// The whole section comes back in the reply, so the budget has to hold it.
	// At 1,024 tokens gpt-oss-20b already ran out in production
	// (finish_reason=length), and the update was lost.
	if client.maxTokens < 4096 {
		t.Errorf("maxTokens = %d; too small to return a whole section", client.maxTokens)
	}
}

// A draft that drops existing benefits in bulk is not saved: an admin would
// have to spot every deletion in the diff, and approving it would remove them.
func TestDetectAndDraft_SkipsADraftThatDropsBenefits(t *testing.T) {
	client := &sectionEditor{edit: func(shown string) string {
		items := strings.SplitAfter(shown, "</li>")
		return strings.Join(items[:3], "") + "<li>Monthly DoorDash credit</li></ul>"
	}}
	if pending := draftFor(t, longBenefits(), client); len(pending) != 0 {
		t.Errorf("saved a draft that drops 12 of 15 benefits: %+v", pending)
	}
}

// Ending one benefit is a real update (a credit is discontinued), so a draft
// that removes a single item is still offered for review.
func TestDetectAndDraft_AllowsRemovingOneEndedBenefit(t *testing.T) {
	client := &sectionEditor{edit: func(shown string) string {
		return strings.Replace(shown, "<li><strong>Benefit 3</strong> — a perk described at the length real card pages use.</li>", "", 1)
	}}
	pending := draftFor(t, longBenefits(), client)
	if len(pending) != 1 {
		t.Fatalf("pending updates = %d, want 1: removing one ended benefit is a valid draft", len(pending))
	}
	if strings.Contains(pending[0].ProposedContent, "Benefit 3<") {
		t.Errorf("draft should not contain the removed benefit")
	}
}
