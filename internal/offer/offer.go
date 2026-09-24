// Package offer works out when the offer an article describes ends, and
// whether that date has passed.
//
// The end date comes from the article's structured Key Details, never from
// its prose: the generator already writes a "Deadline" detail for time-limited
// offers, and the details are stored as JSON. An article whose details state
// no parseable end date has none -- this package never guesses one.
//
// Dates are calendar days, stored as "2006-01-02". An offer runs through the
// whole of its last day in the site's timezone and has ended from the next
// day on.
package offer

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DateLayout is how an end date is stored and compared. ISO dates compare
// correctly as plain strings, which is what lets SQL filter on them.
const DateLayout = "2006-01-02"

// displayLayout is how an end date is shown to readers.
const displayLayout = "January 2, 2006"

// keyDetail is one stored Key Details entry. Rows written from Go structs
// carry "Label"/"Value" and rows written from model JSON carry
// "label"/"value"; encoding/json matches either spelling.
type keyDetail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// labelKind says how a Key Details entry relates to the offer's end.
type labelKind int

const (
	notAnEnd labelKind = iota
	// deadlineLabel is a last day to act ("Deadline", "Booking Deadline").
	// When its value names several dates, the earliest is the one that
	// matters: once it passes, a new reader can no longer take the offer.
	deadlineLabel
	// periodLabel is the window the offer runs in ("Promotion Period:
	// April 6 - June 7, 2026"). Its last date is the end.
	periodLabel
)

// endLabels name an offer's end without the word "deadline". Exact matches
// only: a qualified label such as "Lounge Access End Date" or "Free Days
// Expiration" describes a benefit or a reward's validity, not the offer.
// A bare "Expiration" is left out for the same reason -- in the stored data
// it always describes how long points or gift cards stay valid.
var endLabels = map[string]bool{
	"ends":                  true,
	"end date":              true,
	"offer ends":            true,
	"offer end":             true,
	"offer end date":        true,
	"promotion ends":        true,
	"promotion end date":    true,
	"promo ends":            true,
	"sale ends":             true,
	"bonus ends":            true,
	"expires":               true,
	"expiration date":       true,
	"offer expires":         true,
	"offer expiration":      true,
	"offer expiration date": true,
	"valid through":         true,
	"valid until":           true,
	"available through":     true,
	"available until":       true,
	"last day":              true,
}

// periodLabels name the window an offer runs in. Travel and stay windows are
// deliberately absent: they run past the point where the offer can be taken.
var periodLabels = map[string]bool{
	"promotion period":    true,
	"promo period":        true,
	"offer period":        true,
	"offer dates":         true,
	"offer window":        true,
	"offer duration":      true,
	"promotion dates":     true,
	"sale period":         true,
	"sale dates":          true,
	"booking period":      true,
	"booking window":      true,
	"registration period": true,
	"registration window": true,
	"purchase period":     true,
	"purchase window":     true,
}

var spaceRE = regexp.MustCompile(`\s+`)

func classify(label string) labelKind {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.TrimRight(l, ":")
	l = spaceRE.ReplaceAllString(l, " ")
	switch {
	case strings.Contains(l, "deadline"):
		return deadlineLabel
	case endLabels[l]:
		return deadlineLabel
	case periodLabels[l]:
		return periodLabel
	}
	return notAnEnd
}

// monthDateRE matches "July 29", "Jul. 29th", "Sep 30 2026" and "August 22,
// 2026". A month and year with no day ("October 2026") deliberately does not
// match: its last day would be a guess.
var monthDateRE = regexp.MustCompile(`(?i)\b(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\.?\s+(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(\d{4})\b)?`)

// dayRangeRE continues a match of monthDateRE when a range shares its month:
// the "-10, 2026" in "July 7-10, 2026".
var dayRangeRE = regexp.MustCompile(`^\s*(?:-|–|—|to|through|thru)\s*(\d{1,2})(?:st|nd|rd|th)?\b(?:,?\s+(\d{4})\b)?`)

// rangeJoinRE is the text between the two ends of a range.
var rangeJoinRE = regexp.MustCompile(`(?i)^\s*(?:-|–|—|to|through|thru|until)\s*$`)

// isoDateRE matches "2026-07-29".
var isoDateRE = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)

// beforeRE catches "before July 29", whose last day is July 28.
var beforeRE = regexp.MustCompile(`(?i)\bbefore\s+$`)

// startRE catches a date that opens a window ("starting May 1", "from April
// 6"). A start is never an end, even when it is the only date in a period.
var startRE = regexp.MustCompile(`(?i)\b(?:start(?:s|ing)?|begin(?:s|ning)?|from|effective|as of|launch(?:es|ing)?|after)\s+(?:on\s+)?$`)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March,
	"apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dec": time.December,
}

// found is one date read from a value. year is 0 when the text gave none.
type found struct {
	pos, end  int // byte offsets of the date in the value
	year      int
	month     time.Month
	day       int
	dayBefore bool // "before July 29": the last day is the day before
	start     bool // "starting May 1": opens a window, so it is not an end
}

// scanDates returns the dates written in s, in the order they appear.
func scanDates(s string) []found {
	var out []found
	for _, m := range monthDateRE.FindAllStringSubmatchIndex(s, -1) {
		mon := months[strings.ToLower(s[m[2]:m[2]+3])]
		day, _ := strconv.Atoi(s[m[4]:m[5]])
		year := 0
		if m[6] >= 0 {
			year, _ = strconv.Atoi(s[m[6]:m[7]])
		}
		f := found{pos: m[0], end: m[1], year: year, month: mon, day: day,
			dayBefore: beforeRE.MatchString(s[:m[0]]), start: startRE.MatchString(s[:m[0]])}
		if year == 0 {
			// "July 7-10, 2026": the range's end shares the month, and its
			// year belongs to both ends.
			if r := dayRangeRE.FindStringSubmatchIndex(s[m[1]:]); r != nil {
				endDay, _ := strconv.Atoi(s[m[1]+r[2] : m[1]+r[3]])
				endYear := 0
				if r[4] >= 0 {
					endYear, _ = strconv.Atoi(s[m[1]+r[4] : m[1]+r[5]])
				}
				f.start = true
				out = append(out, f)
				out = append(out, found{pos: m[1] + r[0], end: m[1] + r[1], year: endYear, month: mon, day: endDay})
				continue
			}
		}
		out = append(out, f)
	}
	for _, m := range isoDateRE.FindAllStringSubmatchIndex(s, -1) {
		year, _ := strconv.Atoi(s[m[2]:m[3]])
		mon, _ := strconv.Atoi(s[m[4]:m[5]])
		day, _ := strconv.Atoi(s[m[6]:m[7]])
		out = append(out, found{pos: m[0], end: m[1], year: year, month: time.Month(mon), day: day,
			dayBefore: beforeRE.MatchString(s[:m[0]]), start: startRE.MatchString(s[:m[0]])})
	}
	// Keep reading order: the ISO pass appends after the month-name pass.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].pos < out[j-1].pos; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	// "April 6 - June 7, 2026": the first date of a range opens it.
	for i := 0; i+1 < len(out); i++ {
		if out[i].end <= out[i+1].pos && rangeJoinRE.MatchString(s[out[i].end:out[i+1].pos]) {
			out[i].start = true
		}
	}
	return out
}

// yearlessGraceDays is how far before the article's writing a date given
// without a year may fall and still be read as the one just gone rather than
// next year's.
const yearlessGraceDays = 60

// valid reports whether y-m-d is a real calendar day (no February 30).
func valid(y int, m time.Month, d int) bool {
	if m < time.January || m > time.December || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && t.Month() == m && t.Day() == d
}

// resolve turns the dates in one value into calendar days.
//
// A date written without a year takes the year of the next date in the same
// value that has one ("April 6 - June 7, 2026"), stepping back a year when it
// falls after that date ("December 17 - January 5, 2027"). With no year
// anywhere in the value ("Deadline: June 30"), it is the first occurrence of
// that day no more than yearlessGraceDays before the article was written.
// Offers are written about while they run, so a yearless deadline is almost
// always still ahead ("through December 31" written in May is this December,
// not the one gone); the grace covers a story written just after an offer
// closed. Dates that open a window are skipped: they are never the end.
func resolve(dates []found, anchor time.Time) []time.Time {
	var out []time.Time
	for i, f := range dates {
		if f.start {
			continue
		}
		year := f.year
		if year == 0 {
			for _, later := range dates[i+1:] {
				if later.year == 0 {
					continue
				}
				year = later.year
				if f.month > later.month || (f.month == later.month && f.day > later.day) {
					year--
				}
				break
			}
		}
		var t time.Time
		if year != 0 {
			if !valid(year, f.month, f.day) {
				continue
			}
			t = time.Date(year, f.month, f.day, 0, 0, 0, 0, time.UTC)
		} else {
			ok := false
			floor := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC).
				AddDate(0, 0, -yearlessGraceDays)
			for _, y := range []int{anchor.Year() - 1, anchor.Year(), anchor.Year() + 1} {
				if !valid(y, f.month, f.day) {
					continue
				}
				if c := time.Date(y, f.month, f.day, 0, 0, 0, 0, time.UTC); !c.Before(floor) {
					t, ok = c, true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if f.dayBefore {
			t = t.AddDate(0, 0, -1)
		}
		out = append(out, t)
	}
	return out
}

// EndDate returns the last day of the offer described by keyDetailsJSON, as
// "2006-01-02", or "" when the details state no end date that can be read.
//
// anchor is when the details were written (the article's created_at). It is
// only consulted for a date given without a year.
//
// When several details state an end -- a registration deadline and a travel
// deadline, say -- the earliest wins: after it, a reader can no longer take
// the offer.
func EndDate(keyDetailsJSON string, anchor time.Time) string {
	s := strings.TrimSpace(keyDetailsJSON)
	if s == "" || s == "[]" || s == "null" {
		return ""
	}
	var details []keyDetail
	if err := json.Unmarshal([]byte(s), &details); err != nil {
		return ""
	}
	var end time.Time
	for _, d := range details {
		kind := classify(d.Label)
		if kind == notAnEnd {
			continue
		}
		dates := resolve(scanDates(d.Value), anchor)
		if len(dates) == 0 {
			continue
		}
		pick := dates[0]
		for _, t := range dates[1:] {
			if kind == deadlineLabel && t.Before(pick) || kind == periodLabel && t.After(pick) {
				pick = t
			}
		}
		if end.IsZero() || pick.Before(end) {
			end = pick
		}
	}
	if end.IsZero() {
		return ""
	}
	return end.Format(DateLayout)
}

// Location loads the site's timezone, falling back to UTC for an empty or
// unknown name -- the same fallback the newsletter's send schedule uses, so
// the site and the newsletter agree on what day it is.
func Location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Today returns now's calendar date in loc, as "2006-01-02".
func Today(now time.Time, loc *time.Location) string {
	return now.In(loc).Format(DateLayout)
}

// Ended reports whether an offer whose last day is endsOn has ended by today.
// Both are "2006-01-02". An offer with no end date never ends, and the last
// day itself still counts as live.
func Ended(endsOn, today string) bool {
	return endsOn != "" && endsOn < today
}

// Display formats a stored end date for readers ("July 29, 2026"), or returns
// "" for an empty or malformed one.
func Display(endsOn string) string {
	t, err := time.Parse(DateLayout, endsOn)
	if err != nil {
		return ""
	}
	return t.Format(displayLayout)
}
