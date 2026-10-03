package newsletter

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// Card art is served from the site's own /static/ directory. An email client has
// no base URL to resolve a relative src against, so the Card of the Week image
// must be absolute or it renders as a broken image in every inbox.
func TestLoadCardOfWeekData_ImageURLIsAbsolute(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewServiceWithAI(database, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"}, nil, nil, "milesahead.example")

	for _, tc := range []struct {
		slug, stored, want string
	}{
		{"self-hosted", "/static/img/cards/self-hosted.png", "https://milesahead.example/static/img/cards/self-hosted.png"},
		{"already-absolute", "https://cdn.example.com/card.png", "https://cdn.example.com/card.png"},
	} {
		id, err := database.CreateReferencePage(&db.ReferencePage{
			Slug: tc.slug, Title: tc.slug, Category: "travel", Issuer: "Test",
			SectionsJSON: "[]", ImageURL: tc.stored, Status: "published",
		})
		if err != nil {
			t.Fatalf("creating card %s: %v", tc.slug, err)
		}
		cow := svc.loadCardOfWeekData(id)
		if cow == nil {
			t.Fatalf("loadCardOfWeekData(%s) returned nil", tc.slug)
		}
		if cow.ImageURL != tc.want {
			t.Errorf("%s: ImageURL = %q, want %q", tc.slug, cow.ImageURL, tc.want)
		}
	}
}
