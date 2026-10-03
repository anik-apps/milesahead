package web

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/vote"
)

// Integration, real templates: a downvote that crosses the alert line opens
// an alert, which the admin bar counts on the Published tab, the Review page
// points to, and the Published page lists until it is acknowledged.
func TestVoteAlerts_ShownOnPublishedUntilAcknowledged(t *testing.T) {
	app, database := realTemplateApp(t)
	app.cfg.Votes = config.VotesConfig{AlertThreshold: 3, AlertRatio: 0.5, AlertWindow: 24}
	id, slug := createApprovedArticle(t, database, "Bonus cut overnight", "news", false)
	other, _ := createApprovedArticle(t, database, "Quiet article", "news", false)
	votes := vote.NewService(database, app.cfg.Votes, nil)
	for i := 0; i < 3; i++ {
		if err := votes.CastVote(id, "down", fmt.Sprintf("ip%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := votes.CastVote(other, "down", "ipx"); err != nil {
		t.Fatal(err)
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	published := doAdminGET(t, app, "/admin/published", session).Body.String()
	for _, want := range []string{
		`id="vote-alerts"`,
		`<a href="/article/` + slug + `"`,
		"&#9660; 3",
		"crossed the alert line in the 24 hours before it was raised",
		"Not emailed: <code>votes.alert_email</code> is not set.",
		`action="/admin/vote-alerts/`,
		`title="1 with many downvotes since you last looked">1</span>`,
	} {
		if !strings.Contains(published, want) {
			t.Errorf("Published page should contain %q", want)
		}
	}
	if strings.Contains(published, "Acknowledge all") {
		t.Error("one alert needs no Acknowledge all")
	}
	if start := strings.Index(published, `id="vote-alerts"`); start >= 0 {
		banner := published[start : start+strings.Index(published[start:], "</section>")]
		if strings.Contains(banner, "Quiet article") || !strings.Contains(banner, "Bonus cut overnight") {
			t.Error("the banner should list the alerted article and not the one under the line")
		}
	}
	review := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if !strings.Contains(review, `1 published article crossed the downvote alert line. <a href="/admin/published#vote-alerts">`) {
		t.Error("the Review page should point to the alert")
	}

	alerts, _ := database.ListOpenVoteAlerts()
	if len(alerts) != 1 {
		t.Fatalf("open alerts: %d", len(alerts))
	}
	rec := doAdminPOST(t, app, fmt.Sprintf("/admin/vote-alerts/%d/acknowledge", alerts[0].ID), session, "", url.Values{})
	if rec.Code != 303 || rec.Header().Get("Location") != "/admin/published?msg=alert_acknowledged" {
		t.Fatalf("acknowledge: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	after := doAdminGET(t, app, "/admin/published?msg=alert_acknowledged", session).Body.String()
	if strings.Contains(after, `id="vote-alerts"`) || strings.Contains(after, "since you last looked") {
		t.Error("an acknowledged alert should leave the banner and the tab count")
	}
	if !strings.Contains(after, "Alert acknowledged.") {
		t.Error("acknowledging should say so")
	}
	// The row keeps its chip: its votes still cross the line overall.
	if !strings.Contains(after, `class="p-row p-flagged"`) {
		t.Error("the article's row should still be marked Many downvotes")
	}
}

func TestVoteAlerts_AcknowledgeAll(t *testing.T) {
	app, database := realTemplateApp(t)
	app.cfg.Votes = config.VotesConfig{AlertThreshold: 1, AlertRatio: 0.5}
	app.cfg.Votes.AlertEmail = "owner@example.com"
	votes := vote.NewService(database, app.cfg.Votes, nil)
	for i := 0; i < 2; i++ {
		id, _ := createApprovedArticle(t, database, fmt.Sprintf("Article %d", i), "news", false)
		if err := votes.CastVote(id, "down", "ip"); err != nil {
			t.Fatal(err)
		}
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	page := doAdminGET(t, app, "/admin/published", session).Body.String()
	if !strings.Contains(page, "Acknowledge all") || !strings.Contains(page, "Also emailed to <code>votes.alert_email</code>.") {
		t.Error("two alerts with an alert email: Acknowledge all and the emailed note")
	}
	if rec := doAdminPOST(t, app, "/admin/vote-alerts/acknowledge-all", session, "", url.Values{}); rec.Code != 303 {
		t.Fatalf("acknowledge all: %d", rec.Code)
	}
	if n, _ := database.CountOpenVoteAlerts(); n != 0 {
		t.Errorf("open alerts after acknowledging all: %d", n)
	}
}
