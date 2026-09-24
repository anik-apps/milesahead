package newsletter

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// Deadlines far from any run date, so these tests do not depend on the clock:
// one offer that has long ended and one that has not.
const (
	endedTitle = "Citi 25% Transfer Bonus to Leading Hotels of the World"
	liveTitle  = "Triple United Miles on IHG Stays"
	plainTitle = "Chase Freedom Flex Drops Its Foreign Transaction Fee"
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
	ended = publish(t, database, endedTitle, "citi-lhw", "September 19, 2020", true)
	live = publish(t, database, liveTitle, "united-ihg", "September 30, 2099", false)
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
// them.
func TestDaily_NothingLiveSendsNothing(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	publish(t, database, endedTitle, "citi-lhw", "September 19, 2020", false)
	subscribe(t, database)

	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	if err := svc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Errorf("sent a daily made only of ended offers")
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
	ended := publish(t, database, endedTitle, "citi-lhw", "September 19, 2020", false)
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
