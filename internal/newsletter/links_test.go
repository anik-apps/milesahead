package newsletter

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
)

// An email client has no base URL, so every link in a newsletter must be
// absolute. The daily digest and the admin preview used to render with an empty
// domain, which left their links pointing nowhere.
func TestPreviewNext_LinksAreAbsolute(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	createApprovedArticle(t, database, "Preview Article", "preview-article", "Preview content for the admin.", false)

	svc := NewServiceWithAI(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"}, nil, nil, "milesahead.example")
	html, err := svc.PreviewNext()
	if err != nil {
		t.Fatalf("PreviewNext() error: %v", err)
	}
	if !strings.Contains(html, `href="https://milesahead.example/article/preview-article"`) {
		t.Error("preview should link to the article with an absolute URL")
	}
	if m := regexp.MustCompile(`(?:href|src)="/[^"]*"`).FindString(html); m != "" {
		t.Errorf("preview HTML has a relative URL, which is broken in email: %s", m)
	}
}

// The site's route is /article/{slug}; /articles/ is a 404.
func TestNewsletterTemplate_UsesArticleRoute(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	createApprovedArticle(t, database, "Lead Story", "lead-story", "The only story, so it renders as the lead.", false)

	svc := NewServiceWithAI(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"}, nil, nil, "milesahead.example")
	html, err := svc.PreviewNext()
	if err != nil {
		t.Fatalf("PreviewNext() error: %v", err)
	}
	if strings.Contains(html, "/articles/") {
		t.Error("newsletter links to /articles/, which 404s; the route is /article/")
	}
	if !strings.Contains(html, `href="https://milesahead.example/article/lead-story"`) {
		t.Error("lead story should link to /article/lead-story")
	}

	tmpl, err := os.ReadFile("../../templates/email/newsletter.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tmpl), "/articles/") {
		t.Error("templates/email/newsletter.html still links to /articles/")
	}
}
