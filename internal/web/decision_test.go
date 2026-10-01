package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestApproveArticle_RequiresRevision(t *testing.T) {
	app, database := setupAdminTestApp(t)
	id := createPendingArticle(t, database, "No Revision Posted")
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	for _, action := range []string{"approve", "reject"} {
		rec := doAdminPOST(t, app, "/admin/articles/"+strconv.FormatInt(id, 10)+"/"+action, session, csrf, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s without revision: status %d, want 400", action, rec.Code)
		}
	}
	if a, _ := database.GetGeneratedArticle(id); a.Status != db.StatusPending {
		t.Errorf("status = %q, want pending", a.Status)
	}
}

// The admin loaded revision 1; a rewrite landed (revision 2) before the
// click. Approve and reject must refuse and say so on the dashboard.
func TestApproveArticle_StaleRevisionIsRefused(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Rewritten Under You")
	if err := database.UpdateArticleContent(id, "Rewritten Under You", "<p>v2</p>", 2); err != nil {
		t.Fatal(err)
	}
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	path := "/admin/articles/" + strconv.FormatInt(id, 10)

	for _, action := range []string{"/approve", "/reject"} {
		rec := doAdminPOST(t, app, path+action, session, csrf, url.Values{"revision": {"1"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: status %d, want 303", action, rec.Code)
		}
		loc := rec.Header().Get("Location")
		if want := "msg=changed&id=" + strconv.FormatInt(id, 10) + "&rev=2"; !strings.Contains(loc, want) {
			t.Fatalf("%s: Location %q, want it to contain %q", action, loc, want)
		}
		page := doAdminGET(t, app, loc, session).Body.String()
		if !strings.Contains(page, "changed after you opened it") || !strings.Contains(page, "revision 2") {
			t.Errorf("%s: dashboard does not explain the refusal", action)
		}
	}
	if a, _ := database.GetGeneratedArticle(id); a.Status != db.StatusPending {
		t.Fatalf("status = %q, want pending", a.Status)
	}

	// Approving the revision actually shown goes through.
	rec := doAdminPOST(t, app, path+"/approve", session, csrf, url.Values{"revision": {"2"}})
	if rec.Code != http.StatusSeeOther || strings.Contains(rec.Header().Get("Location"), "changed") {
		t.Fatalf("approve rev 2: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if a, _ := database.GetGeneratedArticle(id); a.Status != db.StatusApproved {
		t.Fatalf("status = %q, want approved", a.Status)
	}
}

// The dashboard's decision forms carry the revision being shown.
func TestDashboard_DecisionFormsPostTheRevisionShown(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Shown At Two")
	if err := database.UpdateArticleContent(id, "Shown At Two", "<p>v2</p>", 2); err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if !strings.Contains(body, `name="revision" value="2"`) {
		t.Error("dashboard forms do not post revision 2")
	}
	if strings.Contains(body, `name="revision" value="1"`) {
		t.Error("dashboard posts a stale revision")
	}
}

// Published has no "Delete": it posted to reject, which refuses published
// articles. Unpublish is the way to take one down.
func TestPublished_HasNoBrokenDelete(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Live One")
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/published", session).Body.String()
	if strings.Contains(body, "/reject") || strings.Contains(body, ">Delete<") {
		t.Error("published page still offers a delete that cannot work")
	}
	if !strings.Contains(body, "/unpublish") {
		t.Error("published page lost Unpublish")
	}
}
