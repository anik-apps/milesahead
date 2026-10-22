package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/vote"
)

type fakeVoteAlertSender struct {
	to   []string
	sent []mail.VoteAlert
}

func (f *fakeVoteAlertSender) SendVoteAlert(to string, a mail.VoteAlert) error {
	f.to = append(f.to, to)
	f.sent = append(f.sent, a)
	return nil
}

// Vote alerts are emailed only to votes.alert_email, never to the noreply
// From address, and only when SMTP can send.
func TestVoteAlertMailer(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: "Hotel points devalued", Slug: "hotel-points-devalued", Content: "x", Type: "summary", Status: db.StatusApproved, SourcesJSON: "[]"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Server: config.ServerConfig{Domain: "milesahead.example"},
		Mail:   config.MailConfig{SMTP: config.SMTPConfig{From: "noreply@milesahead.example"}},
	}

	logs := captureLog(t)
	sender := &fakeVoteAlertSender{}
	if voteAlertMailer(cfg, database, sender, false) != nil {
		t.Error("with no votes.alert_email, nothing should be emailed")
	}
	if !strings.Contains(logs.String(), "not emailed (votes.alert_email is empty)") {
		t.Errorf("startup should say alerts are not emailed:\n%s", logs)
	}

	cfg.Votes.AlertEmail = "owner@example.com"
	if voteAlertMailer(cfg, database, sender, true) != nil {
		t.Error("with SMTP unset, nothing should be emailed")
	}

	notify := voteAlertMailer(cfg, database, sender, false)
	if notify == nil {
		t.Fatal("with an address and SMTP, alerts should be emailed")
	}
	notify(vote.Alert{ArticleID: id, Upvotes: 2, Downvotes: 12, Window: 24 * time.Hour})
	if len(sender.sent) != 1 || sender.to[0] != "owner@example.com" {
		t.Fatalf("sent to %v", sender.to)
	}
	got := sender.sent[0]
	want := mail.VoteAlert{ArticleID: id, Title: "Hotel points devalued", Upvotes: 2, Downvotes: 12, WindowHours: 24,
		AdminURL: "https://milesahead.example/admin/published", ArticleURL: "https://milesahead.example/article/hotel-points-devalued"}
	if got != want {
		t.Errorf("alert = %+v\nwant  %+v", got, want)
	}
}

// The deploy config carries the ALERT_EMAIL secret into votes.alert_email,
// and leaves it empty (no alert email) without it.
func TestDeployConfig_AlertEmailFromSecret(t *testing.T) {
	stubReachable(t, false)
	if cfg := deployConfig(t, nil); cfg.Votes.AlertEmail != "" {
		t.Errorf("without the secret, votes.alert_email = %q, want empty", cfg.Votes.AlertEmail)
	}
	if cfg := deployConfig(t, map[string]string{"ALERT_EMAIL": "owner@example.com"}); cfg.Votes.AlertEmail != "owner@example.com" {
		t.Errorf("with the secret, votes.alert_email = %q", cfg.Votes.AlertEmail)
	}
}

// Startup says review by email is off, and why, once; the deploy config's
// placeholders count as not configured.
func TestLogReviewByEmail(t *testing.T) {
	stubReachable(t, false)
	logs := captureLog(t)
	logReviewByEmail(deployConfig(t, nil).Mail.IMAP)
	if got := logs.String(); strings.Count(got, "review by email is off (mail.imap not configured)") != 1 {
		t.Errorf("startup log:\n%s", got)
	}
}

// The deploy writes ai.alert_email from the same ALERT_EMAIL secret, and the
// hourly check interval explicitly.
func TestDeployConfig_ModelHealthSettings(t *testing.T) {
	stubReachable(t, false)
	cfg := deployConfig(t, map[string]string{"ALERT_EMAIL": "ops@example.com"})
	if cfg.AI.AlertEmail != "ops@example.com" {
		t.Errorf("ai.alert_email = %q", cfg.AI.AlertEmail)
	}
	if cfg.AI.HealthCheckInterval != 60 {
		t.Errorf("health_check_interval = %d, want 60", cfg.AI.HealthCheckInterval)
	}
	if cfg := deployConfig(t, nil); cfg.AI.AlertEmail != "" {
		t.Errorf("no secret: ai.alert_email = %q, want empty", cfg.AI.AlertEmail)
	}
}
