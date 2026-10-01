package web

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// The note leads the content so that every excerpt (home, feed, meta
// description, JSON-LD) would reach it if it were not removed.
const (
	legacyNote    = "Could not find a link for: EVA Air promotional image from Source 2 (LoyaltyLobby)"
	legacyContent = `<div class="editor-note">` + legacyNote + `</div><p>EVA Air is selling miles with a 40% bonus through March 26.</p>`
)

// storeLegacyArticle stores an article the way articles with editor notes were
// stored before they were split out on write: note inline in the content.
func storeLegacyArticle(t *testing.T, database *db.Database, slug, status string) int64 {
	t.Helper()
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "EVA Air 40% Miles Purchase Bonus", Slug: slug, Content: "<p>placeholder</p>",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, legacyContent, id); err != nil {
		t.Fatal(err)
	}
	if err := database.SyncArticleFTS(id); err != nil {
		t.Fatal(err)
	}
	if status != db.StatusPending {
		if err := database.UpdateArticleStatus(id, status); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func realTemplateApp(t *testing.T) (*App, *db.Database) {
	t.Helper()
	base, database := setupAdminTestApp(t)
	base.cfg.Site.PersonaName = "Scout"
	app, err := NewAppWithTemplateDir(base.cfg, database, base.articleManager, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)
	return app, database
}

// Integration, real templates: an approved article whose stored content still
// holds an editor note never shows it to the public -- not in the page source,
// meta/og description, JSON-LD, home excerpts, feed or search.
func TestEditorNotes_NeverReachPublicPages(t *testing.T) {
	app, database := realTemplateApp(t)
	storeLegacyArticle(t, database, "eva-air-legacy", db.StatusApproved)

	pages := map[string]string{
		"article": getPage(t, app, "/article/eva-air-legacy"),
		"home":    getPage(t, app, "/"),
		"feed":    getPage(t, app, "/feed.xml"),
		"search":  getPage(t, app, "/search?q="+url.QueryEscape("EVA Air")),
	}
	for name, body := range pages {
		for _, leak := range []string{"Could not find a link", "LoyaltyLobby", "editor-note"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s contains %q", name, leak)
			}
		}
	}
	// Searching for the note's words finds nothing (the page echoes the
	// query, so look for a result rather than the words).
	noteSearch := getPage(t, app, "/search?q="+url.QueryEscape("Could not find a link"))
	if strings.Contains(noteSearch, "/article/eva-air-legacy") || strings.Contains(noteSearch, "<mark>") {
		t.Errorf("searching for the note's text finds the article")
	}
	// The article itself is all there.
	for name, want := range map[string]string{
		"article": "EVA Air is selling miles with a 40% bonus",
		"feed":    "EVA Air is selling miles with a 40% bonus",
		"search":  "eva-air-legacy",
	} {
		if !strings.Contains(pages[name], want) {
			t.Errorf("%s missing %q", name, want)
		}
	}
	// With no TLDR the meta and og descriptions and JSON-LD fall back to the
	// content, starting after the note.
	if !strings.Contains(pages["article"], `name="description" content="EVA Air is selling miles`) {
		t.Errorf("meta description does not start with the article text")
	}
}

// Integration, real templates: the admin preview shows an article's editor
// notes, both ones stored apart and ones still inline in legacy content, in
// its own box rather than inside the content.
func TestEditorNotes_ShownInAdminPreview(t *testing.T) {
	app, database := realTemplateApp(t)
	storeLegacyArticle(t, database, "eva-air-legacy", db.StatusPending)
	if _, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Split Note Article", Slug: "split-note", Content: "<p>Body</p>",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		EditorNotes: "Could not find a link for: the Wyndham landing page",
	}); err != nil {
		t.Fatal(err)
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<div class="editor-note"><strong>Editor note (not published):</strong> ` + legacyNote + `</div>`,
		`<div class="editor-note"><strong>Editor note (not published):</strong> Could not find a link for: the Wyndham landing page</div>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Count(body, legacyNote) != 1 {
		t.Errorf("legacy note shown %d times, want once (in the notes box, not the content)", strings.Count(body, legacyNote))
	}
}
