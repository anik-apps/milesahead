package web

import (
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// articleTypes is every article type, in the order the homepage's type
// switch lists them. The switch shows only the ones with a published article.
var articleTypes = []string{"deal", "roundup", "alert", "summary"}

// typeFilterLabels names each type in the homepage's type switch. "summary"
// articles are news stories, and read as "News" to readers.
var typeFilterLabels = map[string]string{
	"deal":    "Deals",
	"roundup": "Roundups",
	"alert":   "Alerts",
	"summary": "News",
}

// homeTopics are the topic tags offered as chips in the homepage filter bar,
// in display order. It is a subset of ai.ValidTags: "loyalty-programs" and
// "points-deals" are on most articles, so as filters they barely narrow
// anything. Articles still carry them, and /?tag=loyalty-programs still works.
var homeTopics = []string{
	"airlines", "hotels", "credit-cards", "transfer-bonuses", "bank-bonuses", "travel-deals",
}

// typeBadge is the text of an article's type badge on public pages: the type
// itself, except that "summary" articles are labelled "News". CSS classes and
// URLs keep the stored type.
func typeBadge(articleType string) string {
	if articleType == "summary" {
		return "News"
	}
	return articleType
}

// tagName turns a tag slug into its display name: "credit-cards" becomes
// "Credit Cards".
func tagName(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if r, size := utf8.DecodeRuneInString(w); size > 0 {
			words[i] = string(unicode.ToUpper(r)) + w[size:]
		}
	}
	return strings.Join(words, " ")
}

// typeFilterLabel is a type's name in the filter UI, or the value itself for
// a type the site does not have.
func typeFilterLabel(articleType string) string {
	if l, ok := typeFilterLabels[articleType]; ok {
		return l
	}
	return articleType
}

// homeURL is the homepage URL with the given type and tag filters; an empty
// value leaves that filter out.
func homeURL(articleType, tag string) string {
	v := url.Values{}
	if articleType != "" {
		v.Set("type", articleType)
	}
	if tag != "" {
		v.Set("tag", tag)
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// filterLink is one item in the homepage filter bar.
type filterLink struct {
	Label  string
	URL    string
	Active bool
}

// activeFilter is an applied filter in the strip above the articles, with the
// URL that removes it and keeps the other.
type activeFilter struct {
	Label     string
	RemoveURL string
}

// homeFilters is the homepage filter bar and the strip of applied filters.
type homeFilters struct {
	Types  []filterLink // "All", then each type with a published article
	Topics []filterLink
	// Active lists the applied filters, type first. Empty when unfiltered,
	// which hides the strip.
	Active []activeFilter
	// Summary names the applied filters for the empty state, e.g.
	// "Deals · Hotels".
	Summary string
}

// buildHomeFilters lays out the filter bar for the current type and tag.
// publishedTypes are the types with at least one published article. Every
// link keeps the other filter: a topic chip keeps the type, a type keeps the
// topic, and the active topic chip removes just the topic.
func buildHomeFilters(articleType, tag string, publishedTypes []string) homeFilters {
	f := homeFilters{
		Types: []filterLink{{Label: "All", URL: homeURL("", tag), Active: articleType == ""}},
	}
	for _, t := range articleTypes {
		if slices.Contains(publishedTypes, t) {
			f.Types = append(f.Types, filterLink{Label: typeFilterLabel(t), URL: homeURL(t, tag), Active: articleType == t})
		}
	}
	for _, slug := range homeTopics {
		link := filterLink{Label: tagName(slug), URL: homeURL(articleType, slug)}
		if tag == slug {
			link.Active = true
			link.URL = homeURL(articleType, "")
		}
		f.Topics = append(f.Topics, link)
	}

	var names []string
	if articleType != "" {
		f.Active = append(f.Active, activeFilter{Label: typeFilterLabel(articleType), RemoveURL: homeURL("", tag)})
		names = append(names, typeFilterLabel(articleType))
	}
	if tag != "" {
		f.Active = append(f.Active, activeFilter{Label: tagName(tag), RemoveURL: homeURL(articleType, "")})
		names = append(names, tagName(tag))
	}
	f.Summary = strings.Join(names, " · ")
	return f
}
