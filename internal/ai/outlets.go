package ai

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Outlet names and links.
//
// Articles are written from third-party news sites and blogs (the sources
// table), and the site owner's rule is that articles do not name or refer to
// them. The prompts say so, but a prompt is a request, so generated text is
// also checked here for any outlet the pipeline knows of: the article's own
// sources, every row of the sources table, and a few well-known outlets and
// their abbreviations. Issuers, airlines and hotel chains are never in the
// list -- naming them is the point of the article.
//
// Names match as whole words, ignoring case, except that a multi-word name
// written all in lower case is taken to be the ordinary phrase ("one mile at a
// time"). A written-out URL on an outlet's site is a mention too; names are
// not matched inside URLs, and link targets, which readers do not see, are
// handled separately (StripOutletLinks).
//
// Two narrow exceptions exist (the outlet is itself the subject of the story,
// or an unconfirmed report only that outlet carries). They cannot be judged
// deterministically, so the check still reports such a mention; the pipeline
// asks for one revision, and if the mention survives it the article is kept
// with a failed attribution check for the admin to judge.

// Outlet is a third-party news site or blog articles are written from.
type Outlet struct {
	Name string
	// URL is any page on the site; its host identifies links to the outlet.
	URL string
}

// OutletMention is one place visible text names an outlet.
type OutletMention struct {
	// Outlet is the outlet's canonical name.
	Outlet string
	// Text is the mention as written ("TPG", "One Mile at a Time's").
	Text string
	// Sentence is the visible sentence the mention is in, for the issue and
	// for the admin.
	Sentence string
}

// knownOutlets are always checked, whatever the sources table holds: the
// outlets articles have been written from most, and the sites the prompts
// have always banned links to.
var knownOutlets = []Outlet{
	{Name: "The Points Guy", URL: "https://thepointsguy.com"},
	{Name: "One Mile at a Time", URL: "https://onemileatatime.com"},
	{Name: "Doctor of Credit", URL: "https://www.doctorofcredit.com"},
	{Name: "View from the Wing", URL: "https://viewfromthewing.com"},
	{Name: "Upgraded Points", URL: "https://upgradedpoints.com"},
	{Name: "Loyalty Lobby", URL: "https://loyaltylobby.com"},
	{Name: "Frequent Miler", URL: "https://frequentmiler.com"},
	{Name: "Live and Let's Fly", URL: "https://liveandletsfly.com"},
	{Name: "Miles from Blighty", URL: "https://milesfromblighty.com"},
	{Name: "AwardWallet", URL: "https://awardwallet.com"},
	{Name: "Thrifty Traveler", URL: "https://thriftytraveler.com"},
	{Name: "Miles to Memories", URL: "https://milestomemories.com"},
}

// outletAliases are abbreviations readers know these outlets by. "DoC" and
// "FM" are matched in exactly these spellings: lower-cased, "doc" and "fm"
// are ordinary words.
var outletAliases = []struct {
	alias, outlet string
	exactCase     bool
}{
	{"TPG", "The Points Guy", false},
	{"OMAAT", "One Mile at a Time", false},
	{"VFTW", "View from the Wing", false},
	{"DoC", "Doctor of Credit", true},
	{"DOC", "Doctor of Credit", true},
	{"FM", "Frequent Miler", true},
}

// genericOutletWords are names too ordinary to match on their own. A sources
// row called "Points" must not flag every article about points.
var genericOutletWords = map[string]bool{
	"points": true, "miles": true, "travel": true, "news": true, "deals": true,
	"credit": true, "rewards": true, "blog": true, "unknown": true, "cards": true,
	"loyalty": true, "frequent": true, "flyer": true, "airline": true, "hotel": true,
}

// sharedHosts host many unrelated sites, so a source on one says nothing
// about other links to it.
var sharedHosts = map[string]bool{
	"feeds.feedburner.com": true, "feedburner.com": true, "medium.com": true,
	"substack.com": true, "blogspot.com": true, "wordpress.com": true,
	"news.google.com": true, "google.com": true, "youtube.com": true,
}

type outletTerm struct {
	outlet string
	re     *regexp.Regexp
	// needsCapital skips an all-lower-case match. Several outlet names are
	// also ordinary phrases ("chipping away one mile at a time", "the view
	// from the wing"); written as the outlet, they carry capitals.
	needsCapital bool
}

// OutletMatcher finds outlet names in text and links to outlet sites.
type OutletMatcher struct {
	names []string
	terms []outletTerm
	hosts map[string]string // host without "www." -> outlet
}

// NewOutletMatcher builds a matcher for the given outlets plus the built-in
// ones and their abbreviations. Names that are empty, "Unknown" or a single
// generic word are skipped.
func NewOutletMatcher(outlets []Outlet) *OutletMatcher {
	m := &OutletMatcher{hosts: map[string]string{}}
	seenName := map[string]string{} // normalized name -> canonical
	add := func(o Outlet) {
		name := strings.TrimSpace(o.Name)
		key := normalizeOutlet(name)
		if w := outletWords(name); len(w) > 1 && strings.EqualFold(w[0], "the") {
			key = normalizeOutlet(strings.Join(w[1:], "")) // "The Frequent Miler" is "Frequent Miler"
		}
		canonical := name
		if prev, ok := seenName[key]; ok {
			canonical = prev
		} else if re := outletNameRE(name); re != nil {
			seenName[key] = name
			m.names = append(m.names, name)
			m.terms = append(m.terms, outletTerm{outlet: name, re: re, needsCapital: len(outletWords(name)) > 1})
		} else {
			canonical = ""
		}
		if h := outletHost(o.URL); h != "" {
			if canonical == "" {
				canonical = h
			}
			if _, ok := m.hosts[h]; !ok {
				m.hosts[h] = canonical
				// A domain written out in prose is a mention too.
				m.terms = append(m.terms, outletTerm{outlet: canonical,
					re: regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(h) + `\b`)})
			}
		}
	}
	for _, o := range knownOutlets {
		add(o)
	}
	for _, o := range outlets {
		add(o)
	}
	for _, a := range outletAliases {
		flags := "(?i)"
		if a.exactCase {
			flags = ""
		}
		m.terms = append(m.terms, outletTerm{outlet: a.outlet,
			re: regexp.MustCompile(flags + `\b` + regexp.QuoteMeta(a.alias) + `\b`)})
	}
	sort.Strings(m.names)
	return m
}

// Names returns the outlet names the matcher checks, sorted.
func (m *OutletMatcher) Names() []string { return append([]string(nil), m.names...) }

// outletNameRE builds the pattern for one outlet name: case-insensitive,
// whole words, a leading "The" optional, and the words allowed to run
// together or be split ("LoyaltyLobby" and "Loyalty Lobby" are the same).
func outletNameRE(name string) *regexp.Regexp {
	words := outletWords(name)
	if len(words) == 0 {
		return nil
	}
	letters := strings.Join(words, "")
	if len(letters) < 4 || (len(words) == 1 && genericOutletWords[strings.ToLower(letters)]) ||
		strings.EqualFold(letters, "unknown") {
		return nil
	}
	optionalThe := false
	if len(words) > 1 && strings.EqualFold(words[0], "the") {
		words, optionalThe = words[1:], true
	}
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = regexp.QuoteMeta(w)
	}
	pat := strings.Join(quoted, `[\s\-'’.]*`)
	if optionalThe {
		pat = `(?:the\s+)?` + pat
	}
	return regexp.MustCompile(`(?i)\b` + pat + `\b`)
}

// outletWords splits a name into its words, breaking on anything that is
// not a letter or digit and at lower-to-upper case changes ("LoyaltyLobby").
func outletWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

// outletHost returns the host of an outlet URL without "www.", or "" when
// there is none or it is a shared host.
func outletHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if !strings.Contains(h, ".") || sharedHosts[h] {
		return ""
	}
	return h
}

// OutletForURL reports the outlet a link points to, if any: the link's host
// is an outlet's host or a subdomain of it.
func (m *OutletMatcher) OutletForURL(raw string) (string, bool) {
	h := outletHost(raw)
	for h != "" {
		if name, ok := m.hosts[h]; ok {
			return name, true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 || !strings.Contains(h[i+1:], ".") {
			break
		}
		h = h[i+1:]
	}
	return "", false
}

var (
	blockTagRE = regexp.MustCompile(`(?i)</?(?:p|li|ul|ol|h[1-6]|div|br|hr|tr|td|th|table|blockquote|section)\b[^>]*>`)
	anyTagRE   = regexp.MustCompile(`<[^>]*>`)
	bareURLRE  = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>"')\]]+`)
	spacesRE   = regexp.MustCompile(`[ \t]+`)
)

// VisibleText is what a reader sees of an HTML fragment: tags removed (block
// tags become line breaks), entities decoded, and written-out URLs dropped,
// since an address is not prose. The text of a link is kept.
func VisibleText(s string) string {
	return spacesRE.ReplaceAllString(bareURLRE.ReplaceAllString(visibleWithURLs(s), " "), " ")
}

// visibleWithURLs is VisibleText with written-out URLs kept.
func visibleWithURLs(s string) string {
	s = blockTagRE.ReplaceAllString(s, "\n")
	s = anyTagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return spacesRE.ReplaceAllString(s, " ")
}

// Find returns every outlet mention in the visible text of s, in order: an
// outlet's name, abbreviation or domain, and a written-out URL on its site
// ("https://milestomemories.com/...", "www.doctorofcredit.com", or a link
// whose text is its own address). Link targets are not visible and are not
// checked here; see StripOutletLinks.
func (m *OutletMatcher) Find(s string) []OutletMention {
	if m == nil || strings.TrimSpace(s) == "" {
		return nil
	}
	full := visibleWithURLs(s)
	type hit struct {
		start, end int
		outlet     string
	}
	var hits []hit
	// URLs first, then blanked out to the same length, so names are not
	// matched inside them and positions still line up with full.
	text := []byte(full)
	for _, loc := range bareURLRE.FindAllStringIndex(full, -1) {
		raw := strings.TrimRight(full[loc[0]:loc[1]], ".,;:!?")
		if name, ok := m.OutletForURL(raw); ok {
			hits = append(hits, hit{loc[0], loc[0] + len(raw), name})
		}
		for i := loc[0]; i < loc[1]; i++ {
			text[i] = ' '
		}
	}
	prose := string(text)
	for _, t := range m.terms {
		for _, loc := range t.re.FindAllStringIndex(prose, -1) {
			if t.needsCapital && strings.ToLower(prose[loc[0]:loc[1]]) == prose[loc[0]:loc[1]] {
				continue
			}
			hits = append(hits, hit{loc[0], loc[1], t.outlet})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].start != hits[j].start {
			return hits[i].start < hits[j].start
		}
		return hits[i].end > hits[j].end
	})
	var out []OutletMention
	lastEnd := -1
	for _, h := range hits {
		if h.start < lastEnd {
			continue // inside a longer mention already reported
		}
		lastEnd = h.end
		out = append(out, OutletMention{Outlet: h.outlet, Text: full[h.start:h.end],
			Sentence: sentenceAround(full, h.start, h.end)})
	}
	return out
}

// sentenceAround returns the sentence containing text[start:end], bounded by
// sentence punctuation or a line break and capped at a readable length on a
// word boundary.
func sentenceAround(text string, start, end int) string {
	const maxSide = 200
	from := start
	for from > 0 && start-from < maxSide {
		if text[from-1] == '\n' || (text[from-1] == ' ' && endsSentence(text[:from-1])) {
			break
		}
		from--
	}
	if from > 0 && start-from >= maxSide {
		if i := strings.IndexByte(text[from:start], ' '); i >= 0 {
			from += i + 1
		}
	}
	to := end
	for to < len(text) && to-end < maxSide {
		if text[to] == '\n' || (text[to] == ' ' && endsSentence(text[:to])) {
			break
		}
		to++
	}
	if to < len(text) && to-end >= maxSide {
		if i := strings.LastIndexByte(text[end:to], ' '); i >= 0 {
			to = end + i
		}
	}
	return strings.TrimSpace(text[from:to])
}

// endsSentence reports whether s ends a sentence: ".", "!" or "?", possibly
// followed by closing quotes or brackets.
func endsSentence(s string) bool {
	s = strings.TrimRight(s, "\"')]”’")
	return s != "" && strings.ContainsRune(".!?", rune(s[len(s)-1]))
}

// anchorRE matches an <a> element with an href, capturing the URL and the
// link text.
var anchorRE = regexp.MustCompile(`(?is)<a\b[^>]*?\bhref\s*=\s*["']?([^"'\s>]+)["']?[^>]*>(.*?)</a>`)

// StripOutletLinks unwraps every link to an outlet's site, keeping its text:
// articles link to official pages only. It returns the outlets whose links
// were removed.
func (m *OutletMatcher) StripOutletLinks(s string) (string, []string) {
	if m == nil || !strings.Contains(strings.ToLower(s), "<a") {
		return s, nil
	}
	var removed []string
	out := replaceEach(anchorRE, s, func(g []string, _, _ string) string {
		name, ok := m.OutletForURL(html.UnescapeString(g[1]))
		if !ok {
			return g[0]
		}
		removed = append(removed, name)
		return g[2]
	})
	return out, removed
}

// DropOutletLinks removes links to outlet sites from a link list.
func (m *OutletMatcher) DropOutletLinks(links []ArticleLink) []ArticleLink {
	if m == nil {
		return links
	}
	var kept []ArticleLink
	for _, l := range links {
		if _, ok := m.OutletForURL(l.URL); ok {
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

// OutletsFor is the outlets to check an article against: the given list
// (the sources table) plus the article's own sources.
func OutletsFor(sources []SourceArticle, table []Outlet) []Outlet {
	out := append([]Outlet(nil), table...)
	for _, s := range sources {
		out = append(out, Outlet{Name: s.Name, URL: s.URL})
	}
	return out
}

// outletExceptions is the owner's rule for when an outlet may be named,
// verbatim. The prompts and the issue raised for a mention both quote it.
const outletExceptions = `1. The outlet is itself the subject of the story (e.g. a sale on Thrifty Traveler Premium).
2. An unconfirmed report whose only source is that outlet — then one mention, phrased as a report ("reportedly, according to X").`

// OutletIssues turns mentions into critic issues, one per outlet, worded for
// the revision pass.
func OutletIssues(mentions []OutletMention) []string {
	var issues []string
	seen := map[string]bool{}
	for _, mn := range mentions {
		if seen[mn.Outlet] {
			continue
		}
		seen[mn.Outlet] = true
		issues = append(issues, fmt.Sprintf("Names the news site %q (%q, in: %q). Remove the mention and state the fact "+
			"without attribution, unless one of these exceptions applies: %s", mn.Outlet, mn.Text, mn.Sentence,
			strings.ReplaceAll(outletExceptions, "\n", " ")))
	}
	return issues
}

// NamedIn returns the outlets text names.
func (m *OutletMatcher) NamedIn(text string) map[string]bool {
	named := map[string]bool{}
	for _, mn := range m.Find(text) {
		named[mn.Outlet] = true
	}
	return named
}

// withoutOutlets drops the mentions of the given outlets.
func withoutOutlets(mentions []OutletMention, skip map[string]bool) []OutletMention {
	var out []OutletMention
	for _, mn := range mentions {
		if !skip[mn.Outlet] {
			out = append(out, mn)
		}
	}
	return out
}

// DerivedTextMentions checks text derived from a finished article (Scout's
// Take, extracted key details), which has no revision pass after it. It
// returns the mentions of outlets the article's title does not name: an outlet
// the title names is taken to be the subject of the story (the first
// exception), which the derived text may name too.
func (m *OutletMatcher) DerivedTextMentions(text, title string) []OutletMention {
	return withoutOutlets(m.Find(text), m.NamedIn(title))
}

// HTMLLinks lists the <a href> links in an HTML fragment, with their visible
// text.
func HTMLLinks(s string) []ArticleLink {
	var out []ArticleLink
	for _, g := range anchorRE.FindAllStringSubmatch(s, -1) {
		out = append(out, ArticleLink{URL: html.UnescapeString(g[1]), Text: strings.TrimSpace(VisibleText(g[2]))})
	}
	return out
}
