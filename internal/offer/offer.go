// Package offer works out when the offer an article describes ends, and
// whether that date has passed.
//
// The end date comes from the article's structured Key Details, never from
// its prose, and the rule is deliberately simple:
//
//   - Only a Key Detail whose label is one of endLabels names the offer's
//     end. The match is exact (ignoring case, spacing, dashes and a trailing
//     colon): "Travel Dates", "Blackout Dates", "Marriott Booking Deadline"
//     and "Deadline to Use Credit" are not in the list, so they never count.
//   - If any of those values says the offer runs on with no end
//     ("ongoing", "until further notice", "TBD", ...; see openEndedRE), or
//     says it was extended without saying to what date (see extendRE), the
//     article has no end date, whatever else it says.
//   - Otherwise every date those values name is read, and the offer ends on
//     the LATEST of them. A range contributes its end; a date that only opens
//     a window ("starting June 1", "June 1 - Labor Day") contributes nothing.
//
// Reading a date wrongly fails one of two ways, and they are not equal. A
// date read too early marks a live offer as ended: readers are told it is
// over, and it drops off the home page and out of the newsletter. A date read
// too late, or not at all, leaves an ended offer looking current for a while,
// which a reader can recover from. Taking the latest date, and no date at all
// when the text is unsure, keeps every mistake on the recoverable side. The
// known costs: "Register by April 12; travel by July 1" ends on July 1, and
// an old deadline mentioned without a year ("July 31, 2026 (originally June
// 30)", written in July) is read as next year's and ends the offer then.
//
// Dates are calendar days, stored as "2006-01-02". An offer runs through the
// whole of its last day and has ended from the next day on, judged in the
// westernmost US time zone (see Today).
package offer

import (
	"encoding/json"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
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

// endLabels are the only labels that name an offer's end, lower-cased with
// single spaces. "Deadline", "Booking Deadline", "Registration Deadline",
// "Promotion Period", "Sale Period", "Booking Period", "Booking Window" and
// "Expiration Date" are what the generator writes in production; the rest
// are their plain variants. Anything else -- a benefit's, a credit's, a
// stay's or a trip's dates, a blackout, a status or lounge change, a
// brand-qualified deadline -- is not the offer's end.
var endLabels = map[string]bool{
	"deadline":              true,
	"offer deadline":        true,
	"booking deadline":      true,
	"registration deadline": true,
	"application deadline":  true,
	"enrollment deadline":   true,
	"sale deadline":         true,
	"promo deadline":        true,
	"offer ends":            true,
	"offer end date":        true,
	"ends":                  true,
	"end date":              true,
	"expires":               true,
	"expiration date":       true,
	"offer expires":         true,
	"valid through":         true,
	"valid until":           true,
	"book by":               true,
	"apply by":              true,
	"register by":           true,
	"promotion period":      true,
	"offer period":          true,
	"sale period":           true,
	"booking period":        true,
	"booking window":        true,
	"promotion end":         true,
}

var spaceRE = regexp.MustCompile(`\s+`)

// isEndLabel reports whether label, already normalized, is one of endLabels.
func isEndLabel(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.TrimSpace(strings.TrimRight(l, ":"))
	return endLabels[spaceRE.ReplaceAllString(l, " ")]
}

// openEndedRE finds a value that says the offer has no end yet: then the
// article has no end date, whatever dates the value or others name.
var openEndedRE = regexp.MustCompile(`(?i)\b(?:ongoing|until\s+further\s+notice|no\s+(?:set\s+|fixed\s+|specific\s+|hard\s+|firm\s+|announced\s+)?(?:end(?:\s+date)?|expiration(?:\s+date)?|deadline)|tb[adc]|to\s+be\s+(?:announced|determined|confirmed)|indefinite(?:ly)?|open[\s-]ended|onwards?)\b`)

// extendRE finds a value that says the offer was extended. Unless its last
// mention goes on, in the same clause, to the date it was extended to
// (extendedToRE), the value does not say when the offer now ends: "June 30,
// 2026 (extended by one month)" and "Extended (the original June 30 deadline
// has passed)" have no end date.
var (
	extendRE     = regexp.MustCompile(`(?i)\bextend\w*`)
	extendedToRE = regexp.MustCompile(`(?i)^extend\w*(?:[^;()\[\].]|\.\S)*\b(?:to|through|thru|until|till|into)\s+(?:on\s+)?$`)
)

// extendedWithoutDate reports whether value, holding dates, says the offer
// was extended without saying to when.
func extendedWithoutDate(value string, dates []found) bool {
	ext := extendRE.FindAllStringIndex(value, -1)
	if ext == nil {
		return false
	}
	from := ext[len(ext)-1][0]
	for _, f := range dates {
		if f.pos > from && extendedToRE.MatchString(value[from:f.pos]) {
			return false
		}
	}
	return true
}

// monthNames matches a month name or abbreviation. Its first three letters
// are the key into months.
const monthNames = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// monthDateRE matches "July 29", "Jul. 29th", "Sept.30", "Sep 30 2026" and
// "August 22, 2026". A month and year with no day ("October 2026")
// deliberately does not match: its last day would be a guess.
var monthDateRE = regexp.MustCompile(`(?i)\b` + monthNames + `(?:\.\s*|\s+)(\d{1,2})(?:st|nd|rd|th)?\b(?:(?:,\s*|\s+)(\d{4})\b)?`)

// timeAfterDayRE follows a number that is an hour, not a day: the "11" of
// "30 June 11:59pm", or the "23" of the British "30 June 23.59", which
// monthDateRE would otherwise read as June 11 and June 23.
var timeAfterDayRE = regexp.MustCompile(`(?i)^(?:st|nd|rd|th)?(?::\d|\.\d\d(?:\b|\s*[ap])|\s*(?:[ap]\.?\s?m\b|[ap]\.m\.))`)

// dayRangeRE continues a match of monthDateRE when a range shares its month:
// the "-10" in "July 7-10, 2026". Values are normalized first, so every dash
// is "-" by now.
var dayRangeRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(\d{1,2})(?:st|nd|rd|th)?\b`)

// notADayRE follows a number that is a time, an amount or part of a numeric
// date rather than a day: the "11" of "June 1 - 11:59 PM", the "5" of "June
// 1 - 5 PM ET", the "10" of "June 1 - 10 ET", the "15" of "June 1 - 15,000
// points", the "07" of "March 1 - 07/01/2026".
var notADayRE = regexp.MustCompile(`(?i)^\s*(?::|/|\.\d|,\d{3}\b|%|x\b|k\b|[ap]\.?\s?m\b|[ap]\.m\.|noon\b|midnight\b|(?:e|c|m|p|h|ak)[sd]?t\b|utc\b|gmt\b|bst\b|(?:eastern|central|mountain|pacific)\b|(?:points?|pts|miles?|nights?|avios)\b)`)

// yearAfterRE reads the year after a day: ", 2026" or " 2026".
var yearAfterRE = regexp.MustCompile(`^(?:,\s*|\s+)(\d{4})\b`)

// isoDateRE matches "2026-07-29", and the date of "2026-07-29T23:59".
var isoDateRE = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})(?:T|\b)`)

// numericDateRE matches a US numeric date with a four-digit year,
// "6/30/2026". Only the year makes it a date: "5/24" is a card rule, not May
// 24.
var numericDateRE = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)

// openRangeRE follows a date that opens a window whose end is not a date the
// reader can read: "June 1 - Labor Day", "June 1 through July", "June 1 -",
// "June 1 to book". Such a date is a start, not an end. A number after the
// joiner is a time or an amount ("June 30 - 11:59 PM ET"), or the end of a
// range that is read on its own, so it leaves the date as it is.
var openRangeRE = regexp.MustCompile(`(?i)^\s*,?\s*(?:-|(?:to|through|thru|until|till)\b)\s*(?:$|[^\d\s])`)

// rangeJoinRE is the whole text between the two ends of a range: the " - "
// of "March 1 - 07/01/2026".
var rangeJoinRE = regexp.MustCompile(`(?i)^\s*,?\s*(?:-|to|through|thru|until|till)\s*$`)

// startRE comes before a date that opens a window ("starting May 1", "from
// April 6", "launched June 1"). A start is never an end.
var startRE = regexp.MustCompile(`(?i)\b(?:start(?:s|ed|ing)?|begin(?:s|ning)?|began|from|effective|as\s+of|since|launch(?:es|ed|ing)?|open(?:s|ed)|went\s+live)\s+(?:on\s+)?$`)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March,
	"apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dec": time.December,
}

// found is one date read from a value. year is 0 when the text gave none.
type found struct {
	pos, end int // byte offsets of the date in the value
	year     int
	month    time.Month
	day      int
	numeric  bool // ISO or 6/30/2026: without a plausible year it is no date at all
	start    bool // opens a window, so it is not an end
}

// sameMonthEnd reads the end of a range sharing its start's month, the "10,
// 2026" of "July 7-10, 2026", from the text after the start f. A number that
// is a time ("June 1 - 11:59 PM") or an amount ("June 1 - 15,000 points"),
// that is no later than the start day, or that is not a day of the month is
// not a range end.
func sameMonthEnd(s string, f found) (found, bool) {
	rest := s[f.end:]
	r := dayRangeRE.FindStringSubmatchIndex(rest)
	if r == nil || notADayRE.MatchString(rest[r[1]:]) {
		return found{}, false
	}
	day, _ := strconv.Atoi(rest[r[2]:r[3]])
	// 2028 is a leap year, so February 29 passes here.
	if day <= f.day || !valid(2028, f.month, day) {
		return found{}, false
	}
	e := found{pos: f.end + r[2], end: f.end + r[1], month: f.month, day: day}
	if y := yearAfterRE.FindStringSubmatchIndex(rest[r[1]:]); y != nil {
		e.year, _ = strconv.Atoi(rest[r[1]+y[2] : r[1]+y[3]])
		e.end += y[1]
	}
	return e, true
}

// scanDates returns the dates written in s, in the order they appear.
func scanDates(s string) []found {
	var out []found
	for _, m := range monthDateRE.FindAllStringSubmatchIndex(s, -1) {
		if timeAfterDayRE.MatchString(s[m[5]:]) {
			// "30 June 11:59pm": the number is an hour.
			continue
		}
		f := found{pos: m[0], end: m[1], month: months[strings.ToLower(s[m[2]:m[2]+3])]}
		f.day, _ = strconv.Atoi(s[m[4]:m[5]])
		if m[6] >= 0 {
			f.year, _ = strconv.Atoi(s[m[6]:m[7]])
		} else if e, ok := sameMonthEnd(s, f); ok {
			// "July 7-10, 2026": July 7 opens the range, July 10 ends it.
			f.start = true
			out = append(out, f, e)
			continue
		}
		out = append(out, f)
	}
	for _, m := range isoDateRE.FindAllStringSubmatchIndex(s, -1) {
		year, _ := strconv.Atoi(s[m[2]:m[3]])
		mon, _ := strconv.Atoi(s[m[4]:m[5]])
		day, _ := strconv.Atoi(s[m[6]:m[7]])
		out = append(out, found{pos: m[0], end: m[1], year: year, month: time.Month(mon), day: day, numeric: true})
	}
	for _, m := range numericDateRE.FindAllStringSubmatchIndex(s, -1) {
		a, _ := strconv.Atoi(s[m[2]:m[3]])
		b, _ := strconv.Atoi(s[m[4]:m[5]])
		year, _ := strconv.Atoi(s[m[6]:m[7]])
		f := found{pos: m[0], end: m[1], year: year, month: time.Month(a), day: b, numeric: true}
		if a > 12 {
			// "30/6/2026" can only be day first.
			f.month, f.day = time.Month(b), a
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	for i := range out {
		f := &out[i]
		if startRE.MatchString(s[:f.pos]) || openRangeRE.MatchString(s[f.end:]) ||
			i+1 < len(out) && f.end <= out[i+1].pos && rangeJoinRE.MatchString(s[f.end:out[i+1].pos]) {
			f.start = true
		}
	}
	return out
}

// yearlessGraceDays is how far before the article's writing a date given
// without a year may fall and still be read as the one just gone rather than
// next year's.
const yearlessGraceDays = 14

// yearsBack and yearsAhead bound the years an offer's dates can be in,
// around the year the value was written: "December 31, 1000 bonus points"
// does not name the year 1000, and "March 31, 2500 points" not 2500.
const (
	yearsBack  = 2
	yearsAhead = 5
)

// valid reports whether y-m-d is a real calendar day (no February 30).
func valid(y int, m time.Month, d int) bool {
	if m < time.January || m > time.December || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && t.Month() == m && t.Day() == d
}

// calendarDay is f as a calendar day, for a value written at anchor, if it is one.
//
// A year more than yearsAhead after the one written in is no date at all
// ("December 31, 2099" is not an end to count down to). One more than
// yearsBack before it is not a year: a month-name date keeps its month and
// day and is read as yearless, and an ISO or numeric date is no date.
//
// A date written without a year is the first occurrence of that day no more
// than yearlessGraceDays before the article was written. Offers are written
// about while they run, so a yearless deadline is almost always still ahead
// ("through December 31" written in May is this December, not the one
// gone); the grace covers a story written just after an offer closed.
func (f found) calendarDay(anchor time.Time) (time.Time, bool) {
	y := f.year
	switch {
	case y > anchor.Year()+yearsAhead:
		return time.Time{}, false
	case y != 0 && y < anchor.Year()-yearsBack:
		if f.numeric {
			return time.Time{}, false
		}
		y = 0
	}
	if y != 0 {
		if !valid(y, f.month, f.day) {
			return time.Time{}, false
		}
		return time.Date(y, f.month, f.day, 0, 0, 0, 0, time.UTC), true
	}
	written := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	floor := written.AddDate(0, 0, -yearlessGraceDays)
	for y := anchor.Year() - 1; y <= anchor.Year()+1; y++ {
		if !valid(y, f.month, f.day) {
			continue
		}
		if t := time.Date(y, f.month, f.day, 0, 0, 0, 0, time.UTC); !t.Before(floor) {
			return t, true
		}
	}
	return time.Time{}, false
}

// valueEnd reads value, the normalized value of an end label written at
// anchor. It returns the latest date the value names that is not a start,
// or open when the value says the offer has no end.
func valueEnd(value string, anchor time.Time) (end time.Time, open bool) {
	if openEndedRE.MatchString(value) {
		return time.Time{}, true
	}
	dates := scanDates(value)
	if extendedWithoutDate(value, dates) {
		return time.Time{}, true
	}
	for _, f := range dates {
		if f.start {
			continue
		}
		if t, ok := f.calendarDay(anchor); ok && t.After(end) {
			end = t
		}
	}
	return end, false
}

// EndDate returns the last day of the offer described by keyDetailsJSON, as
// "2006-01-02", or "" when the details state no end date that can be read.
//
// anchor is when the details were written (the article's created_at). It
// places a date given without a year and bounds which years are plausible.
//
// See the package comment for the rule: the latest date in any detail with
// an end label, or none when one of those says the offer has no end.
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
		if !isEndLabel(normalize(d.Label)) {
			continue
		}
		t, open := valueEnd(normalize(d.Value), anchor)
		if open {
			return ""
		}
		if t.After(end) {
			end = t
		}
	}
	if end.IsZero() {
		return ""
	}
	return end.Format(DateLayout)
}

// normalize turns the typographic spaces and dashes the generator writes into
// ASCII, so the patterns above -- whose \s matches only ASCII whitespace --
// read "July 29, 2026" and "June 15‑July 15" exactly like
// "July 29, 2026" and "June 15-July 15". Every Unicode space becomes " " and
// every dash (hyphens, en and em dashes, the minus sign) becomes "-". So
// does an arrow ("June 30 → July 31", "June 30 -> July 31"), which joins a
// range or an extension the way a dash does.
func normalize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r < 0x80:
			return r
		case unicode.IsSpace(r):
			return ' '
		case unicode.Is(unicode.Pd, r) || r == '−':
			return '-'
		case r == '→' || r == '⟶' || r == '⇒' || r == '➔' || r == '➜':
			return '-'
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "->", "-")
}

// Location loads a configured timezone, falling back to UTC for an empty or
// unknown name. The newsletter's send schedule runs in it.
func Location(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		// A misspelt zone would silently shift the schedule by the UTC
		// offset, so say so.
		slog.Warn("unknown timezone; using UTC", "timezone", name, "error", err)
		return time.UTC
	}
	return loc
}

// lastDayZone is where an offer's last day is judged to be over: Hawaii
// (UTC-10, no daylight saving time), the westernmost US time zone.
//
// Deadlines are written as "11:59 PM ET", "11:59 PM PT" or "HT", or with no
// zone at all, and the stored date does not say which. Judged in New York, a
// Pacific-time offer showed as ended from midnight to 3 AM Eastern on its
// last night. Judged in Hawaii, no US deadline is called ended before it has
// passed. The cost falls on the recoverable side: an Eastern-time offer shows
// as live for up to six hours after it closes, until midnight in Honolulu.
// The weekly newsletter's Saturday 8 AM Eastern send is past midnight in
// Hawaii, so an offer that ended the evening before is still left out of it.
var lastDayZone = time.FixedZone("HST", -10*60*60)

// Today returns the date offer end dates are compared against, as
// "2006-01-02": now's calendar date in lastDayZone.
func Today(now time.Time) string {
	return now.In(lastDayZone).Format(DateLayout)
}

// Ended reports whether an offer whose last day is endsOn has ended by today.
// Both are "2006-01-02", today from Today. An offer with no end date never
// ends, and the last day itself still counts as live.
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
