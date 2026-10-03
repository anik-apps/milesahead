package main

import (
	"log/slog"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/vote"
)

// voteAlertSender is the part of mail.Sender vote alerts use.
type voteAlertSender interface {
	SendVoteAlert(to string, a mail.VoteAlert) error
}

// voteAlertMailer returns what emails an opened vote alert to
// votes.alert_email, or nil when none should be: no address is set, or SMTP
// looks unset. Every alert is listed on the admin Published page either
// way. Alerts used to go to mail.smtp.from, the unread noreply address.
// It says, once at startup, which it is. The returned func sends at once;
// main runs it off the voter's request.
func voteAlertMailer(cfg *config.Config, database *db.Database, sender voteAlertSender, smtpUnset bool) func(vote.Alert) {
	to := cfg.Votes.AlertEmail
	switch {
	case to == "":
		slog.Info("vote alerts are shown on the admin Published page and not emailed (votes.alert_email is empty)")
		return nil
	case smtpUnset:
		slog.Warn("vote alerts are shown on the admin Published page and not emailed: votes.alert_email is set but SMTP is not configured")
		return nil
	}
	slog.Info("vote alerts are shown on the admin Published page and emailed to votes.alert_email")
	base := "https://" + cfg.Server.Domain
	return func(a vote.Alert) {
		va := mail.VoteAlert{
			ArticleID: a.ArticleID, Upvotes: a.Upvotes, Downvotes: a.Downvotes,
			WindowHours: int(a.Window.Hours()), AdminURL: base + "/admin/published",
		}
		if art, err := database.GetGeneratedArticle(a.ArticleID); err == nil && art != nil {
			va.Title = art.Title
			va.ArticleURL = base + "/article/" + art.Slug
		}
		if err := sender.SendVoteAlert(to, va); err != nil {
			slog.Error("vote alert email failed", "article_id", a.ArticleID, "error", err)
			return
		}
		slog.Info("vote alert emailed", "article_id", a.ArticleID)
	}
}
