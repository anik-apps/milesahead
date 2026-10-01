package web

import (
	"regexp"
	"sort"
	"strings"

	"github.com/kumaranik/milesahead/internal/db"
)

// cardLink is another card offered from a card page: a rival to compare with,
// or a card from the same category.
type cardLink struct {
	Slug, Title, ImageURL string
	CompareURL            string // /compare?cards=<this>,<that>
}

// cardHref finds links to other card pages in a card's section HTML.
var cardHref = regexp.MustCompile(`href="/cards/([a-z0-9][a-z0-9-]*)"`)

const (
	maxRivals  = 3 // cards offered for a one-tap comparison
	maxRelated = 3 // more cards from the same category
	// verdictLeadMax caps the verdict's first sentence in the summary panel.
	verdictLeadMax = 220
)

// linkedCardSlugs lists the card pages a card's sections link to, in order of
// first appearance. These are the rivals the page itself discusses.
func linkedCardSlugs(sections []db.ReferenceSection) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range sections {
		for _, m := range cardHref.FindAllStringSubmatch(s.Content, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}

// applyLabel names the button that leaves for the issuer. A card that is
// closed to new applicants gets "Card details", not "Apply".
func applyLabel(issuer string, closed bool) string {
	issuer = strings.TrimSpace(issuer)
	switch {
	case issuer == "" && closed:
		return "Card details on the issuer's site"
	case issuer == "":
		return "Apply on the issuer's site"
	case closed:
		return "Card details at " + issuer
	}
	return "Apply at " + issuer
}

// cardDetailData is the card page's summary, actions and next steps, built
// from the card and the other published cards. all may include the card
// itself and unpublished pages; both are skipped.
func cardDetailData(page *db.ReferencePage, sections []db.ReferenceSection, all []db.ReferencePage) map[string]interface{} {
	others := map[string]*db.ReferencePage{}
	var sameCat []*db.ReferencePage
	for i := range all {
		c := &all[i]
		// The handler passes published cards only; the status check keeps
		// this safe for any other caller.
		if c.Slug == page.Slug || c.Status != "published" {
			continue
		}
		others[c.Slug] = c
		if c.Category == page.Category {
			sameCat = append(sameCat, c)
		}
	}
	sort.SliceStable(sameCat, func(i, j int) bool { return strings.ToLower(sameCat[i].Title) < strings.ToLower(sameCat[j].Title) })

	link := func(c *db.ReferencePage) cardLink {
		return cardLink{Slug: c.Slug, Title: c.Title, ImageURL: c.ImageURL, CompareURL: "/compare?cards=" + page.Slug + "," + c.Slug}
	}

	// Rivals: the cards the page's own text links to; failing that, the first
	// cards in its category.
	used := map[string]bool{}
	var named, rivals, related []cardLink
	for _, slug := range linkedCardSlugs(sections) {
		if c, ok := others[slug]; ok {
			named = append(named, link(c))
		}
	}
	rivals = named
	if len(rivals) > maxRivals {
		rivals = rivals[:maxRivals]
	}
	if len(rivals) == 0 {
		for _, c := range sameCat {
			if len(rivals) == maxRivals {
				break
			}
			rivals = append(rivals, link(c))
		}
	}
	for _, r := range rivals {
		used[r.Slug] = true
	}
	for _, c := range sameCat {
		if len(related) == maxRelated {
			break
		}
		if !used[c.Slug] {
			related = append(related, link(c))
		}
	}

	// The compare list: the cards the page names first, then every other
	// published card by category.
	var groups []compareGroup
	inNamed := map[string]bool{}
	if len(named) > 0 {
		g := compareGroup{Category: "Named on this page"}
		for _, n := range named {
			g.Cards = append(g.Cards, struct{ Slug, Title string }{n.Slug, n.Title})
			inNamed[n.Slug] = true
		}
		groups = append(groups, g)
	}
	byCat := map[string][]*db.ReferencePage{}
	present := map[string]bool{}
	for _, c := range others {
		if !inNamed[c.Slug] {
			byCat[c.Category] = append(byCat[c.Category], c)
			present[c.Category] = true
		}
	}
	for _, cat := range categoriesInOrder(present) {
		cs := byCat[cat]
		if len(cs) == 0 {
			continue
		}
		sort.SliceStable(cs, func(i, j int) bool { return strings.ToLower(cs[i].Title) < strings.ToLower(cs[j].Title) })
		g := compareGroup{Category: categoryName(cat)}
		for _, c := range cs {
			g.Cards = append(g.Cards, struct{ Slug, Title string }{c.Slug, c.Title})
		}
		groups = append(groups, g)
	}

	offer := parseOffer(page.WelcomeBonusText)
	return map[string]interface{}{
		"Facts":         buildCardFacts(page),
		"VerdictLead":   leadSentence(page.ScoutsVerdict, verdictLeadMax),
		"ApplyLabel":    applyLabel(page.Issuer, offer.Closed),
		"ApplyHost":     applyHost(page.ApplyURL),
		"Rivals":        rivals,
		"Related":       related,
		"CategoryName":  categoryName(page.Category),
		"CategoryURL":   cardsQuery{Cat: page.Category}.url(),
		"CompareGroups": groups,
		"CompareCount":  len(others),
	}
}
