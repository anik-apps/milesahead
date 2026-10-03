package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// seedHomeArticles publishes n articles with images, newest first, the first
// one featured -- enough for the featured card, the three-card sidebar and the
// grid below it.
func seedHomeArticles(t *testing.T, app *App, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		id, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
			Title:          fmt.Sprintf("Home story %d", i),
			Slug:           fmt.Sprintf("home-story-%d", i),
			Content:        "Body.",
			Type:           "deal",
			Status:         db.StatusPending,
			SourcesJSON:    "[]",
			RevisionNumber: 1,
			Featured:       i == 0,
			ImageURL:       fmt.Sprintf("https://img.example/hero-%d.jpg", i),
			ImageThumbnail: fmt.Sprintf("https://img.example/thumb-%d.jpg", i),
		})
		if err != nil {
			t.Fatalf("create article %d: %v", i, err)
		}
		if err := app.db.UpdateArticleStatus(id, db.StatusApproved); err != nil {
			t.Fatalf("approve article %d: %v", i, err)
		}
		// Distinct publish times keep the order deterministic.
		if _, err := app.db.DB().Exec(`UPDATE generated_articles SET published_at = datetime('2026-09-23', ?) WHERE id = ?`,
			fmt.Sprintf("-%d hours", i), id); err != nil {
			t.Fatalf("date article %d: %v", i, err)
		}
	}
}

// imgTag returns the <img> element whose src is src, or fails.
func imgTag(t *testing.T, body, src string) string {
	t.Helper()
	m := regexp.MustCompile(`<img[^>]*src="` + regexp.QuoteMeta(src) + `"[^>]*>`).FindString(body)
	if m == "" {
		t.Fatalf("no <img> with src %q in the page", src)
	}
	return m
}

// Integration: the real home route and template. The featured image used to
// be an inline CSS background, which the browser only discovers once the
// stylesheet has loaded, and every thumbnail loaded eagerly with no size.
func TestHome_ImageLoadingHints(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedHomeArticles(t, app, 7) // featured + 3 sidebar + 3 grid
	body := getPage(t, app, "/")

	if strings.Contains(body, "background-image: url(") {
		t.Error("featured image is still an inline CSS background")
	}
	featured := imgTag(t, body, "https://img.example/hero-0.jpg")
	for _, want := range []string{`class="featured-image"`, `fetchpriority="high"`, `alt=""`, `width=`, `height=`} {
		if !strings.Contains(featured, want) {
			t.Errorf("featured image %s lacks %s", featured, want)
		}
	}
	if strings.Contains(featured, `loading="lazy"`) {
		t.Error("featured image is lazy-loaded; it is the likely largest paint")
	}

	// Sidebar: the first card is in the first screen on desktop, so it loads
	// eagerly; the two below it are past the fold at 1440x900 and on phones.
	for i := 1; i <= 3; i++ {
		tag := imgTag(t, body, fmt.Sprintf("https://img.example/thumb-%d.jpg", i))
		for _, want := range []string{`width="300"`, `height="200"`, `decoding="async"`} {
			if !strings.Contains(tag, want) {
				t.Errorf("sidebar thumbnail %d %s lacks %s", i, tag, want)
			}
		}
		if lazy := strings.Contains(tag, `loading="lazy"`); lazy != (i > 1) {
			t.Errorf("sidebar thumbnail %d: lazy = %v, want %v", i, lazy, i > 1)
		}
	}

	// Grid: below the fold everywhere.
	for i := 4; i <= 6; i++ {
		tag := imgTag(t, body, fmt.Sprintf("https://img.example/thumb-%d.jpg", i))
		for _, want := range []string{`loading="lazy"`, `decoding="async"`, `width="300"`, `height="200"`} {
			if !strings.Contains(tag, want) {
				t.Errorf("grid thumbnail %d %s lacks %s", i, tag, want)
			}
		}
	}

	// The display image is only for the featured card; the rest use thumbnails.
	// (Grid cards may list it in a srcset when its width is known; these
	// URLs do not say their width.)
	for i := 1; i <= 6; i++ {
		if strings.Contains(body, fmt.Sprintf("https://img.example/hero-%d.jpg", i)) {
			t.Errorf("home page references the full-size image of story %d", i)
		}
	}
}

// The featured <img> covers the card as the CSS background did: cover,
// centred, out of layout, with the link painted over it.
func TestHome_FeaturedImageStyles(t *testing.T) {
	css := readStylesheet(t)
	rule := cssRule(t, css, ".featured-image")
	for _, want := range []string{"position: absolute", "inset: 0", "object-fit: cover", "object-position: center"} {
		if !strings.Contains(rule, want) {
			t.Errorf(".featured-image lacks %q: %s", want, rule)
		}
	}
	if link := cssRule(t, css, ".featured-card a"); !strings.Contains(link, "position: relative") {
		t.Errorf(".featured-card a must be positioned to paint over the image: %s", link)
	}
}

// Integration: an article without an image still renders the featured card,
// with no empty <img>.
func TestHome_FeaturedWithoutImage(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedHomeArticles(t, app, 1)
	if _, err := app.db.DB().Exec(`UPDATE generated_articles SET image_url = '', image_thumbnail = ''`); err != nil {
		t.Fatal(err)
	}
	body := getPage(t, app, "/")
	if !strings.Contains(body, "Home story 0") {
		t.Fatal("featured story missing")
	}
	if strings.Contains(body, `class="featured-image"`) {
		t.Error("featured card renders an <img> with no image")
	}
}

func readStylesheet(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	return string(b)
}

// cssRule returns the body of the first top-level rule whose selector is
// exactly selector.
func cssRule(t *testing.T, css, selector string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(selector) + `\s*\{([^}]*)\}`).FindStringSubmatch(css)
	if m == nil {
		t.Fatalf("no %q rule in style.css", selector)
	}
	return m[1]
}

// Integration: the stylesheet URL carries a hash of the file, so a page whose
// markup depends on new CSS rules is never laid out by a copy the browser
// cached before the deploy.
func TestLayout_StylesheetURLIsVersionedByContent(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	sum := sha256.Sum256([]byte(readStylesheet(t)))
	want := `<link rel="stylesheet" href="/static/css/style.css?v=` + hex.EncodeToString(sum[:])[:12] + `">`
	if !strings.Contains(body, want) {
		t.Errorf("home page should link the stylesheet as %s", want)
	}
}

func TestVersionedAsset_Stylesheet(t *testing.T) {
	static := t.TempDir()
	if got := versionedAsset(static, stylesheetPath); got != "/static/css/style.css" {
		t.Errorf("missing file: %q, want the unversioned path", got)
	}
	if err := os.MkdirAll(filepath.Join(static, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(css string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(static, "css", "style.css"), []byte(css), 0o644); err != nil {
			t.Fatal(err)
		}
		return versionedAsset(static, stylesheetPath)
	}
	v1 := write(".a { color: red; }")
	if !regexp.MustCompile(`^/static/css/style\.css\?v=[0-9a-f]{12}$`).MatchString(v1) {
		t.Fatalf("versioned URL = %q", v1)
	}
	if again := write(".a { color: red; }"); again != v1 {
		t.Errorf("same content, different version: %q vs %q", again, v1)
	}
	if v2 := write(".a { color: blue; }"); v2 == v1 {
		t.Errorf("changed content kept version %q", v1)
	}
}

// Integration: at 769-1024 px the bottom-grid thumbnails are up to 466 CSS px
// wide, and on a 2x screen a 500px file is soft there. The grid offers the
// row's display image as a larger candidate in that range only -- at three
// columns a card is at most 361 px, and offering it there had every 2x
// desktop fetch 16 display images, 2.6x the page's image bytes. It lists only
// the two URLs the row stores, when they are two sizes of one picture, with
// the widths their URLs fix, and only when the thumbnail alone fills the
// widest card at 1x, so a 1x screen never fetches the display image.
func TestHome_GridThumbnailsOfferTheDisplayImage(t *testing.T) {
	const (
		wm     = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/"
		other  = "https://upload.wikimedia.org/wikipedia/commons/thumb/7/7e/Gate.jpg/"
		comma  = "https://upload.wikimedia.org/wikipedia/commons/thumb/a/ab/Paris,_France.jpg/"
		us     = "https://images.unsplash.com/photo-1?fit=max&fm=jpg&q=80&w="
		fl     = "https://live.staticflickr.com/65535/54420209608_0aa8ea537a"
		media  = "(min-width: 769px) and (max-width: 1024px)"
		sizes  = "calc((100vw - 92px) / 2)"
		lobby  = wm + "500px-Lobby.jpg 500w, " + wm + "1280px-Lobby.jpg 1280w"
		flickr = fl + ".jpg 500w, " + fl + "_b.jpg 1024w"
	)
	rows := []struct{ image, thumb, srcset string }{
		// featured, sidebar: never get one.
		{wm + "1280px-Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		{wm + "1280px-Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		{wm + "1280px-Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		{wm + "1280px-Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		// grid
		{wm + "1280px-Lobby.jpg", wm + "500px-Lobby.jpg", lobby},
		{fl + "_b.jpg", fl + ".jpg", flickr},
		// Unsplash's small size is 400px, less than a 466px card: a 1x
		// screen would fetch the display image.
		{us + "1080", us + "400", ""},
		// Two different crops of one photo (row 58 in the dev database).
		{"https://images.unsplash.com/photo-2?w=1200&h=400&fit=crop", "https://images.unsplash.com/photo-2?w=500&h=300&fit=crop", ""},
		// Every copy was over 1 MiB, so the resize kept the lightest, 500px
		// one as the display image, and the thumbnail is the 330px step.
		{wm + "500px-Lobby.jpg", wm + "330px-Lobby.jpg", ""},
		// The display image is no wider than the thumbnail.
		{wm + "500px-Lobby.jpg", wm + "960px-Lobby.jpg", ""},
		{wm + "500px-Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		// Two different files, or photos.
		{other + "1280px-Gate.jpg", wm + "500px-Lobby.jpg", ""},
		{"https://live.staticflickr.com/65535/1_abc_b.jpg", fl + ".jpg", ""},
		// A comma separates srcset entries.
		{comma + "1280px-Paris,_France.jpg", comma + "500px-Paris,_France.jpg", ""},
		// A Wikimedia original: its width is unknown, and it may be the
		// multi-megabyte file the resize has not replaced yet.
		{"https://upload.wikimedia.org/wikipedia/commons/6/6c/Lobby.jpg", wm + "500px-Lobby.jpg", ""},
		// Pexels URLs do not fix the width.
		{"https://images.pexels.com/photos/1/p.jpeg?h=650&w=940", "https://images.pexels.com/photos/1/p.jpeg?h=350", ""},
	}
	app := setupRealTemplateCardApp(t)
	seedHomeArticles(t, app, len(rows))
	for i, r := range rows {
		if _, err := app.db.DB().Exec(`UPDATE generated_articles SET image_url = ?, image_thumbnail = ? WHERE slug = ?`,
			r.image, r.thumb, fmt.Sprintf("home-story-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	body := getPage(t, app, "/")

	// Sidebar thumbnails are 100 px wide at every size: the thumbnail alone.
	sidebar := body[strings.Index(body, `class="sidebar-stack"`):strings.Index(body, `class="bottom-grid"`)]
	if strings.Contains(sidebar, "<picture") || strings.Contains(sidebar, "srcset") {
		t.Error("a sidebar card offers the display image")
	}
	if strings.Contains(imgTag(t, body, wm+"1280px-Lobby.jpg"), "srcset") {
		t.Error("the featured image got a srcset")
	}

	grid := regexp.MustCompile(`(?s)<picture>(.*?)</picture>`).FindAllStringSubmatch(body, -1)
	if len(grid) != len(rows)-4 {
		t.Fatalf("found %d grid thumbnails, want %d", len(grid), len(rows)-4)
	}
	for i, m := range grid {
		row := rows[i+4]
		img := regexp.MustCompile(`<img[^>]*>`).FindString(m[1])
		if src := html.UnescapeString(attr(img, "src")); src != row.thumb {
			t.Errorf("grid card %d src = %q, want the thumbnail %q", i, src, row.thumb)
		}
		// Outside the source's range, every screen gets the thumbnail.
		if strings.Contains(img, "srcset") || strings.Contains(img, "sizes") {
			t.Errorf("grid card %d: the <img> itself has a srcset: %s", i, img)
		}
		source := regexp.MustCompile(`<source[^>]*>`).FindString(m[1])
		if got := html.UnescapeString(attr(source, "srcset")); got != row.srcset {
			t.Errorf("grid card %d srcset = %q, want %q", i, got, row.srcset)
		}
		if row.srcset == "" {
			if source != "" {
				t.Errorf("grid card %d has a <source> with no srcset: %s", i, source)
			}
			continue
		}
		if got := html.UnescapeString(attr(source, "media")); got != media {
			t.Errorf("grid card %d media = %q, want %q", i, got, media)
		}
		if got := html.UnescapeString(attr(source, "sizes")); got != sizes {
			t.Errorf("grid card %d sizes = %q, want %q", i, got, sizes)
		}
	}

	// gridThumbSlotMax is the source's sizes at the top of its media range.
	if gridThumbSlotMax != (1024-92)/2 {
		t.Errorf("gridThumbSlotMax = %d; sizes gives %d px at 1024 px", gridThumbSlotMax, (1024-92)/2)
	}
	// The <picture> must not change how the <img> is laid out.
	if rule := cssRule(t, readStylesheet(t), ".card-thumb picture"); !strings.Contains(rule, "display: contents") {
		t.Errorf(".card-thumb picture must not take part in layout: %s", rule)
	}
}

// attr returns the value of the named attribute in an HTML tag, or "".
func attr(tag, name string) string {
	m := regexp.MustCompile(`\s` + name + `="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}
