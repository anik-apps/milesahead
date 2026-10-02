package web

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// queueSort is the order of the rows within each group of the review queue,
// from ?sort= (or the "sort" field of a form posted from the queue).
type queueSort string

const (
	sortNewest    queueSort = "newest" // the default
	sortOldest    queueSort = "oldest"
	sortScoreHigh queueSort = "score"     // highest critic score first
	sortScoreLow  queueSort = "score-low" // lowest critic score first
)

// sortOption is one choice in a page's Sort menu.
type sortOption struct {
	Value, Label string
	Selected     bool
}

// queueSorts are the review queue's orders, as the Sort menu lists them.
var queueSorts = []sortOption{
	{Value: string(sortNewest), Label: "Newest first"},
	{Value: string(sortOldest), Label: "Oldest first"},
	{Value: string(sortScoreHigh), Label: "Highest score"},
	{Value: string(sortScoreLow), Label: "Lowest score"},
}

// publishedSorts are the Published page's orders: by publish date.
var publishedSorts = []sortOption{
	{Value: string(sortNewest), Label: "Newest first"},
	{Value: string(sortOldest), Label: "Oldest first"},
}

// parseSort is s if it is one of options, else the default, newest first.
func parseSort(s string, options []sortOption) queueSort {
	for _, o := range options {
		if o.Value == s {
			return queueSort(s)
		}
	}
	return sortNewest
}

// sortChoices is options with the current one marked, for the Sort menu.
func sortChoices(current queueSort, options []sortOption) []sortOption {
	out := make([]sortOption, len(options))
	for i, o := range options {
		o.Selected = o.Value == string(current)
		out[i] = o
	}
	return out
}

// label is how the sort reads in a sentence ("newest first").
func (s queueSort) label() string {
	switch s {
	case sortOldest:
		return "oldest first"
	case sortScoreHigh:
		return "highest critic score first"
	case sortScoreLow:
		return "lowest critic score first"
	}
	return "newest first"
}

// queueKey is what a queue row is sorted by: the time it is ordered by (when
// it was created for the ready group, when it last changed for the others),
// its critic score, and its id to break ties.
type queueKey struct {
	at       time.Time
	score    int
	hasScore bool
	id       int64
}

// less reports whether a comes before b in this order. Score orders put rows
// the critic has not scored yet last, either way, and break ties newest
// first; every order ends with the id, so it is stable across reloads.
func (s queueSort) less(a, b queueKey) bool {
	if s == sortScoreHigh || s == sortScoreLow {
		if a.hasScore != b.hasScore {
			return a.hasScore
		}
		if a.hasScore && a.score != b.score {
			if s == sortScoreHigh {
				return a.score > b.score
			}
			return a.score < b.score
		}
		return sortNewest.less(queueKey{at: a.at, id: a.id}, queueKey{at: b.at, id: b.id})
	}
	if !a.at.Equal(b.at) {
		if s == sortOldest {
			return a.at.Before(b.at)
		}
		return a.at.After(b.at)
	}
	if s == sortOldest {
		return a.id < b.id
	}
	return a.id > b.id
}

// withSort is the page address u with the sort the admin was viewing (from
// the query or the posted form) added, so an action brings the list back in
// the same order. The default is left out of the URL.
func withSort(r *http.Request, u string, options []sortOption) string {
	s := parseSort(r.FormValue("sort"), options)
	if s == sortNewest {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "sort=" + url.QueryEscape(string(s))
}
