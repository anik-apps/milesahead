package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// cardWithFacts is baseCardData plus structured data, links to two rivals in
// its text, and the other cards those links point to.
func cardWithFacts() map[string]interface{} {
	data := baseCardData()
	page := db.ReferencePage{
		Slug: "amex-platinum", Title: "American Express Platinum Card", Category: "travel", Issuer: "Amex",
		ImageURL: "/static/img/card-placeholder.svg", ApplyURL: "https://www.americanexpress.com/us/credit-cards/card/platinum/",
		AnnualFeeAmount: 895, AnnualFeeNote: "Increased from $695 in September 2025",
		WelcomeBonusText: "As high as 175,000 MR points after $12,000 spend in 6 months (personalized offer)",
		EarningRatesJSON: `{"airlines_direct":{"rate":"5x","note":""},"hotels_portal":{"rate":"5x","note":"Prepaid via Amex Travel"},"base":{"rate":"1x","note":""}}`,
		ScoutsVerdict:    "The 2025 refresh turned the Platinum into a credit-stacking monster. Worth it if you use the credits.",
		Status:           "published",
	}
	sections := []db.ReferenceSection{
		{Key: "overview", Title: "Overview", Content: `<p>See the <a href="/cards/chase-sapphire-reserve">Reserve</a> and <a href="/cards/capital-one-venture-x">Venture X</a>.</p>`},
	}
	all := []db.ReferencePage{
		page,
		{Slug: "chase-sapphire-reserve", Title: "Chase Sapphire Reserve", Category: "travel", Status: "published", ImageURL: "/static/img/cards/csr.png"},
		{Slug: "capital-one-venture-x", Title: "Capital One Venture X", Category: "travel", Status: "published"},
		{Slug: "amex-gold", Title: "American Express Gold Card", Category: "travel", Status: "published"},
		{Slug: "citi-double-cash", Title: "Citi Double Cash", Category: "cash-back", Status: "published"},
		{Slug: "draft-card", Title: "Draft Card", Category: "travel", Status: "draft"},
	}
	data["Card"] = page
	data["Sections"] = sections
	data["ScoutsVerdict"] = page.ScoutsVerdict
	for k, v := range cardDetailData(&page, sections, all) {
		data[k] = v
	}
	return data
}

func TestCardPage_SummaryPanelFacts(t *testing.T) {
	out := renderRealCardTemplate(t, cardWithFacts())
	for _, want := range []string{
		`<section class="card-summary" aria-label="Summary">`,
		`<dt>Annual fee</dt><dd><span class="card-fact-value">$895</span><span class="card-fact-detail">Increased from $695 in September 2025</span>`,
		`<dt>Welcome offer</dt><dd><span class="card-fact-value">As high as 175,000 MR points</span><span class="card-fact-detail">after $12,000 spend in 6 months</span>`,
		`<dt>Earns</dt><dd><span class="card-fact-value">5x flights</span><span class="card-fact-detail">5x hotels via Amex Travel · 1x everything else</span>`,
		// The verdict's first sentence, with a link to the rest.
		`<span class="card-summary-verdict-label">Scout's verdict</span> The 2025 refresh turned the Platinum into a credit-stacking monster. <a href="#section-verdict">Read the verdict</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary panel should contain %q", want)
		}
	}
	if strings.Contains(out, "Worth it if you use the credits. <a href=\"#section-verdict\"") {
		t.Error("only the verdict's first sentence belongs in the summary")
	}
	// The panel comes before the sections.
	if strings.Index(out, `class="card-summary"`) > strings.Index(out, `id="section-overview"`) {
		t.Error("the summary panel should sit above the sections")
	}
}

// A card whose structured data hasn't been filled in shows no facts rather
// than a $0 fee it doesn't have.
func TestCardPage_NoFactsWithoutData(t *testing.T) {
	data := baseCardData()
	page := data["Card"].(db.ReferencePage)
	for k, v := range cardDetailData(&page, nil, nil) {
		data[k] = v
	}
	out := renderRealCardTemplate(t, data)
	if strings.Contains(out, `class="card-facts"`) {
		t.Error("no structured data: no facts strip")
	}
	if !strings.Contains(out, `class="card-summary"`) {
		t.Error("the summary panel (art, verdict, actions) still renders")
	}
}

// The button says where it goes and that it opens a new tab.
func TestCardPage_ApplyLinkCue(t *testing.T) {
	out := renderRealCardTemplate(t, cardWithFacts())
	want := `<a href="https://www.americanexpress.com/us/credit-cards/card/platinum/" target="_blank" rel="noopener noreferrer" class="card-apply-btn">Apply at Amex <span aria-hidden="true">&#8599;</span><span class="cards-sr-only"> (opens americanexpress.com in a new tab)</span></a>`
	if got := strings.Count(out, want); got != 2 {
		t.Errorf("expected the labelled apply link in the summary and in next steps, got %d", got)
	}
	if strings.Contains(out, "Card Details &rarr;") || strings.Contains(out, "Card Details →") {
		t.Error("the old 'Card Details →' label should be gone")
	}
	if !strings.Contains(out, `<span class="card-next-note" aria-hidden="true">Opens americanexpress.com in a new tab</span>`) {
		t.Error("next steps should say visibly where the button goes")
	}

	// No apply URL, no button.
	data := cardWithFacts()
	page := data["Card"].(db.ReferencePage)
	page.ApplyURL = ""
	data["Card"] = page
	if out := renderRealCardTemplate(t, data); strings.Contains(out, "card-apply-btn") {
		t.Error("a card without an apply URL should have no apply button")
	}
}

func TestApplyLabel(t *testing.T) {
	for _, c := range []struct {
		issuer string
		closed bool
		want   string
	}{
		{"Amex", false, "Apply at Amex"},
		{"Amex", true, "Card details at Amex"},
		{"", false, "Apply on the issuer's site"},
	} {
		if got := applyLabel(c.issuer, c.closed); got != c.want {
			t.Errorf("applyLabel(%q, %v) = %q, want %q", c.issuer, c.closed, got, c.want)
		}
	}
	for in, want := range map[string]string{
		"https://www.americanexpress.com/us/credit-cards/card/platinum/": "americanexpress.com",
		"https://citi.com/credit-cards?x=1":                              "citi.com",
		"":                                                               "",
	} {
		if got := applyHost(in); got != want {
			t.Errorf("applyHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// Compare is a labelled GET form with a button, in the summary panel (which
// phones show), and lists the rivals the page names first.
func TestCardPage_CompareForm(t *testing.T) {
	out := renderRealCardTemplate(t, cardWithFacts())
	for _, want := range []string{
		`<form class="card-compare" method="get" action="/compare">`,
		`<label for="compare-with">Compare with</label>`,
		`<select id="compare-with" name="cards" required>`,
		`<button type="submit">Compare</button>`,
		`<option value="amex-platinum,chase-sapphire-reserve">Chase Sapphire Reserve</option>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compare form should contain %q", want)
		}
	}
	if strings.Contains(out, "onchange") {
		t.Error("choosing a card must not navigate; the button submits")
	}
	groups := regexp.MustCompile(`<optgroup label="([^"]+)">`).FindAllStringSubmatch(out, -1)
	var labels []string
	for _, g := range groups {
		labels = append(labels, g[1])
	}
	if got := strings.Join(labels, ","); got != "Named on this page,Travel &amp; Premium,Cash Back" {
		t.Errorf("compare groups: %s", got)
	}
	if strings.Contains(out, `draft-card`) {
		t.Error("unpublished cards must not be offered")
	}
	// In the summary panel, not the sidebar that phones hide.
	form := strings.Index(out, `<form class="card-compare"`)
	if form < strings.Index(out, `class="card-summary"`) || form > strings.Index(out, `class="card-sidebar"`) {
		t.Error("the compare form belongs in the summary panel")
	}
}

// The form's value is the pair of slugs /compare reads, so it works as a plain
// GET submit.
func TestHandleCompare_AcceptsCompareFormSubmit(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)
	body := getPath(t, app, "/compare?cards=amex-platinum%2Ccapital-one-venture-x")
	for _, want := range []string{"American Express Platinum Card", "Capital One Venture X", "$895/yr"} {
		if !strings.Contains(body, want) {
			t.Errorf("/compare from the card page form should show %q", want)
		}
	}
}

func TestCardPage_NextStepsAndRivals(t *testing.T) {
	out := renderRealCardTemplate(t, cardWithFacts())
	for _, want := range []string{
		`<section class="card-next-steps" id="section-next-steps" aria-labelledby="next-steps-heading">`,
		`<h2 id="next-steps-heading">Next steps</h2>`,
		`Compare American Express Platinum Card with`,
		`<a href="/compare?cards=amex-platinum,chase-sapphire-reserve" class="card-rival">`,
		`<a href="/compare?cards=amex-platinum,capital-one-venture-x" class="card-rival">`,
		`More Travel &amp; Premium cards: <a href="/cards/amex-gold">American Express Gold Card</a>.`,
		`<a href="/cards/?cat=travel">All Travel &amp; Premium cards</a>`,
		`<a href="#compare-with">Compare with any of 4 cards</a>`,
		// Listed in the table of contents and the phone pills.
		`<li><a href="#section-next-steps">Next steps</a></li>`,
		`<a href="#section-next-steps">Next steps</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("next steps should contain %q", want)
		}
	}
	if strings.Index(out, `id="section-next-steps"`) < strings.Index(out, `id="section-verdict"`) {
		t.Error("next steps come after the verdict")
	}
}

// A page whose text links no other card offers cards from its category.
func TestCardDetailData_RivalsFallBackToCategory(t *testing.T) {
	page := db.ReferencePage{Slug: "a", Title: "A", Category: "hotel", Status: "published"}
	all := []db.ReferencePage{page,
		{Slug: "d", Title: "Delta", Category: "hotel", Status: "published"},
		{Slug: "b", Title: "Bravo", Category: "hotel", Status: "published"},
		{Slug: "c", Title: "Charlie", Category: "hotel", Status: "published"},
		{Slug: "e", Title: "Echo", Category: "hotel", Status: "published"},
		{Slug: "x", Title: "X-ray", Category: "airline", Status: "published"},
	}
	d := cardDetailData(&page, []db.ReferenceSection{{Content: `<p>No links.</p>`}}, all)
	var rivals, related []string
	for _, r := range d["Rivals"].([]cardLink) {
		rivals = append(rivals, r.Slug)
	}
	for _, r := range d["Related"].([]cardLink) {
		related = append(related, r.Slug)
	}
	if strings.Join(rivals, ",") != "b,c,d" || strings.Join(related, ",") != "e" {
		t.Errorf("rivals %v, related %v", rivals, related)
	}
	if groups := d["CompareGroups"].([]compareGroup); groups[0].Category != "Hotel" {
		t.Errorf("no named rivals: the compare list starts with the categories, got %q", groups[0].Category)
	}
}

// Integration: every one of the 52 seed cards ends with next steps.
func TestHandleCard_NextStepsOnEveryPage(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)
	pages, err := app.db.ListPublishedReferencePages("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 52 {
		t.Fatalf("expected 52 seed cards, got %d", len(pages))
	}
	for _, p := range pages {
		body := getCardPage(t, app, p.Slug)
		if !strings.Contains(body, `id="section-next-steps"`) {
			t.Errorf("%s: no next steps", p.Slug)
			continue
		}
		next := body[strings.Index(body, `id="section-next-steps"`):]
		if strings.Count(next, `class="card-rival"`) == 0 {
			t.Errorf("%s: next steps offers no card to compare with", p.Slug)
		}
		if p.ApplyURL != "" && !strings.Contains(next, `class="card-apply-btn"`) {
			t.Errorf("%s: next steps has no apply link", p.Slug)
		}
		if !strings.Contains(body, `<select id="compare-with" name="cards" required>`) {
			t.Errorf("%s: no compare form", p.Slug)
		}
	}

	// Real data: the Platinum's text names the Reserve and Venture X.
	body := getCardPage(t, app, "amex-platinum")
	for _, want := range []string{
		`<span class="card-fact-value">$895</span>`,
		`<span class="card-fact-value">5x flights</span>`,
		`/compare?cards=amex-platinum,chase-sapphire-reserve`,
		`/compare?cards=amex-platinum,capital-one-venture-x`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("amex-platinum should contain %q", want)
		}
	}
	// A card closed to new applicants isn't offered as "Apply".
	closed := 0
	for _, p := range pages {
		if p.ApplyURL == "" || !parseOffer(p.WelcomeBonusText).Closed {
			continue
		}
		closed++
		body := getCardPage(t, app, p.Slug)
		if !strings.Contains(body, ">Card details at "+p.Issuer+" <") || strings.Contains(body, ">Apply at ") {
			t.Errorf("%s is closed to new applicants: its button should read 'Card details at %s'", p.Slug, p.Issuer)
		}
	}
	if closed == 0 {
		t.Error("expected at least one seed card closed to new applicants")
	}
}

// The verdict is a real heading, so it is in the page outline the TOC links to.
func TestCardPage_VerdictIsHeading(t *testing.T) {
	out := renderRealCardTemplate(t, cardWithFacts())
	if !strings.Contains(out, `<h2 class="scout-label" id="verdict-heading">Scout's Verdict</h2>`) {
		t.Error("Scout's Verdict should be an h2")
	}
	if strings.Contains(out, "<h4") {
		t.Error("no h4 should skip heading levels on the card page")
	}
}

// The table of contents and the phone pills list the same places, including
// Recent Changes.
func TestCardPage_PillsMatchTOC(t *testing.T) {
	data := cardWithFacts()
	data["RecentChanges"] = []recentChangeRow{{RecentChange: db.RecentChange{Date: "2026-09-22", Summary: "Sample"}}}
	out := renderRealCardTemplate(t, data)
	toc := regexp.MustCompile(`(?s)<nav class="card-toc".*?</nav>`).FindString(out)
	pills := regexp.MustCompile(`(?s)<div class="card-mobile-pills">.*?</div>`).FindString(out)
	hrefs := func(s string) string {
		var out []string
		for _, m := range regexp.MustCompile(`href="(#[^"]+)"`).FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return strings.Join(out, ",")
	}
	if hrefs(toc) == "" || hrefs(toc) != hrefs(pills) {
		t.Errorf("TOC %s and pills %s should match", hrefs(toc), hrefs(pills))
	}
	if !strings.Contains(pills, "#section-recent-changes") {
		t.Error("the pills should include Recent Changes")
	}
	if !strings.Contains(out, `/static/js/card-toc.js`) {
		t.Error("the current-section script should load")
	}
}

// The TOC is a <nav>, which the site styles as the sticky header; its own rule
// must undo that. Sticky elements share one offset.
func TestCardPageStyles_TOCAndStickyOffset(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	rule := func(sel string) string {
		m := regexp.MustCompile(`(?s)(?:^|\n)\s*` + regexp.QuoteMeta(sel) + ` \{(.*?)\}`).FindStringSubmatch(css)
		if m == nil {
			t.Fatalf("no CSS rule for %s", sel)
		}
		return m[1]
	}
	toc := rule("nav.card-toc")
	for _, want := range []string{"position: static", "padding: 0", "background: none", "border: 0", "z-index: auto"} {
		if !strings.Contains(toc, want) {
			t.Errorf("nav.card-toc should set %q", want)
		}
	}
	if !strings.Contains(css, ":root { --sticky-offset: 84px; }") {
		t.Error("--sticky-offset should default to the header's height")
	}
	if !strings.Contains(rule(".card-sidebar"), "top: calc(var(--sticky-offset) + 1rem)") {
		t.Error("the sidebar should stick at --sticky-offset")
	}
	if !strings.Contains(rule(".card-section"), "scroll-margin-top: calc(var(--sticky-offset) + 1rem)") {
		t.Error("section anchors should clear --sticky-offset")
	}
	if strings.Contains(css, "top: 5rem") {
		t.Error("no sticky element should hard-code the old 5rem offset")
	}
	// Card prose links are underlined, not marked by colour alone.
	if !strings.Contains(css, ".card-section-content a, .scouts-verdict-content a, .card-next-more a {\n    text-decoration: underline;") {
		t.Error("links in card prose should be underlined")
	}
}

// Phones get a zoom in the infographic viewer; it starts hidden and the script
// shows it when the image is drawn smaller than its real width.
func TestCardPage_InfographicZoom(t *testing.T) {
	out := renderRealCardTemplate(t, infographicCardData())
	if !strings.Contains(out, `<button type="button" class="ig-lightbox-zoom" data-ig-zoom aria-pressed="false" hidden>Zoom</button>`) {
		t.Error("the viewer should carry a hidden zoom toggle")
	}
	js, err := os.ReadFile(filepath.Join("..", "..", "static", "js", "card-infographic.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[data-ig-zoom]", "aria-pressed", "is-zoomed", "zoom.hidden ="} {
		if !strings.Contains(string(js), want) {
			t.Errorf("card-infographic.js should handle the zoom (%q)", want)
		}
	}
}

func TestLinkedCardSlugs(t *testing.T) {
	got := linkedCardSlugs([]db.ReferenceSection{
		{Content: `<a href="/cards/b">B</a> <a href="/cards/">all</a> <a href="/cards/a">A</a>`},
		{Content: `<a href="/cards/b">B again</a> <a href="https://example.com/cards/z">ext</a>`},
	})
	if strings.Join(got, ",") != "b,a" {
		t.Errorf("got %v", got)
	}
}

func TestLeadSentence(t *testing.T) {
	for in, want := range map[string]string{
		"The CSP is still the best entry point at $95. You get 14+ partners.":  "The CSP is still the best entry point at $95.",
		"Earn 1.5x everywhere. Simple.":                                        "Earn 1.5x everywhere.",
		"<p>Has been the default \"just give me a good card\" pick. More.</p>": "Has been the default \"just give me a good card\" pick.",
		"No full stop at all": "No full stop at all",
	} {
		if got := leadSentence(in, verdictLeadMax); got != want {
			t.Errorf("leadSentence(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("word ", 80) + "end."
	if got := leadSentence(long, 60); len([]rune(got)) > 61 || !strings.HasSuffix(got, "…") {
		t.Errorf("long sentence should be cut at a word with an ellipsis: %q", got)
	}
}

// The offer headline copies the stored text; it never makes up a figure.
func TestParseOffer(t *testing.T) {
	for in, want := range map[string]cardOffer{
		"As high as 175,000 MR points after $12,000 spend in 6 months (personalized offer)":                {Headline: "As high as 175,000 MR points", Condition: "after $12,000 spend in 6 months", Short: "Up to 175K points", Available: true},
		"75,000 bonus miles after $4,000 spend in 3 months, plus a one-time $300 credit":                   {Headline: "75,000 bonus miles", Condition: "after $4,000 spend in 3 months", Short: "75K miles", Available: true},
		"$200 cash back (20,000 ThankYou Points) after $1,500 spend in 6 months":                           {Headline: "$200 cash back", Condition: "after $1,500 spend in 6 months", Short: "$200 cash back", Available: true},
		"$100 in Bilt Cash upon approval (no spending requirement)":                                        {Headline: "$100 in Bilt Cash", Condition: "upon approval", Short: "$100 Bilt Cash", Available: true},
		"3 Free Night Awards after $3,000 spend in 3 months (each up to 50,000 points)":                    {Headline: "3 Free Night Awards", Condition: "after $3,000 spend in 3 months", Short: "3 Free Night Awards", Available: true},
		"Cashback Match: Discover matches 100% of all cash back earned in first year (unlimited, no cap).": {Headline: "Cashback Match: Discover matches 100% of all cash back earned in first year", Short: "Cashback Match", Available: true},
		"No longer available (closed to new applicants July 23, 2026). Previously: 40,000 MR points":       {Headline: "No longer available", Short: "No current offer", Closed: true},
		"No welcome bonus": {Headline: "No welcome bonus", Short: "No current offer"},
		"":                 {},
	} {
		if got := parseOffer(in); got != want {
			t.Errorf("parseOffer(%q)\n got  %+v\n want %+v", in, got, want)
		}
	}
	// Text without a figure falls back to itself, shortened at a word.
	got := parseOffer("A generous welcome package that changes often and is described here at considerable length for testing")
	if !strings.HasPrefix(got.Headline, "A generous welcome package") || !strings.HasSuffix(got.Headline, "…") || got.Condition != "" {
		t.Errorf("fallback headline: %+v", got)
	}
}

// Every seed card's offer reads cleanly: a headline, no stray punctuation, and
// any figure in it appears in the stored text.
func TestParseOffer_SeedCards(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	seedAllCards(t, app)
	pages, _ := app.db.ListPublishedReferencePages("")
	digits := regexp.MustCompile(`\d[\d,]*`)
	for _, p := range pages {
		o := parseOffer(p.WelcomeBonusText)
		if o.Headline == "" || o.Short == "" {
			t.Errorf("%s: empty offer from %q", p.Slug, p.WelcomeBonusText)
		}
		for _, n := range digits.FindAllString(o.Headline+" "+o.Condition, -1) {
			if !strings.Contains(p.WelcomeBonusText, n) {
				t.Errorf("%s: %q is not in the stored text", p.Slug, n)
			}
		}
	}
}
