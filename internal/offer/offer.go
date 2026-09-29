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
var benefitDeadlineRE = regexp.MustCompile(`\b(?:use|using|usage|credits?|discounts?|redeem|redeeming|redemptions?|spend|spending|statement|certificates?|vouchers?)\b`)

// heldBenefitRE is the part of benefitDeadlineRE that names a benefit
// already in hand -- a credit, a certificate, a voucher, a redemption, a
// spend. Booking with it or entering its code is using it: "Hotel Credit
// Booking Deadline" and "Deadline to Redeem Code" are about the benefit.
var heldBenefitRE = regexp.MustCompile(`\b(?:credits?|redeem|redeeming|redemptions?|spend|spending|statement|certificates?|vouchers?)\b`)

// takeDeadlineRE marks a deadline for taking the offer even when a benefit
// word is in the label too: "Credit Card Application Deadline", "Statement
// Credit Offer Deadline". "Offer" counts only right before "deadline": an
// "Offer Spend Deadline" is still a spend.
var takeDeadlineRE = regexp.MustCompile(`\b(?:appl(?:y|ication)|enroll(?:ing|ment)?|regist(?:er|ering|ration)|sign[\s-]?ups?|offer\s+deadline)\b`)

// bookDeadlineRE marks a deadline for booking or entering a code, which
// takes the offer only when the benefit word beside it is a use or a
// discount: "Promo Code Use Deadline", "Discount Code Deadline".
var bookDeadlineRE = regexp.MustCompile(`\b(?:book(?:ing)?|codes?)\b`)

// benefitDeadline reports whether l, a lower-case label with "deadline" in
// it, is a deadline for using or earning a benefit rather than for taking
// the offer.
func benefitDeadline(l string) bool {
	l = creditCardRE.ReplaceAllString(l, "")
	if !benefitDeadlineRE.MatchString(l) || takeDeadlineRE.MatchString(l) {
		return false
	}
	return heldBenefitRE.MatchString(l) || !bookDeadlineRE.MatchString(l)
}

// creditCardRE is the product, not a statement credit: "Credit Card
// Application Deadline" is not about a credit.
var creditCardRE = regexp.MustCompile(`\bcredit\s+cards?\b`)

// replacementLabelRE names a deadline that replaces the others in the same
// article: "Extended Deadline", "New End Date", "Extended To".
var replacementLabelRE = regexp.MustCompile(`^(?:extended|new|revised|updated|current)\s+(?:deadline|end\s+date|end|expiration(?:\s+date)?|to|through|thru|until|till)$`)

// classify says how a label relates to the offer's end, and whether it
// names a replacement for the other end dates in the article.
func classify(label string) (labelKind, bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	l = strings.TrimRight(l, ":")
	l = spaceRE.ReplaceAllString(l, " ")
	if replacementLabelRE.MatchString(l) {
		return deadlineLabel, true
	}
	if staleLabelRE.MatchString(l) {
		return notAnEnd, false
	}
	switch {
	case strings.Contains(l, "deadline"):
		if benefitDeadline(l) {
			return notAnEnd, false
		}
		return deadlineLabel, false
	case endLabels[l]:
		return deadlineLabel, false
	case periodLabels[l]:
		return periodLabel, false
	}
	return notAnEnd, false
}

// monthNames matches a month name or abbreviation. Its first three letters
// are the key into months.
const monthNames = `(jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:t(?:ember)?)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)`

// monthDateRE matches "July 29", "Jul. 29th", "Sept.30", "Sep 30 2026" and
// "August 22, 2026". A month and year with no day ("October 2026")
// deliberately does not match: its last day would be a guess.
var monthDateRE = regexp.MustCompile(`(?i)\b` + monthNames + `(?:\.\s*|\s+)(\d{1,2})(?:st|nd|rd|th)?\b(?:(?:,\s*|\s+)(\d{4})\b)?`)

// timeAfterDayRE follows a number that is an hour, not a day: the "11" of
// "30 June 11:59pm", which monthDateRE would otherwise read as June 11.
var timeAfterDayRE = regexp.MustCompile(`(?i)^(?:st|nd|rd|th)?(?::\d|\.\d\d\s*[ap]|\s*(?:[ap]\.?\s?m\b|[ap]\.m\.))`)

// dayRangeRE continues a match of monthDateRE when a range shares its month:
// the "-10" in "July 7-10, 2026". Values are normalized first, so every dash
// is "-" by now.
var dayRangeRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(\d{1,2})(?:st|nd|rd|th)?\b`)

// notADayRE follows a number that is a time, an amount or part of a numeric
// date rather than a day: the "11" of "June 30 - 11:59 PM", the "5" of "June
// 30 - 5 PM ET", the "10" of "June 13 - 10 ET", the "75" of "June 30 -
// 75,000 points", the "07" of "March 1 - 07/01/2026".
var notADayRE = regexp.MustCompile(`(?i)^\s*(?::|/|\.\d|,\d{3}\b|%|x\b|k\b|[ap]\.?\s?m\b|[ap]\.m\.|noon\b|midnight\b|(?:e|c|m|p|h|ak)[sd]?t\b|utc\b|gmt\b|bst\b|(?:eastern|central|mountain|pacific)\b|(?:points?|pts|miles?|nights?|avios)\b)`)

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

// numericDateRE matches a numeric date with a four-digit year, "6/30/2026".
// Only the year makes it a date: "5/24" is a card rule, not May 24.
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

// startAfterRE catches a date the text after it calls a start: "June 1
// (start date; no end announced)", "June 1 start, no end date".
var startAfterRE = regexp.MustCompile(`(?i)^\s*(?:\(\s*)?(?:start\s+date\b|start(?:s|ed)?\s*(?:$|[,;)]))`)

// supersededRE catches a date something else replaced or pushed past: "was
// September 30", "instead of June 30", "old deadline June 30", "the original
// June 30 deadline", "was scheduled to end June 30", "extended past June
// 30", "no longer June 30". pastEndRE catches one already over ("expired May
// 31", "ended early: June 5"). Such a date gives way to any other date in the
// value; alone, it stands only when the value says the offer is over.
var (
	supersededRE = regexp.MustCompile(`(?i)(?:` +
		`\b(?:was|were|previously|originally|formerly|initially|instead\s+of|rather\s+than|replac(?:es|ed|ing)|not|no\s+longer)` +
		`|\b(?:past|beyond)(?:\s+the)?` +
		`|\b(?:old|original|previous|prior|earlier|former|initial|usual|planned)` +
		`|\b(?:old|original|previous|prior|earlier|former|initial)\s+(?:deadline|end\s+date|end|date|expiration(?:\s+date)?)(?:\s+(?:was|of|is))?\s*:?` +
		`|\b(?:was|were|had\s+been)\s+(?:originally\s+|initially\s+|first\s+)?(?:scheduled|set|due|supposed|slated|planned|meant|expected)(?:\s+to\s+(?:end|expire|close|finish|run))?(?:\s+(?:on|through|thru|until|till|for|by))?` +
		`|\b(?:earlier|previous|prior|initial|original|old|first)\s+(?:reports?|listings?|terms|announcements?|coverage)\s+(?:said|stated|listed|showed|had|gave)` +
		`|\b(?:days?|weeks?|months?)\s+later\s+than(?:\s+the)?` +
		`)\s+(?:on\s+)?$`)
	pastEndRE = regexp.MustCompile(`(?i)\b(?:ended|expired|closed|lapsed)(?:\s+early)?\s*:?\s+(?:on\s+)?$`)
)

// extendedAfterRE and beenExtendedRE catch a date the text after it says
// was extended: "June 30, 2026 (extended indefinitely)", "June 30 has been
// extended". "June 30 was extended from May 31" is left alone: there June 30
// is the new date.
var (
	extendedAfterRE = regexp.MustCompile(`(?i)^\s*[,;:(-]?\s*(?:(?:has|have|had)\s+been\s+|(?:was|is)\s+(?:now\s+)?|now\s+)?extended\s+(?:indefinitely|until\s+further\s+notice|with\s+no\s+(?:new\s+)?end)\b`)
	beenExtendedRE  = regexp.MustCompile(`(?i)^\s*[,;:(-]?\s*(?:(?:it\s+)?(?:has|have|had)\s+been|was)\s+extended\b(\s+from\b)?`)
)

// replacementRE catches a date that replaces the dates it pushes back: the
// "July 31" of "June 30, now extended through July 31", "June 30 (replaced
// by July 31)", "Chase has extended it to July 31" and "New deadline July
// 31". A "now" has to open its clause: in "book now through June 30" it
// means from today. resolve decides which dates it replaces.
var replacementRE = regexp.MustCompile(`(?i)(?:\b(?:extended|extends|extension|pushed(?:\s+back)?|moved|postponed|delayed|changed|(?:replaced|superseded)\s+by|new\s+(?:deadline|end\s+date|end|date))(?:\s+(?:it|this|again|once\s+more|the\s+(?:offer|deadline|promo(?:tion)?|sale|bonus|end(?:\s+date)?)))?|(?:^|[,;(!.:-])\s*now)\s*:?\s*(?:(?:to|through|thru|until|till|into|out\s+to)\s+)?(?:on\s+)?$`)

// endedWordRE finds a value that says the offer is over ("Was September 19,
// 2026 (offer has ended)"), which keeps a superseded date that stands alone.
var endedWordRE = regexp.MustCompile(`(?i)\b(?:ended|expired|is\s+over|now\s+over|no\s+longer\s+(?:available|offered|live|running)|discontinued)\b`)

// stillOpenRE finds a value that says the offer has no end yet, or denies it
// ended ("Ongoing (the 60K offer ended June 1)", "hasn't ended yet"). The
// "ended" in it is then about something else, and keeps no date.
var stillOpenRE = regexp.MustCompile(`(?i)(?:\b(?:ongoing|unknown|unannounced|indefinite(?:ly)?|open[\s-]ended|tb[adc]|until\s+further\s+notice|to\s+be\s+(?:announced|determined|confirmed)|not\s+(?:yet\s+)?(?:announced|set|known|specified|disclosed)|no\s+(?:set\s+|fixed\s+|firm\s+|announced\s+|known\s+|official\s+)?(?:end(?:\s+date)?|expiration(?:\s+date)?|deadline)|still\s+(?:live|available|open|running|active)|never\s+expires?)\b|(?:n't|\bnot|\bnever)\s+(?:yet\s+)?(?:been\s+)?(?:ended|expired|over)\b)`)

// historyRE finds a clause about an earlier run of the offer ("last year's
// version ended June 30, 2025", "prior offer expired May 31", "a similar
// promo expired March 31"). Its dates are never this offer's end.
var historyRE = regexp.MustCompile(`(?i)\b(?:last\s+(?:\w+\s+)?(?:year|time|round|version|promo(?:tion)?|offer|sale|run|deal)|(?:prior|previous|earlier|past|similar|older)\s+(?:\w+\s+)?(?:years?|offers?|promos?|promotions?|versions?|rounds?|sales?|bonus(?:es)?|runs?|deals?))\b`)

// historyYearRE finds a clause about a run of the offer in a given year
// ("the 2025 version ran June 1 - July 15"). It is history when the year is
// before the one the value was written in.
var historyYearRE = regexp.MustCompile(`(?i)\b((?:19|20)\d\d)(?:'s)?\s+(?:version|offer|promo(?:tion)?|edition|run|sale|round|bonus)s?\b`)

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
// onward", "June 1 - (no end date announced)". Only these explicit words make
// the date a start in every label. A dash is also how the generator sets off
// a remark ("September 30, 2026 - act fast"), and "through" can name a
// channel ("June 30 through the Chase portal"), so in a deadline either
// followed by anything else leaves the date as the end.
var openEndRE = regexp.MustCompile(`(?i)^\s*,?\s*(?:(?:-|to|through|thru|until|till)\s*\(?\s*(?:tbd|tba|tbc|to\s+be\s+(?:announced|determined|confirmed)|ongoing|present|open[\s-]?ended|indefinite(?:ly)?|further\s+notice|(?:while|as\s+long\s+as)\s+supplies|supplies\s+last|(?:the\s+)?end\s+of\s+(?:the\s+)?(?:year|month|quarter|season|summer|winter|spring|fall|autumn)|year[\s-]end|no\s+(?:set\s+|fixed\s+|announced\s+)?end(?:\s+date)?|not\s+(?:yet\s+)?(?:announced|set|known)|unknown|unannounced|end\s+date\s+(?:unknown|tbd|not\s+(?:yet\s+)?announced|unannounced))|onwards?|(?:and|or)\s+(?:later|beyond|onwards?|ongoing))\b`)

// danglingJoinRE follows a date that opens a range whose end is missing:
// "Promotion Period: June 1 -", "June 1 - ?".
var danglingJoinRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(?:$|[);.?])`)

// periodJoinRE follows a date in a period that a joiner leaves open: in
// "Promotion Period: June 1 - Labor Day" or "June 1 until Chase pulls it"
// no end can be read, and the one date is the start. endPrefixRE comes
// before a date that already closes a window ("through June 9", "April 6 -
// June 7"), which stays the end whatever follows it.
var (
	periodJoinRE = regexp.MustCompile(`(?i)^\s*,?\s*(?:-|(?:to|through|thru|until|till)\b)`)
	endPrefixRE  = regexp.MustCompile(`(?i)(?:-|\b(?:to|through|thru|until|till|by|ends?|ending|expires?|closes?))\s*(?:on\s+)?$`)
)

// monthOnlyEndRE is a range that ends on a month with no day: "June 1
// through July", "June 1 - mid-July" and "June 1 - end of July" run to July
// 31. The month must end the clause, so "June 30 - may end early" is not
// read as running to May.
var monthOnlyEndRE = regexp.MustCompile(`(?i)^\s*(?:-|to|through|thru|until|till)\s*(?:(?:mid|early|late)[\s-]*|(?:the\s+)?end\s+of\s+)?\b` + monthNames + `\b\.?(?:(?:,\s*|\s+)(\d{4})\b)?\s*(?:$|[;,.()])`)

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
	swappable  bool // 1/7/2026: either number can be the month
	dayBefore  bool // "before July 29": the last day is the day before
	start      bool // "starting May 1": opens a window, so it is not an end
	rangeEnd   bool // closes a range: "April 6 - June 7"
	superseded bool // "was September 30": replaced by another date, if any
	pastEnd    bool // "ended June 5": superseded, but says when the offer ended
	history    bool // "last year's version ended June 30": an earlier run
	incidental bool // "updated May 1": not about the offer's end at all
	replaces   bool // "now extended to July 31": the dates it pushes back are superseded
	dropped    bool // not a usable date after all
}

// candidate reports whether f can be the end in its own right.
func (f found) candidate() bool {
	return !f.start && !f.superseded && !f.history && !f.incidental && !f.dropped
}

// readPrefix sets what the words just before a date in s say about it. The
// value was written at anchor, which tells a year's earlier run from this one.
func (f *found) readPrefix(s string, anchor time.Time) {
	prefix := s[:f.pos]
	if beforeRE.MatchString(prefix) && !orBeforeRE.MatchString(prefix) {
		f.dayBefore = true
	}
	if startRE.MatchString(prefix) {
		f.start = true
	}
	if supersededRE.MatchString(prefix) {
		f.superseded = true
	}
	if pastEndRE.MatchString(prefix) {
		f.superseded = true
		f.pastEnd = true
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
	for _, h := range historyYearRE.FindAllStringSubmatchIndex(clause, -1) {
		if y, _ := strconv.Atoi(clause[h[2]:h[3]]); y < anchor.Year() && !backToNowRE.MatchString(clause[h[1]:]) {
			f.history = true
		}
	}
}

// readSuffix sets what the words just after a date in s say about it.
func (f *found) readSuffix(s string) {
	rest := s[f.end:]
	if extendedAfterRE.MatchString(rest) {
		f.superseded = true
	}
	if m := beenExtendedRE.FindStringSubmatchIndex(rest); m != nil && m[2] < 0 {
		f.superseded = true
	}
	if startAfterRE.MatchString(rest) {
		f.start = true
	}
}

// sameMonthEnd reads the end of a range sharing its start's month, the "10,
// 2026" of "July 7-10, 2026", from the text after the start f. A number that
// is a time ("June 30 - 11:59 PM") or an amount ("June 30 - 75,000
// points"), that is no later than the start day, or that is not a day of the
// month is not a range end.
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

// monthOnlyEnd reads "through July" after the range start f, whose text runs
// to s[f.end:next], as the last day of July.
func monthOnlyEnd(s string, f found, next int) (found, bool) {
	gap := s[f.end:next]
	m := monthOnlyEndRE.FindStringSubmatchIndex(gap)
	// A capital letter: "may" in "- may end early" is not the month.
	if m == nil || !unicode.IsUpper(rune(gap[m[2]])) {
		return found{}, false
	}
	e := found{pos: f.end + m[2], end: f.end + m[3], month: months[strings.ToLower(gap[m[2]:m[2]+3])], monthEnd: true, rangeEnd: true}
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

// scanDates returns the dates written in s, a value of a label of kind
// written at anchor, in the order they appear, with what the text around
// each says about it.
func scanDates(s string, kind labelKind, anchor time.Time) []found {
	var out []found
	for _, m := range monthDateRE.FindAllStringSubmatchIndex(s, -1) {
		if timeAfterDayRE.MatchString(s[m[5]:]) {
			// "30 June 11:59pm": the number is an hour.
			continue
		}
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
				e.rangeEnd = true
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
		a, _ := strconv.Atoi(s[m[2]:m[3]])
		b, _ := strconv.Atoi(s[m[4]:m[5]])
		year, _ := strconv.Atoi(s[m[6]:m[7]])
		f := found{pos: m[0], end: m[1], year: year, month: time.Month(a), day: b, numeric: true}
		switch {
		case a > 12 && b >= 1 && b <= 12:
			// "30/6/2026" can only be day first.
			f.month, f.day = time.Month(b), a
		case a >= 1 && a <= 12 && b >= 1 && b <= 12 && a != b:
			// "1/7/2026": US order unless that falls long before the
			// article; resolve decides.
			f.swappable = true
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })

	for i := range out {
		out[i].readPrefix(s, anchor)
		out[i].readSuffix(s)
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
		joined := false
		switch {
		// "April 6 - June 7, 2026": the first date of a range opens it.
		case i+1 < len(out) && rangeJoinRE.MatchString(gap):
			f.start, joined = true, true
			out[i+1].rangeEnd = true
		case i+1 < len(out) && andJoinRE.MatchString(gap) && betweenRE.MatchString(s[:f.pos]):
			f.start, joined = true, true
			out[i+1].rangeEnd = true
		// "June 1 - TBD", "June 1 -": a window with no end yet.
		case openEndRE.MatchString(s[f.end:]) || danglingJoinRE.MatchString(s[f.end:]):
			f.start = true
		default:
			// "June 1 through July": the window ends with July.
			if e, ok := monthOnlyEnd(s, *f, next); ok {
				f.start = true
				e.superseded = f.superseded
				monthEnds = append(monthEnds, e)
			} else if kind == periodLabel && !f.rangeEnd && !endPrefixRE.MatchString(s[:f.pos]) && periodJoinRE.MatchString(gap) {
				// "Promotion Period: June 1 - Labor Day": no end to read.
				f.start = true
			}
		}
		// "was June 1 - June 30": the whole range was replaced.
		if joined && f.superseded {
			out[i+1].superseded = true
		}
	}
	if len(monthEnds) > 0 {
		for i := range monthEnds {
			monthEnds[i].readPrefix(s, anchor)
		}
		out = append(out, monthEnds...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	}
	return out
}

// yearlessGraceDays is how far before the article's writing a date given
// without a year may fall and still be read as the one just gone rather than
// next year's.
const yearlessGraceDays = 14

// staleEndDays is how long before the article a lone past-tense date ("was
// June 30", "expired June 30") may fall and still be read as when this
// offer ended. Older than that, it is about some earlier offer.
const staleEndDays = 30

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

// dayIn is f's day of the month in year y.
func (f found) dayIn(y int) int {
	if f.monthEnd {
		return time.Date(y, f.month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	}
	return f.day
}

// in is f's date in year y, if that is a real day.
func (f found) in(y int) (time.Time, bool) {
	if !valid(y, f.month, f.dayIn(y)) {
		return time.Time{}, false
	}
	return time.Date(y, f.month, f.dayIn(y), 0, 0, 0, 0, time.UTC), true
}

// nextOccurrence is the first f on or after floor, trying the years around
// anchor's.
func (f found) nextOccurrence(floor, anchor time.Time) (time.Time, bool) {
	for _, y := range []int{anchor.Year() - 1, anchor.Year(), anchor.Year() + 1} {
		if t, ok := f.in(y); ok && !t.Before(floor) {
			return t, true
		}
	}
	return time.Time{}, false
}

// nearest is the occurrence of f closest to written, the later one on a tie.
func (f found) nearest(written, anchor time.Time) time.Time {
	var best time.Time
	var bestGap time.Duration
	for _, y := range []int{anchor.Year() - 1, anchor.Year(), anchor.Year() + 1} {
		t, ok := f.in(y)
		if !ok {
			continue
		}
		gap := t.Sub(written)
		if gap < 0 {
			gap = -gap
		}
		if best.IsZero() || gap <= bestGap {
			best, bestGap = t, gap
		}
	}
	return best
}

// onOrAfter is the first occurrence of f on or after from.
func (f found) onOrAfter(from time.Time) (time.Time, bool) {
	if from.IsZero() {
		return time.Time{}, false
	}
	for y := from.Year(); y <= from.Year()+4; y++ {
		if t, ok := f.in(y); ok && !t.Before(from) {
			return t, true
		}
	}
	return time.Time{}, false
}

// saysEnded reports whether value says the offer is over, and does not also
// say it runs on ("Ongoing (the 60K offer ended June 1)") or deny it ended
// ("but the offer hasn't ended yet").
func saysEnded(value string) bool {
	return endedWordRE.MatchString(value) && !stillOpenRE.MatchString(value)
}

// resolve turns the dates found in value, written at anchor, into the
// calendar days that can be its end. Wherever a date could be read two
// ways, it takes the later reading.
//
// Years. A year more than yearsAhead after the one written in is no date at
// all ("December 31, 2099" is not an end to count down to). One more than
// yearsBack before it is not a year: a month-name date keeps its month and
// day and is read as yearless, and an ISO or numeric date is no date.
//
// A numeric date that reads either way ("1/7/2026") is month first, unless
// that falls more than yearlessGraceDays before the article; then it takes
// the later of the two readings.
//
// A date written without a year is the first occurrence of that day no more
// than yearlessGraceDays before the article was written. Offers are written
// about while they run, so a yearless deadline is almost always still ahead
// ("through December 31" written in May is this December, not the one
// gone); the grace covers a story written just after an offer closed. When a
// later date in the value has a year ("April 6 - June 7, 2026", "December 17
// - January 5, 2027"), the date may take that year instead, stepping back one
// when it falls after that date -- but only when that reads later. The later
// date may be a start or an earlier run's, so its year is no better a guess:
// "June 30 (launched May 1, 2026)" is not June 30, 2025. A range's end
// without a year falls after its start, taken as the occurrence nearest the
// writing: written in June, "September 30 - mid-July" runs to next July.
//
// Dates that open a window, that belong to an earlier run of the offer, or
// that only say when the text was written are skipped: they are never the
// end. When nothing else in the value can be, an earlier run's date with a
// year on or after the day the value was written is this run's after all --
// an earlier run lies in the past ("Same as last year: September 30, 2026").
//
// An extension supersedes every date in the value that it pushes back: in
// "June 30, now extended to July 31" and "Extended again: July 31 (after
// being extended once to June 30)" June 30 is gone. An extension to a date
// earlier than one written before it still pushes back the dates at or
// before it: in "Book by June 15 for travel through December 31 (extended to
// July 15)" the booking deadline is July 15. When it pushes back no end, it
// is an older one: in "Extended to July 31 (after it was first extended to
// June 30)" June 30 is gone, and in "June 1 - August 31; now through July 15
// for double points" July 15 does not cut the period short.
//
// A superseded date ("was September 30") is skipped when the value names
// another end, and kept alone only when the value says the offer is over and
// the date is no more than staleEndDays before the article: "Was September
// 19 (offer has ended)" says when it ended, while "Extended indefinitely (was
// June 30)" says there is no end date now. Among kept dates, one the value
// says the offer ended on wins: in "Ended June 5 (was scheduled through June
// 30)" the offer ended on June 5.
//
// Last, a date before the article was written gives way to one on or after
// it. The article describes an offer that is running, so in "July 31, 2026
// (launched June 1)" written in July the earlier date is history.
func resolve(dates []found, value string, anchor time.Time) []time.Time {
	written := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	floor := written.AddDate(0, 0, -yearlessGraceDays)

	for i := range dates {
		f := &dates[i]
		switch {
		case f.year == 0:
		case f.year > anchor.Year()+yearsAhead:
			f.dropped = true
		case f.year < anchor.Year()-yearsBack:
			if f.numeric {
				f.dropped = true
			} else {
				f.year = 0
			}
		}
	}

	days := make([]time.Time, len(dates))
	// Dates with a year first: their days lend years to the rest.
	for i := range dates {
		f := &dates[i]
		if f.dropped || f.year == 0 {
			continue
		}
		t, ok := f.in(f.year)
		if f.swappable && (!ok || t.Before(floor)) {
			swapped := *f
			swapped.month, swapped.day = time.Month(f.day), int(f.month)
			if u, uok := swapped.in(f.year); uok && (!ok || u.After(t)) {
				t, ok = u, true
			}
		}
		if !ok {
			f.dropped = true
			continue
		}
		days[i] = t
	}
	for i := range dates {
		f := &dates[i]
		if f.dropped || f.year != 0 {
			continue
		}
		t, ok := f.nextOccurrence(floor, anchor)
		for _, later := range dates[i+1:] {
			if later.year == 0 || later.dropped {
				continue
			}
			y := later.year
			if f.month > later.month || (f.month == later.month && f.dayIn(y) > later.dayIn(y)) {
				y--
			}
			if b, bok := f.in(y); bok && (!ok || b.After(t)) {
				t, ok = b, true
			}
			break
		}
		if !ok {
			f.dropped = true
			continue
		}
		// A range ends after it starts: written in June, "September 30 -
		// mid-July" runs to next July. The start comes just before its end,
		// and without a year it is the occurrence nearest the writing.
		if f.rangeEnd && i > 0 && dates[i-1].start && !dates[i-1].dropped {
			from := days[i-1]
			if s := dates[i-1]; s.year == 0 {
				from = s.nearest(written, anchor)
			}
			if u, uok := f.onOrAfter(from); uok && u.After(t) {
				t = u
			}
		}
		days[i] = t
	}
	for i := range dates {
		if dates[i].dayBefore && !dates[i].dropped {
			days[i] = days[i].AddDate(0, 0, -1)
		}
	}

	// Extensions, in the order written.
	for i := range dates {
		r := &dates[i]
		if !r.replaces || r.dropped || r.superseded || r.history || r.incidental {
			continue
		}
		older := false
		for j := range dates[:i] {
			if dates[j].candidate() && days[j].After(days[i]) {
				older = true
				break
			}
		}
		pushes := false
		for j := range dates {
			if j != i && dates[j].candidate() && !days[j].After(days[i]) {
				pushes = true
				break
			}
		}
		// After a later date written before it, and pushing back no end, it
		// is an older extension, or one for some readers only.
		if older && !pushes {
			r.superseded = true
			continue
		}
		for j := range dates {
			if j != i && !dates[j].dropped && !days[j].After(days[i]) {
				dates[j].superseded = true
			}
		}
	}

	var ends []int
	for i, f := range dates {
		if f.candidate() {
			ends = append(ends, i)
		}
	}
	if len(ends) == 0 {
		// An earlier run lies in the past: "Same as last year: September
		// 30, 2026", written before that day, is this run's end.
		for i, f := range dates {
			if f.history && f.year != 0 && !f.start && !f.superseded && !f.incidental && !f.dropped && !days[i].Before(written) {
				ends = append(ends, i)
			}
		}
	}
	if len(ends) == 0 && saysEnded(value) {
		stale := written.AddDate(0, 0, -staleEndDays)
		var past, replaced []int
		for i, f := range dates {
			if !f.superseded || f.dropped || f.start || f.history || f.incidental || days[i].Before(stale) {
				continue
			}
			if f.pastEnd {
				past = append(past, i)
			} else {
				replaced = append(replaced, i)
			}
		}
		ends = replaced
		if len(past) > 0 {
			ends = past
		}
	}

	out := make([]time.Time, 0, len(ends))
	current := false
	for _, i := range ends {
		out = append(out, days[i])
		if !days[i].Before(written) {
			current = true
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
// the offer. A detail labelled as a replacement ("Extended Deadline", "New
// End Date") sets aside the others: the deadline it extends is over.
func EndDate(keyDetailsJSON string, anchor time.Time) string {
	s := strings.TrimSpace(keyDetailsJSON)
	if s == "" || s == "[]" || s == "null" {
		return ""
	}
	var details []keyDetail
	if err := json.Unmarshal([]byte(s), &details); err != nil {
		return ""
	}
	var end, replacement time.Time
	for _, d := range details {
		kind, replaces := classify(normalize(d.Label))
		if kind == notAnEnd {
			continue
		}
		value := normalize(d.Value)
		dates := resolve(scanDates(value, kind, anchor), value, anchor)
		if len(dates) == 0 {
			continue
		}
		pick := dates[0]
		for _, t := range dates[1:] {
			if kind == deadlineLabel && t.Before(pick) || kind == periodLabel && t.After(pick) {
				pick = t
			}
		}
		if replaces {
			if replacement.IsZero() || pick.Before(replacement) {
				replacement = pick
			}
			continue
		}
		if end.IsZero() || pick.Before(end) {
			end = pick
		}
	}
	if !replacement.IsZero() {
		end = replacement
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
