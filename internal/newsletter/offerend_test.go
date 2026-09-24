package newsletter

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

const (
	endedTitle = "Citi 25% Transfer Bonus to Leading Hotels of the World"
	liveTitle  = "Triple United Miles on IHG Stays"
	plainTitle = "Chase Freedom Flex Drops Its Foreign Transaction Fee"
)

// Deadlines a month either side of the run date: one offer that has ended
// and one that has not. They move with the clock because a deadline is read
// relative to when its article was written -- a year far from that, such as
// 2020 or 2099, is not taken as a year at all.
var (
	endedDeadline = time.Now().AddDate(0, 0, -30).Format("January 2, 2006")
	liveDeadline  = time.Now().AddDate(0, 0, 30).Format("January 2, 2006")
)

// publish stores an approved article through the real write path, so its end
// date is derived from the Key Details as in production.
func publish(t *testing.T, database *db.Database, title, slug, deadline string, featured bool) int64 {
	t.Helper()
	kd := `[{"Label":"Bonus","Value":"25%"}]`
	if deadline != "" {
		kd = `[{"Label":"Bonus","Value":"25%"},{"Label":"Deadline","Value":"` + deadline + `"}]`
	}
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: title, Slug: slug, Content: "Body of " + title + ".", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", Featured: featured,
		TLDR: "Summary of " + title, ScoutsTake: "Take on " + title,
		KeyDetails: kd, TagsJSON: `["transfer-bonuses"]`,
	})
	if err != nil {
		t.Fatalf("creating %s: %v", slug, err)
	}
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("approving %s: %v", slug, err)
	}
	return id
}

// seed publishes the ended offer as the featured one, so without the filter it
// would lead the daily and be ranked first -- and so made Scout's Pick -- in
// the weekly.
func seed(t *testing.T, database *db.Database) (ended, live, plain int64) {
	t.Helper()
	ended = publish(t, database, endedTitle, "citi-lhw", endedDeadline, true)
	live = publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	plain = publish(t, database, plainTitle, "freedom-flex", "", false)
	return
}

func subscribe(t *testing.T, database *db.Database) {
	t.Helper()
	if _, err := database.CreateSubscriber(&db.Subscriber{Email: "reader@example.com", Token: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := database.ConfirmSubscriber("tok"); err != nil {
		t.Fatal(err)
	}
}

func TestDaily_LeavesOutEndedOffers(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	seed(t, database)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "America/New_York"})
	if err := svc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	html := sender.calls[0].html
	if strings.Contains(html, endedTitle) || strings.Contains(html, "citi-lhw") {
		t.Error("the daily newsletter includes an offer that has ended")
	}
	for _, want := range []string{liveTitle, plainTitle} {
		if !strings.Contains(html, want) {
			t.Errorf("the daily newsletter is missing live article %q", want)
		}
	}

	preview, err := svc.PreviewNext()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(preview, endedTitle) {
		t.Error("the admin preview includes an offer that has ended")
	}
}

// Nothing but ended offers means no daily at all, rather than one that sends
// them, and the admin is told why.
func TestDaily_NothingLiveSendsNothing(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, endedTitle, "citi-lhw", endedDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	if err := svc.SendDaily(); !errors.Is(err, ErrNothingLive) {
		t.Errorf("SendDaily() = %v, want ErrNothingLive", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("sent a daily made only of ended offers")
	}
}

// storeDaily saves today's daily as GenerateDaily would have, earlier, when
// everything in html was still live.
func storeDaily(t *testing.T, database *db.Database, svc *Service, status, html string) int64 {
	t.Helper()
	id, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.todayDate(), Subject: "MilesAhead Daily Digest — " + svc.todayDate(),
		HTMLContent: html, Status: status, ArticleCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The admin "send" button sends today's stored daily. It was rendered when
// it was generated, and an offer in it may have ended since, so the issue is
// rendered again from what is live when it is sent.
func TestSendDaily_RerendersTheStoredIssue(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, endedTitle, "citi-lhw", endedDeadline, true)
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database, svc, "draft", "<p>"+endedTitle+"</p><p>"+liveTitle+"</p>")

	if err := svc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	html := sender.calls[0].html
	if strings.Contains(html, endedTitle) {
		t.Error("the daily sent an offer that ended after the issue was stored")
	}
	if !strings.Contains(html, liveTitle) || !strings.Contains(html, "/article/united-ihg") {
		t.Error("the daily should be the re-rendered issue with the live offer")
	}
	nl, err := database.GetNewsletterByDate(svc.todayDate())
	if err != nil {
		t.Fatal(err)
	}
	if nl.Status != "sent" {
		t.Errorf("status = %q, want sent", nl.Status)
	}
	if nl.HTMLContent != html {
		t.Error("the stored issue should be what was sent")
	}
}

// If everything in the stored daily has ended by the time it is sent, it is
// not sent at all.
func TestSendDaily_StoredIssueAllEndedSendsNothing(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, endedTitle, "citi-lhw", endedDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database, svc, "approved", "<p>"+endedTitle+"</p>")

	if err := svc.SendDaily(); !errors.Is(err, ErrNothingLive) {
		t.Errorf("SendDaily() = %v, want ErrNothingLive", err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("sent the stored issue:\n%s", sender.calls[0].html)
	}
	if nl, _ := database.GetNewsletterByDate(svc.todayDate()); nl == nil || nl.Status == "sent" {
		t.Error("the issue should be left unsent")
	}
}

// An issue an admin held or skipped is not sent from the daily path.
func TestSendDaily_RefusesHeldAndSkippedIssues(t *testing.T) {
	for _, status := range []string{"hold", "skipped"} {
		t.Run(status, func(t *testing.T) {
			setTemplateDir(t)
			database, cleanup := setupTestDB(t)
			defer cleanup()
			publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
			subscribe(t, database)

			sender := &mockSender{}
			svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
			storeDaily(t, database, svc, status, "<p>"+liveTitle+"</p>")

			if err := svc.SendDaily(); err == nil {
				t.Error("SendDaily should say the issue was not sent")
			}
			if len(sender.calls) != 0 {
				t.Errorf("sent a %s issue", status)
			}
			nl, err := database.GetNewsletterByDate(svc.todayDate())
			if err != nil {
				t.Fatal(err)
			}
			if nl.Status != status {
				t.Errorf("status = %q, want it left %q", nl.Status, status)
			}
		})
	}
}

// On a Saturday, today's issue is the weekly one BuildWeekly made on Friday.
// SendWeekly sends it on its own schedule and checks; the daily path does
// not send it.
func TestSendDaily_RefusesTheWeeklyIssue(t *testing.T) {
	cases := []struct {
		name  string
		build func(svc *Service, database *db.Database, pick int64) *db.Newsletter
	}{
		{"as built", func(svc *Service, _ *db.Database, pick int64) *db.Newsletter {
			return &db.Newsletter{Date: svc.todayDate(), Subject: weeklySubject + " — " + svc.todayDate(),
				Status: "auto_approved", ArticleCount: 1, ScoutsPickArticleID: &pick}
		}},
		// Known only by its candidates.
		{"renamed, no pick", func(svc *Service, _ *db.Database, _ int64) *db.Newsletter {
			return &db.Newsletter{Date: svc.todayDate(), Subject: "This week", Status: "approved", ArticleCount: 1}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setTemplateDir(t)
			database, cleanup := setupTestDB(t)
			defer cleanup()
			live := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
			subscribe(t, database)

			sender := &mockSender{}
			svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
			nl := c.build(svc, database, live)
			nl.HTMLContent = "<p>" + liveTitle + "</p>"
			id, err := database.CreateNewsletter(nl)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CreateNewsletterCandidate(&db.NewsletterCandidate{NewsletterID: id, ArticleID: live, Rank: 1, Featured: true}); err != nil {
				t.Fatal(err)
			}

			if err := svc.SendDaily(); err == nil || !strings.Contains(err.Error(), "weekly") {
				t.Errorf("SendDaily() = %v, want a refusal naming the weekly issue", err)
			}
			if len(sender.calls) != 0 {
				t.Error("the daily path sent the weekly issue")
			}
			if got, _ := database.GetNewsletterByDate(svc.todayDate()); got == nil || got.Status == "sent" {
				t.Error("the weekly issue should be left for SendWeekly")
			}
		})
	}
}

// The weekly build drops ended offers before ranking: not a candidate, not
// Scout's Pick, not in the rendered issue.
func TestBuildWeekly_NeverPicksEndedOffers(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	ended, live, _ := seed(t, database)

	svc := NewService(database, &mockSender{}, config.NewsletterConfig{Timezone: "America/New_York"})
	nl, err := svc.BuildWeekly()
	if err != nil {
		t.Fatal(err)
	}
	if nl == nil {
		t.Fatal("BuildWeekly built nothing")
	}
	if nl.ScoutsPickArticleID == nil || *nl.ScoutsPickArticleID == ended {
		t.Fatalf("Scout's Pick = %v, must not be the ended offer %d", nl.ScoutsPickArticleID, ended)
	}
	if *nl.ScoutsPickArticleID != live {
		t.Errorf("Scout's Pick = %d, want the top live article %d", *nl.ScoutsPickArticleID, live)
	}
	candidates, err := database.ListCandidatesForNewsletter(nl.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.ArticleID == ended {
			t.Error("the ended offer is a newsletter candidate")
		}
	}
	if nl.ArticleCount != 2 {
		t.Errorf("ArticleCount = %d, want 2 live articles", nl.ArticleCount)
	}
	if strings.Contains(nl.HTMLContent, endedTitle) {
		t.Error("the weekly issue includes an offer that has ended")
	}
}

// The weekly is built on Friday evening and sent on Saturday morning. An offer
// that ends in between must not go out, and if it was Scout's Pick the slot
// passes to the top article still being sent.
func TestSendWeekly_ReplacesScoutsPickThatEndedSinceBuild(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	ended, live, _ := seed(t, database)
	subscribe(t, database)

	svc := NewService(database, &mockSender{}, config.NewsletterConfig{Timezone: "America/New_York"})
	sender := &mockSender{}
	svc.sender = sender

	// As BuildWeekly left it when the offer was still live.
	pick := ended
	nlID, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSaturday(), Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + endedTitle + "</p>", ArticleCount: 3, ScoutsPickArticleID: &pick,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{ended, live} {
		if err := database.CreateNewsletterCandidate(&db.NewsletterCandidate{
			NewsletterID: nlID, ArticleID: id, Rank: i + 1, Featured: i == 0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := svc.SendWeekly(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	html := sender.calls[0].html
	if strings.Contains(html, endedTitle) {
		t.Error("the weekly sent an offer that ended after it was built")
	}
	if !strings.Contains(html, "Take on "+liveTitle) {
		t.Error("Scout's Pick should have passed to the top live article")
	}
	nl, err := database.GetNewsletterByDate(svc.thisSaturday())
	if err != nil {
		t.Fatal(err)
	}
	if nl.ScoutsPickArticleID == nil || *nl.ScoutsPickArticleID != live {
		t.Errorf("stored Scout's Pick = %v, want %d", nl.ScoutsPickArticleID, live)
	}
}

// If every article in the issue has ended by send time, the stored HTML (which
// holds them) must not go out either.
func TestSendWeekly_AllEndedSendsNothing(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	ended := publish(t, database, endedTitle, "citi-lhw", endedDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	pick := ended
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSaturday(), Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + endedTitle + "</p>", ArticleCount: 1, ScoutsPickArticleID: &pick,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SendWeekly(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Error("sent a weekly made only of ended offers")
	}
}

// The weekly is re-checked at send time because an offer in it may have ended
// overnight. If the articles cannot be reloaded, the stored HTML cannot be
// checked either, so it must not go out in their place.
func TestSendWeekly_UncheckableIssueIsNotSent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	ended := publish(t, database, endedTitle, "citi-lhw", endedDeadline, false)
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	pick := ended
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSaturday(), Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + endedTitle + "</p><p>" + liveTitle + "</p>", ArticleCount: 2, ScoutsPickArticleID: &pick,
	}); err != nil {
		t.Fatal(err)
	}
	// Make the reload fail while the newsletter and subscribers still load.
	if _, err := database.DB().Exec(`ALTER TABLE generated_articles RENAME TO generated_articles_gone`); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendWeekly(); err == nil {
		t.Error("SendWeekly should report that it could not recheck the issue")
	}
	if len(sender.calls) != 0 {
		t.Errorf("sent the stored issue unchecked:\n%s", sender.calls[0].html)
	}
	nl, err := database.GetNewsletterByDate(svc.thisSaturday())
	if err != nil {
		t.Fatal(err)
	}
	if nl.Status == "sent" {
		t.Error("the issue was marked sent")
	}
}

// With nothing approved left in the week (every article unpublished since the
// build), the stored HTML holds only articles that are no longer on the site;
// it is not sent.
func TestSendWeekly_NothingLeftToSendSendsNothing(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	id := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	pick := id
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSaturday(), Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + liveTitle + "</p>", ArticleCount: 1, ScoutsPickArticleID: &pick,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateArticleStatus(id, db.StatusUnpublished); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendWeekly(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Error("sent a weekly whose articles are no longer published")
	}
}

// A Scout's Pick or Card of the Week set on a daily from the admin page does
// not make it the weekly: it is still sent, re-rendered.
func TestSendDaily_DailyWithAdminPicksIsStillSent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	live := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	id := storeDaily(t, database, svc, "approved", "<p>"+liveTitle+"</p>")
	if err := database.UpdateNewsletterScoutsPick(id, live); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateNewsletterCardOfWeek(id, 1); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 || !strings.Contains(sender.calls[0].html, liveTitle) {
		t.Error("the daily should have been sent")
	}
}
