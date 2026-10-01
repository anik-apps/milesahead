package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestNewsletterStatusNote(t *testing.T) {
	sent := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		nl   *db.Newsletter
		n    int
		want string
	}{
		{&db.Newsletter{Date: "2026-10-03", Status: "auto_approved"}, 42, "Goes out Sat Oct 3, 8:00 AM ET, to 42 confirmed subscribers."},
		{&db.Newsletter{Date: "2026-10-03", Status: "approved"}, 1, "Goes out Sat Oct 3, 8:00 AM ET, to 1 confirmed subscriber."},
		{&db.Newsletter{Date: "2026-10-03", Status: "draft"}, 5, "Draft: it will not go out on Sat Oct 3 unless you approve it."},
		{&db.Newsletter{Date: "2026-10-03", Status: "hold"}, 5, "On hold: it will not go out on Sat Oct 3 unless you approve it."},
		{&db.Newsletter{Date: "2026-10-03", Status: "skipped"}, 5, "Skipped: the Sat Oct 3 issue will not go out."},
		{&db.Newsletter{Date: "2026-09-26", Status: "sent", SentAt: &sent}, 5, "Sent Sep 26, 2026 12:00 PM."},
	} {
		if got := newsletterStatusNote(c.nl, c.n); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: got %q, want it to start %q", c.nl.Status, got, c.want)
		}
	}
	if newsletterStatusNote(nil, 3) != "" {
		t.Error("no issue, no note")
	}
}

// Integration, real templates: the page says when the issue goes out, offers
// only the status changes that do something, and links to the manual send.
func TestAdminNewsletter_SaysWhenItGoesOut(t *testing.T) {
	app, database := realTemplateApp(t)
	if _, err := database.CreateNewsletter(&db.Newsletter{Date: "2026-10-03", Subject: "Weekly", HTMLContent: "<p>x</p>", Status: "auto_approved", ArticleCount: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateSubscriber(&db.Subscriber{Email: "a@example.com", Token: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := database.ConfirmSubscriber("t1"); err != nil {
		t.Fatal(err)
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/newsletter", session).Body.String()
	for _, want := range []string{
		"Goes out Sat Oct 3, 8:00 AM ET, to 1 confirmed subscriber.",
		">Auto-approved<",
		`action="/admin/newsletter/send"`,
		`id="history"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("newsletter page missing %q", want)
		}
	}
	if strings.Contains(body, `name="status" value="approved"`) {
		t.Error("an auto-approved issue should not offer Approve")
	}
}

// The newsletter page used dark-theme colours inline, which showed dark slabs
// in the light theme. It must use the theme's variables.
func TestAdminNewsletter_NoHardCodedThemeColours(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "templates", "admin", "newsletter.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hex := range []string{"#1a1a2e", "#22223a", "#2a2a4a", "#0f0f1a", "#e0e0e0", "#8b8ba0"} {
		if strings.Contains(string(b), hex) {
			t.Errorf("newsletter.html hard-codes %s", hex)
		}
	}
}
