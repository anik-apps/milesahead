package mail

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"

	"github.com/kumaranik/milesahead/internal/db"
)

// templateDir is the base directory for email templates.
// It can be overridden for testing.
var templateDir = "templates/email"

type notificationData struct {
	Article      *db.GeneratedArticle
	Verification *db.VerificationResult
	Token        string
	Domain       string
}

// confirmData is what the confirmation email shows. The links are
// absolute: an email client has no base URL.
type confirmData struct {
	Domain         string
	ConfirmURL     string
	UnsubscribeURL string
	// When is how often the newsletter comes, "every Saturday at 8:00 AM
	// ET" (newsletter.WeeklySendWhen); empty, the email says "once a week".
	When string
}

// RenderNotification renders the admin notification email HTML for an article review.
func RenderNotification(article *db.GeneratedArticle, verification *db.VerificationResult, token, domain string) (string, error) {
	tmplPath := filepath.Join(templateDir, "notification.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading notification template: %w", err)
	}

	tmpl, err := template.New("notification").Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("parsing notification template: %w", err)
	}

	data := notificationData{
		Article:      article,
		Verification: verification,
		Token:        token,
		Domain:       domain,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing notification template: %w", err)
	}

	return buf.String(), nil
}

// RenderConfirmation renders the subscriber confirmation email, as HTML and
// as plain text, for the subscriber with token on the site at domain (a bare
// host, "milesahead.example"). when is how often the newsletter comes; see
// confirmData.
func RenderConfirmation(token, domain, when string) (htmlBody, textBody string, err error) {
	tmplPath := filepath.Join(templateDir, "confirm.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", "", fmt.Errorf("reading confirmation template: %w", err)
	}

	tmpl, err := template.New("confirm").Parse(string(tmplContent))
	if err != nil {
		return "", "", fmt.Errorf("parsing confirmation template: %w", err)
	}

	base := "https://" + domain
	data := confirmData{
		Domain:         domain,
		ConfirmURL:     base + "/subscribe/confirm?token=" + url.QueryEscape(token),
		UnsubscribeURL: base + "/unsubscribe?token=" + url.QueryEscape(token),
		When:           when,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", "", fmt.Errorf("executing confirmation template: %w", err)
	}

	return buf.String(), confirmationText(data), nil
}

// confirmationText is the plain-text part of the confirmation email, for
// clients that show no HTML. It says what the HTML says.
func confirmationText(d confirmData) string {
	how := "once a week"
	if d.When != "" {
		how = d.When
	}
	return "Confirm your subscription to MilesAhead\n\n" +
		"Thanks for signing up for the MilesAhead weekly email. Open this link to confirm your address and start getting it:\n\n" +
		d.ConfirmURL + "\n\n" +
		"What you'll get: the week's best miles, points and credit card deals, and the ones about to end. One email, " + how + ".\n\n" +
		"We only use your address to send the newsletter. Every email has a link to unsubscribe.\n\n" +
		"Didn't sign up? Ignore this email: you won't be subscribed unless you open the link above.\n" +
		"Unsubscribe: " + d.UnsubscribeURL + "\n"
}
