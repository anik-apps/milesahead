package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/jpeg"
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

// Every configured infographic needs its images committed, or the summary
// panel renders a broken teaser and the viewer opens on nothing.
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
		for _, name := range infographicFiles(slug) {
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
		Alt:     `Amex Platinum infographic. "Quoted" & <tagged> text.`,
		Text:    []string{"Annual fee: $895.", `Credits: <script>alert(1)</script> & "more".`},
		Checked: "Sep 22, 2026",
		Facts:   []string{"$895"},
	}
	data["InfographicImage"] = "/static/img/infographic-amex-platinum.jpg?v=abc123"
	data["InfographicTeaser"] = "/static/img/infographic-amex-platinum-teaser.jpg?v=abc123"
	data["InfographicWidth"] = 1024
	data["InfographicHeight"] = 1536
	return data
}

// The card art stays; the infographic is a compact bar under it in the
// summary panel.
func TestCardPage_InfographicKeepsCardArt(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())

	for _, want := range []string{
		`data-ig-open`,
		`href="/static/img/infographic-amex-platinum.jpg?v=abc123"`,
		`src="/static/img/infographic-amex-platinum-teaser.jpg?v=abc123"`,
		`id="ig-lightbox"`,
		`/static/js/card-infographic.js`,
		`Figures checked Sep 22, 2026`,
		`<span class="ig-bar-title">Infographic</span>`,
		`<span class="ig-bar-cta">Expand `,
		// The viewer opens on a named control rather than the unnamed scroll area.
		`data-ig-close autofocus`,
		// The text version is offered, collapsed, rather than shown under the image.
		`<details class="ig-text">`,
		`<summary>Text version</summary>`,
		`<li>Annual fee: $895.</li>`,
		// The viewer reserves the image's real box before it loads.
		`alt="Amex Platinum infographic. &#34;Quoted&#34; &amp; &lt;tagged&gt; text." width="1024" height="1536">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card page with an infographic should contain %q", want)
		}
	}
	// One summary panel at every width, so the art and the bar render once.
	if got := strings.Count(out, `<img src="/static/img/card-placeholder.svg"`); got != 1 {
		t.Errorf("card art should render once, in the summary panel, got %d", got)
	}
	if got := strings.Count(out, `class="ig-bar"`); got != 1 {
		t.Errorf("expected one infographic bar, in the summary panel, got %d", got)
	}
	// The bar comes after the card art.
	if strings.Index(out, `card-summary-art`) > strings.Index(out, `class="ig-bar"`) {
		t.Error("the infographic bar should follow the card art")
	}
	if strings.Contains(out, `<details class="ig-text" open`) {
		t.Error("the text version must start collapsed")
	}
	// Its accessible name is its visible text, so no aria-label may override it
	// (WCAG 2.5.3 Label in Name).
	if regexp.MustCompile(`class="ig-bar-link"[^>]*aria-label=`).MatchString(out) {
		t.Error("the infographic bar link must not carry an aria-label that replaces its visible text")
	}
}

// The full image is about 400 KB. It must not download until the viewer opens,
// so it is referenced only by the link href and the viewer's data-src.
func TestCardPage_InfographicFullImageNotEagerlyLoaded(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())

	// \s before src so data-src="..." does not count as a match.
	eager := regexp.MustCompile(`\ssrc="/static/img/infographic-amex-platinum\.jpg[?"]`)
	if eager.MatchString(out) {
		t.Error("the full infographic must not be in an eager src attribute")
	}
	if !strings.Contains(out, `data-src="/static/img/infographic-amex-platinum.jpg?v=abc123"`) {
		t.Error("the viewer should carry the full image as data-src")
	}
}

// A design taller than 2:3 (a 1080x1917 source becomes 1024x1818) gets its own
// height, so the viewer reserves the right box instead of the 2:3 one.
func TestCardPage_InfographicTallImageKeepsItsProportions(t *testing.T) {
	data := infographicCardData()
	data["InfographicHeight"] = 1818
	out := renderRealCardTemplate(t, data)

	if !strings.Contains(out, `width="1024" height="1818">`) {
		t.Error("the viewer image should carry the tall image's own 1024x1818 size")
	}
	if strings.Contains(out, `height="1536"`) {
		t.Error("a tall image must not be given the 2:3 layout's height")
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
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Error("text version items must be HTML-escaped")
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("expected the escaped text version item")
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

	v := app.infographicVersions["amex-platinum"]
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(v) {
		t.Fatalf("expected a 12-hex-digit content version for amex-platinum, got %q", v)
	}
	for _, want := range []string{
		`href="/static/img/infographic-amex-platinum.jpg?v=` + v + `"`,
		`src="/static/img/infographic-amex-platinum-teaser.jpg?v=` + v + `"`,
		`id="ig-lightbox"`,
		`Figures checked ` + cardInfographics["amex-platinum"].Checked,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /cards/amex-platinum should contain %q", want)
		}
	}
	if !strings.Contains(body, `<img src="https://example.com/card-art-amex-platinum.png"`) {
		t.Error("a card with an infographic should keep its card art")
	}
	if strings.Contains(body, `name="robots"`) {
		t.Error("the infographic is public and must not add a noindex tag")
	}
}

// The route passes the height read from the JPEG, and falls back to the 2:3
// layout's height for a card whose image could not be read.
func TestHandleCard_InfographicHeightFollowsImage(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, "amex-platinum", "American Express Platinum Card")

	// Compare with the committed file rather than a fixed number, so a
	// re-exported (e.g. taller) design does not fail this test.
	want := jpegSize(t, filepath.Join("..", "..", "static", "img", "infographic-amex-platinum.jpg")).Y
	if got := app.infographicHeights["amex-platinum"]; got != want {
		t.Fatalf("expected the committed amex-platinum image to read as %d high, got %d", want, got)
	}
	if body := getCardPage(t, app, "amex-platinum"); !strings.Contains(body, fmt.Sprintf(`width="1024" height="%d">`, want)) {
		t.Errorf("the viewer should carry the image's own size, 1024x%d", want)
	}

	app.infographicHeights["amex-platinum"] = 1818
	if body := getCardPage(t, app, "amex-platinum"); !strings.Contains(body, `width="1024" height="1818">`) {
		t.Error("a taller image should be given its own height")
	}

	delete(app.infographicHeights, "amex-platinum")
	if body := getCardPage(t, app, "amex-platinum"); !strings.Contains(body, `width="1024" height="1536">`) {
		t.Error("an unreadable image should fall back to the 2:3 layout's height")
	}
}

func TestHandleCard_OtherCardsUnchanged(t *testing.T) {
	const slug = "citi-custom-cash"
	if _, ok := cardInfographics[slug]; ok {
		t.Fatalf("%s now has an infographic; pick a card without one for this test", slug)
	}
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, slug, "Citi Custom Cash")

	body := getCardPage(t, app, slug)

	if !strings.Contains(body, `<img src="https://example.com/card-art-`+slug+`.png"`) {
		t.Error("a card without an infographic should still render its card art")
	}
	if strings.Contains(body, `id="ig-lightbox"`) || strings.Contains(body, "card-infographic.js") {
		t.Error("a card without an infographic should not render the viewer or its script")
	}
}

// The alt names the image; the content lives in the structured text version,
// which must carry every figure the drift check relies on.
func TestCardInfographics_TextVersionCarriesTheFacts(t *testing.T) {
	for slug, ig := range cardInfographics {
		if n := len(ig.Alt); n > 200 {
			t.Errorf("cardInfographics[%q].Alt is %d characters; keep it a short name and put content in Text", slug, n)
		}
		if len(ig.Text) == 0 {
			t.Errorf("cardInfographics[%q] has no text version", slug)
			continue
		}
		text := strings.ToLower(strings.Join(ig.Text, " "))
		for _, fact := range ig.Facts {
			if !strings.Contains(text, strings.ToLower(fact)) {
				t.Errorf("infographic for %q states %q, but its text version does not", slug, fact)
			}
		}
	}
}

func writeInfographicFiles(t *testing.T, staticDir, slug, content string) {
	t.Helper()
	for _, name := range infographicFiles(slug) {
		if err := os.WriteFile(filepath.Join(staticDir, "img", name), []byte(content+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Replacing an image must change its URL, or browsers keep serving the old
// artwork beside the new "Figures checked" date.
func TestInfographicVersions_ChangeWithContent(t *testing.T) {
	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	for slug := range cardInfographics {
		writeInfographicFiles(t, staticDir, slug, "v1")
	}
	before := infographicVersions(staticDir)
	if len(before) != len(cardInfographics) {
		t.Fatalf("expected a version for every card, got %d of %d", len(before), len(cardInfographics))
	}
	if again := infographicVersions(staticDir); again["amex-platinum"] != before["amex-platinum"] {
		t.Error("the version must be stable for unchanged files")
	}

	writeInfographicFiles(t, staticDir, "amex-platinum", "v2")
	after := infographicVersions(staticDir)
	if after["amex-platinum"] == before["amex-platinum"] {
		t.Error("replacing a card's images must change its version")
	}
	if after["bilt-palladium"] != before["bilt-palladium"] {
		t.Error("another card's version must not change")
	}

	if err := os.Remove(filepath.Join(staticDir, "img", "infographic-bilt-palladium-teaser.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, ok := infographicVersions(staticDir)["bilt-palladium"]; ok {
		t.Error("a card with a missing file should be served unversioned")
	}
}

// The script writes every full image 1024 wide at its source's aspect ratio,
// at least as tall as the 2:3 layout, and every teaser at 256x160. An image
// added by hand at another size would be squeezed or cropped by the page.
func TestCardInfographics_CommittedImageSizes(t *testing.T) {
	staticDir := filepath.Join("..", "..", "static")
	heights := infographicHeights(staticDir)
	for slug := range cardInfographics {
		names := infographicFiles(slug)
		full := jpegSize(t, filepath.Join(staticDir, "img", names[0]))
		if full.X != infographicWidth || full.Y < infographicDefaultHeight {
			t.Errorf("%s is %dx%d; expected 1024 wide and at least 1536 high (run scripts/infographic-images.sh)", names[0], full.X, full.Y)
		}
		if heights[slug] != full.Y {
			t.Errorf("infographicHeights[%q] = %d, want %d", slug, heights[slug], full.Y)
		}
		if teaser := jpegSize(t, filepath.Join(staticDir, "img", names[1])); teaser != image.Pt(256, 160) {
			t.Errorf("%s is %dx%d; expected 256x160", names[1], teaser.X, teaser.Y)
		}
	}
}

func jpegSize(t *testing.T, path string) image.Point {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return image.Pt(cfg.Width, cfg.Height)
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
}

func TestInfographicHeights_ReadsEachImage(t *testing.T) {
	staticDir := t.TempDir()
	img := filepath.Join(staticDir, "img")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJPEG(t, filepath.Join(img, "infographic-amex-platinum.jpg"), 1024, 1536)
	writeJPEG(t, filepath.Join(img, "infographic-bilt-palladium.jpg"), 1024, 1818)
	writeJPEG(t, filepath.Join(img, "infographic-amex-gold.jpg"), 1080, 1917) // not scaled by the script
	if err := os.WriteFile(filepath.Join(img, "infographic-amex-green.jpg"), []byte("not a jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := infographicHeights(staticDir)
	if got["amex-platinum"] != 1536 {
		t.Errorf("amex-platinum: got %d, want 1536", got["amex-platinum"])
	}
	if got["bilt-palladium"] != 1818 {
		t.Errorf("bilt-palladium: got %d, want 1818", got["bilt-palladium"])
	}
	for _, slug := range []string{"amex-gold", "amex-green", "chase-sapphire-reserve"} {
		if h, ok := got[slug]; ok {
			t.Errorf("%s should fall back to the default height, got %d", slug, h)
		}
	}
	if got := infographicHeights(filepath.Join(t.TempDir(), "absent")); len(got) != 0 {
		t.Errorf("expected no heights without a static directory, got %v", got)
	}
}

func TestInfographicVersions_NoStaticDir(t *testing.T) {
	if got := infographicVersions(filepath.Join(t.TempDir(), "absent")); len(got) != 0 {
		t.Errorf("expected no versions without a static directory, got %v", got)
	}
}
