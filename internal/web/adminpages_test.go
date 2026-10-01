package web

import (
	"strings"
	"testing"
	"time"

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

// Integration, real templates: every admin page shows the one shared
// navigation, with its own tab marked.
func TestAdminPages_ShareOneNavigation(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Nav Article")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	for path, tab := range map[string]string{
		"/admin/dashboard":                       "Pending",
		"/admin/published":                       "Published",
		"/admin/sources":                         "Sources",
		"/admin/subscribers":                     "Subscribers",
		"/admin/cards":                           "Cards",
		"/admin/cards/new":                       "Cards",
		"/admin/newsletter":                      "Newsletter",
		"/admin/settings":                        "Settings",
		"/admin/articles/" + itoa(id) + "/cards": "Pending",
	} {
		body := doAdminGET(t, app, path, session).Body.String()
		if n := strings.Count(body, `aria-label="Admin"`); n != 1 {
			t.Errorf("%s: %d admin navs, want 1", path, n)
			continue
		}
		if !strings.Contains(body, `aria-current="page">`+tab+`<`) {
			t.Errorf("%s: tab %q not marked current", path, tab)
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
	for _, want := range []string{"Last article just now", "1 in 7 days", "Nothing new yet", `class="source-stale"`, `id="edit-`} {
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
	if !strings.Contains(body, "3 total: 2 confirmed") || !strings.Contains(body, "1 waiting for confirmation") {
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
