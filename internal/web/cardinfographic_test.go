package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// Every configured infographic needs both images committed, or the sidebar
// renders a broken teaser and the viewer opens on nothing.
func TestCardInfographics_HaveCommittedImages(t *testing.T) {
	if len(cardInfographics) == 0 {
		t.Fatal("cardInfographics is empty")
	}
	for slug, ig := range cardInfographics {
		if strings.TrimSpace(ig.Alt) == "" {
			t.Errorf("cardInfographics[%q] has no alt text", slug)
		}
		if strings.TrimSpace(ig.Checked) == "" {
			t.Errorf("cardInfographics[%q] has no checked date", slug)
		}
		if len(ig.Facts) == 0 {
			t.Errorf("cardInfographics[%q] lists no facts, so nothing ties the image to the page", slug)
		}
		for _, name := range []string{"infographic-" + slug + ".jpg", "infographic-" + slug + "-teaser.jpg"} {
			path := filepath.Join("..", "..", "static", "img", name)
			if _, err := os.Stat(path); err != nil {
				t.Errorf("cardInfographics[%q] has no committed image at %s: %v", slug, path, err)
			}
		}
	}
}

// An image cannot be corrected by a reseed the way page prose can. Each figure
// the image states must still appear in the card's own text, so changing a fee
// or a credit in cards.json without updating the image fails here instead of
// shipping a page that contradicts itself.
func TestCardInfographics_FactsMatchCardProse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "cmd", "seed-cards", "cards.json"))
	if err != nil {
		t.Fatalf("reading cards.json: %v", err)
	}
	var cards []struct {
		Slug          string `json:"slug"`
		ScoutsVerdict string `json:"scouts_verdict"`
		Sections      []struct {
			Content string `json:"content"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &cards); err != nil {
		t.Fatalf("parsing cards.json: %v", err)
	}

	tags := regexp.MustCompile(`<[^>]+>`)
	prose := map[string]string{}
	for _, c := range cards {
		var b strings.Builder
		for _, s := range c.Sections {
			b.WriteString(s.Content)
			b.WriteString(" ")
		}
		b.WriteString(c.ScoutsVerdict)
		prose[c.Slug] = strings.ToLower(html.UnescapeString(tags.ReplaceAllString(b.String(), " ")))
	}

	for slug, ig := range cardInfographics {
		text, ok := prose[slug]
		if !ok {
			t.Errorf("cardInfographics[%q] has no matching card in cards.json", slug)
			continue
		}
		for _, fact := range ig.Facts {
			if !strings.Contains(text, strings.ToLower(fact)) {
				t.Errorf("infographic for %q states %q, but the card's text no longer does; update the image or the fact list", slug, fact)
			}
		}
	}
}

func infographicCardData() map[string]interface{} {
	data := baseCardData()
	data["Infographic"] = cardInfographic{
		Alt:     `Amex Platinum infographic. $895 annual fee. "Quoted" & <tagged> text.`,
		Checked: "Sep 22, 2026",
		Facts:   []string{"$895"},
	}
	data["InfographicImage"] = "/static/img/infographic-amex-platinum.jpg"
	data["InfographicTeaser"] = "/static/img/infographic-amex-platinum-teaser.jpg"
	return data
}

func TestCardPage_InfographicReplacesCardArt(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())

	for _, want := range []string{
		`data-ig-open`,
		`href="/static/img/infographic-amex-platinum.jpg"`,
		`src="/static/img/infographic-amex-platinum-teaser.jpg"`,
		`id="ig-lightbox"`,
		`/static/js/card-infographic.js`,
		`Figures checked Sep 22, 2026`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card page with an infographic should contain %q", want)
		}
	}
	// The teaser takes the card-art slot in both the sidebar and the mobile
	// header; the infographic already shows the card.
	if strings.Contains(out, "card-placeholder.svg") {
		t.Error("card art should be replaced by the infographic teaser")
	}
	if got := strings.Count(out, `class="ig-teaser"`); got != 2 {
		t.Errorf("expected a teaser in the sidebar and the mobile header, got %d", got)
	}
}

// The full image is about 400 KB. It must not download until the viewer opens,
// so it is referenced only by the link href and the viewer's data-src.
func TestCardPage_InfographicFullImageNotEagerlyLoaded(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())

	// \s before src so data-src="..." does not count as a match.
	eager := regexp.MustCompile(`\ssrc="/static/img/infographic-amex-platinum\.jpg"`)
	if eager.MatchString(out) {
		t.Error("the full infographic must not be in an eager src attribute")
	}
	if !strings.Contains(out, `data-src="/static/img/infographic-amex-platinum.jpg"`) {
		t.Error("the viewer should carry the full image as data-src")
	}
}

func TestCardPage_InfographicAltIsEscaped(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())

	if strings.Contains(out, "<tagged>") {
		t.Error("alt text must be HTML-escaped")
	}
	if !strings.Contains(out, "&lt;tagged&gt;") {
		t.Error("expected the escaped alt text in the viewer image")
	}
}

func TestCardPage_NoInfographicKeepsCardArt(t *testing.T) {
	out := renderRealCardTemplate(t, baseCardData())

	if !strings.Contains(out, "card-placeholder.svg") {
		t.Error("a card without an infographic should still show its card art")
	}
	for _, unwanted := range []string{`data-ig-open`, `id="ig-lightbox"`, `card-infographic.js`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("card page without an infographic should not contain %q", unwanted)
		}
	}
}

// setupRealTemplateCardApp builds an App against the repository's real
// templates, so the route-level tests exercise the shipped markup rather than
// stubs that would pass whatever the real template does.
func setupRealTemplateCardApp(t *testing.T) *App {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	cfg := &config.Config{Server: config.ServerConfig{Port: 8080, Domain: "localhost"}}
	app, err := NewAppWithTemplateDir(cfg, database, nil, nil, nil, nil, context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("failed to create app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)
	return app
}

func createPublishedCard(t *testing.T, app *App, slug, title string) {
	t.Helper()
	if _, err := app.db.CreateReferencePage(&db.ReferencePage{
		Slug:          slug,
		Title:         title,
		Category:      "travel",
		Issuer:        "Amex",
		SectionsJSON:  `[{"key":"overview","title":"Overview","content":"<p>Overview body.</p>"}]`,
		ScoutsVerdict: "Worth it if you use the credits.",
		ImageURL:      "https://example.com/card-art-" + slug + ".png",
		Status:        "published",
	}); err != nil {
		t.Fatalf("failed to create card %s: %v", slug, err)
	}
}

func getCardPage(t *testing.T, app *App, slug string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/cards/"+slug, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /cards/%s: expected 200, got %d", slug, rec.Code)
	}
	return rec.Body.String()
}

// Integration: the real route, real templates and the real map. The infographic
// is public, not behind ?beta=1 like the video.
func TestHandleCard_ServesInfographicForConfiguredCard(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, "amex-platinum", "American Express Platinum Card")

	body := getCardPage(t, app, "amex-platinum")

	for _, want := range []string{
		`href="/static/img/infographic-amex-platinum.jpg"`,
		`src="/static/img/infographic-amex-platinum-teaser.jpg"`,
		`id="ig-lightbox"`,
		`Figures checked ` + cardInfographics["amex-platinum"].Checked,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /cards/amex-platinum should contain %q", want)
		}
	}
	// The card art still appears in og:image and JSON-LD, which is right; it must
	// just not be rendered as a visible image on the page.
	if strings.Contains(body, `<img src="https://example.com/card-art-amex-platinum.png"`) {
		t.Error("configured card should show the teaser in place of its card art")
	}
	if strings.Contains(body, `name="robots"`) {
		t.Error("the infographic is public and must not add a noindex tag")
	}
}

func TestHandleCard_OtherCardsUnchanged(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, "chase-sapphire-preferred", "Chase Sapphire Preferred")

	body := getCardPage(t, app, "chase-sapphire-preferred")

	if !strings.Contains(body, `<img src="https://example.com/card-art-chase-sapphire-preferred.png"`) {
		t.Error("a card without an infographic should still render its card art")
	}
	if strings.Contains(body, `id="ig-lightbox"`) || strings.Contains(body, "card-infographic.js") {
		t.Error("a card without an infographic should not render the viewer or its script")
	}
}
