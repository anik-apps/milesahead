package web

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/newsletter"
)

// Integration, real templates: every public page links to the sign-up from
// the header and the footer, and the header link is marked current on the
// sign-up page itself.
func TestNewsletterPlacements_HeaderAndFooter(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	for _, path := range []string{"/", "/cards/", "/compare", "/subscribe"} {
		body := getPage(t, app, path)
		if !strings.Contains(body, `<a href="/subscribe" class="nav-subscribe"`) || !strings.Contains(body, `<span class="nav-subscribe-label">Newsletter</span>`) {
			t.Errorf("%s: header should have the Newsletter link", path)
		}
		footer := body[strings.Index(body, `<footer class="site-footer">`):]
		if !strings.Contains(footer, `<a href="/subscribe">Newsletter</a>`) {
			t.Errorf("%s: footer should link to the newsletter", path)
		}
	}
	if body := getPage(t, app, "/subscribe"); !strings.Contains(body, `class="nav-subscribe" aria-current="page"`) {
		t.Error("on /subscribe the header Newsletter link should be marked current")
	}
	if body := getPage(t, app, "/"); strings.Contains(body, `class="nav-subscribe" aria-current`) {
		t.Error("the header Newsletter link should be current only on the sign-up page")
	}
}

// The home hero has one small link to the sign-up, inside its subline.
func TestNewsletterPlacements_HomeHero(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/")
	hero := body[strings.Index(body, `<section class="hero-strip">`):]
	hero = hero[:strings.Index(hero, "</section>")]
	if !strings.Contains(hero, `<a class="hero-signup" href="/subscribe">Get it weekly by email`) {
		t.Errorf("the hero should link to the sign-up:\n%s", hero)
	}
	if strings.Contains(hero, "<form") {
		t.Error("the hero should stay compact: a link, not a form")
	}
}

// Every article ends with a sign-up box after Scout's Take (or after the
// story when there is none): an email field posting to /subscribe, the same
// honeypot as the sign-up page, and the one-line privacy promise.
func TestNewsletterPlacements_ArticleSignupBox(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	publishStory(t, app, db.GeneratedArticle{Title: "No Take Story", Slug: "no-take", Content: "<p>Body.</p>", Type: "news"}, 2)

	body := getPage(t, app, "/article/flex-250")
	inOrder(t, body,
		`<section class="scouts-take"`,
		`<section class="article-signup" aria-labelledby="article-signup-title">`,
		`<section class="card-embeds"`,
	)
	box := body[strings.Index(body, `<section class="article-signup"`):]
	box = box[:strings.Index(box, "</section>")]
	for _, want := range []string{
		`<form method="POST" action="/subscribe" class="article-signup-form">`,
		`<label for="article-signup-email" class="visually-hidden">Email address</label>`,
		`<input type="email" id="article-signup-email" name="email"`,
		`autocomplete="email"`,
		`<div class="hp-field" aria-hidden="true">`,
		`name="website" tabindex="-1" autocomplete="off"`,
		`<button type="submit">Subscribe</button>`,
		`One email a week. Unsubscribe anytime. We only use your address to send the newsletter.`,
	} {
		if !strings.Contains(box, want) {
			t.Errorf("sign-up box missing %s", want)
		}
	}
	if strings.Count(body, `id="article-signup-email"`) != 1 {
		t.Error("the article should have exactly one sign-up box")
	}

	plain := getPage(t, app, "/article/no-take")
	inOrder(t, plain, `<div class="article-content">`, `<section class="article-signup"`, `<div class="article-feedback">`)
}

// A sign-up from the article box takes the same path as the sign-up page:
// the honeypot is honoured, and a real address lands on "check your inbox".
func TestNewsletterPlacements_ArticleBoxPostsLikeTheForm(t *testing.T) {
	app, mailer, _ := newsletterApp(t)
	if rec := do(t, app, "POST", "/subscribe", "email=bot%40example.com&website=http%3A%2F%2Fspam"); rec.Code != 303 {
		t.Fatalf("honeypot sign-up: status %d", rec.Code)
	}
	rec := subscribe(t, app, "reader%40example.com")
	if rec.Code != 303 || rec.Header().Get("Location") != "/subscribe/check-email" {
		t.Fatalf("sign-up: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if mailer.count() != 1 || !strings.HasPrefix(mailer.sent[0], "reader@example.com ") {
		t.Errorf("only the real reader should get a confirmation; sent %v", mailer.sent)
	}
}

// The sign-up page says what readers get, how often (from the send
// schedule) and what the address is used for.
func TestSubscribePage_SaysWhatHowOftenAndWhy(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	body := getPage(t, app, "/subscribe")
	for _, want := range []string{
		"The week&rsquo;s best miles, points and credit card deals, and the ones about to end.",
		"One email, " + newsletter.WeeklySendWhen + ".",
		"One email a week. Unsubscribe anytime. We only use your address to send the newsletter.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-up page should say %q", want)
		}
	}
}

// The admin has its own shell: none of the sign-up placements appear there.
func TestNewsletterPlacements_NotOnAdminPages(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Admin Article")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	for _, path := range []string{"/admin/dashboard", "/admin/newsletter", "/admin/subscribers", "/admin/published", "/admin/articles/" + itoa(id) + "/cards"} {
		rec := doAdminGET(t, app, path, session)
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, placement := range []string{"nav-subscribe", "article-signup", "hero-signup", `<a href="/subscribe">Newsletter</a>`} {
			if strings.Contains(body, placement) {
				t.Errorf("%s: admin page carries the public %s", path, placement)
			}
		}
	}
}
