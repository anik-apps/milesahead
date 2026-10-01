package main

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/web"
)

// main hands the one SMTP sender to the web app for confirmation emails and
// to the newsletter; it must satisfy both.
var (
	_ web.ConfirmationMailer = (*mail.Sender)(nil)
	_ newsletter.MailSender  = (*mail.Sender)(nil)
)

func TestSMTPLooksUnset(t *testing.T) {
	ok := config.SMTPConfig{Host: "smtp.email.example.com", Port: 587, User: "u", Password: "p", From: "noreply@milesahead.example"}
	if smtpLooksUnset(ok) {
		t.Error("a complete SMTP setup should not be reported as unset")
	}
	for name, mod := range map[string]func(*config.SMTPConfig){
		"no host":              func(c *config.SMTPConfig) { c.Host = "" },
		"localhost":            func(c *config.SMTPConfig) { c.Host = "localhost" },
		"no from":              func(c *config.SMTPConfig) { c.From = "" },
		"placeholder password": func(c *config.SMTPConfig) { c.Password = "placeholder" },
		"unexpanded user":      func(c *config.SMTPConfig) { c.User = "${SMTP_USER}" },
	} {
		c := ok
		mod(&c)
		if !smtpLooksUnset(c) {
			t.Errorf("%s: should be reported as unset", name)
		}
	}
}
