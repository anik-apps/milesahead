package mail

import (
	"fmt"
	"io"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"gopkg.in/gomail.v2"
)

// SendCloser abstracts the gomail sending interface for testability.
type SendCloser interface {
	Send(from string, to []string, msg io.WriterTo) error
	Close() error
}

// DialFunc is a function that returns a SendCloser. Injectable for testing.
type DialFunc func() (SendCloser, error)

// Sender handles outbound email via SMTP.
type Sender struct {
	from     string
	smtpUser string
	dialFn   DialFunc
}

// NewSender creates a new Sender configured with SMTP settings.
func NewSender(cfg config.SMTPConfig) *Sender {
	s := &Sender{
		from:     cfg.From,
		smtpUser: cfg.User,
	}
	s.dialFn = func() (SendCloser, error) {
		d := gomail.NewDialer(cfg.Host, cfg.Port, cfg.User, cfg.Password)
		sc, err := d.Dial()
		if err != nil {
			return nil, fmt.Errorf("dialing SMTP: %w", err)
		}
		return sc, nil
	}
	return s
}

// SetDialFunc allows overriding the dial function for testing.
func (s *Sender) SetDialFunc(fn DialFunc) {
	s.dialFn = fn
}

// SendAdminNotification sends an article review email to the admin.
// The reply-to is set to review+{token}@gmail.com for inbound processing.
func (s *Sender) SendAdminNotification(toEmail string, article *db.GeneratedArticle, verification *db.VerificationResult, reviewToken string) error {
	htmlBody, err := RenderNotification(article, verification, reviewToken, "")
	if err != nil {
		return fmt.Errorf("rendering notification: %w", err)
	}

	m := gomail.NewMessage()
	m.SetHeader("From", s.from)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", fmt.Sprintf("Review: %s", article.Title))
	m.SetHeader("Reply-To", fmt.Sprintf("review+%s@gmail.com", reviewToken))
	m.SetBody("text/html", htmlBody)

	return s.send(m)
}

// SendConfirmation sends a subscription confirmation email.
func (s *Sender) SendConfirmation(toEmail, token, domain string) error {
	htmlBody, err := RenderConfirmation(token, domain)
	if err != nil {
		return fmt.Errorf("rendering confirmation: %w", err)
	}

	m := gomail.NewMessage()
	m.SetHeader("From", s.from)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", "Confirm your subscription to MilesAhead")
	m.SetBody("text/html", htmlBody)

	return s.send(m)
}

// SendNewsletter sends the newsletter to all confirmed subscribers.
func (s *Sender) SendNewsletter(subject, htmlContent string, recipients []string) error {
	if len(recipients) == 0 {
		return nil
	}

	sc, err := s.dialFn()
	if err != nil {
		return err
	}
	defer sc.Close()

	for _, to := range recipients {
		m := gomail.NewMessage()
		m.SetHeader("From", s.from)
		m.SetHeader("To", to)
		m.SetHeader("Subject", subject)
		m.SetBody("text/html", htmlContent)

		if err := gomail.Send(sc, m); err != nil {
			return fmt.Errorf("sending newsletter to %s: %w", to, err)
		}
		m.Reset()
	}

	return nil
}

// SendVoteAlert notifies the admin of high downvotes on an article.
func (s *Sender) SendVoteAlert(toEmail string, articleID int64, articleTitle string) error {
	subject := fmt.Sprintf("Vote Alert: %s", articleTitle)
	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="margin:0;padding:0;background-color:#f4f4f4;font-family:Arial,Helvetica,sans-serif;">
<table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f4f4f4;">
<tr><td align="center" style="padding:20px 0;">
<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="background-color:#ffffff;border-radius:8px;overflow:hidden;">
<tr><td style="background-color:#c62828;padding:24px 32px;">
<h1 style="margin:0;color:#ffffff;font-size:20px;">Vote Alert</h1>
</td></tr>
<tr><td style="padding:32px;">
<h2 style="margin:0 0 16px 0;color:#1a1a2e;">High Downvotes Detected</h2>
<p style="color:#555;font-size:15px;line-height:1.6;">
The article <strong>"%s"</strong> (ID: %d) has received a high number of downvotes and may need attention.
</p>
</td></tr>
<tr><td style="background-color:#f8f9fa;padding:16px 32px;border-top:1px solid #e0e0e0;">
<p style="margin:0;color:#999;font-size:12px;">Automated alert from MilesAhead.</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`, articleTitle, articleID)

	m := gomail.NewMessage()
	m.SetHeader("From", s.from)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", subject)
	m.SetBody("text/html", body)

	return s.send(m)
}

// send dials and sends a single message.
func (s *Sender) send(m *gomail.Message) error {
	sc, err := s.dialFn()
	if err != nil {
		return err
	}
	defer sc.Close()

	if err := gomail.Send(sc, m); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	return nil
}
