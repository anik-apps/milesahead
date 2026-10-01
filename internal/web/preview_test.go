package web

import (
	"context"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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
		`<h2 class="scout-label" id="scouts-take-` + strconv.FormatInt(enriched, 10) + `">Scout's Take</h2>`,
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

// Integration, real templates: the admin preview shows the article in the
// public page's order -- headline and summary, photo, story with key details,
// Scout's Take after the story.
func TestAdminPreview_ArticleOrderMatchesPublicPage(t *testing.T) {
	base, database := setupAdminTestApp(t)
	base.cfg.Site.PersonaName = "Scout"
	app, err := NewAppWithTemplateDir(base.cfg, database, base.articleManager, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)

	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Preview Order Article", Slug: "preview-order-article",
		Content: "<p>Preview body text.</p>", Type: "deal", Status: db.StatusPending,
		SourcesJSON: "[]", RevisionNumber: 1,
		TLDR:       "Preview summary sentence.",
		KeyDetails: `[{"Label":"Bonus","Value":"25%"}]`,
		ImageURL:   "https://images.pexels.com/photos/1/p.jpeg", ScoutsTake: "Preview take.",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	modal := regexp.MustCompile(`(?s)<div id="preview-` + strconv.FormatInt(id, 10) + `" class="preview-overlay".*?<div class="preview-actionbar">`).FindString(rec.Body.String())
	inOrder(t, modal,
		`<h1>Preview Order Article</h1>`,
		`<p class="article-standfirst">Preview summary sentence.</p>`,
		`<details class="article-facts">`,
		`<figure class="article-hero">`,
		`<img src="https://images.pexels.com/photos/1/p.jpeg" alt="" width="1100" height="280">`,
		`<aside class="article-sidebar"`,
		`<div class="article-content">`,
		`Preview body text.`,
		`<section class="scouts-take"`,
		`Preview take.`,
	)
}

// Integration, real templates: an article's sources are listed in the admin
// preview's admin-only area, and nowhere on the published page.
func TestAdminPreview_ListsSourcesOnlyInAdmin(t *testing.T) {
	base, database := setupAdminTestApp(t)
	app, err := NewAppWithTemplateDir(base.cfg, database, base.articleManager, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)

	id := createPendingArticle(t, database, "Sourced Preview Article")
	src, err := database.CreateSource(&db.Source{Name: "Doctor of Credit", URL: "https://www.doctorofcredit.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := database.CreateRawArticle(&db.RawArticle{SourceID: src, Title: "Citi bonus is back",
		URL: "https://www.doctorofcredit.com/citi-bonus/", Content: "c", PublishedAt: time.Now(), Hash: "sourced-preview"})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.LinkArticleSources(id, []int64{raw}); err != nil {
		t.Fatal(err)
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	admin := regexp.MustCompile(`(?s)<div class="preview-admin-info">.*?<div class="admin-sources-section">.*?</ul>`).FindString(rec.Body.String())
	if !strings.Contains(admin, `<li><strong>Doctor of Credit</strong>: <a href="https://www.doctorofcredit.com/citi-bonus/" target="_blank" rel="noopener">Citi bonus is back</a></li>`) {
		t.Errorf("the admin preview should list the article's sources:\n%s", admin)
	}

	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	var slug string
	if err := database.DB().QueryRow(`SELECT slug FROM generated_articles WHERE id = ?`, id).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	page := getPage(t, app, "/article/"+slug)
	for _, leak := range []string{"Doctor of Credit", "doctorofcredit.com", "Citi bonus is back"} {
		if strings.Contains(page, leak) {
			t.Errorf("the published page names its source: %q", leak)
		}
	}
}
