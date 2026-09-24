// Package offer works out when the offer an article describes ends, and
// whether that date has passed.
//
// The end date comes from the article's structured Key Details, never from
// its prose: the generator already writes a "Deadline" detail for time-limited
// offers, and the details are stored as JSON. An article whose details state
// no parseable end date has none -- this package never guesses one.
//
// Reading a date wrongly fails one of two ways, and they are not equal. A
// date read too early marks a live offer as ended: readers are told it is
// over, and it drops off the home page and out of the newsletter. That is the
// worst failure. A date read too late, or not at all, leaves an ended offer
// looking current for a while, which a reader can recover from. So wherever
// a value is ambiguous, the parser takes the later date, or none.
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
	"ends on":               true,
	"end date":              true,
	"offer ends":            true,
	"offer ends on":         true,
	"offer end":             true,
	"offer end date":        true,
	"promotion ends":        true,
	"promotion end":         true,
	"promotion end date":    true,
	"promo ends":            true,
	"promo end":             true,
	"sale ends":             true,
	"bonus ends":            true,
	"expires":               true,
	"expires on":            true,
	"expiration date":       true,
	"offer expires":         true,
	"offer expiration":      true,
	"offer expiration date": true,
	"valid through":         true,
	"valid until":           true,
	"offer valid through":   true,
	"offer valid until":     true,
	"available through":     true,
	"available until":       true,
	"last day":              true,
	"book by":               true,
	"apply by":              true,
	"register by":           true,
	"enroll by":             true,
	"sign up by":            true,
	"purchase by":           true,
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

// staleLabelRE marks a label for an end that no longer applies ("Original
// Deadline", "Previous Deadline"): something has replaced it.
var staleLabelRE = regexp.MustCompile(`\b(?:original|originally|previous|previously|old|former|formerly|initial|initially|prior)\b`)

// benefitDeadlineRE marks a deadline for using or earning a benefit
// ("Deadline to Use Credit", "Discount Deadline", "Spend Deadline"), not for
// taking the offer. The offer can run on past it -- a card's sign-up bonus
// stays open after its first-year discount window closes -- so it never sets
// the end.
var benefitDeadlineRE = regexp.MustCompile(`\b(?:use|using|usage|credits?|discounts?|redeem|redeeming|redemptions?|spend|spending|statement)\b`)

func classify(label string) labelKind {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.TrimRight(l, ":")
	l = spaceRE.ReplaceAllString(l, " ")
	if staleLabelRE.MatchString(l) {
		return notAnEnd
	}
	switch {
	case strings.Contains(l, "deadline"):
		if benefitDeadlineRE.MatchString(l) {
			return notAnEnd
		}
		return deadlineLabel
	case endLabels[l]:
		return deadlineLabel
	case periodLabels[l]:
		return periodLabel
	}
	return notAnEnd
}

// monthNames matches a month name or abbreviation. Its first three letters
// are the key into months.
const monthNames = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// monthDateRE matches "July 29", "Jul. 29th", "Sept.30", "Sep 30 2026" and
// "August 22, 2026". A month and year with no day ("October 2026")
// deliberately does not match: its last day would be a guess.
var monthDateRE = regexp.MustCompile(`(?i)\b` + monthNames + `(?:\.\s*|\s+)(\d{1,2})(?:st|nd|rd|th)?\b(?:(?:,\s*|\s+)(\d{4})\b)?`)

// dayRangeRE continues a match of monthDateRE when a range shares its month:
// the "-10" in "July 7-10, 2026". Values are normalized first, so every dash
// is "-" by now.
var dayRangeRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(\d{1,2})(?:st|nd|rd|th)?\b`)

// notADayRE follows a number that is a time or an amount rather than a day:
// the "11" of "June 30 - 11:59 PM", the "5" of "June 30 - 5 PM".
var notADayRE = regexp.MustCompile(`(?i)^\s*(?::|%|[ap]\.?\s?m\b|[ap]\.m\.)`)

// yearAfterRE reads the year after a day: ", 2026" or " 2026".
var yearAfterRE = regexp.MustCompile(`^(?:,\s*|\s+)(\d{4})\b`)

// rangeJoinRE is the text between the two ends of a range.
var rangeJoinRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*$`)

// betweenRE and andJoinRE read "between June 1 and June 30" as a range.
var (
	betweenRE = regexp.MustCompile(`(?i)\bbetween\s+$`)
	andJoinRE = regexp.MustCompile(`(?i)^\s*and\s*$`)
)

// isoDateRE matches "2026-07-29", and the date of "2026-07-29T23:59".
var isoDateRE = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})(?:T|\b)`)

// numericDateRE matches a US month/day/year date, "6/30/2026". Only a
// four-digit year makes it a date: "5/24" is a card rule, not May 24.
var numericDateRE = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)

// beforeRE catches "before July 29", whose last day is July 28. orBeforeRE
// catches "on or before July 29", whose last day is July 29 itself.
var (
	beforeRE   = regexp.MustCompile(`(?i)\bbefore\s+$`)
	orBeforeRE = regexp.MustCompile(`(?i)\bor\s+before\s+$`)
)

// startRE catches a date that opens a window ("starting May 1", "from April
// 6", "launched June 1", "live since June 1"). A start is never an end, even
// when it is the only date in a period.
var startRE = regexp.MustCompile(`(?i)\b(?:start(?:s|ed|ing)?|begin(?:s|ning)?|began|begun|from|effective|as\s+of|launch(?:es|ed|ing)?|after|since|open(?:s|ed|ing)?|kick(?:s|ed|ing)?\s+off|(?:went|goes|go|going)\s+live|return(?:s|ed|ing)|debut(?:s|ed)?|introduced|rolled\s+out)\s+(?:on\s+)?$`)

// supersededRE catches a date something else replaced ("was September 30",
// "instead of June 30", "old deadline June 30"), and pastEndRE one already
// over ("expired May 31"). Such a date gives way to any other date in the
// value; alone, it stands only when the value says the offer is over.
var (
	supersededRE = regexp.MustCompile(`(?i)(?:\b(?:was|were|previously|originally|formerly|initially|instead\s+of|rather\s+than|replac(?:es|ed|ing)|not)|\b(?:old|original|previous|prior|earlier|former|initial)\s+(?:deadline|end\s+date|end|date|expiration(?:\s+date)?)(?:\s+(?:was|of|is))?\s*:?)\s+(?:on\s+)?$`)
	pastEndRE    = regexp.MustCompile(`(?i)\b(?:ended|expired|closed|lapsed)\s+(?:on\s+)?$`)
)

// replacementRE catches a date that replaces every date written before it:
// the "July 31" of "June 30, now extended through July 31", "June 30 (replaced
// by July 31)" and "New deadline July 31". A "now" has to open its clause: in
// "book now through June 30" it means from today.
var replacementRE = regexp.MustCompile(`(?i)(?:\b(?:extended|extends|extension|pushed(?:\s+back)?|moved|postponed|delayed|changed|(?:replaced|superseded)\s+by|new\s+(?:deadline|end\s+date|end|date))|(?:^|[,;(])\s*now)\s*:?\s*(?:(?:to|through|thru|until|till|into|out\s+to)\s+)?(?:on\s+)?$`)

// endedWordRE finds a value that says the offer is over ("Was September 19,
// 2026 (offer has ended)"), which keeps a superseded date that stands alone.
var endedWordRE = regexp.MustCompile(`(?i)\b(?:ended|expired|is\s+over|now\s+over|no\s+longer\s+(?:available|offered|live|running)|discontinued)\b`)

// historyRE finds a clause about an earlier run of the offer ("last year's
// version ended June 30, 2025", "prior offer expired May 31"). Its dates are
// never this offer's end.
var historyRE = regexp.MustCompile(`(?i)\b(?:last\s+(?:year|time|round|version|promo(?:tion)?|offer|sale|run)|(?:prior|previous|earlier|past)\s+(?:years?|offers?|promos?|promotions?|versions?|rounds?|sales?|bonus(?:es)?|runs?))\b`)

// clauseBreakRE splits a value into clauses, so a history marker reaches
// only the dates in its own clause.
var clauseBreakRE = regexp.MustCompile(`[;()\[\]]|\.\s`)

// backToNowRE turns a clause from an earlier run back to this one: "Unlike
// the previous offer, this one ends June 30".
var backToNowRE = regexp.MustCompile(`(?i)\b(?:this|current|new|now)\b`)

// incidentalRE catches a date that says when the text was written, not when
// the offer ends ("last updated May 1", "announced on August 3").
var incidentalRE = regexp.MustCompile(`(?i)\b(?:updated|posted|published|announced|reported)\s+(?:on\s+)?$`)

// openEndRE follows a date that opens a window with no end yet: "June 1 -
// TBD", "June 1 - ongoing", "June 1, 2026 until further notice", "June 1
// onward". Only these explicit words make the date a start. A dash is also
// how the generator sets off a remark ("September 30, 2026 - act fast"), and
// "through" can name a channel ("June 30 through the Chase portal"), so
// either followed by anything else leaves the date as the end.
var openEndRE = regexp.MustCompile(`(?i)^\s*,?\s*(?:(?:-|to|through|thru|until|till)\s*(?:tbd|tba|tbc|to\s+be\s+(?:announced|determined|confirmed)|ongoing|present|open[\s-]?ended|indefinite(?:ly)?|further\s+notice|(?:while|as\s+long\s+as)\s+supplies|supplies\s+last|(?:the\s+)?end\s+of\s+(?:the\s+)?(?:year|month|quarter|season|summer|winter|spring|fall|autumn)|year[\s-]end|no\s+(?:set\s+|fixed\s+)?end(?:\s+date)?|not\s+(?:yet\s+)?(?:announced|set|known))|onwards?|forward|(?:and|or)\s+(?:later|beyond|after|onwards?))\b`)

// danglingJoinRE follows a date that opens a range whose end is missing:
// "Promotion Period: June 1 -".
var danglingJoinRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(?:$|[);.])`)

// monthOnlyEndRE is a range that ends on a month with no day: "June 1
// through July" runs to July 31. The month must end the clause, so "June 30
// - may end early" is not read as running to May.
var monthOnlyEndRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*\b` + monthNames + `\b\.?(?:(?:,\s*|\s+)(\d{4})\b)?\s*(?:$|[;,.()])`)

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March,
	"apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dec": time.December,
}

// found is one date read from a value. year is 0 when the text gave none.
type found struct {
	pos, end   int // byte offsets of the date in the value
	year       int
	month      time.Month
	day        int  // unused when monthEnd
	monthEnd   bool // "through July": the last day of the month
	numeric    bool // ISO or 6/30/2026: without a plausible year it is no date at all
	dayBefore  bool // "before July 29": the last day is the day before
	start      bool // "starting May 1": opens a window, so it is not an end
	superseded bool // "was September 30": replaced by another date, if any
	history    bool // "last year's version ended June 30": an earlier run
	incidental bool // "updated May 1": not about the offer's end at all
	replaces   bool // "now extended to July 31": the dates before it are superseded
}

// candidate reports whether f can be the end in its own right.
func (f found) candidate() bool {
	return !f.start && !f.superseded && !f.history && !f.incidental
}

// readPrefix sets what the words just before a date in s say about it.
func (f *found) readPrefix(s string) {
	prefix := s[:f.pos]
	if beforeRE.MatchString(prefix) && !orBeforeRE.MatchString(prefix) {
		f.dayBefore = true
	}
	if startRE.MatchString(prefix) {
		f.start = true
	}
	if supersededRE.MatchString(prefix) || pastEndRE.MatchString(prefix) {
		f.superseded = true
	}
	if incidentalRE.MatchString(prefix) {
		f.incidental = true
	}
	if replacementRE.MatchString(prefix) {
		f.replaces = true
	}
	clause := prefix
	if breaks := clauseBreakRE.FindAllStringIndex(prefix, -1); len(breaks) > 0 {
		clause = prefix[breaks[len(breaks)-1][1]:]
	}
	if h := historyRE.FindAllStringIndex(clause, -1); h != nil && !backToNowRE.MatchString(clause[h[len(h)-1][1]:]) {
		f.history = true
	}
}

// sameMonthEnd reads the end of a range sharing its start's month, the "10,
// 2026" of "July 7-10, 2026", from the text after the start f. A number that
// is a time ("June 30 - 11:59 PM") or no later than the start day is not a
// range end.
func sameMonthEnd(s string, f found) (found, bool) {
	rest := s[f.end:]
	r := dayRangeRE.FindStringSubmatchIndex(rest)
	if r == nil || notADayRE.MatchString(rest[r[1]:]) {
		return found{}, false
	}
	day, _ := strconv.Atoi(rest[r[2]:r[3]])
	if day <= f.day {
		return found{}, false
	}
	e := found{pos: f.end + r[2], end: f.end + r[1], month: f.month, day: day}
	if y := yearAfterRE.FindStringSubmatchIndex(rest[r[1]:]); y != nil {
		e.year, _ = strconv.Atoi(rest[r[1]+y[2] : r[1]+y[3]])
		e.end += y[1]
	}
	return e, true
}

// monthOnlyEnd reads "through July" after the range start f, whose text runs
// to s[f.end:next], as the last day of July.
func monthOnlyEnd(s string, f found, next int) (found, bool) {
	gap := s[f.end:next]
	m := monthOnlyEndRE.FindStringSubmatchIndex(gap)
	// A capital letter: "may" in "- may end early" is not the month.
	if m == nil || !unicode.IsUpper(rune(gap[m[2]])) {
		return found{}, false
	}
	e := found{pos: f.end + m[2], end: f.end + m[3], month: months[strings.ToLower(gap[m[2]:m[2]+3])], monthEnd: true}
	switch {
	case m[4] >= 0:
		e.year, _ = strconv.Atoi(gap[m[4]:m[5]])
	case f.year != 0:
		e.year = f.year
		if e.month < f.month {
			e.year++
		}
	}
	return e, true
}

// scanDates returns the dates written in s, in the order they appear, with
// what the text around each says about it.
func scanDates(s string) []found {
	var out []found
	for _, m := range monthDateRE.FindAllStringSubmatchIndex(s, -1) {
		mon := months[strings.ToLower(s[m[2]:m[2]+3])]
		day, _ := strconv.Atoi(s[m[4]:m[5]])
		year := 0
		if m[6] >= 0 {
			year, _ = strconv.Atoi(s[m[6]:m[7]])
		}
		f := found{pos: m[0], end: m[1], year: year, month: mon, day: day}
		if year == 0 {
			// "July 7-10, 2026": the range's end shares the month, and its
			// year belongs to both ends.
			if e, ok := sameMonthEnd(s, f); ok {
				f.start = true
				out = append(out, f, e)
				continue
			}
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
		mon, _ := strconv.Atoi(s[m[2]:m[3]])
		day, _ := strconv.Atoi(s[m[4]:m[5]])
		year, _ := strconv.Atoi(s[m[6]:m[7]])
		out = append(out, found{pos: m[0], end: m[1], year: year, month: time.Month(mon), day: day, numeric: true})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })

	for i := range out {
		out[i].readPrefix(s)
	}

	var monthEnds []found
	for i := range out {
		f := &out[i]
		next := len(s)
		if i+1 < len(out) {
			next = out[i+1].pos
		}
		if f.end > next {
			continue
		}
		gap := s[f.end:next]
		switch {
		// "April 6 - June 7, 2026": the first date of a range opens it.
		case i+1 < len(out) && rangeJoinRE.MatchString(gap):
			f.start = true
		case i+1 < len(out) && andJoinRE.MatchString(gap) && betweenRE.MatchString(s[:f.pos]):
			f.start = true
		// "June 1 - TBD", "June 1 -": a window with no end yet.
		case openEndRE.MatchString(s[f.end:]) || danglingJoinRE.MatchString(s[f.end:]):
			f.start = true
		default:
			// "June 1 through July": the window ends with July.
			if e, ok := monthOnlyEnd(s, *f, next); ok {
				f.start = true
				monthEnds = append(monthEnds, e)
			}
		}
	}
	if len(monthEnds) > 0 {
		for i := range monthEnds {
			monthEnds[i].readPrefix(s)
		}
		out = append(out, monthEnds...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	}

	// "June 30, now extended through July 31": the extension supersedes
	// every date written before it.
	for i := range out {
		if out[i].replaces {
			for j := 0; j < i; j++ {
				out[j].superseded = true
			}
		}
	}
	return out
}

// yearlessGraceDays is how far before the article's writing a date given
// without a year may fall and still be read as the one just gone rather than
// next year's.
const yearlessGraceDays = 14

// plausibleYear reports whether year can be in an offer's dates when the
// value was written at anchor: "December 31, 1000 bonus points" does not
// name the year 1000.
func plausibleYear(year int, anchor time.Time) bool {
	return year >= anchor.Year()-2 && year <= anchor.Year()+5
}

// valid reports whether y-m-d is a real calendar day (no February 30).
func valid(y int, m time.Month, d int) bool {
	if m < time.January || m > time.December || d < 1 || d > 31 {
		return false
	}
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && t.Month() == m && t.Day() == d
}

// dayIn is f's day of the month in year y.
func (f found) dayIn(y int) int {
	if f.monthEnd {
		return time.Date(y, f.month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	}
	return f.day
}

// resolve turns the dates found in value into the calendar days that can be
// its end.
//
// A year outside plausibleYear is not a year: a month-name date keeps its
// month and day and is read as yearless, and an ISO or numeric date is not a
// date at all.
//
// A date written without a year takes the year of the next date in the same
// value that has one ("April 6 - June 7, 2026"), stepping back a year when it
// falls after that date ("December 17 - January 5, 2027"). With no year
// anywhere in the value ("Deadline: June 30"), it is the first occurrence of
// that day no more than yearlessGraceDays before the article was written.
// Offers are written about while they run, so a yearless deadline is almost
// always still ahead ("through December 31" written in May is this December,
// not the one gone); the grace covers a story written just after an offer
// closed.
//
// Dates that open a window, that belong to an earlier run of the offer, or
// that only say when the text was written are skipped: they are never the
// end. A superseded date ("was September 30") is skipped when the value names
// another end, and kept alone only when the value says the offer is over:
// "Was September 19 (offer has ended)" says when it ended, while "Extended
// indefinitely (was June 30)" says there is no end date now.
//
// Last, a date before the article was written gives way to one on or after
// it. The article describes an offer that is running, so in "July 31, 2026
// (launched June 1)" written in July the earlier date is history.
func resolve(dates []found, value string, anchor time.Time) []time.Time {
	for i := range dates {
		if dates[i].year != 0 && !plausibleYear(dates[i].year, anchor) {
			dates[i].year = 0
			if dates[i].numeric {
				// Not a date: take it out of the running.
				dates[i].start = true
			}
		}
	}
	anyCandidate := false
	for _, f := range dates {
		if f.candidate() {
			anyCandidate = true
			break
		}
	}
	keepSuperseded := !anyCandidate && endedWordRE.MatchString(value)

	written := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	for i, f := range dates {
		if f.start || f.incidental || f.history || (f.superseded && !keepSuperseded) {
			continue
		}
		year := f.year
		if year == 0 {
			for _, later := range dates[i+1:] {
				if later.year == 0 {
					continue
				}
				year = later.year
				if f.month > later.month || (f.month == later.month && f.dayIn(year) > later.dayIn(year)) {
					year--
				}
				break
			}
		}
		var t time.Time
		if year != 0 {
			if !valid(year, f.month, f.dayIn(year)) {
				continue
			}
			t = time.Date(year, f.month, f.dayIn(year), 0, 0, 0, 0, time.UTC)
		} else {
			ok := false
			floor := written.AddDate(0, 0, -yearlessGraceDays)
			for _, y := range []int{anchor.Year() - 1, anchor.Year(), anchor.Year() + 1} {
				if !valid(y, f.month, f.dayIn(y)) {
					continue
				}
				if c := time.Date(y, f.month, f.dayIn(y), 0, 0, 0, 0, time.UTC); !c.Before(floor) {
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

	current := false
	for _, t := range out {
		if !t.Before(written) {
			current = true
			break
		}
	}
	if current {
		kept := out[:0]
		for _, t := range out {
			if !t.Before(written) {
				kept = append(kept, t)
			}
		}
		out = kept
	}
	return out
}

// EndDate returns the last day of the offer described by keyDetailsJSON, as
// "2006-01-02", or "" when the details state no end date that can be read.
//
// anchor is when the details were written (the article's created_at). It
// places a date given without a year, bounds which years are plausible, and
// tells an offer's history from its current end.
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
		kind := classify(normalize(d.Label))
		if kind == notAnEnd {
			continue
		}
		value := normalize(d.Value)
		dates := resolve(scanDates(value), value, anchor)
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

// normalize turns the typographic spaces and dashes the generator writes into
// ASCII, so the patterns above -- whose \s matches only ASCII whitespace --
// read "July 29, 2026" and "June 15‑July 15" exactly like
// "July 29, 2026" and "June 15-July 15". Every Unicode space becomes " " and
// every dash (hyphens, en and em dashes, the minus sign) becomes "-".
func normalize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r < 0x80:
			return r
		case unicode.IsSpace(r):
			return ' '
		case unicode.Is(unicode.Pd, r) || r == '−':
			return '-'
		}
		return r
	}, s)
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
