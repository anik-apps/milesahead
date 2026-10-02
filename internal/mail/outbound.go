package mail

import (
	"errors"
	"fmt"
	"html"
	"io"
	netmail "net/mail"
	"net/url"
	"strings"

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
	// newsletterWhen is how often the newsletter comes, for the
	// confirmation email; see SetNewsletterSchedule.
	newsletterWhen string
	// unsubscribeMailbox, when set, gets a mailto in List-Unsubscribe; see
	// SetUnsubscribeMailbox.
	unsubscribeMailbox string
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

// SetNewsletterSchedule sets how the confirmation email says the
// newsletter comes, "every Saturday at 8:00 AM ET"
// (newsletter.WeeklySendWhen). Unset, it says "once a week".
func (s *Sender) SetNewsletterSchedule(when string) {
	s.newsletterWhen = when
}

// SendConfirmation sends a subscription confirmation email, in plain text
// and HTML, with absolute links to https://domain.
func (s *Sender) SendConfirmation(toEmail, token, domain string) error {
	htmlBody, textBody, err := RenderConfirmation(token, domain, s.newsletterWhen)
	if err != nil {
		return fmt.Errorf("rendering confirmation: %w", err)
	}

	m := gomail.NewMessage()
	m.SetHeader("From", s.from)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", "Confirm your subscription to MilesAhead")
	// The reader's own unsubscribe link, as on every newsletter. Without one,
	// OCI Email Delivery adds its own List-Unsubscribe, and a reader who uses
	// it lands on OCI's suppression list while the app still counts them
	// subscribed, so no newsletter would ever reach them.
	s.setListUnsubscribe(m, "https://"+domain+"/unsubscribe?token="+url.QueryEscape(token))
	m.SetBody("text/plain", textBody)
	m.AddAlternative("text/html", htmlBody)

	return s.send(m)
}

// Recipient is one subscriber a newsletter goes to, with the link that
// unsubscribes that subscriber alone.
type Recipient struct {
	Email string
	// UnsubscribeURL is the subscriber's own absolute unsubscribe link,
	// https://DOMAIN/unsubscribe?token=... GET shows a confirm button; POST
	// (the button, or a mail provider's RFC 8058 one-click request)
	// unsubscribes.
	UnsubscribeURL string
}

// UnsubscribeURLPlaceholder stands in a newsletter's HTML wherever the
// reader's own unsubscribe link goes. The issue is rendered once, and
// SendNewsletter puts each recipient's link in place of it.
const UnsubscribeURLPlaceholder = "https://unsubscribe.invalid/milesahead-reader-link"

// SendNewsletter sends the newsletter to each of recipients in turn, and
// returns how many of them, from the first, it reached. It stops at the
// first failure, which is then at recipients[sent].
//
// Each copy carries its recipient's own unsubscribe link, in the body (in
// place of UnsubscribeURLPlaceholder) and in the List-Unsubscribe and
// List-Unsubscribe-Post headers (RFC 2369, RFC 8058), which Gmail and Yahoo
// require of bulk senders; see setListUnsubscribe. Nothing is sent unless every copy can have one:
// htmlContent must hold the placeholder and every recipient an https link.
func (s *Sender) SendNewsletter(subject, htmlContent string, recipients []Recipient) (sent int, err error) {
	if len(recipients) == 0 {
		return 0, nil
	}
	if !strings.Contains(htmlContent, UnsubscribeURLPlaceholder) {
		return 0, errors.New("newsletter not sent: its HTML has no place for the reader's unsubscribe link")
	}
	for i, r := range recipients {
		if !strings.HasPrefix(r.UnsubscribeURL, "https://") {
			return 0, fmt.Errorf("newsletter not sent: subscriber %d has no https unsubscribe link", i+1)
		}
	}

	sc, err := s.dialFn()
	if err != nil {
		return 0, err
	}
	defer sc.Close()

	for _, r := range recipients {
		m := gomail.NewMessage()
		m.SetHeader("From", s.from)
		m.SetHeader("To", r.Email)
		m.SetHeader("Subject", subject)
		s.setListUnsubscribe(m, r.UnsubscribeURL)
		m.SetBody("text/html", strings.ReplaceAll(htmlContent, UnsubscribeURLPlaceholder, html.EscapeString(r.UnsubscribeURL)))

		if err := gomail.Send(sc, m); err != nil {
			return sent, fmt.Errorf("sending newsletter to %s: %w", r.Email, err)
		}
		sent++
	}

	return sent, nil
}

// setListUnsubscribe adds the headers mail providers use for their own
// Unsubscribe button: the reader's https link, which takes the one-click
// POST (what Gmail and Yahoo require), and, only when a monitored mailbox is
// set (SetUnsubscribeMailbox), a mailto to it. A mailto to an address nobody
// reads would leave readers who use it thinking they had unsubscribed.
func (s *Sender) setListUnsubscribe(m *gomail.Message, unsubscribeURL string) {
	values := []string{"<" + unsubscribeURL + ">"}
	if s.unsubscribeMailbox != "" {
		values = append(values, "<mailto:"+s.unsubscribeMailbox+"?subject=unsubscribe>")
	}
	m.SetHeader("List-Unsubscribe", values...)
	m.SetHeader("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
}

// SetUnsubscribeMailbox adds a mailto to that address to every newsletter's
// List-Unsubscribe header. Set it only to a mailbox someone reads and acts
// on (newsletter.unsubscribe_mailbox). An empty or invalid address leaves the
// mailto out; it reports an invalid one.
func (s *Sender) SetUnsubscribeMailbox(addr string) error {
	s.unsubscribeMailbox = ""
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	parsed, err := netmail.ParseAddress(addr)
	if err != nil {
		return fmt.Errorf("unsubscribe mailbox %q is not an email address: %w", addr, err)
	}
	s.unsubscribeMailbox = parsed.Address
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
