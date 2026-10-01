package newsletter

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
)

// An approved article whose stored content still holds an editor note (as
// articles revised in March 2026 did) sends the newsletter none of it, in the
// lead excerpt or a sidebar summary. The note leads the content so both would
// reach it.
func TestNewsletter_OmitsEditorNotes(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()

	const note = `<div class="editor-note">Could not find a link for: exact launch date</div>`
	for i, slug := range []string{"lead-legacy", "sidebar-legacy"} {
		id := createApprovedArticle(t, database, "Legacy "+slug, slug, "<p>placeholder</p>", i == 0)
		if _, err := database.DB().Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`,
			note+"<p>Amex adds a ChatGPT credit to "+slug+".</p>", id); err != nil {
			t.Fatal(err)
		}
	}

	svc := NewService(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC", SendTime: "08:00"})
	nl, err := svc.GenerateDaily()
	if err != nil || nl == nil {
		t.Fatalf("GenerateDaily: %v %v", nl, err)
	}
	for _, leak := range []string{"Could not find a link", "editor-note"} {
		if strings.Contains(nl.HTMLContent, leak) {
			t.Errorf("newsletter contains %q", leak)
		}
	}
	for _, want := range []string{"Amex adds a ChatGPT credit to lead-legacy", "Amex adds a ChatGPT credit to sidebar-legacy"} {
		if !strings.Contains(nl.HTMLContent, want) {
			t.Errorf("newsletter missing %q", want)
		}
	}
}
