package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

const (
	oldImage = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg"
	oldThumb = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/500px-Lobby.jpg"
)

// withImage gives article id an image and a used-up enrichment budget.
func withImage(t *testing.T, database *db.Database, id int64) {
	t.Helper()
	if _, err := database.DB().Exec(`UPDATE generated_articles SET image_url = ?, image_thumbnail = ?,
		image_attribution = 'Photo by A', scouts_take = 'Take', enrichment_attempts = 3 WHERE id = ?`,
		oldImage, oldThumb, id); err != nil {
		t.Fatal(err)
	}
}

func imageOf(t *testing.T, database *db.Database, id int64) (img, thumb string, attempts int) {
	t.Helper()
	if err := database.DB().QueryRow(`SELECT image_url, image_thumbnail, enrichment_attempts FROM generated_articles WHERE id = ?`, id).
		Scan(&img, &thumb, &attempts); err != nil {
		t.Fatal(err)
	}
	return img, thumb, attempts
}

func imageRequest(t *testing.T, database *db.Database, id int64) db.ImageRequest {
	t.Helper()
	reqs, err := database.ImageRequestsFor([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	return reqs[id]
}

// The action changes what is on the site, so it takes a session and the
// CSRF token like every other admin form.
func TestRequestNewImage_RequiresAuthAndCSRF(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Guarded Image")
	withImage(t, database, id)
	path := "/admin/articles/" + itoa(id) + "/new-image"

	// No session: off to the login page.
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("hint=x&csrf_token=t"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "t"})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login") {
		t.Errorf("without a session: %d %q, want 303 to /admin/login", rec.Code, rec.Header().Get("Location"))
	}

	// A session but no CSRF token.
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader("hint=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session", Value: session})
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("without a CSRF token: %d, want 403", rec.Code)
	}

	if r := imageRequest(t, database, id); r.State != "" {
		t.Errorf("a refused request was stored: %+v", r)
	}
	if got, _ := database.RejectedImageURLs(id); len(got) != 0 {
		t.Errorf("a refused request turned down images: %v", got)
	}
}

// A published article keeps its live image until the reconciler has found
// another; the request, the admin's words and the turned-down image are
// stored, and the enrichment budget starts again.
func TestRequestNewImage_PublishedKeepsItsImageUntilReplaced(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Live Hotel Story")
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	withImage(t, database, id)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/new-image", session, csrf, url.Values{
		"hint": {"  Hyatt   Regency\nlobby "}, "return": {"published"}, "sort": {"oldest"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if want := "/admin/published?msg=image_requested&id=" + itoa(id) + "&sent=image-" + itoa(id) + "&sort=oldest"; rec.Header().Get("Location") != want {
		t.Errorf("Location %q, want %q", rec.Header().Get("Location"), want)
	}

	img, thumb, attempts := imageOf(t, database, id)
	if img != oldImage || thumb != oldThumb {
		t.Errorf("the live image changed before a new one was found: %q / %q", img, thumb)
	}
	if attempts != 0 {
		t.Errorf("enrichment_attempts = %d, want a fresh budget (0)", attempts)
	}
	r := imageRequest(t, database, id)
	if !r.Pending() || r.Hint != "Hyatt Regency lobby" || r.Seq != 1 {
		t.Errorf("request = %+v, want pending, hint tidied to one line", r)
	}
	got, err := database.RejectedImageURLs(id)
	if err != nil || len(got) != 2 {
		t.Fatalf("turned down %v (%v), want the image and its thumbnail", got, err)
	}

	// The reconciler picks it up first.
	list, err := database.ListArticlesNeedingEnrichment(3, 10)
	if err != nil || len(list) != 1 || list[0].ID != id || !list[0].MissingImage ||
		list[0].ImageRequestSeq != 1 || list[0].ImageHint != "Hyatt Regency lobby" {
		t.Fatalf("enrichment list = %+v (%v)", list, err)
	}

	page := doAdminGET(t, app, "/admin/published?msg=image_requested&id="+itoa(id), session).Body.String()
	if !strings.Contains(page, imageRequestedMessage) {
		t.Error("published page does not confirm the request")
	}
	if !strings.Contains(page, `>Finding a new image&hellip;</span>`) || !strings.Contains(page, "Searching for &ldquo;Hyatt Regency lobby&rdquo;") {
		t.Error("published row does not show the pending request")
	}
}

// Every status but rejected can ask; an article still in review goes back to
// its preview, and keeps its image too.
func TestRequestNewImage_FromThePreview(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Queue Hotel Story")
	withImage(t, database, id)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/new-image", session, csrf, url.Values{"hint": {""}})
	if want := "/admin/dashboard?msg=image_requested&id=" + itoa(id) + "&open=" + itoa(id) + "&sent=image-" + itoa(id); rec.Header().Get("Location") != want {
		t.Errorf("Location %q, want %q", rec.Header().Get("Location"), want)
	}
	if img, _, _ := imageOf(t, database, id); img != oldImage {
		t.Errorf("image = %q, want it kept until a replacement lands", img)
	}
	if r := imageRequest(t, database, id); !r.Pending() || r.Hint != "" {
		t.Errorf("request = %+v, want pending with no words", r)
	}

	// Asking again replaces the request: new words, the next seq.
	doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/new-image", session, csrf, url.Values{"hint": {"pool"}})
	if r := imageRequest(t, database, id); !r.Pending() || r.Hint != "pool" || r.Seq != 2 {
		t.Errorf("second request = %+v, want pending, hint pool, seq 2", r)
	}

	rejected := createPendingArticle(t, database, "Thrown Out")
	database.UpdateArticleStatus(rejected, db.StatusRejected)
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(rejected)+"/new-image", session, csrf, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("rejected article: %d, want 400", rec.Code)
	}
	rec = doAdminPOST(t, app, "/admin/articles/999999/new-image", session, csrf, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing article: %d, want 400", rec.Code)
	}
}

// Integration, real templates: the preview offers the action under the
// photo, the row menu goes straight to it, and a request's state shows on
// the row and in the preview -- pending, then failed.
func TestDashboard_FindANewImage(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Pictured Story")
	withImage(t, database, id)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	for _, want := range []string{
		`<form method="POST" action="/admin/articles/` + itoa(id) + `/new-image" class="image-form">`,
		`<input type="text" id="image-hint-` + itoa(id) + `" name="hint" maxlength="100"`,
		`>Find a new image</button>`,
		// The action bar points at it, and the rewrite box warns.
		`data-jump-image="image-hint-` + itoa(id) + `"`,
		`data-image-hint="rewrite-image-hint-` + itoa(id) + `"`,
		`id="rewrite-image-hint-` + itoa(id) + `" role="status" hidden>Rewrites only change the text &mdash; use`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview missing %q", want)
		}
	}
	if m := rowMenu(body, id); !strings.Contains(m, `data-focus="new-image">Find a new image&hellip;</a>`) {
		t.Error("row menu does not offer Find a new image")
	}
	if strings.Contains(body, "Finding a new image") || strings.Contains(body, "No new image found") {
		t.Error("a request state shows with no request")
	}

	doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/new-image", session, csrf, url.Values{"hint": {"Hyatt Regency lobby"}})
	body = doAdminGET(t, app, "/admin/dashboard?msg=image_requested&id="+itoa(id)+"&open="+itoa(id), session).Body.String()
	if !strings.Contains(body, imageRequestedMessage) {
		t.Error("no confirmation banner")
	}
	if !strings.Contains(body, `title="Searching for &ldquo;Hyatt Regency lobby&rdquo;">Finding a new image&hellip;</span>`) {
		t.Error("queue row has no pending chip")
	}
	if !strings.Contains(body, "This image stays until a new one is found") {
		t.Error("preview does not say the current image stays")
	}

	r := imageRequest(t, database, id)
	if err := database.FailImageRequest(id, r.Seq, "none of 4 images was relevant enough (best scored 4 of 10)"); err != nil {
		t.Fatal(err)
	}
	body = doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if !strings.Contains(body, `>No new image found &mdash; try other search words</span>`) {
		t.Error("queue row has no failure chip")
	}
	if !strings.Contains(body, "Try other search words; the image above stays.") ||
		!strings.Contains(body, "best scored 4 of 10") {
		t.Error("preview does not explain the failure")
	}
	if strings.Contains(body, "Finding a new image") {
		t.Error("a failed request still shows as pending")
	}
}

// Integration, real templates: Published rows reveal a small form, since
// they have no preview.
func TestPublished_FindANewImage(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Published Pictured Story")
	database.UpdateArticleStatus(id, db.StatusApproved)
	withImage(t, database, id)
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))

	body := doAdminGET(t, app, "/admin/published", session).Body.String()
	if m := rowMenu(body, id); !strings.Contains(m, `data-reveal="image-panel-`+itoa(id)+`">Find a new image&hellip;</button>`) {
		t.Error("published menu does not offer Find a new image")
	}
	panel := between(body, `id="image-panel-`+itoa(id)+`"`, `</form>`)
	for _, want := range []string{
		`hidden>`,
		`<input type="hidden" name="return" value="published">`,
		`name="hint"`,
		"keeps its current image until a new one is found",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("image panel missing %q", want)
		}
	}
	if !strings.Contains(body, `action="/admin/articles/`+itoa(id)+`/new-image" class="p-unpublish-panel p-image-panel"`) {
		t.Error("image panel does not post to new-image")
	}
	if !strings.Contains(body, `data-image-hint="unpub-image-hint-`+itoa(id)+`"`) {
		t.Error("the unpublish-and-rewrite box has no image hint")
	}
}
