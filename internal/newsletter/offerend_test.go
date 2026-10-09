package newsletter

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
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
//
// Among the two live articles, live is the newer, so it ranks first and is the
// pick when no AI ranks them. Their publish times are set, not left to
// CURRENT_TIMESTAMP: that has one-second resolution, so the order used to hold
// only when both were published within the same second. Under -race in CI the
// clock ticked between them, plain became the newest, and Deploy #117 failed.
func seed(t *testing.T, database *db.Database) (ended, live, plain int64) {
	t.Helper()
	ended = publish(t, database, endedTitle, "citi-lhw", endedDeadline, true)
	live = publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	plain = publish(t, database, plainTitle, "freedom-flex", "", false)
	now := time.Now().UTC()
	for id, age := range map[int64]time.Duration{ended: 1 * time.Second, live: 2 * time.Second, plain: 3 * time.Second} {
		if _, err := database.DB().Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`,
			now.Add(-age).Format("2006-01-02 15:04:05"), id); err != nil {
			t.Fatalf("setting published_at: %v", err)
		}
	}
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
	// The stored copy is the issue as sent, with the plain /unsubscribe
	// link in place of each reader's own.
	if nl.HTMLContent != svc.forDisplay(html) {
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
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved",
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
	nl, err := database.GetNewsletterByDate(svc.thisSendDate())
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
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved",
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
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved",
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
	nl, err := database.GetNewsletterByDate(svc.thisSendDate())
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
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved",
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

// publishedAt moves an article's publication time, as if it had gone out
// then.
func publishedAt(t *testing.T, database *db.Database, id int64, at time.Time) {
	t.Helper()
	if _, err := database.DB().Exec(`UPDATE generated_articles SET published_at = ? WHERE id = ?`, at.UTC(), id); err != nil {
		t.Fatal(err)
	}
}

// A daily generated in the morning and sent in the afternoon is re-rendered
// from the articles it was generated from. Re-rendering from the 24 hours
// before the send silently dropped the morning's older articles.
func TestSendDaily_KeepsTheStoredIssuesArticles(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	now := time.Now()
	// Published 30 hours ago: in an issue generated 10 hours ago, but more
	// than 24 hours before the send.
	early := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	publishedAt(t, database, early, now.Add(-30*time.Hour))
	gone := publish(t, database, endedTitle, "citi-lhw", endedDeadline, false)
	publishedAt(t, database, gone, now.Add(-30*time.Hour))
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	generated := now.Add(-10 * time.Hour)
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.todayDate(), Subject: "MilesAhead Daily Digest — " + svc.todayDate(),
		HTMLContent: "<p>" + liveTitle + "</p><p>" + endedTitle + "</p>", Status: "draft", ArticleCount: 2,
		CreatedAt: &generated,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendDaily(); err != nil {
		t.Fatalf("SendDaily() = %v, want the stored issue's live article sent", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	html := sender.calls[0].html
	if !strings.Contains(html, "/article/united-ihg") {
		t.Error("the re-rendered daily dropped a live article the stored issue held")
	}
	if strings.Contains(html, endedTitle) {
		t.Error("the re-rendered daily sent an ended offer")
	}
}

// An issue stored before generation times were recorded has none; its
// articles are taken from 24 hours before the start of its date.
//
// The send is at 00:30, and the article published 23 hours 45 minutes
// before the start of the day: more than 24 hours before the send, so only
// the issue's date brings it in. (Unpinned, the test passed without that
// rule whenever it ran in the first hour of a UTC day.)
func TestSendDaily_IssueWithoutAGenerationTime(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	startOfToday := time.Now().UTC().Truncate(24 * time.Hour)
	svc.now = func() time.Time { return startOfToday.Add(30 * time.Minute) }
	early := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	publishedAt(t, database, early, startOfToday.Add(-24*time.Hour+15*time.Minute))
	subscribe(t, database)
	id := storeDaily(t, database, svc, "approved", "<p>"+liveTitle+"</p>")
	if _, err := database.DB().Exec(`UPDATE newsletters SET created_at = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if nl, err := database.GetNewsletterByDate(svc.todayDate()); err != nil || nl.CreatedAt != nil {
		t.Fatalf("want a stored issue with no generation time, got %+v, %v", nl, err)
	}

	if err := svc.SendDaily(); err != nil {
		t.Fatalf("SendDaily() = %v", err)
	}
	if len(sender.calls) != 1 || !strings.Contains(sender.calls[0].html, "/article/united-ihg") {
		t.Error("the daily should have been sent with the article from its day")
	}
}

// The generation time is recorded, and read back.
func TestGenerateDaily_RecordsWhenItWasGenerated(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	svc := NewService(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"})

	before := time.Now().Add(-time.Second)
	if _, err := svc.GenerateDaily(); err != nil {
		t.Fatal(err)
	}
	nl, err := database.GetNewsletterByDate(svc.todayDate())
	if err != nil {
		t.Fatal(err)
	}
	if nl.CreatedAt == nil || nl.CreatedAt.Before(before) || nl.CreatedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("CreatedAt = %v, want about now", nl.CreatedAt)
	}
}

// A daily sent late -- retried hours after it was generated -- holds only
// what it was generated from. An article published in between belongs to
// the next day's issue; sending it now as well sent it twice.
func TestSendDaily_LeavesLaterArticlesForTheNextIssue(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	now := time.Now()
	before := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	publishedAt(t, database, before, now.Add(-12*time.Hour))
	after := publish(t, database, plainTitle, "freedom-flex", "", false)
	publishedAt(t, database, after, now.Add(-2*time.Hour))
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	generated := now.Add(-10 * time.Hour)
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.todayDate(), Subject: "MilesAhead Daily Digest — " + svc.todayDate(),
		HTMLContent: "<p>" + liveTitle + "</p>", Status: "draft", ArticleCount: 1,
		CreatedAt: &generated,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendDaily(); err != nil {
		t.Fatalf("SendDaily() = %v", err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	html := sender.calls[0].html
	if !strings.Contains(html, "/article/united-ihg") {
		t.Error("the daily dropped the article it was generated from")
	}
	if strings.Contains(html, "/article/freedom-flex") {
		t.Error("the daily took in an article published after it was generated")
	}
}

// hookSender is a mockSender that runs during in each send, and fails
// the send when fail is set: after reaching the first reach recipients.
type hookSender struct {
	mockSender
	during func()
	fail   error
	reach  int
}

func (h *hookSender) SendNewsletter(subject, htmlContent string, recipients []mail.Recipient) (int, error) {
	if h.during != nil {
		h.during()
	}
	if h.fail != nil {
		if h.reach > 0 {
			h.mockSender.SendNewsletter(subject, htmlContent, recipients[:h.reach])
		}
		return h.reach, h.fail
	}
	return h.mockSender.SendNewsletter(subject, htmlContent, recipients)
}

// breakNewsletterWrites makes every later write to the newsletters table
// fail, as a database that goes away mid-send would.
func breakNewsletterWrites(t *testing.T, database *db.Database) {
	t.Helper()
	if _, err := database.DB().Exec(`CREATE TRIGGER no_newsletter_writes BEFORE UPDATE ON newsletters BEGIN SELECT RAISE(ABORT, 'database unavailable'); END`); err != nil {
		t.Fatal(err)
	}
}

// Once the emails have gone out, nothing can leave the issue looking unsent:
// recording the send after it, a failure to record it reported an error and
// left the issue a draft, and the admin's second click sent it to everyone
// again.
func TestSendDaily_ASecondClickCannotSendTwice(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &hookSender{}
	sender.during = func() {
		if len(sender.calls) == 0 {
			breakNewsletterWrites(t, database)
		}
	}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database, svc, "draft", "<p>"+liveTitle+"</p>")

	if err := svc.SendDaily(); err != nil {
		t.Errorf("first SendDaily() = %v, want it reported as sent", err)
	}
	if err := svc.SendDaily(); !errors.Is(err, ErrAlreadySent) {
		t.Errorf("second SendDaily() = %v, want ErrAlreadySent", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("the daily went out %d times, want once", len(sender.calls))
	}
}

// The weekly is sent once as well, however many times SendWeekly runs.
func TestSendWeekly_SentOnlyOnce(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	live := publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &hookSender{}
	sender.during = func() {
		if len(sender.calls) == 0 {
			breakNewsletterWrites(t, database)
		}
	}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	pick := live
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + liveTitle + "</p>", ArticleCount: 1, ScoutsPickArticleID: &pick,
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.SendWeekly(); err != nil {
		t.Errorf("first SendWeekly() = %v", err)
	}
	if err := svc.SendWeekly(); err != nil {
		t.Errorf("second SendWeekly() = %v", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("the weekly went out %d times, want once", len(sender.calls))
	}
}

// A send that fails leaves the issue as it was, to be sent again; if even
// that cannot be recorded, the admin is told it shows as sent.
func TestSendDaily_FailedSendIsPutBack(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &hookSender{fail: errors.New("smtp: connection refused")}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database, svc, "approved", "<p>"+liveTitle+"</p>")

	if err := svc.SendDaily(); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("SendDaily() = %v, want the send failure", err)
	}
	nl, err := database.GetNewsletterByDate(svc.todayDate())
	if err != nil {
		t.Fatal(err)
	}
	if nl.Status != "approved" || nl.SentAt != nil {
		t.Errorf("after a failed send: status %q, sent at %v; want approved, never sent", nl.Status, nl.SentAt)
	}

	sender.fail = nil
	if err := svc.SendDaily(); err != nil {
		t.Errorf("retry SendDaily() = %v", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("want the retry sent once, got %d sends", len(sender.calls))
	}

	// The send fails and the issue cannot be put back.
	database2, cleanup2 := setupTestDB(t)
	defer cleanup2()
	publish(t, database2, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database2)
	sender2 := &hookSender{fail: errors.New("smtp: connection refused")}
	sender2.during = func() { breakNewsletterWrites(t, database2) }
	svc2 := NewService(database2, sender2, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database2, svc2, "approved", "<p>"+liveTitle+"</p>")
	if err := svc2.SendDaily(); err == nil || !strings.Contains(err.Error(), "shows as sent") {
		t.Errorf("SendDaily() = %v, want it to say the issue shows as sent", err)
	}
}

// A send that fails partway has reached some subscribers. The issue stays
// sent -- putting it back let a retry send it to them again -- and the
// admin is told how far it got and where it failed.
func TestSendDaily_PartialSendStaysSent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	for i, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		tok := fmt.Sprintf("tok%d", i)
		if _, err := database.CreateSubscriber(&db.Subscriber{Email: email, Token: tok}); err != nil {
			t.Fatal(err)
		}
		if err := database.ConfirmSubscriber(tok); err != nil {
			t.Fatal(err)
		}
	}

	sender := &hookSender{fail: errors.New("smtp: 421 too many messages"), reach: 2}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	storeDaily(t, database, svc, "approved", "<p>"+liveTitle+"</p>")

	err := svc.SendDaily()
	var partial *PartialSendError
	if !errors.As(err, &partial) {
		t.Fatalf("SendDaily() = %v, want a *PartialSendError", err)
	}
	if partial.Sent != 2 || partial.Total != 3 {
		t.Errorf("partial send = %d of %d, want 2 of 3", partial.Sent, partial.Total)
	}
	if !strings.Contains(err.Error(), "sent to 2 of 3") || !strings.Contains(err.Error(), "failed at subscriber 3") || !strings.Contains(err.Error(), "too many messages") {
		t.Errorf("admin message = %q, want it to say sent to 2 of 3, where it failed, and why", err)
	}
	if strings.Contains(err.Error(), "@") {
		t.Errorf("admin message = %q, want no subscriber address in it (it reaches the redirect URL and logs)", err)
	}
	withAddr := &PartialSendError{Sent: 1, Total: 2, FailedAt: "x@example.com", Err: errors.New("sending newsletter to x@example.com: 421 busy")}
	if strings.Contains(withAddr.Error(), "x@example.com") {
		t.Errorf("an address in the SMTP error should be redacted: %q", withAddr.Error())
	}
	reached := sender.calls[0].recipients
	if len(reached) != 2 || partial.FailedAt == reached[0].Email || partial.FailedAt == reached[1].Email {
		t.Errorf("reached %v, failed at %q: want it to fail at the third", reached, partial.FailedAt)
	}
	nl, gerr := database.GetNewsletterByDate(svc.todayDate())
	if gerr != nil {
		t.Fatal(gerr)
	}
	if nl.Status != "sent" || nl.SentAt == nil {
		t.Errorf("after a partial send: status %q, sent at %v; want it recorded as sent", nl.Status, nl.SentAt)
	}

	// A retry must not send it to the two who have it.
	sender.fail = nil
	if err := svc.SendDaily(); !errors.Is(err, ErrAlreadySent) {
		t.Errorf("retry SendDaily() = %v, want ErrAlreadySent", err)
	}
	if len(sender.calls) != 1 {
		t.Errorf("the daily went out %d times, want once", len(sender.calls))
	}
}

// An issue an admin holds while it is being sent is not sent, and the
// admin is told it was held, not that it was already sent.
func TestSendDaily_HeldMidSendIsNotSent(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, liveTitle, "united-ihg", liveDeadline, false)
	subscribe(t, database)

	sender := &hookSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	id := storeDaily(t, database, svc, "draft", "<p>"+liveTitle+"</p>")
	// Held after SendDaily read it: the subscriber list is read in between.
	if _, err := database.DB().Exec(`CREATE TRIGGER hold_mid_send AFTER UPDATE OF html_content ON newsletters BEGIN UPDATE newsletters SET status = 'hold' WHERE id = NEW.id; END`); err != nil {
		t.Fatal(err)
	}

	err := svc.SendDaily()
	if err == nil || errors.Is(err, ErrAlreadySent) || !strings.Contains(err.Error(), "hold") {
		t.Errorf("SendDaily() = %v, want it to say the issue was put on hold", err)
	}
	if len(sender.calls) != 0 {
		t.Error("sent an issue held mid-send")
	}
	var status string
	if err := database.DB().QueryRow(`SELECT status FROM newsletters WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "hold" {
		t.Errorf("status = %q, want hold", status)
	}
}
