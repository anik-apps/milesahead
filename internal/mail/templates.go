package mail

import (
	"bytes"
	"fmt"
	"html/template"
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

type confirmData struct {
	Token  string
	Domain string
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

// RenderConfirmation renders the subscriber confirmation email HTML.
func RenderConfirmation(token, domain string) (string, error) {
	tmplPath := filepath.Join(templateDir, "confirm.html")
	tmplContent, err := os.ReadFile(tmplPath)
	if err != nil {
		return "", fmt.Errorf("reading confirmation template: %w", err)
	}

	tmpl, err := template.New("confirm").Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("parsing confirmation template: %w", err)
	}

	data := confirmData{
		Token:  token,
		Domain: domain,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing confirmation template: %w", err)
	}

	return buf.String(), nil
}
