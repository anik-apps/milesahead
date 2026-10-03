package web

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
)

// rowMenu is the actions menu of article id on a page, or "".
func rowMenu(body string, id int64) string {
	start := strings.Index(body, `<div class="row-menu-list" id="menu-`+itoa(id)+`"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</div>")
	return body[start : start+end]
}

// The menu offers only what the server allows for the article's status.
func TestArticleRow_ActionsFollowTheServerRules(t *testing.T) {
	for status, want := range map[string]bool{
		db.StatusPending: true, db.StatusDraft: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true,
		db.StatusApproved: false, db.StatusRejected: false,
	} {
		row := adminArticleRow{Status: status}
		if row.CanDecide() != want || row.CanDecide() != article.Decidable(status) {
			t.Errorf("%s: CanDecide %v, want %v", status, row.CanDecide(), want)
		}
		if row.CanRequestRevision() != want {
			t.Errorf("%s: CanRequestRevision %v, want %v", status, row.CanRequestRevision(), want)
		}
	}
}

// Integration, real templates: each queue row has an actions menu that is
// an accessible menu button, with the decisions its state allows.
func TestDashboard_RowMenus(t *testing.T) {
	app, database := realTemplateApp(t)
	ready := createPendingArticle(t, database, "Menu Ready")
	if err := database.UpdateArticleContent(ready, "Menu Ready", "<p>v3</p>", 3); err != nil {
		t.Fatal(err)
	}
	down := createPendingArticle(t, database, "Menu Taken Down")
	database.UpdateArticleStatus(down, db.StatusApproved)
	database.UpdateArticleStatus(down, db.StatusUnpublished)
	rewriting := createPendingArticle(t, database, "Menu Rewriting")
	if err := app.articleManager.RequestRevision(rewriting, "shorter", "admin"); err != nil {
		t.Fatal(err)
	}
	gaveUp := createPendingArticle(t, database, "Menu Gave Up")
	if err := app.articleManager.RequestRevision(gaveUp, "longer", "admin"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < article.DefaultRevisionMaxFailures; i++ {
		database.RecordRevisionFailure(gaveUp, "503", func(int) time.Time { return time.Now().Add(time.Hour) })
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()

	// The button: a real button outside the row link, naming its menu.
	btn := `<button type="button" class="row-menu-btn" id="menu-btn-` + itoa(ready) + `" aria-haspopup="menu" aria-expanded="false" aria-controls="menu-` + itoa(ready) + `"`
	if !strings.Contains(body, btn) {
		t.Fatal("ready row has no menu button with aria-haspopup, aria-expanded and aria-controls")
	}
	if strings.Contains(body, `<a class="q-row`) && strings.Contains(between(body, `<a class="q-row`, `</a>`), "row-menu") {
		t.Error("the menu is inside the row link; a button cannot be nested in a link")
	}

	m := rowMenu(body, ready)
	for _, want := range []string{
		`role="menu" aria-labelledby="menu-btn-` + itoa(ready) + `" hidden`,
		`href="#preview-` + itoa(ready) + `" data-preview="preview-` + itoa(ready) + `">Open preview</a>`,
		`action="/admin/articles/` + itoa(ready) + `/approve"`,
		// The row's revision, so the server's revision guard applies.
		`<input type="hidden" name="revision" value="3">`,
		`role="menuitem" tabindex="-1" class="row-menu-approve">Approve rev 3</button>`,
		`data-focus="rewrite">Request rewrite&hellip;</a>`,
		// No image yet: the menu offers to find one.
		`data-focus="new-image">Find an image&hellip;</a>`,
		// Reject still asks first: it cannot be undone.
		`action="/admin/articles/` + itoa(ready) + `/reject" class="row-menu-form" data-confirm="Reject article #` + itoa(ready) + `? This cannot be undone."`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("ready row menu missing %q", want)
		}
	}
	// Deciding from the list stays on the list: no next article to open.
	if strings.Contains(m, `name="next"`) {
		t.Error("menu decisions should not open the next article")
	}
	if n := strings.Count(m, `role="menuitem"`); n != 5 {
		t.Errorf("ready menu has %d items, want 5", n)
	}

	if m := rowMenu(body, down); !strings.Contains(m, ">Republish rev 1</button>") {
		t.Error("an unpublished article's menu should offer Republish")
	}
	// A rewrite pending: the server still lets the admin decide on what is
	// shown, as the preview does.
	if m := rowMenu(body, rewriting); !strings.Contains(m, ">Approve rev 1</button>") || !strings.Contains(m, "/reject") {
		t.Error("the rewriting row's menu lost approve or reject")
	}
	if m := rowMenu(body, gaveUp); !strings.Contains(m, "Request the rewrite again&hellip;") {
		t.Error("a rewrite that gave up should offer to request it again")
	}
}

// between is the text of s from the first open to the following close.
func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], close)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j]
}

// Approving from a row's menu publishes, shows the Undo toast and leaves the
// admin on the list, in the same order; a stale revision is refused with
// the dashboard's usual explanation.
func TestDashboard_ApproveFromTheList(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Approved From The List")
	stale := createPendingArticle(t, database, "Rewritten Before The Click")
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	// What the menu's form posts: the revision and the sort, no next.
	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/approve", session, csrf, url.Values{"revision": {"1"}, "sort": {"oldest"}})
	loc := rec.Header().Get("Location")
	if want := "/admin/dashboard?msg=approved&id=" + itoa(id) + "&open=0&sort=oldest"; loc != want {
		t.Fatalf("Location %q, want %q", loc, want)
	}
	page := doAdminGET(t, app, loc, session).Body.String()
	if !strings.Contains(page, `data-open=""`) {
		t.Error("approving from the list should not open a preview")
	}
	if !strings.Contains(page, `action="/admin/articles/`+itoa(id)+`/undo-approve"`) || !strings.Contains(page, "Approved &ldquo;Approved From The List&rdquo; and published") {
		t.Error("no undo toast after approving from the list")
	}
	if !strings.Contains(page, `<option value="oldest" selected>`) {
		t.Error("the list came back in a different order")
	}
	if a, _ := database.GetGeneratedArticle(id); a.Status != db.StatusApproved {
		t.Errorf("status %q, want approved", a.Status)
	}

	// The list was loaded at revision 1; a rewrite landed since.
	if err := database.UpdateArticleContent(stale, "Rewritten Before The Click", "<p>v2</p>", 2); err != nil {
		t.Fatal(err)
	}
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(stale)+"/approve", session, csrf, url.Values{"revision": {"1"}})
	page = doAdminGET(t, app, rec.Header().Get("Location"), session).Body.String()
	if !strings.Contains(page, "changed after you opened it: it is now revision 2. Nothing was approved or rejected.") {
		t.Error("a stale approve from the list is not explained")
	}
	if a, _ := database.GetGeneratedArticle(stale); a.Status != db.StatusPending {
		t.Errorf("stale article status %q, want pending", a.Status)
	}
}

// The type label is a compact pill, not the public site's big badge.
func TestAdminLists_TypeIsACompactPill(t *testing.T) {
	app, database := realTemplateApp(t)
	createPendingArticle(t, database, "Pill In Queue")
	live := createPendingArticle(t, database, "Pill In Published")
	database.UpdateArticleStatus(live, db.StatusApproved)
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))

	for _, path := range []string{"/admin/dashboard", "/admin/published"} {
		body := doAdminGET(t, app, path, session).Body.String()
		if !strings.Contains(body, `<span class="type-pill type-pill-analysis q-type">analysis</span>`) {
			t.Errorf("%s: rows have no type pill", path)
		}
		if strings.Contains(body, `class="badge badge-`) {
			t.Errorf("%s: rows still use the large badge", path)
		}
	}
	css, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "admin.css"))
	if err != nil {
		t.Fatal(err)
	}
	if rule := cssRule(t, string(css), ".type-pill"); !strings.Contains(rule, "font-size: 0.66rem") {
		t.Errorf(".type-pill should be small:%s", rule)
	}
}

// Published: newest first by default, oldest first on request, and each row
// has an actions menu with Unpublish and Unpublish with a rewrite request.
func TestPublished_SortAndRowMenu(t *testing.T) {
	app, database := realTemplateApp(t)
	older := createPendingArticle(t, database, "Published Long Ago")
	newer := createPendingArticle(t, database, "Published Just Now")
	for id, ago := range map[int64]string{older: "-3 days", newer: "-1 hours"} {
		database.UpdateArticleStatus(id, db.StatusApproved)
		if _, err := database.DB().Exec(`UPDATE generated_articles SET published_at = datetime('now', ?) WHERE id = ?`, ago, id); err != nil {
			t.Fatal(err)
		}
	}
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	first := func(body string) int64 {
		if strings.Index(body, "Published Just Now") < strings.Index(body, "Published Long Ago") {
			return newer
		}
		return older
	}
	body := doAdminGET(t, app, "/admin/published", session).Body.String()
	if first(body) != newer || !strings.Contains(body, `<option value="newest" selected>Newest first</option>`) {
		t.Error("published is not newest first by default")
	}
	if strings.Contains(body, `value="score"`) {
		t.Error("published offers a score sort it cannot do")
	}
	if body := doAdminGET(t, app, "/admin/published?sort=oldest", session).Body.String(); first(body) != older {
		t.Error("sort=oldest does not put the earliest published first")
	}

	m := rowMenu(body, newer)
	for _, want := range []string{
		`role="menu"`,
		`action="/admin/articles/` + itoa(newer) + `/unpublish" class="row-menu-form"`,
		`class="row-menu-danger">Unpublish</button>`,
		`data-reveal="unpub-panel-` + itoa(newer) + `">Unpublish and request a rewrite&hellip;</button>`,
		`href="/admin/articles/` + itoa(newer) + `/cards">Linked cards</a>`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("published menu missing %q", want)
		}
	}
	if !strings.Contains(body, `class="p-unpublish-panel" id="unpub-panel-`+itoa(newer)+`" hidden>`) {
		t.Error("no hidden unpublish-with-rewrite panel for the menu to reveal")
	}

	// Unpublishing from the menu keeps the order, and offers the undo.
	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(newer)+"/unpublish", session, csrf, url.Values{"sort": {"oldest"}})
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "msg=unpublished&undo=1") || !strings.HasSuffix(loc, "&sort=oldest") {
		t.Fatalf("Location %q", loc)
	}
	page := doAdminGET(t, app, loc, session).Body.String()
	toast := between(page, `<div class="toast"`, `</form>`)
	if !strings.Contains(toast, `name="return" value="published"`) || !strings.Contains(toast, `<input type="hidden" name="sort" value="oldest">`) {
		t.Error("the undo toast is missing or would lose the order")
	}
}
