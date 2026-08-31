package web

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// renderRealCardTemplate parses the actual layout.html + public/card.html from the
// repository (not stubs) exactly as loadTemplates does, and renders them with data.
func renderRealCardTemplate(t *testing.T, data map[string]interface{}) string {
	t.Helper()

	tmpl, err := template.New("card.html").Funcs(templateFuncs).
		ParseFiles("../../templates/layout.html", "../../templates/public/card.html")
	if err != nil {
		t.Fatalf("failed to parse real card templates: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "card.html", data); err != nil {
		t.Fatalf("failed to execute card.html: %v", err)
	}
	return buf.String()
}

// baseCardData returns the template data a published card page renders with.
func baseCardData() map[string]interface{} {
	return map[string]interface{}{
		"Title": "American Express Platinum Card",
		"Card": db.ReferencePage{
			Slug:     "amex-platinum",
			Title:    "American Express Platinum Card",
			Issuer:   "Amex",
			ImageURL: "/static/img/card-placeholder.svg",
			ApplyURL: "https://example.com/apply",
		},
		"Sections": []db.ReferenceSection{
			{Key: "overview", Title: "Overview", Content: "<p>Overview body.</p>"},
		},
		"ScoutsVerdict": "Worth it if you use the credits.",
		"PersonaName":   "Scout",
		"CanonicalURL":  "https://milesahead.kumaranik.com/cards/amex-platinum",
	}
}

func TestCardPage_NoVideoByDefault(t *testing.T) {
	out := renderRealCardTemplate(t, baseCardData())

	for _, unwanted := range []string{"card-video-facade", "card-video.js", `id="card-video"`} {
		if strings.Contains(out, unwanted) {
			t.Errorf("card page without Video should not contain %q", unwanted)
		}
	}
	if strings.Contains(out, "noindex") {
		t.Error("card page without Video should not be noindexed")
	}
}

func TestCardPage_WithVideoRendersFacade(t *testing.T) {
	data := baseCardData()
	data["Video"] = cardVideo{ID: "jmcufDcHZwE", Heading: "Watch: is the $895 fee worth it?", Duration: "3:30"}
	data["VideoPoster"] = "/static/img/video-poster-amex-platinum.jpg"
	data["Noindex"] = true

	out := renderRealCardTemplate(t, data)

	for _, want := range []string{
		"card-video-facade",
		`data-video-id="jmcufDcHZwE"`,
		"/static/img/video-poster-amex-platinum.jpg",
		`id="card-video"`,
		"/static/js/card-video.js",
		`<meta name="robots" content="noindex, nofollow">`,
		"Watch the video (3:30)", // sidebar table of contents entry
	} {
		if !strings.Contains(out, want) {
			t.Errorf("card page with Video should contain %q", want)
		}
	}
}

// The point of the click-to-load facade is that the browser makes no third-party
// *request* until the visitor opts in. A plain <noscript> link is fine — it loads
// nothing — but any element that fetches a YouTube resource on page load is not.
func TestCardPage_WithVideoIssuesNoThirdPartyRequest(t *testing.T) {
	data := baseCardData()
	data["Video"] = cardVideo{ID: "jmcufDcHZwE", Heading: "Watch", Duration: "3:30"}
	data["VideoPoster"] = "/static/img/video-poster-amex-platinum.jpg"

	out := strings.ToLower(renderRealCardTemplate(t, data))

	if strings.Contains(out, "<iframe") {
		t.Error("no iframe may be present before the visitor clicks play")
	}
	// Catch any resource-loading attribute pointing at a third-party host.
	for _, attr := range []string{"src", "srcset", "poster"} {
		for _, host := range []string{"youtube", "ytimg", "youtu.be"} {
			if regexp.MustCompile(attr + `="[^"]*` + host).MatchString(out) {
				t.Errorf("%s= must not load from %q on page load", attr, host)
			}
		}
	}
}

// A noindex page must not also advertise a canonical URL: Google may consolidate
// the two and propagate the noindex to the canonical target, deindexing the real page.
func TestCardPage_NoindexOmitsCanonical(t *testing.T) {
	data := baseCardData()
	if out := renderRealCardTemplate(t, data); !strings.Contains(out, `rel="canonical"`) {
		t.Error("a normal card page should still emit a canonical link")
	}

	data["Noindex"] = true
	out := renderRealCardTemplate(t, data)
	if strings.Contains(out, `rel="canonical"`) {
		t.Error("a noindexed page must not also emit a canonical link")
	}
	if !strings.Contains(out, `content="noindex, nofollow"`) {
		t.Error("expected the robots noindex meta tag")
	}
}

// Every configured video needs its poster committed, or the page renders a broken
// image inside a black button with no error anywhere at runtime.
func TestCardVideos_HaveCommittedPosters(t *testing.T) {
	for slug, v := range cardVideos {
		if v.ID == "" {
			t.Errorf("cardVideos[%q] has an empty video ID", slug)
		}
		if v.Heading == "" || v.Duration == "" {
			t.Errorf("cardVideos[%q] is missing Heading or Duration", slug)
		}
		poster := filepath.Join("..", "..", "static", "img", "video-poster-"+slug+".jpg")
		if _, err := os.Stat(poster); err != nil {
			t.Errorf("cardVideos[%q] has no committed poster at %s: %v", slug, poster, err)
		}
	}
}

// The video block sits above the article grid, so the page's single <h1> has to
// precede it — otherwise the first heading a screen reader announces is the video
// rather than the card name.
func TestCardPage_SingleH1BeforeVideo(t *testing.T) {
	data := baseCardData()
	data["Video"] = cardVideo{ID: "jmcufDcHZwE", Heading: "Watch", Duration: "3:30"}
	data["VideoPoster"] = "/static/img/video-poster-amex-platinum.jpg"

	out := renderRealCardTemplate(t, data)

	if got := strings.Count(out, "<h1>"); got != 1 {
		t.Errorf("card page should render exactly one <h1>, got %d", got)
	}
	h1 := strings.Index(out, "<h1>")
	video := strings.Index(out, `id="card-video"`)
	if h1 == -1 || video == -1 {
		t.Fatalf("expected both an <h1> and the video block (h1=%d, video=%d)", h1, video)
	}
	if h1 > video {
		t.Error("the <h1> must appear before the video block in DOM order")
	}
}
