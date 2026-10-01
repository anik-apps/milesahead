package web

import (
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/offer"
)

// This file holds the article page's helpers: the story's own card first,
// short card verdicts, the hero image's srcset, external links in the story,
// related stories and share links.

// relatedStoryLimit is how many related stories an article page lists.
const relatedStoryLimit = 3

// verdictLeadMax is the most of a card's verdict, in characters, that an
// article's card row shows. The full verdict is on the card's page.
const verdictLeadMax = 180

// heroWidth and heroHeight are the article photo band's size on a desktop
// screen, in CSS px. The <img> carries them so the browser reserves the band
// before the photo loads; CSS crops the photo to fill it at every width.
const (
	heroWidth  = 1100
	heroHeight = 280
)

// heroSizes is the hero's rendered width for the srcset: the page width less
// main's padding on phones, the article column (at most 1100px) elsewhere.
const heroSizes = "(max-width: 768px) calc(100vw - 2rem), 1100px"

// broadTags are on most articles, so as a basis for related stories they
// match almost anything (the home filter bar leaves them out for the same
// reason). They are used only when an article has no other tag.
var broadTags = []string{"loyalty-programs", "points-deals"}

// relatedStory is one entry in an article's related stories.
type relatedStory struct {
	Title       string
	Slug        string
	Type        string
	PublishedAt string
	Ended       bool
}

// shareLinks are the plain share URLs at the end of an article page. None of
// them loads anything from a third party until the reader follows it.
type shareLinks struct {
	URL   string // the article's canonical URL, for "Copy link"
	Email string // mailto: with the title as subject and the URL as body
	X     string // X's post intent
}

// storyCardsFirst moves the cards the headline names ahead of the others,
// keeping each group in its stored order. The stored order is whatever the
// linker chose, so an article about the Freedom Flex could lead with the
// Sapphire Reserve. A card counts as named when its title, or its title less
// the issuer wherever it appears ("The Platinum Card from American Express"),
// is in the headline as whole words ("Freedom Flex" in "Chase Freedom Flex
// $250 Bonus Returns").
func storyCardsFirst(cards []linkedCardEmbed, headline string) []linkedCardEmbed {
	h := " " + wordsOnly(headline) + " "
	named := func(c linkedCardEmbed) bool {
		short := c.Title
		if c.Issuer != "" {
			short = strings.Replace(c.Title, c.Issuer, "", 1)
		}
		for _, name := range []string{c.Title, short} {
			if n := wordsOnly(name); n != "" && strings.Contains(h, " "+n+" ") {
				return true
			}
		}
		return false
	}
	out := slices.Clone(cards)
	sort.SliceStable(out, func(i, j int) bool { return named(out[i]) && !named(out[j]) })
	return out
}

// wordsOnly lower-cases s and reduces it to its letters and digits separated
// by single spaces, so names compare as whole words: "Sapphire Preferred®"
// and "sapphire-preferred" both become "sapphire preferred".
func wordsOnly(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), " ")
}

// sentenceEndRE finds the end of a sentence: '.', '!' or '?', possibly closed
// by a quote or bracket, then white space. "1.5x" and "$3.50" do not end one.
var sentenceEndRE = regexp.MustCompile(`[.!?]["'”’)\]]*\s`)

// verdictLead is the start of a card verdict for an article's card row: the
// first paragraph's whole sentences that fit in max characters, or, when the
// first sentence alone is longer, that sentence cut at a word with "…".
func verdictLead(verdict string, max int) string {
	text := strings.TrimSpace(stripHTML(verdict))
	if i := strings.Index(text, "\n\n"); i >= 0 {
		text = text[:i]
	}
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	cut := 0
	for _, m := range sentenceEndRE.FindAllStringIndex(text+" ", -1) {
		end := m[1] - 1 // drop the white space
		if utf8.RuneCountInString(text[:end]) > max {
			break
		}
		cut = end
	}
	if cut > 0 {
		return strings.TrimSpace(text[:cut])
	}
	runes := []rune(text)[:max]
	s := string(runes)
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " ,;:—–-") + "…"
}

// annualFeeLabel is a card's fee as its row on an article shows it.
func annualFeeLabel(amount int) string {
	if amount <= 0 {
		return "No annual fee"
	}
	return fmt.Sprintf("$%d annual fee", amount)
}

// applyLabel names where a card's apply link goes: "Apply at Chase".
func applyLabel(issuer string) string {
	if issuer = strings.TrimSpace(issuer); issuer == "" {
		return "Apply"
	}
	return "Apply at " + issuer
}

// heroSrcset offers the stored thumbnail and display image as sizes of the
// article photo, when the two URLs are sizes of one picture whose widths the
// URLs fix (see images.SrcsetWidths) and the display image is the wider. A
// phone on a 1x screen then fetches the thumbnail. "" otherwise, and for URLs
// with a comma or white space, which separate srcset entries.
func heroSrcset(thumbnail, image string) string {
	tw, iw, ok := images.SrcsetWidths(thumbnail, image)
	if !ok || iw <= tw || strings.ContainsAny(thumbnail+image, ", \t\n\r\f") {
		return ""
	}
	return fmt.Sprintf("%s %dw, %s %dw", thumbnail, tw, image, iw)
}

// articleTopic is the topic an article's related stories and its "more in"
// link are about, the most specific of its tags: its first homepage topic
// other than "credit-cards"; else its first other tag that is not a broad
// one; else "credit-cards", which is on most card and points stories (an
// ended Citi transfer bonus should point to Transfer Bonuses, an Amex story
// to its Amex tag); else its first tag; "" with no tags.
func articleTopic(tags []string) string {
	for _, t := range tags {
		if t != "credit-cards" && slices.Contains(homeTopics, t) {
			return t
		}
	}
	for _, t := range tags {
		if t != "credit-cards" && !slices.Contains(broadTags, t) {
			return t
		}
	}
	if slices.Contains(tags, "credit-cards") {
		return "credit-cards"
	}
	if len(tags) > 0 {
		return tags[0]
	}
	return ""
}

// relatedTags are the tags related stories may share with an article: all of
// its tags but the broad ones, or the broad ones when it has nothing else.
func relatedTags(tags []string) []string {
	var specific []string
	for _, t := range tags {
		if !slices.Contains(broadTags, t) {
			specific = append(specific, t)
		}
	}
	if len(specific) > 0 {
		return specific
	}
	return tags
}

// relatedStories lists up to relatedStoryLimit approved stories sharing a tag
// with the article, live offers first (see db.ListRelatedArticles).
func (app *App) relatedStories(a *db.GeneratedArticle, tags []string, topic, today string) []relatedStory {
	rel, err := app.db.ListRelatedArticles(a.ID, topic, relatedTags(tags), today, relatedStoryLimit)
	if err != nil {
		slog.Warn("article: loading related stories", "article_id", a.ID, "error", err)
		return nil
	}
	out := make([]relatedStory, 0, len(rel))
	for _, r := range rel {
		s := relatedStory{Title: r.Title, Slug: r.Slug, Type: r.Type, Ended: offer.Ended(r.ExpiresOn, today)}
		if r.PublishedAt != nil {
			s.PublishedAt = r.PublishedAt.Format("Jan 2, 2006")
		}
		out = append(out, s)
	}
	return out
}

// buildShareLinks returns the share URLs for an article.
func buildShareLinks(title, canonicalURL string) shareLinks {
	q := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return shareLinks{
		URL:   canonicalURL,
		Email: "mailto:?subject=" + q(title) + "&body=" + q(title+"\n\n"+canonicalURL),
		X:     "https://x.com/intent/post?text=" + q(title) + "&url=" + q(canonicalURL),
	}
}

// anchorTagRE finds an opening <a> tag; hrefAttrRE and targetAttrRE read its
// attributes.
var (
	anchorTagRE  = regexp.MustCompile(`(?i)<a\s[^>]*>`)
	hrefAttrRE   = regexp.MustCompile(`(?i)\shref\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	targetAttrRE = regexp.MustCompile(`(?i)\starget\s*=`)
	relAttrRE    = regexp.MustCompile(`(?i)(\srel\s*=\s*)(?:"([^"]*)"|'([^']*)')`)
)

// externalLinksNewTab makes the story's links to other sites open in a new
// tab, as the generator writes them, and leaves links within the site (the
// card pages, other articles) in the reader's tab. main.js used to force every
// story link into a new tab, which turned "Freedom Flex" into a new tab of
// our own card page. A link that already sets a target keeps it.
func externalLinksNewTab(content, domain string) string {
	return anchorTagRE.ReplaceAllStringFunc(content, func(tag string) string {
		m := hrefAttrRE.FindStringSubmatch(tag)
		if m == nil || targetAttrRE.MatchString(tag) || !isExternalURL(m[1]+m[2], domain) {
			return tag
		}
		// A new tab must not get a handle on this one: add noopener, to an
		// existing rel if there is one.
		if m := relAttrRE.FindStringSubmatchIndex(tag); m != nil {
			start, end := m[4], m[5] // the double-quoted value
			if start < 0 {
				start, end = m[6], m[7]
			}
			if rel := strings.Fields(strings.ToLower(tag[start:end])); !slices.Contains(rel, "noopener") && !slices.Contains(rel, "noreferrer") {
				add := " noopener"
				if start == end {
					add = "noopener"
				}
				tag = tag[:end] + add + tag[end:]
			}
			return tag[:len(tag)-1] + ` target="_blank">`
		}
		return tag[:len(tag)-1] + ` target="_blank" rel="noopener">`
	})
}

// isExternalURL reports whether href leads off the site: an absolute http(s)
// URL, or a scheme-relative one, whose host is not domain or a subdomain of
// it. Relative links, fragments and mailto: stay.
func isExternalURL(href, domain string) bool {
	href = strings.TrimSpace(href)
	lower := strings.ToLower(href)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "//") {
		return false
	}
	u, err := url.Parse(href)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	domain = strings.ToLower(domain)
	return domain == "" || (host != domain && !strings.HasSuffix(host, "."+domain))
}
