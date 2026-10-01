package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
)

// Integration, real templates: the queue is grouped by what the admin can do,
// with counts, and the previews step through it in page order.
func TestDashboard_GroupsTheQueue(t *testing.T) {
	app, database := realTemplateApp(t)
	ready := createPendingArticle(t, database, "Plain Pending")
	draft := createPendingArticle(t, database, "Weak Draft")
	if _, err := database.DB().Exec(`UPDATE generated_articles SET status = 'draft' WHERE id = ?`, draft); err != nil {
		t.Fatal(err)
	}
	down := createPendingArticle(t, database, "Taken Down")
	database.UpdateArticleStatus(down, db.StatusApproved)
	database.UpdateArticleStatus(down, db.StatusUnpublished)
	rewriting := createPendingArticle(t, database, "Being Rewritten Now")
	if err := app.articleManager.RequestRevision(rewriting, "shorter", "admin"); err != nil {
		t.Fatal(err)
	}
	gaveUp := createPendingArticle(t, database, "Gave Up Rewrite")
	if err := app.articleManager.RequestRevision(gaveUp, "longer", "admin"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < article.DefaultRevisionMaxFailures; i++ {
		database.RecordRevisionFailure(gaveUp, "503", func(int) time.Time { return time.Now().Add(time.Hour) })
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()

	want := map[string][]string{
		"q-decide":    {"Taken Down", "Gave Up Rewrite"},
		"q-ready":     {"Plain Pending", "Weak Draft"},
		"q-rewriting": {"Being Rewritten Now"},
	}
	for key, titles := range want {
		sec := section(body, key)
		for _, title := range titles {
			if !strings.Contains(sec, title) {
				t.Errorf("%s is missing %q", key, title)
			}
		}
		if n := strings.Count(sec, `class="q-row`); n != len(titles) {
			t.Errorf("%s has %d rows, want %d", key, n, len(titles))
		}
	}
	if !strings.Contains(section(body, "q-decide"), `<span class="tab-n alert">2</span>`) {
		t.Error("Needs a decision should show its count in red")
	}
	// Page order: decide (oldest updated first), then ready (oldest first).
	// The preview of the first ready article points back at the last
	// decide row and on to the next ready one.
	if !strings.Contains(body, `id="preview-`+itoa(ready)+`" class="preview-overlay" data-id="`+itoa(ready)+`" data-next="preview-`+itoa(draft)+`"`) {
		t.Error("the first ready article's preview does not lead to the next one")
	}
	// Each decision posts the revision shown and the article to open next.
	if !strings.Contains(body, `<input type="hidden" name="next" value="`+itoa(draft)+`">`) {
		t.Error("approve does not carry the next article")
	}
	if !strings.Contains(body, "Approve rev 1 &amp; next") {
		t.Error(`no "Approve rev N & next" button`)
	}
}

// The ready group is capped, and says so instead of dropping articles.
func TestDashboard_SaysWhenTheQueueIsCapped(t *testing.T) {
	app, database := realTemplateApp(t)
	for i := 0; i < readyLimit+2; i++ {
		createPendingArticle(t, database, "Queued Article "+strconv.Itoa(i))
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if !strings.Contains(body, "Showing 25 of 27, oldest first.") || !strings.Contains(body, `href="/admin/dashboard?all=1"`) {
		t.Error("capped queue does not say how many are hidden")
	}
	if n := strings.Count(section(body, "q-ready"), `class="q-row`); n != readyLimit {
		t.Errorf("ready rows = %d, want %d", n, readyLimit)
	}
	all := doAdminGET(t, app, "/admin/dashboard?all=1", session).Body.String()
	if n := strings.Count(section(all, "q-ready"), `class="q-row`); n != readyLimit+2 {
		t.Errorf("?all=1 shows %d rows, want %d", n, readyLimit+2)
	}
}

// Approve-and-next opens the next article and offers an Undo that puts the
// approved one back where it was.
func TestDashboard_ApproveNextAndUndo(t *testing.T) {
	app, database := realTemplateApp(t)
	first := createPendingArticle(t, database, "Approve Then Undo")
	second := createPendingArticle(t, database, "Opened Next")
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(first)+"/approve", session, csrf,
		url.Values{"revision": {"1"}, "next": {itoa(second)}})
	loc := rec.Header().Get("Location")
	if want := "/admin/dashboard?msg=approved&id=" + itoa(first) + "&open=" + itoa(second); loc != want {
		t.Fatalf("Location %q, want %q", loc, want)
	}
	page := doAdminGET(t, app, loc, session).Body.String()
	for _, want := range []string{
		`data-open="preview-` + itoa(second) + `"`,
		`action="/admin/articles/` + itoa(first) + `/undo-approve"`,
		"Approved &ldquo;Approve Then Undo&rdquo; and published",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("after approve, dashboard missing %q", want)
		}
	}
	if strings.Contains(page, `name="prev"`) {
		t.Error("the undo form should not carry the previous status; the server records it")
	}

	// A tampered prev is ignored: the article goes back to what the server
	// recorded (pending), not to what the form says.
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(first)+"/undo-approve", session, csrf, url.Values{"prev": {"draft"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=undone") {
		t.Fatalf("undo: Location %q", loc)
	}
	if a, _ := database.GetGeneratedArticle(first); a.Status != db.StatusPending || a.PublishedAt != nil {
		t.Errorf("after undo: status %q published %v, want pending and unpublished", a.Status, a.PublishedAt)
	}
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(first)+"/undo-approve", session, csrf, nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "msg=undo_expired") {
		t.Errorf("second undo: Location %q, want undo_expired", loc)
	}
	// An article approved by other means, with no record left, offers no Undo.
	if page := doAdminGET(t, app, "/admin/dashboard?msg=approved&id="+itoa(second), session).Body.String(); strings.Contains(page, "/undo-approve") {
		t.Error("Undo offered for an approval the server has no record of")
	}
}

// The page polls the queue's fingerprint; it changes when the queue does.
func TestDashboard_StateChangesWithTheQueue(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Watched")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	read := func() string {
		rec := doAdminGET(t, app, "/admin/dashboard/state", session)
		var s struct{ Version string }
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || s.Version == "" {
			t.Fatalf("state: %d %q %v", rec.Code, rec.Body.String(), err)
		}
		return s.Version
	}
	before := read()
	if !strings.Contains(doAdminGET(t, app, "/admin/dashboard", session).Body.String(), `data-state="`+before+`"`) {
		t.Error("dashboard is not rendered with the current state")
	}
	if read() != before {
		t.Error("state changed with nothing changing")
	}
	if err := database.UpdateArticleContent(id, "Watched", "<p>v2</p>", 2); err != nil {
		t.Fatal(err)
	}
	if read() == before {
		t.Error("state did not change when a rewrite landed")
	}
}

// Published: a plain Unpublish offers an Undo that republishes the same
// revision; unpublishing with a rewrite request does not.
func TestPublished_UnpublishUndo(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Live Then Down")
	database.UpdateArticleStatus(id, db.StatusApproved)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	live, _ := database.GetGeneratedArticle(id)
	if !strings.Contains(doAdminGET(t, app, "/admin/published", session).Body.String(), `href="/article/`+live.Slug+`"`) {
		t.Error("published titles should link to the live article")
	}

	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/unpublish", session, csrf, nil)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "msg=unpublished&undo=1") {
		t.Fatalf("Location %q", loc)
	}
	page := doAdminGET(t, app, loc, session).Body.String()
	if !strings.Contains(page, `<input type="hidden" name="return" value="published">`) || !strings.Contains(page, `name="revision" value="1"`) {
		t.Fatal("published page offers no undo")
	}
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/approve", session, csrf, url.Values{"revision": {"1"}, "return": {"published"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/published?msg=republished") {
		t.Errorf("undo: Location %q", loc)
	}
	if a, _ := database.GetGeneratedArticle(id); a.Status != db.StatusApproved {
		t.Errorf("status %q, want approved", a.Status)
	}
}

func TestPublished_UnpublishWithRewriteHasNoUndo(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Down For Rewrite")
	database.UpdateArticleStatus(id, db.StatusApproved)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/unpublish", session, csrf, url.Values{"feedback": {"fix the date"}})
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "msg=unpublished_revise") || strings.Contains(loc, "undo=1") {
		t.Fatalf("Location %q", loc)
	}
	if page := doAdminGET(t, app, loc, session).Body.String(); strings.Contains(page, `name="return" value="published"`) {
		t.Error("unpublish-and-rewrite should not offer an undo")
	}
}

// countingDetector records which articles card update detection ran for.
type countingDetector struct {
	mu  sync.Mutex
	ran []int64
}

func (d *countingDetector) DetectAndDraft(_ context.Context, id int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ran = append(d.ran, id)
	return nil
}

// Card update detection waits out the undo window: an approval undone in
// time drafts no card updates; one that stands does.
func TestApprove_CardDetectionWaitsForUndo(t *testing.T) {
	app, database := setupAdminTestApp(t)
	det := &countingDetector{}
	app.cardDetector = det
	app.cardDetectDelay = 200 * time.Millisecond
	undone := createPendingArticle(t, database, "Undone Quickly")
	kept := createPendingArticle(t, database, "Kept Published")
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	doAdminPOST(t, app, "/admin/articles/"+itoa(undone)+"/approve", session, csrf, url.Values{"revision": {"1"}})
	doAdminPOST(t, app, "/admin/articles/"+itoa(undone)+"/undo-approve", session, csrf, nil)
	doAdminPOST(t, app, "/admin/articles/"+itoa(kept)+"/approve", session, csrf, url.Values{"revision": {"1"}})
	app.WaitBackground()

	det.mu.Lock()
	defer det.mu.Unlock()
	if len(det.ran) != 1 || det.ran[0] != kept {
		t.Errorf("detection ran for %v, want only %d", det.ran, kept)
	}
}
