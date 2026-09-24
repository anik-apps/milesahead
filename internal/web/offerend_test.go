package web

import (
	"html"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/offer"
	"golang.org/x/crypto/bcrypt"
)

// The review found these on the live home page on Sep 23, 2026, four days
// after the last of them had ended.
const endedBadge = `<span class="ended-badge">Ended</span>`

// offerApp is the real-template app with its clock fixed at 16:00 UTC on
// Sep 23, 2026 (noon in New York) and the production timezone.
func offerApp(t *testing.T) *App {
	t.Helper()
	app := setupRealTemplateCardApp(t)
	app.now = func() time.Time { return time.Date(2026, 9, 23, 16, 0, 0, 0, time.UTC) }
	app.siteLoc = offer.Location("America/New_York")
	return app
}

// publishOffer stores an approved article through the real write path, so its
// end date is derived from the Key Details exactly as in production.
func publishOffer(t *testing.T, app *App, slug, title, articleType, deadline, tagsJSON string) {
	t.Helper()
	kd := `[{"Label":"Bonus","Value":"20% transfer bonus"}]`
	if deadline != "" {
		kd = `[{"Label":"Bonus","Value":"20% transfer bonus"},{"Label":"Deadline","Value":"` + deadline + `"}]`
	}
	if tagsJSON == "" {
		tagsJSON = "[]"
	}
	id, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: title, Slug: slug, Type: articleType,
		Content:     "<p>Citi ThankYou points transfer body for " + title + ".</p>",
		TLDR:        "Summary of " + title,
		Status:      db.StatusPending,
		SourcesJSON: "[]", RevisionNumber: 1,
		KeyDetails: kd, TagsJSON: tagsJSON,
	})
	if err != nil {
		t.Fatalf("creating %s: %v", slug, err)
	}
	if err := app.db.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatalf("approving %s: %v", slug, err)
	}
	// Distinct publish times, oldest first, so list order is deterministic.
	if _, err := app.db.DB().Exec(`UPDATE generated_articles SET published_at = datetime('now', ?) WHERE id = ?`,
		"-"+strconv.FormatInt(1000-id, 10)+" minutes", id); err != nil {
		t.Fatal(err)
	}
}

// seedOffers publishes one ended offer (newest, so it would otherwise lead),
// one ending today, one ending next week and one with no end date.
func seedOffers(t *testing.T, app *App) {
	t.Helper()
	publishOffer(t, app, "no-deadline", "Chase Freedom Flex Drops Its Foreign Transaction Fee", "summary", "", `["credit-cards"]`)
	publishOffer(t, app, "ends-next-week", "Triple United Miles on IHG Stays Through Sep 30", "deal", "September 30, 2026", `["transfer-bonuses"]`)
	publishOffer(t, app, "ends-today", "Amex 30% Transfer Bonus Ends Today", "deal", "September 23, 2026", `["transfer-bonuses"]`)
	publishOffer(t, app, "ended", "Citi 25% Transfer Bonus to Leading Hotels of the World", "deal", "September 19, 2026", `["transfer-bonuses"]`)
}

// cardFor returns the listing card that links to slug, or "".
func cardFor(body, slug string) string {
	re := regexp.MustCompile(`(?s)<div class="(?:article-card|featured-card)[^"]*"[^>]*>\s*<a href="/article/` + regexp.QuoteMeta(slug) + `">.*?</a>\s*</div>`)
	return re.FindString(body)
}

// The default home view is what readers take as current: ended offers are
// left out, and an offer is still live on its last day.
func TestHome_DefaultViewExcludesEndedOffers(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)

	body := getPage(t, app, "/")
	if strings.Contains(body, "/article/ended") {
		t.Error("the default home view lists an offer that ended on Sep 19")
	}
	for _, slug := range []string{"ends-today", "ends-next-week", "no-deadline"} {
		if cardFor(body, slug) == "" {
			t.Errorf("live article %q missing from the home page", slug)
		}
	}
	if strings.Contains(body, endedBadge) {
		t.Error("nothing on the default home view should carry an Ended badge")
	}
}

// "Today" is the site's date. At 11pm in New York on the last day it is
// already the next day in UTC, and the offer must still be listed.
func TestHome_LastDayIsJudgedInSiteTimezone(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)
	app.now = func() time.Time { return time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC) }

	if cardFor(getPage(t, app, "/"), "ends-today") == "" {
		t.Error("at 11pm New York time on Sep 23 an offer ending Sep 23 should still be live")
	}

	app.siteLoc = time.UTC
	if strings.Contains(getPage(t, app, "/"), "/article/ends-today") {
		t.Error("with a UTC site the same instant is Sep 24, so the offer should have ended")
	}
}

// The generator writes dates with typographic spaces. A transfer bonus whose
// deadline reads "June\u202f11,\u202f2026" ended in June and must be treated
// like any other ended offer, not left on the home page as current.
func TestHome_DeadlineWithUnicodeSpacesIsRead(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)
	publishOffer(t, app, "cathay-marriott", "Cathay 25% Bonus on Marriott Transfers", "deal", "June\u202f11,\u202f2026", `["transfer-bonuses"]`)

	if strings.Contains(getPage(t, app, "/"), "/article/cathay-marriott") {
		t.Error("an offer that ended on June 11 is on the default home view")
	}
	if card := cardFor(getPage(t, app, "/?tag=transfer-bonuses"), "cathay-marriott"); !strings.Contains(card, endedBadge) {
		t.Errorf("the tag view should badge the ended offer, got:\n%s", card)
	}
	if !strings.Contains(getPage(t, app, "/article/cathay-marriott"), "This offer ended on June 11, 2026.") {
		t.Error("the ended offer's page should carry the banner")
	}
}

// Filtered views still list ended offers, badged next to the type badge and
// de-emphasised, and never put one in the featured slot while a live article
// is available.
func TestHome_FilteredViewsMarkEndedOffers(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)

	for _, path := range []string{"/?type=deal", "/?tag=transfer-bonuses"} {
		body := getPage(t, app, path)

		card := cardFor(body, "ended")
		if card == "" {
			t.Fatalf("%s: the ended offer should still be listed", path)
		}
		if !strings.Contains(card, `class="article-card`) || !strings.Contains(card, ` is-ended"`) {
			t.Errorf("%s: the ended offer should be a de-emphasised list card, got:\n%s", path, card)
		}
		if !strings.Contains(card, `<span class="type-badge badge-deal">deal</span> `+endedBadge) {
			t.Errorf("%s: the Ended badge should sit next to the type badge, got:\n%s", path, card)
		}
		if strings.Count(body, endedBadge) != 1 {
			t.Errorf("%s: want exactly one Ended badge, got %d", path, strings.Count(body, endedBadge))
		}
		for _, live := range []string{"ends-today", "ends-next-week"} {
			c := cardFor(body, live)
			if c == "" || strings.Contains(c, "is-ended") || strings.Contains(c, endedBadge) {
				t.Errorf("%s: live offer %q should be listed without the ended treatment:\n%s", path, live, c)
			}
		}
		if strings.Contains(body, `class="featured-card is-ended`) || strings.Contains(body, `featured-card has-image is-ended`) {
			t.Errorf("%s: an ended offer took the featured slot", path)
		}
		if !strings.Contains(body, `<div class="featured-card">`+"\n"+`        <a href="/article/ends-today">`) {
			t.Errorf("%s: the newest live offer should be featured", path)
		}
	}
}

// Search lists ended articles too, marked.
func TestSearch_MarksEndedOffers(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)

	body := getPage(t, app, "/search?q=Citi")
	item := regexp.MustCompile(`(?s)<li class="search-item[^"]*">\s*(?:<span class="ended-badge">Ended</span> )?<a href="/article/ended".*?</li>`).FindString(body)
	if item == "" {
		t.Fatalf("search should list the ended offer:\n%s", body)
	}
	if !strings.Contains(item, `<li class="search-item is-ended">`) || !strings.Contains(item, endedBadge) {
		t.Errorf("the ended search result should carry the badge and ended class:\n%s", item)
	}
	live := regexp.MustCompile(`(?s)<li class="search-item[^"]*">\s*<a href="/article/ends-next-week".*?</li>`).FindString(body)
	if live == "" || strings.Contains(live, "is-ended") {
		t.Errorf("a live search result should not be marked ended:\n%s", live)
	}
}

// The article page of an ended offer says so near the top, as a note, and
// otherwise stays as it was: indexable, content intact.
func TestArticle_EndedOfferShowsBanner(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)

	body := getPage(t, app, "/article/ended")
	banner := `<p class="offer-ended-banner" role="note">This offer ended on September 19, 2026.</p>`
	i := strings.Index(body, banner)
	if i < 0 {
		t.Fatalf("ended article should show %s", banner)
	}
	if h1 := strings.Index(body, "<h1>Citi 25% Transfer Bonus"); h1 < 0 || i > h1 {
		t.Error("the banner should come before the headline")
	}
	if !strings.Contains(body, `<span class="type-badge badge-deal">deal</span> `+endedBadge) {
		t.Error("the article header should carry the Ended badge next to the type badge")
	}
	for _, want := range []string{
		"Citi ThankYou points transfer body",                     // body intact
		"<li><strong>Deadline:</strong> September 19, 2026</li>", // key details intact
		`<link rel="canonical" href="https://localhost/article/ended">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ended article should still contain %s", want)
		}
	}
	if strings.Contains(body, `name="robots"`) {
		t.Error("an ended offer's page must stay indexable")
	}

	for _, slug := range []string{"ends-today", "ends-next-week", "no-deadline"} {
		if b := getPage(t, app, "/article/"+slug); strings.Contains(b, "offer-ended-banner") || strings.Contains(b, endedBadge) {
			t.Errorf("live article %q should not show the ended banner or badge", slug)
		}
	}
}

// A card page's Recent Changes link to articles; the ones whose offer ended
// are marked.
func TestCardPage_RecentChangesMarkEndedOffers(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)
	if _, err := app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "citi-strata-premier", Title: "Citi Strata Premier", Category: "travel", Issuer: "Citi",
		SectionsJSON:  `[{"key":"overview","title":"Overview","content":"<p>Overview.</p>"}]`,
		ScoutsVerdict: "Good.", Status: "published",
		RecentChangesJSON: `[{"date":"2026-09-10","summary":"Article published: Citi 25% to LHW","article_slug":"ended"},` +
			`{"date":"2026-07-22","summary":"Article published: Triple United Miles","article_slug":"ends-next-week"}]`,
	}); err != nil {
		t.Fatal(err)
	}

	body := getCardPage(t, app, "citi-strata-premier")
	if !strings.Contains(body, `<li class="is-ended">`) ||
		!strings.Contains(body, `<span class="change-summary">`+endedBadge+` Article published: Citi 25% to LHW</span>`) {
		t.Errorf("the recent change linking to the ended offer should be marked:\n%s", body)
	}
	if strings.Count(body, endedBadge) != 1 {
		t.Errorf("want one Ended badge on the card page, got %d", strings.Count(body, endedBadge))
	}
}

// The ended treatment must stay readable: badge text and titles at WCAG AA
// (4.5:1) against the page and card backgrounds, in both themes.
func TestEndedStyles_MeetContrast(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	vars := func(selector string) map[string]string {
		// Every block for the selector, later ones overriding earlier ones.
		out := map[string]string{}
		re := regexp.MustCompile(`(?s)(?:^|\n)` + regexp.QuoteMeta(selector) + ` \{(.*?)\}`)
		for _, m := range re.FindAllStringSubmatch(string(css), -1) {
			for _, d := range regexp.MustCompile(`(--[\w-]+):\s*(#[0-9a-fA-F]{6})`).FindAllStringSubmatch(m[1], -1) {
				out[d[1]] = d[2]
			}
		}
		return out
	}
	for theme, v := range map[string]map[string]string{"light": vars(":root"), "dark": vars(`[data-theme="dark"]`)} {
		checks := []struct{ name, fg, bg string }{
			{"badge", v["--ended-badge-fg"], v["--ended-badge-bg"]},
			{"title on page", v["--ended-text"], v["--bg"]},
			{"title on card", v["--ended-text"], v["--card-bg"]},
			{"banner", v["--ended-banner-fg"], v["--ended-banner-bg"]},
		}
		for _, c := range checks {
			if c.fg == "" || c.bg == "" {
				t.Errorf("%s %s: colour variable missing", theme, c.name)
				continue
			}
			if r := contrast(c.fg, c.bg); r < 4.5 {
				t.Errorf("%s %s: %s on %s is %.2f:1, below 4.5:1", theme, c.name, c.fg, c.bg, r)
			}
		}
	}
}

func contrast(a, b string) float64 {
	lum := func(hex string) float64 {
		var c [3]float64
		for i := range c {
			n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
			v := float64(n) / 255
			if v <= 0.03928 {
				c[i] = v / 12.92
			} else {
				c[i] = math.Pow((v+0.055)/1.055, 2.4)
			}
		}
		return 0.2126*c[0] + 0.7152*c[1] + 0.0722*c[2]
	}
	la, lb := lum(a), lum(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// The feed and sitemap keep ended articles: the page stays indexable.
func TestSitemap_KeepsEndedOffers(t *testing.T) {
	app := offerApp(t)
	seedOffers(t, app)
	req := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "/article/ended</loc>") {
		t.Error("the sitemap should still list the ended offer's page")
	}
}

// When every published article has ended, the empty default feed says so
// instead of claiming nothing has been published.
func TestHome_AllEndedEmptyState(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "ended", "Citi 25% Transfer Bonus to Leading Hotels of the World", "deal", "September 19, 2026", `["transfer-bonuses"]`)

	body := getPage(t, app, "/")
	if !strings.Contains(body, "every recent one has ended") {
		t.Error("an all-ended feed should say every recent offer has ended")
	}
	if strings.Contains(body, "No articles published yet") {
		t.Error("the site has published articles; the empty state should not say otherwise")
	}
	if cardFor(getPage(t, app, "/?type=deal"), "ended") == "" {
		t.Error("the deals view should still list the ended offer")
	}

	fresh := setupRealTemplateCardApp(t)
	if !strings.Contains(getPage(t, fresh, "/"), "No articles published yet") {
		t.Error("a site with nothing published keeps its original empty state")
	}
}

func TestPickFeatured(t *testing.T) {
	cases := []struct {
		name string
		rows []publicArticleRow
		want int
	}{
		{"featured and live", []publicArticleRow{{}, {Featured: true}}, 1},
		{"featured but ended: newest live instead", []publicArticleRow{{Featured: true, Ended: true}, {Ended: true}, {}}, 2},
		{"nothing featured", []publicArticleRow{{Ended: true}, {}, {}}, 1},
		{"all ended: featured still wins", []publicArticleRow{{Ended: true}, {Featured: true, Ended: true}}, 1},
		{"all ended, none featured", []publicArticleRow{{Ended: true}, {Ended: true}}, 0},
	}
	for _, c := range cases {
		if got := pickFeatured(c.rows); got != c.want {
			t.Errorf("%s: pickFeatured = %d, want %d", c.name, got, c.want)
		}
	}
}

// The admin newsletter page previews the weekly issue as it was built on
// Friday. An offer in it that has ended since is left out when the issue is
// sent, and the page says so rather than implying it will go out.
func TestAdminNewsletter_MarksCandidatesThatEnded(t *testing.T) {
	app := offerApp(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("testpassword123"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	app.cfg.Admin.PasswordHash = string(hash)
	seedOffers(t, app)

	ended, err := app.db.GetGeneratedArticleBySlug("ended")
	if err != nil {
		t.Fatal(err)
	}
	live, err := app.db.GetGeneratedArticleBySlug("ends-next-week")
	if err != nil {
		t.Fatal(err)
	}
	nlID, err := app.db.CreateNewsletter(&db.Newsletter{
		Date: "2026-09-26", Subject: "MilesAhead Weekly", Status: "auto_approved",
		HTMLContent: "<p>" + ended.Title + "</p>", ArticleCount: 2, ScoutsPickArticleID: &ended.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []int64{ended.ID, live.ID} {
		if err := app.db.CreateNewsletterCandidate(&db.NewsletterCandidate{
			NewsletterID: nlID, ArticleID: id, Rank: i + 1, Featured: i == 0,
		}); err != nil {
			t.Fatal(err)
		}
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/newsletter", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/newsletter: %d", rec.Code)
	}
	body := rec.Body.String()

	candidate := func(title string) string {
		return regexp.MustCompile(`(?s)<label[^>]*>\s*<input type="radio" name="article_id"[^>]*>\s*<div>\s*<strong[^>]*>` +
			regexp.QuoteMeta(html.EscapeString(title)) + `</strong>.*?</label>`).FindString(body)
	}
	if c := candidate(ended.Title); !strings.Contains(c, "Ended") {
		t.Errorf("the ended candidate should be marked Ended:\n%s", c)
	}
	if c := candidate(live.Title); c == "" || strings.Contains(c, "Ended") {
		t.Errorf("the live candidate should be listed unmarked:\n%s", c)
	}
	note := regexp.MustCompile(`(?s)<p class="preview-note" role="note"[^>]*>(.*?)</p>`).FindStringSubmatch(body)
	if note == nil {
		t.Fatal("the preview should carry a note about the ended offer")
	}
	if want := "1 offer in this preview has ended since it was built; it is left out when the issue is sent. Scout&#39;s Pick passes to the top article still in it."; note[1] != want {
		t.Errorf("preview note = %q, want %q", note[1], want)
	}

	// Once the issue has gone out, the page no longer speaks of a send.
	if err := app.db.MarkNewsletterSent(nlID); err != nil {
		t.Fatal(err)
	}
	body = doAdminGET(t, app, "/admin/newsletter", session).Body.String()
	if strings.Contains(body, "preview-note") || strings.Contains(body, "left out when sent") {
		t.Error("a sent issue should not be annotated as if it were still to be sent")
	}
}

// A news story ("summary") reports a deadline rather than offering a deal, so
// its banner says the deadline passed instead of that an offer ended.
func TestArticle_EndedNewsStoryBannerWording(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "bag-fees", "Airlines Keep Raising Bag Fees", "summary", "April 8, 2026", `["airlines"]`)

	body := getPage(t, app, "/article/bag-fees")
	if want := `<p class="offer-ended-banner" role="note">The deadline in this story passed on April 8, 2026.</p>`; !strings.Contains(body, want) {
		t.Errorf("a news story's banner should read %s", want)
	}
	if strings.Contains(body, "This offer ended") {
		t.Error("a news story is not an offer")
	}
}
