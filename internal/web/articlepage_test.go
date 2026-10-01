package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestStoryCardsFirst(t *testing.T) {
	reserve := linkedCardEmbed{Slug: "chase-sapphire-reserve", Title: "Chase Sapphire Reserve", Issuer: "Chase"}
	flex := linkedCardEmbed{Slug: "chase-freedom-flex", Title: "Chase Freedom Flex", Issuer: "Chase"}
	unlimited := linkedCardEmbed{Slug: "chase-freedom-unlimited", Title: "Chase Freedom Unlimited", Issuer: "Chase"}
	preferred := linkedCardEmbed{Slug: "chase-sapphire-preferred", Title: "Chase Sapphire Preferred", Issuer: "Chase"}
	slugs := func(cs []linkedCardEmbed) string {
		var s []string
		for _, c := range cs {
			s = append(s, c.Slug)
		}
		return strings.Join(s, ",")
	}
	for _, tt := range []struct {
		headline string
		in       []linkedCardEmbed
		want     string
	}{
		// The production case: the linker put the Reserve first.
		{"Chase Freedom Flex $250 Bonus Returns — 50% Return on $500 Spend", []linkedCardEmbed{reserve, flex},
			"chase-freedom-flex,chase-sapphire-reserve"},
		// Named without the issuer; two named cards keep their order.
		{"Chase devaluing Hyatt transfers for Sapphire Preferred and Freedom Unlimited", []linkedCardEmbed{reserve, unlimited, flex, preferred},
			"chase-freedom-unlimited,chase-sapphire-preferred,chase-sapphire-reserve,chase-freedom-flex"},
		// No card named: the stored order stands.
		{"Citi ThankYou Points 25% Transfer Bonus", []linkedCardEmbed{reserve, flex}, "chase-sapphire-reserve,chase-freedom-flex"},
		// Whole words only: "Flexible" does not name the Flex.
		{"Chase Freedom Flexible Rewards Explained", []linkedCardEmbed{reserve, flex}, "chase-sapphire-reserve,chase-freedom-flex"},
	} {
		if got := slugs(storyCardsFirst(tt.in, tt.headline)); got != tt.want {
			t.Errorf("%q: got %s, want %s", tt.headline, got, tt.want)
		}
	}
}

func TestVerdictLead(t *testing.T) {
	long := "The Freedom Flex is the overachiever of the Chase family. While its sibling the Unlimited is content being reliable and boring, the Flex rewards you for paying attention — and it rewards you handsomely. Those 5% quarterly categories add up."
	for _, tt := range []struct{ name, in, want string }{
		{"short stays whole", "Best $95 you'll spend.", "Best $95 you'll spend."},
		{"whole sentences that fit", long, "The Freedom Flex is the overachiever of the Chase family."},
		{"decimals do not end a sentence", "Earns 1.5x everywhere and 3x on dining at $3.50 a coffee. " + strings.Repeat("More words here. ", 20),
			// 58 + 7 x 17 = 177 characters: seven whole sentences fit.
			"Earns 1.5x everywhere and 3x on dining at $3.50 a coffee." + strings.TrimRight(strings.Repeat(" More words here.", 7), " ")},
		{"first paragraph only", "First paragraph here.\n\nSecond paragraph.", "First paragraph here."},
		{"one long sentence is cut at a word", strings.Repeat("word ", 60), strings.TrimSpace(strings.Repeat("word ", 36)) + "…"},
		{"html stripped", "<p>Strong <b>pick</b>.</p>", "Strong pick."},
	} {
		got := verdictLead(tt.in, verdictLeadMax)
		if got != tt.want {
			t.Errorf("%s:\n got %q\nwant %q", tt.name, got, tt.want)
		}
		if n := len([]rune(got)); n > verdictLeadMax+1 {
			t.Errorf("%s: %d characters, over %d", tt.name, n, verdictLeadMax)
		}
	}
}

func TestCardRowLabels(t *testing.T) {
	if got := annualFeeLabel(0); got != "No annual fee" {
		t.Errorf("annualFeeLabel(0) = %q", got)
	}
	if got := annualFeeLabel(795); got != "$795 annual fee" {
		t.Errorf("annualFeeLabel(795) = %q", got)
	}
	if got := applyLabel("American Express"); got != "Apply at American Express" {
		t.Errorf("applyLabel = %q", got)
	}
	if got := applyLabel(" "); got != "Apply" {
		t.Errorf("applyLabel(blank) = %q", got)
	}
}

func TestHeroSrcset(t *testing.T) {
	const base = "https://images.unsplash.com/photo-1770827401792-ad4b35156794?crop=entropy&cs=tinysrgb&fit=max&fm=jpg&q=80"
	if got, want := heroSrcset(base+"&w=400", base+"&w=1080"), base+"&w=400 400w, "+base+"&w=1080 1080w"; got != want {
		t.Errorf("unsplash sizes:\n got %q\nwant %q", got, want)
	}
	for name, pair := range map[string][2]string{
		"pexels fixes the height": {"https://images.pexels.com/photos/1/p.jpeg?h=350", "https://images.pexels.com/photos/1/p.jpeg?h=650&w=940"},
		"no thumbnail":            {"", base + "&w=1080"},
		"thumbnail wider":         {base + "&w=1080", base + "&w=400"},
	} {
		if got := heroSrcset(pair[0], pair[1]); got != "" {
			t.Errorf("%s: want no srcset, got %q", name, got)
		}
	}
}

func TestArticleTopicAndRelatedTags(t *testing.T) {
	for _, tt := range []struct {
		tags    []string
		topic   string
		related string
	}{
		{[]string{"points-deals", "credit-cards", "loyalty-programs"}, "credit-cards", "credit-cards"},
		{[]string{"credit-cards", "transfer-bonuses", "points-deals"}, "transfer-bonuses", "credit-cards,transfer-bonuses"},
		{[]string{"loyalty-programs", "amex", "transfer-bonuses"}, "transfer-bonuses", "amex,transfer-bonuses"},
		{[]string{"loyalty-programs", "amex"}, "amex", "amex"},
		{[]string{"points-deals", "loyalty-programs"}, "points-deals", "points-deals,loyalty-programs"},
		{nil, "", ""},
	} {
		if got := articleTopic(tt.tags); got != tt.topic {
			t.Errorf("articleTopic(%v) = %q, want %q", tt.tags, got, tt.topic)
		}
		if got := strings.Join(relatedTags(tt.tags), ","); got != tt.related {
			t.Errorf("relatedTags(%v) = %q, want %q", tt.tags, got, tt.related)
		}
	}
}

func TestExternalLinksNewTab(t *testing.T) {
	const domain = "milesahead.example.com"
	in := `<p><a href="/cards/chase-freedom-flex">Freedom Flex</a>, ` +
		`<a href="https://milesahead.example.com/article/x">ours, absolute</a>, ` +
		`<a href="https://www.milesahead.example.com/cards/y">ours, www</a>, ` +
		`<a href="#sources">jump</a>, <a href="mailto:a@b.c">mail</a>, ` +
		`<a href="https://creditcards.chase.com/flex">Chase</a>, ` +
		`<a href='//example.org/x'>scheme-relative</a>, ` +
		`<a href="https://example.org/y" target="_self">kept</a>, ` +
		`<a href="https://example.org/z" rel="nofollow">has rel</a>, ` +
		`<a href="https://example.org/w" rel='noreferrer'>safe rel</a>, ` +
		`<a href="https://example.org/v" rel="">empty rel</a></p>`
	want := `<p><a href="/cards/chase-freedom-flex">Freedom Flex</a>, ` +
		`<a href="https://milesahead.example.com/article/x">ours, absolute</a>, ` +
		`<a href="https://www.milesahead.example.com/cards/y">ours, www</a>, ` +
		`<a href="#sources">jump</a>, <a href="mailto:a@b.c">mail</a>, ` +
		`<a href="https://creditcards.chase.com/flex" target="_blank" rel="noopener">Chase</a>, ` +
		`<a href='//example.org/x' target="_blank" rel="noopener">scheme-relative</a>, ` +
		`<a href="https://example.org/y" target="_self">kept</a>, ` +
		`<a href="https://example.org/z" rel="nofollow noopener" target="_blank">has rel</a>, ` +
		`<a href="https://example.org/w" rel='noreferrer' target="_blank">safe rel</a>, ` +
		`<a href="https://example.org/v" rel="noopener" target="_blank">empty rel</a></p>`
	if got := externalLinksNewTab(in, domain); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestBuildShareLinks(t *testing.T) {
	s := buildShareLinks("Flex & friends: 50% back", "https://milesahead.example.com/article/flex")
	if s.Email != "mailto:?subject=Flex%20%26%20friends%3A%2050%25%20back&body=Flex%20%26%20friends%3A%2050%25%20back%0A%0Ahttps%3A%2F%2Fmilesahead.example.com%2Farticle%2Fflex" {
		t.Errorf("email = %q", s.Email)
	}
	if !strings.HasPrefix(s.X, "https://x.com/intent/post?text=Flex%20%26%20friends") || !strings.Contains(s.X, "&url=https%3A%2F%2Fmilesahead.example.com%2Farticle%2Fflex") {
		t.Errorf("x = %q", s.X)
	}
}

// articlePageApp is the real-template app with a persona and a fixed clock
// (Sep 23, 2026).
func articlePageApp(t *testing.T) *App {
	t.Helper()
	app := setupRealTemplateCardApp(t)
	app.cfg.Site.PersonaName = "Scout"
	app.now = func() time.Time { return time.Date(2026, 9, 23, 16, 0, 0, 0, time.UTC) }
	return app
}

// seedStoryCards publishes the Reserve and the Flex as reference pages.
func seedStoryCards(t *testing.T, app *App) {
	t.Helper()
	for _, c := range []db.ReferencePage{
		{Slug: "chase-sapphire-reserve", Title: "Chase Sapphire Reserve", Issuer: "Chase", Category: "travel",
			AnnualFeeAmount: 795, WelcomeBonusText: "100,000 points after $6,000 in 3 months",
			EarningRatesJSON: `{"dining":{"rate":"3x","note":""},"groceries":{"rate":"1x","note":""}}`,
			ScoutsVerdict:    "The revamped Reserve throws the kitchen sink at you. But at $795, the math only works if you use the credits.",
			ImageURL:         "/static/img/cards/chase-sapphire-reserve.png", ApplyURL: "https://creditcards.chase.com/reserve", Status: "published"},
		{Slug: "chase-freedom-flex", Title: "Chase Freedom Flex", Issuer: "Chase", Category: "cash-back",
			AnnualFeeAmount: 0, WelcomeBonusText: "$250 after $500 in 3 months",
			EarningRatesJSON: `{"dining":{"rate":"3%","note":""}}`,
			ScoutsVerdict:    "The Freedom Flex is the overachiever of the Chase family. While its sibling the Unlimited is content being reliable and boring, the Flex rewards you for paying attention — and it rewards you handsomely.\n\nThe power move? Carry both.",
			ImageURL:         "/static/img/cards/chase-freedom-flex.png", ApplyURL: "https://creditcards.chase.com/flex", Status: "published"},
	} {
		if _, err := app.db.CreateReferencePage(&c); err != nil {
			t.Fatal(err)
		}
	}
}

// publishStory creates an approved article through the real write path and
// returns its id.
func publishStory(t *testing.T, app *App, a db.GeneratedArticle, minutesAgo int) int64 {
	t.Helper()
	a.Status = db.StatusPending
	if a.SourcesJSON == "" {
		a.SourcesJSON = "[]"
	}
	a.RevisionNumber = 1
	id, err := app.db.CreateGeneratedArticle(&a)
	if err != nil {
		t.Fatalf("creating %s: %v", a.Slug, err)
	}
	if err := app.db.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.DB().Exec(`UPDATE generated_articles SET published_at = datetime('now', ?) WHERE id = ?`,
		fmt.Sprintf("-%d minutes", minutesAgo), id); err != nil {
		t.Fatal(err)
	}
	return id
}

const flexBody = `<p>At 50% return on spend, this <a href="/cards/chase-freedom-flex">Freedom Flex</a> offer is a no-brainer. ` +
	`See <a href="https://creditcards.chase.com/flex">Chase's offer page</a>.</p><p>Bottom line: take it.</p>`

// seedFlexStory publishes the article A of the review: a photo, two linked
// cards (the Reserve stored first), Scout's Take, two sources, tags.
func seedFlexStory(t *testing.T, app *App) int64 {
	t.Helper()
	seedStoryCards(t, app)
	id := publishStory(t, app, db.GeneratedArticle{
		Title: "Chase Freedom Flex $250 Bonus Returns — 50% Return on $500 Spend", Slug: "flex-250",
		Content: flexBody, Type: "deal",
		TLDR:             "The Freedom Flex is back to a $250 cash-back bonus after $500 in spend over three months.",
		KeyDetails:       `[{"Label":"Bonus","Value":"$250 after $500 in 3 months"},{"Label":"Annual Fee","Value":"$0"}]`,
		Links:            `[{"Text":"Chase Freedom Flex","URL":"https://creditcards.chase.com/flex"}]`,
		ImageURL:         "https://images.unsplash.com/photo-1?crop=entropy&cs=tinysrgb&fit=max&fm=jpg&q=80&w=1080",
		ImageThumbnail:   "https://images.unsplash.com/photo-1?crop=entropy&cs=tinysrgb&fit=max&fm=jpg&q=80&w=400",
		ImageAttribution: "Photo by Osmany M Leyva Aldana on Unsplash",
		ScoutsTake:       "A 50% return on a no-fee card is one of the best deals going.",
		RelatedCardsJSON: `[{"slug":"chase-sapphire-reserve","title":"Chase Sapphire Reserve"},{"slug":"chase-freedom-flex","title":"Chase Freedom Flex"}]`,
		TagsJSON:         `["points-deals","credit-cards"]`,
	}, 1)
	dc, err := app.db.CreateSource(&db.Source{Name: "Doctor of Credit", URL: "https://www.doctorofcredit.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	om, err := app.db.CreateSource(&db.Source{Name: "One Mile at a Time", URL: "https://onemileatatime.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	var raws []int64
	for i, r := range []db.RawArticle{
		{SourceID: dc, Title: "Chase Freedom Flex $250 bonus is back", URL: "https://www.doctorofcredit.com/flex-250/"},
		{SourceID: om, Title: "Freedom Flex drops foreign transaction fees", URL: "https://onemileatatime.com/news/flex-ftf/"},
	} {
		r.Content, r.PublishedAt, r.Hash = "c", time.Now(), fmt.Sprint("flex-src-", i)
		rid, err := app.db.CreateRawArticle(&r)
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, rid)
	}
	if err := app.db.LinkArticleSources(id, raws); err != nil {
		t.Fatal(err)
	}
	return id
}

// inOrder fails the test unless each marker appears in body, after the one
// before it.
func inOrder(t *testing.T, body string, markers ...string) {
	t.Helper()
	at := 0
	for _, m := range markers {
		i := strings.Index(body[at:], m)
		if i < 0 {
			if strings.Contains(body, m) {
				t.Errorf("%q is out of order (expected after the previous marker)", m)
			} else {
				t.Errorf("%q is missing", m)
			}
			return
		}
		at += i + len(m)
	}
}

// Integration, real templates: headline and summary come before the photo,
// the story before Scout's Take, and the ending runs cards, sources, related
// stories, then the vote and share line.
func TestArticlePage_SectionOrder(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	body := getPage(t, app, "/article/flex-250")

	inOrder(t, body,
		`class="back-link"`,
		`<header class="article-header">`,
		`<span class="type-badge badge-deal">deal</span>`,
		`<h1>Chase Freedom Flex $250 Bonus Returns`,
		`<p class="article-meta">`,
		`<p class="article-standfirst">The Freedom Flex is back`,
		`</header>`,
		`<details class="article-facts">`,
		`<figure class="article-hero">`,
		`<aside class="article-sidebar" aria-label="Key details">`,
		`<div class="article-content">`,
		`<section class="scouts-take"`,
		`<section class="card-embeds"`,
		`<div class="article-feedback">`,
	)
	for _, want := range []string{
		`<time class="published-date" datetime="`,
		`<span class="article-byline">By Scout</span>`,
		`<h2 class="scout-label" id="scouts-take-title">Scout's Take</h2>`,
		`<summary>Key details (2)</summary>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %s", want)
		}
	}
	// The TL;DR is the standfirst now, not a sidebar box.
	if strings.Contains(body, "<h4>TL;DR</h4>") {
		t.Error("the TL;DR should not also be in the sidebar")
	}
}

// The photo is decorative and reserves its band before it loads: alt="",
// width and height, and a srcset when the stored URLs are two sizes of it.
func TestArticlePage_HeroImage(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	body := getPage(t, app, "/article/flex-250")

	hero := regexp.MustCompile(`(?s)<figure class="article-hero">.*?</figure>`).FindString(body)
	for _, want := range []string{
		`alt=""`,
		`width="1100" height="280"`,
		`srcset="https://images.unsplash.com/photo-1?crop=entropy&amp;cs=tinysrgb&amp;fit=max&amp;fm=jpg&amp;q=80&amp;w=400 400w, https://images.unsplash.com/photo-1?crop=entropy&amp;cs=tinysrgb&amp;fit=max&amp;fm=jpg&amp;q=80&amp;w=1080 1080w"`,
		`sizes="(max-width: 768px) calc(100vw - 2rem), 1100px"`,
		`<figcaption class="image-attribution">Photo by Osmany M Leyva Aldana on Unsplash</figcaption>`,
	} {
		if !strings.Contains(hero, want) {
			t.Errorf("hero missing %s:\n%s", want, hero)
		}
	}
	if strings.Contains(body, `alt="Chase Freedom Flex $250`) {
		t.Error("the photo's alt text should not repeat the headline")
	}
}

// The cards the headline names come first; each row links to our review and
// says where Apply goes. The verdict is its first sentences, not the whole
// card-page verdict.
func TestArticlePage_CardRows(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	body := getPage(t, app, "/article/flex-250")

	section := regexp.MustCompile(`(?s)<section class="card-embeds".*?</section>`).FindString(body)
	inOrder(t, section,
		`<h2 class="article-section-title card-embeds-heading" id="card-embeds-title">Cards in this story</h2>`,
		`<a href="/cards/chase-freedom-flex">Chase Freedom Flex</a>`,
		`<a href="/cards/chase-sapphire-reserve">Chase Sapphire Reserve</a>`,
	)
	for _, want := range []string{
		`<p class="card-embed-issuer">Chase · No annual fee</p>`,
		`<p class="card-embed-issuer">Chase · $795 annual fee</p>`,
		`<dt>Welcome bonus</dt><dd>$250 after $500 in 3 months</dd>`,
		`<dt>Top earnings</dt>`,
		`<a class="card-embed-review" href="/cards/chase-freedom-flex">Full review<span class="article-sr-only"> of the Chase Freedom Flex</span></a>`,
		`<a class="card-embed-cta" href="https://creditcards.chase.com/flex" target="_blank" rel="noopener">Apply at Chase<span aria-hidden="true">&nbsp;&#8599;</span><span class="article-sr-only"> (opens in a new tab)</span></a>`,
		`<p class="card-embed-verdict">The Freedom Flex is the overachiever of the Chase family.</p>`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("card rows missing %s", want)
		}
	}
	for _, gone := range []string{"View card", "The power move", "is-muted"} {
		if strings.Contains(section, gone) {
			t.Errorf("card rows should not contain %q", gone)
		}
	}
}

// Public pages do not name or link the news sites an article was written
// from: no Sources section, no source count, no outlet names or URLs, even
// when the article has linked sources. (The admin preview lists them; see
// TestAdminPreview_ListsSourcesOnlyInAdmin.)
func TestArticlePage_NoSourceOutlets(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app) // links Doctor of Credit and One Mile at a Time
	body := getPage(t, app, "/article/flex-250")
	for _, leak := range []string{
		"Doctor of Credit", "One Mile at a Time", "doctorofcredit.com", "onemileatatime.com",
		"Chase Freedom Flex $250 bonus is back", ">Sources<", `id="sources"`, "article-source",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("public article page contains %q", leak)
		}
	}
	// The meta line keeps the date, byline and tags.
	meta := regexp.MustCompile(`(?s)<div class="article-meta-row">.*?</div>\s*</div>`).FindString(body)
	inOrder(t, meta, `<time class="published-date"`, `<span class="article-byline">By Scout</span>`, `<a href="/?tag=points-deals" class="tag-badge">`)
}

// Links in the story to our own pages stay in the reader's tab; links to
// other sites open a new one. main.js no longer forces every story link into
// a new tab.
func TestArticlePage_InternalLinksStayInTab(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	content := regexp.MustCompile(`(?s)<div class="article-content">.*?</div>`).FindString(getPage(t, app, "/article/flex-250"))
	if !strings.Contains(content, `<a href="/cards/chase-freedom-flex">Freedom Flex</a>`) {
		t.Errorf("the internal card link should be left as written:\n%s", content)
	}
	if !strings.Contains(content, `<a href="https://creditcards.chase.com/flex" target="_blank" rel="noopener">`) {
		t.Errorf("the issuer link should open in a new tab:\n%s", content)
	}

	js, err := os.ReadFile(filepath.Join("..", "..", "static", "js", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(js), "querySelectorAll('.article-content a')") {
		t.Error("main.js should not force every article link into a new tab")
	}
	if !strings.Contains(string(js), "querySelectorAll('.preview-modal .article-content a')") {
		t.Error("main.js should still open story links in a new tab inside the admin preview")
	}
}

// Related stories: up to three, sharing a tag (the broad ones only when there
// is nothing else), live offers first, never the article itself; ended ones
// are badged. A link leads on to the topic.
func TestArticlePage_RelatedStories(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	for i, a := range []struct{ slug, tags, deadline string }{
		{"ended-card-deal", `["credit-cards"]`, "September 19, 2026"},
		{"live-card-1", `["credit-cards"]`, ""},
		{"live-card-2", `["credit-cards","hotels"]`, "September 30, 2026"},
		{"live-card-3", `["credit-cards"]`, ""},
		{"only-broad", `["points-deals"]`, ""},
		{"hotel-only", `["hotels"]`, ""},
	} {
		kd := `[{"Label":"Bonus","Value":"x"}]`
		if a.deadline != "" {
			kd = `[{"Label":"Bonus","Value":"x"},{"Label":"Deadline","Value":"` + a.deadline + `"}]`
		}
		publishStory(t, app, db.GeneratedArticle{Title: "Story " + a.slug, Slug: a.slug, Content: "<p>x</p>",
			Type: "deal", TagsJSON: a.tags, KeyDetails: kd}, 10+i)
	}

	body := getPage(t, app, "/article/flex-250")
	section := regexp.MustCompile(`(?s)<section class="related-stories".*?</section>`).FindString(body)
	links := regexp.MustCompile(`href="/article/([^"]+)"`).FindAllStringSubmatch(section, -1)
	var got []string
	for _, l := range links {
		got = append(got, l[1])
	}
	if want := "live-card-1,live-card-2,live-card-3"; strings.Join(got, ",") != want {
		t.Errorf("related = %v, want %s", got, want)
	}
	if !strings.Contains(section, `<a class="related-more" href="/?tag=credit-cards">More in Credit Cards`) {
		t.Errorf("related stories should link on to the topic:\n%s", section)
	}

	// With the live ones gone, the ended story fills in, badged.
	if _, err := app.db.DB().Exec(`UPDATE generated_articles SET status = ? WHERE slug IN ('live-card-1','live-card-3')`, db.StatusUnpublished); err != nil {
		t.Fatal(err)
	}
	section = regexp.MustCompile(`(?s)<section class="related-stories".*?</section>`).FindString(getPage(t, app, "/article/flex-250"))
	inOrder(t, section, `/article/live-card-2`, `<li class="is-ended">`, `<span class="ended-badge">Ended</span> <span class="related-title">Story ended-card-deal</span>`)
	if strings.Contains(section, "/article/flex-250") || strings.Contains(section, "only-broad") || strings.Contains(section, "hotel-only") {
		t.Errorf("related stories should not include the article itself or stories sharing only a broad or other tag:\n%s", section)
	}
}

// An ended offer de-emphasises the apply buttons; the review link stays.
func TestArticlePage_EndedOfferMutesApply(t *testing.T) {
	app := articlePageApp(t)
	seedStoryCards(t, app)
	publishStory(t, app, db.GeneratedArticle{
		Title: "Chase Freedom Flex $250 Bonus Ended", Slug: "flex-ended", Content: "<p>x</p>", Type: "deal",
		KeyDetails:       `[{"Label":"Bonus","Value":"$250"},{"Label":"Deadline","Value":"September 19, 2026"}]`,
		RelatedCardsJSON: `[{"slug":"chase-freedom-flex","title":"Chase Freedom Flex"}]`,
		TagsJSON:         `["credit-cards"]`,
	}, 1)
	body := getPage(t, app, "/article/flex-ended")
	if !strings.Contains(body, `<a class="card-embed-cta is-muted" href="https://creditcards.chase.com/flex"`) {
		t.Error("an ended offer's apply button should be de-emphasised")
	}
	if !strings.Contains(body, `<a class="card-embed-review" href="/cards/chase-freedom-flex">`) {
		t.Error("the review link should remain")
	}
}

// Vote and share sit on one line; share links are plain URLs, and Copy link
// stays hidden until main.js finds the clipboard API.
func TestArticlePage_VoteAndShare(t *testing.T) {
	app := articlePageApp(t)
	seedFlexStory(t, app)
	body := getPage(t, app, "/article/flex-250")
	feedback := regexp.MustCompile(`(?s)<div class="article-feedback">.*?</div>\s*</div>\s*</div>\s*</article>`).FindString(body)
	for _, want := range []string{
		`<div class="vote-buttons" role="group" aria-labelledby="vote-prompt"`,
		`<button type="button" class="share-copy" data-share-url="https://localhost/article/flex-250" hidden>Copy link</button>`,
		`<a href="mailto:?subject=Chase%20Freedom%20Flex%20%24250`,
		`<a href="https://x.com/intent/post?text=Chase%20Freedom%20Flex`,
	} {
		if !strings.Contains(feedback, want) {
			t.Errorf("vote/share line missing %s\n%s", want, feedback)
		}
	}
	if strings.Contains(body, "<script src=\"https://") {
		t.Error("the article page should load no third-party script")
	}
}
