package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", 5 * time.Minute: "5 min ago", 3 * time.Hour: "3 h ago", 50 * time.Hour: "2 days ago",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
	if ago(time.Time{}, now) != "never" {
		t.Error("zero time should be never")
	}
}

// Integration, real templates: every admin page shows the one admin bar,
// with its own tab marked, and none of the public site's header or footer.
func TestAdminPages_ShareOneNavigation(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Nav Article")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	for path, tab := range map[string]string{
		"/admin/dashboard":                       "Review",
		"/admin/published":                       "Published",
		"/admin/sources":                         "Sources",
		"/admin/subscribers":                     "Subscribers",
		"/admin/cards":                           "Cards",
		"/admin/cards/new":                       "Cards",
		"/admin/newsletter":                      "Newsletter",
		"/admin/settings":                        "Settings",
		"/admin/models":                          "Models",
		"/admin/articles/" + itoa(id) + "/cards": "Review",
	} {
		body := doAdminGET(t, app, path, session).Body.String()
		if n := strings.Count(body, `aria-label="Admin"`); n != 1 {
			t.Errorf("%s: %d admin navs, want 1", path, n)
			continue
		}
		if !strings.Contains(body, `aria-current="page">`+tab) {
			t.Errorf("%s: tab %q not marked current", path, tab)
		}
		for _, public := range []string{`class="site-nav"`, `class="site-footer"`, `id="nav-search-form"`} {
			if strings.Contains(body, public) {
				t.Errorf("%s: carries the public site's %s", path, public)
			}
		}
		if !strings.Contains(body, `<meta name="robots" content="noindex, nofollow">`) {
			t.Errorf("%s: admin page is not noindex", path)
		}
	}
}

func TestAdminSources_ShowsHealth(t *testing.T) {
	app, database := realTemplateApp(t)
	live, _ := database.CreateSource(&db.Source{Name: "Live Feed", URL: "https://live.example", Active: true})
	if _, err := database.CreateSource(&db.Source{Name: "Dead Feed", URL: "https://dead.example", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateRawArticle(&db.RawArticle{SourceID: live, Title: "t", URL: "u", Content: "c", PublishedAt: time.Now(), Hash: "h1"}); err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/sources", session).Body.String()
	for _, want := range []string{"Last article just now", "1 this week", "Nothing new yet", `class="source-stale"`, `id="edit-`} {
		if !strings.Contains(body, want) {
			t.Errorf("sources page missing %q", want)
		}
	}
	if strings.Count(body, `class="source-stale"`) != 1 {
		t.Error("only the source with nothing stored should be marked stale")
	}
}

func TestAdminSettings_NestedValuesAreRows(t *testing.T) {
	app, _ := realTemplateApp(t)
	app.cfg.Mail.SMTP.Host = "smtp.example.com"
	app.cfg.Mail.SMTP.Password = "s3cret-value"
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/settings", session).Body.String()
	if strings.Contains(body, "map[") {
		t.Error("settings prints a raw Go map")
	}
	if !strings.Contains(body, "SMTP host") || !strings.Contains(body, "smtp.example.com") {
		t.Error("settings lost the SMTP host row")
	}
	if strings.Contains(body, "s3cret-value") {
		t.Error("settings shows a password")
	}
}

// The settings page says what the ops settings come to: review by email is
// off (and why), where vote alerts go, and the newsletter schedule in words.
func TestAdminSettings_SaysWhatIsOff(t *testing.T) {
	app, _ := realTemplateApp(t)
	app.cfg.Mail.IMAP = config.IMAPConfig{Host: "localhost", Port: 993, User: "placeholder", Password: "placeholder"}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/settings", session).Body.String()
	for _, want := range []string{
		`<td class="setting-key">Review by email</td>`,
		"off (mail.imap not configured)",
		"not set: vote alerts are shown on the Published page only",
		"every Saturday at 8:00 AM ET (built Friday 8:00 PM)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings should show %q", want)
		}
	}
}

func TestAdminSubscribers_TotalsAndNoDeadLink(t *testing.T) {
	app, database := realTemplateApp(t)
	for i, e := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		tok := "tok" + itoa(int64(i))
		if _, err := database.CreateSubscriber(&db.Subscriber{Email: e, Token: tok}); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			database.ConfirmSubscriber(tok)
		}
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/subscribers", session).Body.String()
	if !strings.Contains(body, "3 total: 2 active") || !strings.Contains(body, "1 waiting for confirmation") {
		t.Error("subscribers page has no totals")
	}
	if strings.Contains(body, `href="/subscribe"`) {
		t.Error("links to /subscribe, which only accepts POST")
	}
}

func TestAdminArticleCards_SavedAndBackLink(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Linked Cards Article")
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/articles/"+itoa(id)+"/cards?msg=saved", session).Body.String()
	if !strings.Contains(body, "Saved.") {
		t.Error("save confirmation not shown")
	}
	if !strings.Contains(body, `<a href="/admin/published">Published</a>`) {
		t.Error("a published article's breadcrumb should go back to Published")
	}
	if strings.Contains(body, " of 0 selected") || strings.Contains(body, "selected</small>") {
		t.Error(`"N of N selected" is still shown`)
	}
}

// The tabs carry counts: what needs a decision (red), what is ready, card
// updates and flags, stale sources, and the newsletter's state.
func TestAdminBar_Counts(t *testing.T) {
	app, database := realTemplateApp(t)
	app.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	createPendingArticle(t, database, "Ready One")
	createPendingArticle(t, database, "Ready Two")
	down := createPendingArticle(t, database, "Taken Down")
	database.UpdateArticleStatus(down, db.StatusApproved)
	database.UpdateArticleStatus(down, db.StatusUnpublished)
	page := createEditableCard(t, database)
	database.AppendFlaggedField(page.ID, "annual_fee_amount", "check")
	database.CreateSource(&db.Source{Name: "Silent", URL: "https://silent.example", Active: true})
	database.CreateNewsletter(&db.Newsletter{Date: "2026-10-03", Subject: "W", Status: "draft", ArticleCount: 2})

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/settings", session).Body.String()
	for _, want := range []string{
		`<span class="tab-n alert" title="1 need a decision">1</span>`,
		`<span class="tab-n" title="2 ready to review">2</span>`,
		`<span class="tab-n" title="1 card updates and flags to look at">1</span>`,
		`<span class="tab-n alert" title="1 sources with nothing new for days">1</span>`,
		`<span class="tab-n warn">Draft</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("admin bar missing %s", want)
		}
	}
}

// The login page gets the admin bar without tabs or counts.
func TestAdminBar_LoginPageHasNoTabs(t *testing.T) {
	app, _ := realTemplateApp(t)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `class="admin-bar"`) || strings.Contains(body, `aria-label="Admin"`) {
		t.Error("login page should have the bar but no tabs")
	}
	if strings.Contains(body, "/admin/logout") {
		t.Error("login page offers Log out")
	}
}

// An unsent issue whose Saturday has passed is not "upcoming": the tab does
// not warn about it forever.
func TestAdminBar_IgnoresPastIssues(t *testing.T) {
	app, database := realTemplateApp(t)
	app.now = func() time.Time { return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC) }
	database.CreateNewsletter(&db.Newsletter{Date: "2026-09-26", Subject: "W", Status: "draft"})
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	if body := doAdminGET(t, app, "/admin/settings", session).Body.String(); strings.Contains(body, `<span class="tab-n warn">`) {
		t.Error("a past draft issue still flags the Newsletter tab")
	}
}
