package web

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// Card art used to be hotlinked from issuer sites, and 15 of 52 cards showed a
// broken or placeholder image after issuers redeployed and their build-hashed
// URLs went 404. Every card now points at its own committed copy, so an issuer
// redeploy cannot break the page.
func TestCardArt_IsSelfHosted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "seed-cards", "cards.json"))
	if err != nil {
		t.Fatalf("reading cards.json: %v", err)
	}
	var cards []struct {
		Slug     string `json:"slug"`
		ImageURL string `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatalf("parsing cards.json: %v", err)
	}
	pngMagic := []byte("\x89PNG\r\n\x1a\n")
	for _, c := range cards {
		want := "/static/img/cards/" + c.Slug + ".png"
		if c.ImageURL != want {
			t.Errorf("%s: image_url = %q, want the self-hosted %q", c.Slug, c.ImageURL, want)
			continue
		}
		b, err := os.ReadFile(filepath.Join("..", "..", strings.TrimPrefix(want, "/")))
		if err != nil {
			t.Errorf("%s: card art not committed: %v", c.Slug, err)
			continue
		}
		if !bytes.HasPrefix(b, pngMagic) {
			t.Errorf("%s: %s is not a PNG", c.Slug, want)
		}
	}
}

func TestAbsoluteURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/static/img/cards/amex-gold.png", "https://milesahead.example/static/img/cards/amex-gold.png"},
		{"https://cdn.example.com/card.png", "https://cdn.example.com/card.png"},
		{"//cdn.example.com/card.png", "//cdn.example.com/card.png"},
		{"", ""},
	} {
		if got := absoluteURL("milesahead.example", tc.in); got != tc.want {
			t.Errorf("absoluteURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// og:image and JSON-LD are read by crawlers with no base URL, so self-hosted
// card art must reach them absolute or link previews lose their image.
func TestHandleCard_SocialImageIsAbsolute(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	if _, err := app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "amex-gold", Title: "American Express Gold Card", Category: "travel", Issuer: "Amex",
		SectionsJSON: `[{"key":"overview","title":"Overview","content":"<p>Body.</p>"}]`,
		ImageURL:     "/static/img/cards/amex-gold.png", Status: "published",
	}); err != nil {
		t.Fatal(err)
	}
	body := getCardPage(t, app, "amex-gold")

	for _, want := range []string{
		`<meta property="og:image" content="https://localhost/static/img/cards/amex-gold.png">`,
		`"image":"https://localhost/static/img/cards/amex-gold.png"`,
		`<img src="/static/img/cards/amex-gold.png"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card page should contain %q", want)
		}
	}
}
