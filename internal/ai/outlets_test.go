package ai

import (
	"strings"
	"testing"
)

// tableOutlets stands in for rows of the sources table, including outlets
// that are not built in.
func tableOutlets() []Outlet {
	return []Outlet{
		{Name: "Some Travel Blog", URL: "https://www.sometravelblog.net/feed"},
		{Name: "LoyaltyLobby", URL: "https://loyaltylobby.com"},
		{Name: "Points", URL: "https://points.example"}, // a generic name is never matched as a word
		{Name: "Unknown", URL: ""},
	}
}

// TestOutletMatcher_FindsEveryOutletAndAlias covers each built-in outlet, each
// abbreviation and a sources-table outlet, in each field an article has.
func TestOutletMatcher_FindsEveryOutletAndAlias(t *testing.T) {
	m := NewOutletMatcher(tableOutlets())
	mentions := map[string]string{
		"The Points Guy":     "The Points Guy",
		"Points Guy":         "The Points Guy",
		"TPG":                "The Points Guy",
		"tpg":                "The Points Guy",
		"ONE MILE AT A TIME": "One Mile at a Time",
		"one Mile at a time": "One Mile at a Time",
		"One Mile at a Time": "One Mile at a Time",
		"OMAAT":              "One Mile at a Time",
		"Doctor of Credit":   "Doctor of Credit",
		"DoC":                "Doctor of Credit",
		"View from the Wing": "View from the Wing",
		"VFTW":               "View from the Wing",
		"Upgraded Points":    "Upgraded Points",
		"Loyalty Lobby":      "Loyalty Lobby",
		"LoyaltyLobby":       "Loyalty Lobby",
		"Frequent Miler":     "Frequent Miler",
		"FM":                 "Frequent Miler",
		"AwardWallet":        "AwardWallet",
		"Thrifty Traveler":   "Thrifty Traveler",
		"Miles to Memories":  "Miles to Memories",
		"Live and Let's Fly": "Live and Let's Fly",
		"Some Travel Blog":   "Some Travel Blog",
		"thepointsguy.com":   "The Points Guy",
		"sometravelblog.net": "Some Travel Blog",
	}
	fields := map[string]func(string) string{
		"title":      func(n string) string { return n + " Says the Amex Bonus Is Back" },
		"tldr":       func(n string) string { return "TLDR: According to " + n + ", the bonus returns." },
		"key detail": func(n string) string { return "<li>Value: ~1.41 cents according to " + n + "</li>" },
		"body": func(n string) string {
			return "<p>American confirmed to <strong>" + n + "</strong>: the change is permanent.</p>"
		},
		"link text":   func(n string) string { return `<p>Read <a href="https://www.chase.com/x">` + n + `'s guide</a>.</p>` },
		"scouts take": func(n string) string { return "Book now; " + n + " runs the math slightly differently." },
	}
	for mention, outlet := range mentions {
		for field, wrap := range fields {
			got := m.Find(wrap(mention))
			if len(got) != 1 || got[0].Outlet != outlet {
				t.Errorf("%s %q: got %+v, want one mention of %q", field, mention, got, outlet)
			}
		}
	}
}

func TestOutletMatcher_SentenceIsVisibleText(t *testing.T) {
	m := NewOutletMatcher(nil)
	got := m.Find(`<p>Chase raised the bonus.</p><p>That's about 1.4 cents per point (<a href="https://onemileatatime.com/x">One Mile at a Time</a>'s valuation). Act fast.</p>`)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if want := "That's about 1.4 cents per point (One Mile at a Time's valuation)."; got[0].Sentence != want {
		t.Errorf("sentence = %q, want %q", got[0].Sentence, want)
	}
}

// TestOutletMatcher_SentenceBoundaries: a sentence ending in a closing quote
// still ends there, and a long one is cut on a word.
func TestOutletMatcher_SentenceBoundaries(t *testing.T) {
	m := NewOutletMatcher(nil)
	got := m.Find(`Upgraded Points puts it bluntly: "especially true in this situation." One Mile at a Time's take is sharper.`)
	if len(got) != 2 || got[1].Sentence != "One Mile at a Time's take is sharper." {
		t.Errorf("got %+v", got)
	}
	long := strings.Repeat("word ", 80) + "per TPG " + strings.Repeat("more ", 80)
	got = m.Find(long)
	if len(got) != 1 || strings.HasPrefix(got[0].Sentence, "ord") || len(got[0].Sentence) > 420 {
		t.Errorf("long sentence = %q", got[0].Sentence)
	}
}

// TestOutletMatcher_TheIsOptional: a sources row "The Frequent Miler" is the
// built-in "Frequent Miler", listed once.
func TestOutletMatcher_TheIsOptional(t *testing.T) {
	m := NewOutletMatcher([]Outlet{{Name: "The Frequent Miler", URL: "https://frequentmiler.com"}})
	n := 0
	for _, name := range m.Names() {
		if strings.Contains(name, "Frequent Miler") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("names = %v", m.Names())
	}
	if got := m.Find("The Frequent Miler reported it."); len(got) != 1 || got[0].Outlet != "Frequent Miler" {
		t.Errorf("got %+v", got)
	}
}

func TestOutletMatcher_NoFalsePositives(t *testing.T) {
	m := NewOutletMatcher(tableOutlets())
	legit := []string{
		"Chase, American Express, Citi and Capital One all raised bonuses.",
		"American confirmed the change; United and Delta have not.",
		"Hyatt, Marriott Bonvoy, Hilton Honors and IHG One Rewards members benefit.",
		"Sources say the devaluation is coming.",
		"Points are worth about 1.4 cents each; frequent flyers will want miles.",
		"The points you earn are yours, guy.",
		"A frequent traveler earns more miles at a time like this.",
		"Upgrade your points strategy.",
		"Upload the doc to the fm portal: in lower case both are ordinary words.",
		"The credit card doctor is in.",
		`<a href="https://thepointsguy.com/news/x">Hilton's sale page</a>`,
		"Book at https://www.onemileatatime.com/deal today.",
		"Loyalty programs lobby for changes.",
		// Outlet names that are also ordinary phrases, in ordinary lower case.
		"You build a balance one mile at a time.",
		"The view from the wing seat is better than the bulkhead.",
		"Any frequent miler knows the drill; upgraded points balances help too.",
		"Delta SkyMiles Medallion members, Air France-KLM Flying Blue and Alaska Mileage Plan.",
	}
	for _, s := range legit {
		if got := m.Find(s); len(got) != 0 {
			t.Errorf("Find(%q) = %+v, want none", s, got)
		}
	}
}

func TestOutletMatcher_StripOutletLinks(t *testing.T) {
	m := NewOutletMatcher(tableOutlets())
	in := `<p>Per <a href="https://thepointsguy.com/news/x" target="_blank">this report</a>, book via ` +
		`<a href="https://www.hilton.com/en/offers/">Hilton</a> or <a href='https://blog.sometravelblog.net/a'>here</a>.</p>`
	got, removed := m.StripOutletLinks(in)
	want := `<p>Per this report, book via <a href="https://www.hilton.com/en/offers/">Hilton</a> or here.</p>`
	if got != want {
		t.Errorf("StripOutletLinks:\n got  %q\n want %q", got, want)
	}
	if strings.Join(removed, ",") != "The Points Guy,Some Travel Blog" {
		t.Errorf("removed = %v", removed)
	}

	links := m.DropOutletLinks([]ArticleLink{
		{Text: "Hilton offers", URL: "https://www.hilton.com/en/offers/"},
		{Text: "OMAAT post", URL: "https://onemileatatime.com/news/x"},
		{Text: "LL", URL: "http://www.loyaltylobby.com/2026/x"},
	})
	if len(links) != 1 || links[0].Text != "Hilton offers" {
		t.Errorf("DropOutletLinks = %+v; want only the official page", links)
	}
}

func TestOutletIssues_OnePerOutletWithExceptions(t *testing.T) {
	m := NewOutletMatcher(nil)
	issues := OutletIssues(m.Find("TPG says yes. The Points Guy says yes again. OMAAT disagrees."))
	if len(issues) != 2 {
		t.Fatalf("issues = %q; want one per outlet", issues)
	}
	for _, want := range []string{`"The Points Guy"`, "Remove the mention and state the fact without attribution",
		"The outlet is itself the subject of the story", "An unconfirmed report whose only source is that outlet"} {
		if !strings.Contains(issues[0], want) {
			t.Errorf("issue %q missing %q", issues[0], want)
		}
	}
}

// outletReply names an outlet in every field and links to one.
const outletReply = `TPG: Capital One's BA Bonus Is Rare

TLDR: One Mile at a Time says a 20% bonus runs through October 31.

KEY_DETAILS_START
- Value: ~1.41 cents according to Upgraded Points
- Deadline: October 31, 2026
KEY_DETAILS_END

LINKS_START
- Capital One transfer partners | https://www.capitalone.com/learn-grow/money-management/transfer-partners/
- OMAAT post | https://onemileatatime.com/news/capital-one-ba/
LINKS_END

<p>American confirmed to Some Travel Blog that <a href="https://onemileatatime.com/news/x">the bonus</a> is rare.</p>`

const cleanOutletReply = `Capital One's BA Bonus Is Rare

TLDR: A 20% bonus runs through October 31.

KEY_DETAILS_START
- Value: ~1.41 cents per point
- Deadline: October 31, 2026
KEY_DETAILS_END

LINKS_START
- Capital One transfer partners | https://www.capitalone.com/learn-grow/money-management/transfer-partners/
LINKS_END

<p>American confirmed that the bonus is rare.</p>`

// TestCheckOutlets_IgnoresDroppedLinkEntries: a link-list entry pointing to a
// news site is dropped from the result, so its text is not worth a revision;
// an entry to an official page that names an outlet still is.
func TestCheckOutlets_IgnoresDroppedLinkEntries(t *testing.T) {
	m := NewOutletMatcher(nil)
	reply := "Title\n\nLINKS_START\n<li>OMAAT post | https://onemileatatime.com/x</li>\n" +
		"<li>Hilton sale | https://www.hilton.com/en/offers/</li>\nLINKS_END\n<p>Body.</p>"
	if _, mentions := checkOutlets("test", reply, m); len(mentions) != 0 {
		t.Errorf("mentions = %+v; want none", mentions)
	}
	reply = strings.Replace(reply, "Hilton sale", "TPG's Hilton guide", 1)
	if _, mentions := checkOutlets("test", reply, m); len(mentions) != 1 || mentions[0].Outlet != "The Points Guy" {
		t.Errorf("mentions = %+v; want the TPG link text", mentions)
	}
}

// sequenceClient returns its replies in turn, repeating the last.
type sequenceClient struct {
	replies []string
	prompts []string
}

func (c *sequenceClient) Generate(model, prompt string, maxTokens int) (string, error) {
	c.prompts = append(c.prompts, prompt)
	r := c.replies[min(len(c.prompts), len(c.replies))-1]
	return r, nil
}

// TestPipeline_Process_OutletMentionTriggersRevision: a mention in any field
// fails attribution with an issue naming the outlet, which the auto-revision
// receives; a revision without the mention passes.
func TestPipeline_Process_OutletMentionTriggersRevision(t *testing.T) {
	gen := &sequenceClient{replies: []string{outletReply, cleanOutletReply}}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)
	p.SetOutlets(func() []Outlet { return tableOutlets() })

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(gen.prompts) != 2 {
		t.Fatalf("generator called %d times; want generation then one auto-revision", len(gen.prompts))
	}
	revision := gen.prompts[1]
	for _, want := range []string{`Names the news site "The Points Guy"`, `"One Mile at a Time"`, `"Upgraded Points"`,
		`"Some Travel Blog"`, "state the fact without attribution"} {
		if !strings.Contains(revision, want) {
			t.Errorf("auto-revision prompt missing %q", want)
		}
	}

	// The link was unwrapped before the critic saw the article.
	if strings.Contains(critic.prompts[0], `href="https://onemileatatime.com`) {
		t.Error("the critic was shown a link to a news site")
	}
	if !res.Verification.AttributionCheck || len(res.Verification.Issues) != 0 {
		t.Errorf("verification = %+v; the clean revision should pass", res.Verification)
	}
	if res.Title != "Capital One's BA Bonus Is Rare" {
		t.Errorf("title = %q; want the revision's", res.Title)
	}
}

// TestPipeline_Process_OutletMentionDoesNotLoop: a mention that survives the
// one auto-revision (it may be one of the exceptions) is kept, marked failed
// for the admin; the pipeline does not revise again.
func TestPipeline_Process_OutletMentionDoesNotLoop(t *testing.T) {
	gen := &recordingClient{reply: outletReply}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)
	p.SetOutlets(func() []Outlet { return tableOutlets() })

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(gen.prompts) != 2 {
		t.Errorf("generator called %d times; want exactly one auto-revision", len(gen.prompts))
	}
	verifies := 0
	for _, pr := range critic.prompts {
		if strings.Contains(pr, "VERIFICATION CHECKLIST") {
			verifies++
		}
	}
	if verifies != 2 {
		t.Errorf("critic verified %d times; want the article and its one revision", verifies)
	}
	v := res.Verification
	if v.AttributionCheck {
		t.Error("attribution check passed with a news site still named")
	}
	found := false
	for _, is := range v.Issues {
		found = found || strings.Contains(is, `Names the news site "The Points Guy"`)
	}
	if !found {
		t.Errorf("issues = %q; want the outlet issue kept for the admin", v.Issues)
	}
	if res.Title == "" || res.Content == "" {
		t.Error("the article was not kept")
	}
	// Links to news sites never survive, in the body or the link list.
	if strings.Contains(res.Content, "onemileatatime.com") {
		t.Errorf("content still links to a news site: %q", res.Content)
	}
	if !strings.Contains(res.Content, "the bonus") {
		t.Errorf("link text was lost: %q", res.Content)
	}
	for _, l := range res.Links {
		if strings.Contains(l.URL, "onemileatatime.com") {
			t.Errorf("link list kept %+v", l)
		}
	}
}

// TestPipeline_ProcessWithFeedback_FlagsOutletMentions covers the admin
// revision path, which has no auto-revision of its own.
func TestPipeline_ProcessWithFeedback_FlagsOutletMentions(t *testing.T) {
	gen := &recordingClient{reply: outletReply}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.ProcessWithFeedback(numberedRefSources(), "<p>old</p>", "tighten it", "")
	if err != nil {
		t.Fatalf("ProcessWithFeedback: %v", err)
	}
	if res.Verification.AttributionCheck || len(res.Verification.Issues) == 0 {
		t.Errorf("verification = %+v; want the outlet mention flagged", res.Verification)
	}
	if strings.Contains(res.Content, "onemileatatime.com") {
		t.Errorf("content still links to a news site: %q", res.Content)
	}
}

// TestPipeline_InlineDetails_DropOutletMentions: extracted details have no
// revision after them, so one naming an outlet is dropped -- unless the title
// names it, which makes the outlet the subject of the story.
func TestPipeline_InlineDetails_DropOutletMentions(t *testing.T) {
	run := func(title string) []KeyDetail {
		gen := &recordingClient{reply: title + "\n\n<p>Body.</p>"}
		client := &perModelMockHFClient{responses: map[string]string{
			"critic": cleanVerdict,
			"details": `{"key_details": [
				{"Label": "Value", "Value": "~1.41 cents according to One Mile at a Time"},
				{"Label": "Product", "Value": "Thrifty Traveler Premium"},
				{"Label": "Deadline", "Value": "October 31, 2026"}], "links": []}`,
		}}
		p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(client, "critic"), 80)
		p.SetDetailModels([]string{"details"})
		res, err := p.Process("deal", numberedRefSources())
		if err != nil {
			t.Fatalf("Process: %v", err)
		}
		return res.KeyDetails
	}
	if got := run("Capital One BA Bonus"); len(got) != 1 || got[0].Label != "Deadline" {
		t.Errorf("details = %+v; want both outlet details dropped", got)
	}
	if got := run("Thrifty Traveler Premium Is 30% Off"); len(got) != 2 || got[0].Label != "Product" {
		t.Errorf("details = %+v; want the subject outlet's detail kept", got)
	}
}

func TestGenerateScoutsTake_OutletRuleInPrompt(t *testing.T) {
	c := &mockHFClient{response: "Transfer now."}
	if _, err := GenerateScoutsTake(c, []string{"m"}, "Scout", "T", "tldr", "body"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.lastPrompt, noOutletsRule) {
		t.Error("Scout's Take prompt is missing the news-site rule")
	}
}
