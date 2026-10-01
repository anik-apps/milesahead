package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
)

// filterNav returns the homepage's filter <nav>, or fails the test.
func filterNav(t *testing.T, body string) string {
	t.Helper()
	nav := regexp.MustCompile(`(?s)<nav class="home-filters" aria-label="Filter articles">.*?</nav>`).FindString(body)
	if nav == "" {
		t.Fatalf("no filter bar on the page:\n%s", body)
	}
	return nav
}

// filterLinkTag returns the <a> start tag of the link with class cls (or a
// class starting with it) whose text is label, or "".
func filterLinkTag(html, cls, label string) string {
	re := regexp.MustCompile(`<a href="[^"]*" class="` + regexp.QuoteMeta(cls) + `(?: active)?"[^>]*>` + regexp.QuoteMeta(label) + `</a>`)
	return re.FindString(html)
}

// hrefOf returns the href of an <a> tag.
func hrefOf(tag string) string {
	m := regexp.MustCompile(`href="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}

// removeLink returns the strip's link that removes the filter named label.
func removeLink(html, label string) string {
	re := regexp.MustCompile(`<a href="[^"]*" class="filter-remove" aria-label="Remove filter: ` + regexp.QuoteMeta(label) + `">`)
	return re.FindString(html)
}

// Unfiltered, the bar has one "All", active; only the types that have a
// published article; and the six topics in order, without the two broad
// ones. There is no applied-filters strip.
func TestHomeFilters_Unfiltered(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "a-deal", "Hyatt Points Sale", "deal", "", `["hotels", "loyalty-programs"]`)
	publishOffer(t, app, "a-story", "United Changes Its Award Chart", "summary", "", `["airlines", "points-deals"]`)

	body := getPage(t, app, "/")
	nav := filterNav(t, body)

	if n := strings.Count(nav, ">All</a>"); n != 1 {
		t.Errorf("want one \"All\" in the filter bar, got %d", n)
	}
	all := filterLinkTag(nav, "filter-switch-item", "All")
	if !strings.Contains(all, `class="filter-switch-item active" aria-current="page"`) || hrefOf(all) != "/" {
		t.Errorf("unfiltered, \"All\" should be the active switch item linking to /, got %q", all)
	}
	if n := strings.Count(nav, "aria-current"); n != 1 {
		t.Errorf("unfiltered, only \"All\" should be current, got %d aria-current", n)
	}
	for label, href := range map[string]string{"Deals": "/?type=deal", "News": "/?type=summary"} {
		if got := hrefOf(filterLinkTag(nav, "filter-switch-item", label)); got != href {
			t.Errorf("switch item %q: href %q, want %q", label, got, href)
		}
	}
	for _, gone := range []string{"Roundups", "Alerts", "Summaries"} {
		if strings.Contains(nav, ">"+gone+"<") {
			t.Errorf("the switch offers %q, which has no published articles", gone)
		}
	}

	last := -1
	for _, topic := range []string{"Airlines", "Hotels", "Credit Cards", "Transfer Bonuses", "Bank Bonuses", "Travel Deals"} {
		tag := filterLinkTag(nav, "filter-chip", topic)
		i := strings.Index(nav, tag)
		if tag == "" || i < last {
			t.Errorf("topic %q missing or out of order", topic)
		}
		last = i
	}
	if hrefOf(filterLinkTag(nav, "filter-chip", "Credit Cards")) != "/?tag=credit-cards" {
		t.Error("unfiltered, a topic links to just that topic")
	}
	for _, dropped := range []string{"Loyalty Programs", "Points Deals"} {
		if strings.Contains(nav, dropped) {
			t.Errorf("%q should not be in the filter bar", dropped)
		}
	}
	if strings.Contains(body, `class="active-filters"`) {
		t.Error("unfiltered, there is no applied-filters strip")
	}
	for _, old := range []string{"category-tabs", "tag-filters", "tag-pill"} {
		if strings.Contains(body, old) {
			t.Errorf("the old filter markup %q is still on the page", old)
		}
	}

	// A type comes back once something of it is published.
	publishOffer(t, app, "a-roundup", "Best Hotel Deals This Week", "roundup", "", `["hotels"]`)
	nav = filterNav(t, getPage(t, app, "/"))
	if hrefOf(filterLinkTag(nav, "filter-switch-item", "Roundups")) != "/?type=roundup" {
		t.Error("a published roundup should bring back the Roundups switch item")
	}
	if strings.Contains(nav, ">Alerts<") {
		t.Error("still no alerts, so no Alerts item")
	}
}

// Type and topic apply together, both show as selected, and every link keeps
// the other filter.
func TestHomeFilters_TypeAndTagCombine(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "hotel-story", "Marriott Ends Free Night Award Top-Ups", "summary", "", `["hotels"]`)
	// Tagged hotels but a deal: the old handler, which used the tag alone,
	// would have listed it.
	publishOffer(t, app, "hotel-deal", "Hilton 100% Points Bonus", "deal", "", `["hotels"]`)
	// News but not hotels.
	publishOffer(t, app, "airline-story", "Delta Raises Lounge Prices", "summary", "", `["airlines"]`)

	body := getPage(t, app, "/?type=summary&tag=hotels")
	if cardFor(body, "hotel-story") == "" {
		t.Error("the news story tagged hotels should be listed")
	}
	for _, slug := range []string{"hotel-deal", "airline-story"} {
		if strings.Contains(body, "/article/"+slug) {
			t.Errorf("%q does not match both filters but is listed", slug)
		}
	}

	nav := filterNav(t, body)
	news := filterLinkTag(nav, "filter-switch-item", "News")
	if !strings.Contains(news, `active" aria-current="page"`) || hrefOf(news) != "/?tag=hotels&amp;type=summary" {
		t.Errorf("News should be the current switch item, got %q", news)
	}
	if all := filterLinkTag(nav, "filter-switch-item", "All"); strings.Contains(all, "active") || hrefOf(all) != "/?tag=hotels" {
		t.Errorf("\"All\" should drop the type but keep the topic, got %q", all)
	}
	if got := hrefOf(filterLinkTag(nav, "filter-switch-item", "Deals")); got != "/?tag=hotels&amp;type=deal" {
		t.Errorf("Deals should keep the topic, href %q", got)
	}
	hotels := filterLinkTag(nav, "filter-chip", "Hotels")
	if !strings.Contains(hotels, `class="filter-chip active" aria-current="true"`) || hrefOf(hotels) != "/?type=summary" {
		t.Errorf("the active Hotels chip should link to the view without it, got %q", hotels)
	}
	if got := hrefOf(filterLinkTag(nav, "filter-chip", "Airlines")); got != "/?tag=airlines&amp;type=summary" {
		t.Errorf("another topic should keep the type, href %q", got)
	}

	if !strings.Contains(nav, "Showing 1 article<") {
		t.Error("the strip should count the one article shown")
	}
	if got := hrefOf(removeLink(nav, "News")); got != "/?tag=hotels" {
		t.Errorf("removing News should keep Hotels, href %q", got)
	}
	if got := hrefOf(removeLink(nav, "Hotels")); got != "/?type=summary" {
		t.Errorf("removing Hotels should keep News, href %q", got)
	}
	if !strings.Contains(nav, `<a href="/" class="filter-clear">Clear all</a>`) {
		t.Error("the strip should offer Clear all")
	}

	// The card's type badge reads "News" for a summary; its class is unchanged.
	if card := cardFor(body, "hotel-story"); !strings.Contains(card, `<span class="type-badge badge-summary">News</span>`) {
		t.Errorf("a summary's badge should read News, got:\n%s", card)
	}
	if !strings.Contains(getPage(t, app, "/article/hotel-story"), `<span class="type-badge badge-summary">News</span>`) {
		t.Error("the article page's badge should read News too")
	}
	if card := cardFor(getPage(t, app, "/?tag=hotels"), "hotel-deal"); !strings.Contains(card, `<span class="type-badge badge-deal">deal</span>`) {
		t.Errorf("other types keep their badge text, got:\n%s", card)
	}
}

// Topics dropped from the bar still filter, and show in the strip so the
// reader can clear them.
func TestHomeFilters_TopicNotInBarStillWorks(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "loyal", "Status Match to Hyatt Globalist", "deal", "", `["loyalty-programs"]`)
	publishOffer(t, app, "other", "Chase Sapphire Bonus", "deal", "", `["credit-cards"]`)

	body := getPage(t, app, "/?tag=loyalty-programs")
	if cardFor(body, "loyal") == "" || strings.Contains(body, "/article/other") {
		t.Error("/?tag=loyalty-programs should list just the loyalty-programs article")
	}
	nav := filterNav(t, body)
	if got := hrefOf(removeLink(nav, "Loyalty Programs")); got != "/" {
		t.Errorf("the strip should offer to remove Loyalty Programs, href %q", got)
	}
	if strings.Contains(nav, `aria-current="true"`) {
		t.Error("no chip in the bar should be active for a topic that is not in it")
	}
	if got := hrefOf(filterLinkTag(nav, "filter-switch-item", "Deals")); got != "/?tag=loyalty-programs&amp;type=deal" {
		t.Errorf("the switch should keep the off-bar topic, href %q", got)
	}
}

// Unknown or empty filter values never fail the page: they match nothing and
// show the empty state, with the strip to clear them.
func TestHomeFilters_InvalidValues(t *testing.T) {
	app := offerApp(t)
	publishOffer(t, app, "a-deal", "Hyatt Points Sale", "deal", "", `["hotels"]`)

	for _, path := range []string{
		"/?type=bogus",
		"/?tag=no-such-topic",
		"/?type=roundup",
		"/?type=deal&tag=bank-bonuses",
		"/?tag=%3Cscript%3Ealert(1)%3C/script%3E",
		"/?tag=%FF%FE&type=%00",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<div class="empty-state">`) || !strings.Contains(body, "No articles for <strong>") {
			t.Errorf("%s: want the filtered empty state", path)
		}
		if strings.Contains(body, "<script>alert(1)") {
			t.Errorf("%s: the tag value is not escaped", path)
		}
		if !strings.Contains(filterNav(t, body), `class="filter-clear">Clear all</a>`) {
			t.Errorf("%s: the strip should offer Clear all", path)
		}
	}

	// A blank value is no filter at all.
	if body := getPage(t, app, "/?type=&tag=%20"); cardFor(body, "a-deal") == "" || strings.Contains(body, `class="active-filters"`) {
		t.Error("blank filter values should give the unfiltered page")
	}
}

func TestHomeURL(t *testing.T) {
	for _, c := range []struct{ typ, tag, want string }{
		{"", "", "/"},
		{"deal", "", "/?type=deal"},
		{"", "hotels", "/?tag=hotels"},
		{"summary", "hotels", "/?tag=hotels&type=summary"},
		{"", "a&b=c d", "/?tag=a%26b%3Dc+d"},
	} {
		if got := homeURL(c.typ, c.tag); got != c.want {
			t.Errorf("homeURL(%q, %q) = %q, want %q", c.typ, c.tag, got, c.want)
		}
	}
}

// The filter bar's text meets WCAG AA (4.5:1) in both themes.
func TestHomeFilterStyles_MeetContrast(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("..", "..", "static", "css", "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	vars := func(selector string) map[string]string {
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
		for _, c := range []struct{ name, fg, bg string }{
			{"switch item", v["--filter-text"], v["--filter-track"]},
			{"active switch item", v["--filter-on-text"], v["--filter-on-bg"]},
			{"chip", v["--filter-text"], v["--bg"]},
			{"active chip", v["--filter-chip-on-fg"], v["--filter-chip-on-bg"]},
			{"applied filter", v["--filter-pill-fg"], v["--filter-pill-bg"]},
			{"strip text", v["--text-muted"], v["--bg"]},
			{"clear all", v["--accent"], v["--bg"]},
		} {
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

// Every topic in the bar is a tag the generator can assign; a typo would be a
// chip that always leads to an empty page.
func TestHomeTopicsAreValidTags(t *testing.T) {
	for _, slug := range homeTopics {
		if !slices.Contains(ai.ValidTags, slug) {
			t.Errorf("home topic %q is not in ai.ValidTags", slug)
		}
	}
}
