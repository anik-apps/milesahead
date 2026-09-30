package newsletter

import (
	"os"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
)

// The masthead and footer band are images, so like links they must be absolute.
func TestPreviewNext_BrandImagesAreAbsolute(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	createApprovedArticle(t, database, "Preview Article", "preview-article", "Preview content for the admin.", false)

	svc := NewServiceWithAI(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"}, nil, nil, "milesahead.example")
	html, err := svc.PreviewNext()
	if err != nil {
		t.Fatalf("PreviewNext() error: %v", err)
	}
	for _, want := range []string{
		`src="https://milesahead.example/static/img/brand/email-lockup.png"`,
		`src="https://milesahead.example/static/img/brand/email-band.png"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("preview HTML should contain %s", want)
		}
	}
	if strings.Contains(html, "Miles Ahead") {
		t.Error(`the name is one word, "MilesAhead"`)
	}
}

// Outlook and Gmail do not render SVG, so email brand images must be raster.
func TestNewsletterTemplate_BrandImagesAreRaster(t *testing.T) {
	tmpl, err := os.ReadFile("../../templates/email/newsletter.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tmpl), ".svg") {
		t.Error("the newsletter references an SVG, which Outlook and Gmail will not show")
	}
	for _, img := range []string{"email-lockup.png", "email-band.png"} {
		if _, err := os.Stat("../../static/img/brand/" + img); err != nil {
			t.Errorf("newsletter image %s is missing: %v", img, err)
		}
	}
}
