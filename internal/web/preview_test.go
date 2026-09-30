package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// Integration, real templates: the dashboard's article preview shows the
// article's id and Scout's Take, and says so when enrichment has not filled
// the image or Scout's Take yet. None of the admin-only markup reaches the
// public article page.
func TestAdminPreview_ShowsIDAndScoutsTakeOnlyInAdmin(t *testing.T) {
	base, database := setupAdminTestApp(t)
	base.cfg.Site.PersonaName = "Scout"
	app, err := NewAppWithTemplateDir(base.cfg, database, base.articleManager, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)

	const take = "Worth it only if you already hold a Venture card."
	enriched, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Enriched Preview Article", Slug: "enriched-preview-article",
		Content: "<p>Body</p>", Type: "analysis", Status: db.StatusPending,
		SourcesJSON: "[]", RevisionNumber: 1,
		ImageURL: "https://images.pexels.com/photos/1/p.jpeg", ScoutsTake: take,
	})
	if err != nil {
		t.Fatalf("create enriched: %v", err)
	}
	bare := createPendingArticle(t, database, "Not Yet Enriched Article")

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<span class="preview-id">#` + strconv.FormatInt(enriched, 10) + `</span>`,
		`<span class="preview-id">#` + strconv.FormatInt(bare, 10) + `</span>`,
		`<div class="scout-label">Scout's Take</div>`,
		"<p>" + take + "</p>",
		"No image yet.",
		"No Scout's Take yet.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// The enriched article has both fields, so exactly one of each note: the
	// bare article's.
	if n := strings.Count(body, `class="preview-missing"`); n != 2 {
		t.Errorf("preview-missing notes = %d, want 2 (image and take of the bare article)", n)
	}

	// Published, the same article renders its Scout's Take through the public
	// template, with none of the admin-only markup.
	if err := database.UpdateArticleStatus(enriched, db.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	page := getPage(t, app, "/article/enriched-preview-article")
	if !strings.Contains(page, take) {
		t.Errorf("public article lost its Scout's Take")
	}
	for _, leak := range []string{"preview-id", "preview-missing", "Article Preview"} {
		if strings.Contains(page, leak) {
			t.Errorf("public article contains admin-only %q", leak)
		}
	}
}
