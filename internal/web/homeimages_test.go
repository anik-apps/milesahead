package web

import (
	"fmt"
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

	// Sidebar: at the top of the page on desktop, so not lazy.
	for i := 1; i <= 3; i++ {
		tag := imgTag(t, body, fmt.Sprintf("https://img.example/thumb-%d.jpg", i))
		for _, want := range []string{`width="300"`, `height="200"`, `decoding="async"`} {
			if !strings.Contains(tag, want) {
				t.Errorf("sidebar thumbnail %d %s lacks %s", i, tag, want)
			}
		}
		if strings.Contains(tag, `loading="lazy"`) {
			t.Errorf("sidebar thumbnail %d is lazy-loaded", i)
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
