package offer

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// details builds a key_details JSON array the way the article manager stores
// it (json.Marshal of Go structs, so "Label"/"Value").
func details(t *testing.T, pairs ...string) string {
	t.Helper()
	type kd struct{ Label, Value string }
	var out []kd
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, kd{pairs[i], pairs[i+1]})
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func day(s string) time.Time {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

// endDateCase is one Key Details entry and the end date it should give.
type endDateCase struct {
	name, label, value, anchor, want string
}

func runEndDateCases(t *testing.T, cases []endDateCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EndDate(details(t, c.label, c.value), day(c.anchor)); got != c.want {
				t.Errorf("EndDate(%s: %q, written %s) = %q, want %q", c.label, c.value, c.anchor, got, c.want)
			}
		})
	}
}

// Every value below is copied from a live article's Key Details, so the
// reader is tested against what the generator really writes.
func TestEndDate_FormatsSeenInRealData(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"full date", "Deadline", "July 29, 2026", "2026-07-22", "2026-07-29"},
		{"time and zone after the date", "Deadline", "August 22, 2026, 23:59 ET", "2026-07-22", "2026-08-22"},
		{"time before the date", "Deadline", "Midnight ET on March 26, 2026", "2026-03-27", "2026-03-26"},
		{"through", "Promotion Period", "Through June 9, 2026", "2026-04-04", "2026-06-09"},
		{"now through", "Promotion Period", "Now through January 19, 2027", "2026-09-25", "2027-01-19"},
		{"range across months", "Promotion Period", "April 6 - June 7, 2026", "2026-03-28", "2026-06-07"},
		{"range repeating the month", "Booking Period", "May 26 - May 29, 2026", "2026-05-27", "2026-05-29"},
		{"range within one month, en dash", "Booking Window", "July 7–10, 2026", "2026-07-08", "2026-07-10"},
		{"yearless through", "Sale Period", "Through October 5", "2026-09-24", "2026-10-05"},
		{"expiration date label", "Expiration date", "May 21, 2026", "2026-04-02", "2026-05-21"},
		{"registration deadline", "Registration Deadline", "April 12", "2026-04-11", "2026-04-12"},
		{"booking deadline", "Booking Deadline", "April 30", "2026-04-15", "2026-04-30"},
		{"abbreviated month, no comma", "Deadline", "Sep 30 2026", "2026-07-22", "2026-09-30"},
		{"ordinal day", "Deadline", "September 19th, 2026", "2026-09-10", "2026-09-19"},
		{"iso date", "Deadline", "2026-07-29", "2026-07-22", "2026-07-29"},
	})
}

// Only the listed labels name the offer's end, matched exactly but for case,
// spacing and a trailing colon.
func TestEndDate_OnlyListedLabelsCount(t *testing.T) {
	for _, label := range []string{
		"Deadline", "Offer Deadline", "Booking Deadline", "Registration Deadline",
		"Application Deadline", "Enrollment Deadline", "Sale Deadline", "Promo Deadline",
		"Offer Ends", "Offer End Date", "Ends", "End Date", "Expires", "Expiration Date",
		"Offer Expires", "Valid Through", "Valid Until", "Book By", "Apply By", "Register By",
		"Promotion Period", "Offer Period", "Sale Period", "Booking Period", "Booking Window",
		"Promotion End",
		"deadline", "DEADLINE:", "  Offer   ends: ", "Offer ends", "Promotion Period",
	} {
		if got := EndDate(details(t, label, "September 30, 2026"), day("2026-07-01")); got != "2026-09-30" {
			t.Errorf("EndDate(%q: September 30, 2026) = %q, want 2026-09-30", label, got)
		}
	}
	// Everything else is about something other than taking the offer, or
	// is not an exact match. None of these may set an end.
	for _, label := range []string{
		"Travel Deadline", "Travel Dates", "Travel Period", "Stay Dates", "Eligible Stay Dates",
		"Blackout Dates", "Updated Blackout Dates", "Blackout Periods",
		"Deadline to Use Credit", "Discount Deadline", "Statement Credit Deadline", "Spend Deadline",
		"Free Days Expiration", "Expiration", "Mileage Expiration", "Volare Program Expiration",
		"Lounge Access End Date", "Status Match Deadline", "Status Valid Through", "Effective Date",
		"Closure Date", "Launch Date", "Start Date",
		"Marriott Booking Deadline", "Hyatt Booking Deadline", "Southwest Booking Deadline",
		"Deadline for sharing ThankYou points", "Original Deadline", "Extended Deadline",
		"New Deadline", "Extended Stay Booking Deadline", "Deadlines", "Deadline (extended)",
	} {
		if got := EndDate(details(t, label, "July 5, 2026"), day("2026-07-01")); got != "" {
			t.Errorf("EndDate(%q: July 5, 2026) = %q, want no end date", label, got)
		}
	}
}

// The BofA Atmos Rewards Summit article: its only dated detail is when the
// first-year flight discount must be used. The offer may run on past it.
func TestEndDate_BenefitDeadlineAloneIsNoEnd(t *testing.T) {
	atmos := details(t,
		"Sign-up Bonus", "100,000 points",
		"Spend Requirement", "$6,500 in 90 days",
		"Discount Deadline", "September 30, 2026",
	)
	if got := EndDate(atmos, day("2026-04-02")); got != "" {
		t.Errorf("Atmos Summit end = %q, want none", got)
	}
}

// The offer ends on the latest date named by any end label, in one value or
// across several.
func TestEndDate_LatestDateWins(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		// The documented trade-off: registering closes on April 12, but the
		// value is read as ending on July 1. Too late is recoverable; too
		// early is not.
		{"register by, travel by", "Deadline", "Register by April 12; travel by July 1", "2026-04-01", "2026-07-01"},
		{"two deadlines in one value", "Deadline", "July 7, 2026 (stay completion) and July 6, 2026 (booking)", "2026-05-12", "2026-07-07"},
		{"new and old", "Deadline", "July 31, 2026 (originally June 30)", "2026-05-20", "2026-07-31"},
		{"old after new", "Deadline", "Previously September 30, now October 31, 2026", "2026-07-01", "2026-10-31"},
		{"extended to", "Deadline", "June 30, 2026 (extended to July 31, 2026)", "2026-05-20", "2026-07-31"},
		{"launch date beside it", "Deadline", "July 31, 2026 (launched June 1)", "2026-05-20", "2026-07-31"},
		{"last updated", "Deadline", "September 30, 2026 (last updated May 1, 2026)", "2026-07-01", "2026-09-30"},
		// Every date counts, so the history of an extension never ends the
		// offer on its old deadline.
		{"extended, then again", "Deadline", "December 31, 2026 (the June 30 deadline was extended to August 31, then again)", "2026-05-20", "2026-12-31"},
		{"between and", "Promotion Period", "Between June 1 and June 30, 2026", "2026-05-20", "2026-06-30"},
		{"before", "Deadline", "Enroll before June 30, 2026", "2026-06-01", "2026-06-30"},
		// The cost of taking the latest date: a yearless date already gone
		// when the article was written is read as next year's, so an old
		// deadline mentioned in passing can push the end a year out. Late,
		// never early.
		{"old yearless date", "Deadline", "July 31, 2026 (originally June 30)", "2026-07-20", "2027-06-30"},
	})

	kd := details(t,
		"Bonus", "Double points",
		"Registration Deadline", "April 12",
		"Promotion Period", "April 6 - June 7, 2026",
		"Travel Deadline", "December 31",
	)
	if got := EndDate(kd, day("2026-04-11")); got != "2026-06-07" {
		t.Errorf("across details: got %q, want the latest listed end 2026-06-07", got)
	}
}

// A detail that is not an end label is ignored even when it holds a date
// that would otherwise read early.
func TestEndDate_UnlistedDatesNeverEndEarly(t *testing.T) {
	kd := details(t,
		"Updated Blackout Dates", "July 3 - 6, 2026",
		"Deadline", "September 30, 2026",
	)
	if got := EndDate(kd, day("2026-06-20")); got != "2026-09-30" {
		t.Errorf("got %q, want 2026-09-30", got)
	}
	if got := EndDate(details(t, "Updated Blackout Dates", "July 3 - 6, 2026"), day("2026-06-20")); got != "" {
		t.Errorf("blackout dates alone: got %q, want no end", got)
	}
}

// A value that says the offer has no end yet leaves the article with none,
// whatever dates it or other details name.
func TestEndDate_OpenEndedWording(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"ongoing", "Deadline", "Ongoing", "2026-05-20", ""},
		{"ongoing with an old date", "Deadline", "Ongoing (the 60K offer ended June 1, 2026)", "2026-06-10", ""},
		{"until further notice", "Deadline", "Until further notice (originally June 30, 2026)", "2026-05-20", ""},
		{"no end date", "Promotion Period", "June 1, 2026 - no end date announced", "2026-05-20", ""},
		{"no set end", "Deadline", "No set end; expected to run through June 30, 2026", "2026-05-20", ""},
		{"tbd", "Promotion Period", "June 1 - TBD", "2026-05-20", ""},
		{"tba", "Deadline", "TBA (last year's ran to June 30)", "2026-05-20", ""},
		{"to be announced", "Deadline", "To be announced", "2026-05-20", ""},
		{"extended indefinitely", "Deadline", "June 30, 2026 (extended indefinitely)", "2026-05-20", ""},
		{"indefinitely, old date first", "Deadline", "Extended indefinitely (was June 30, 2026)", "2026-05-20", ""},
		{"onward", "Promotion Period", "June 1 onward", "2026-05-20", ""},
		{"no hard deadline", "Deadline", "Limited-time offer, no hard deadline specified", "2026-07-22", ""},
		{"no specific deadline", "Deadline", "No specific deadline, offers tend to disappear without warning", "2026-04-04", ""},
	})

	kd := details(t,
		"Registration Deadline", "June 15, 2026",
		"Promotion Period", "June 1, 2026 until further notice",
	)
	if got := EndDate(kd, day("2026-05-20")); got != "" {
		t.Errorf("open-ended period beside a deadline: got %q, want no end", got)
	}
}

// A value that says the offer was extended, with no date after the
// extension, does not say when it now ends: there is no end date. Read
// otherwise, "June 30, 2026 (extended by one month)" ended a month early.
func TestEndDate_ExtendedWithNoNewDate(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"extended by one month", "Deadline", "June 30, 2026 (extended by one month)", "2026-05-20", ""},
		{"has been extended", "Deadline", "June 30, 2026 has been extended", "2026-05-20", ""},
		{"original deadline has expired", "Deadline", "Extended (original June 30, 2026 deadline has expired)", "2026-07-05", ""},
		{"extended again", "Deadline", "Extended to July 31, 2026, then extended again", "2026-05-20", ""},
		// "From" names the old date, not the new one: no end, rather than
		// the old deadline.
		{"extended from, alone", "Deadline", "Extended from June 30, 2026", "2026-05-20", ""},
		{"extended from, beside the new date", "Deadline", "July 31, 2026 (extended from June 30)", "2026-05-20", ""},
		// "Extended" as in a longer stay is still read as an extension:
		// no end, the recoverable side.
		{"extended stays", "Booking Deadline", "June 30, 2026 for extended stays", "2026-05-20", ""},
		// A date after the extension says when.
		{"extended to a date", "Deadline", "Extended to July 31, 2026 (was June 30)", "2026-05-20", "2026-07-31"},
		{"extended by a month to a date", "Deadline", "June 30, 2026, extended by a month to July 31, 2026", "2026-05-20", "2026-07-31"},
		{"history of extensions", "Deadline", "December 31, 2026 (the June 30 deadline was extended to August 31, then again)", "2026-05-20", "2026-12-31"},
		{"extended through", "Promotion Period", "April 1 - June 30, 2026, extended through August 15, 2026", "2026-05-20", "2026-08-15"},
	})
}

// A date that only opens a window is not its end. When nothing closes the
// window, there is no end date.
func TestEndDate_StartsAreNotEnds(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"starting", "Promotion Period", "Starting May 1, 2026", "2026-04-01", ""},
		{"from", "Offer Period", "From June 1, 2026", "2026-05-20", ""},
		{"effective", "Deadline", "Effective June 1, 2026", "2026-05-20", ""},
		{"launched", "Promotion Period", "Launched June 1, 2026", "2026-05-20", ""},
		{"opens", "Booking Window", "Opens June 1, 2026", "2026-05-20", ""},
		{"dangling dash", "Promotion Period", "June 1 -", "2026-05-20", ""},
		{"present", "Offer Period", "June 1 - present", "2026-06-20", ""},
		{"a holiday", "Promotion Period", "June 1 - Labor Day", "2026-05-20", ""},
		{"a month with no day", "Promotion Period", "June 1 through July", "2026-05-20", ""},
		{"a month and year", "Promotion Period", "June 1 - July 2026", "2026-05-20", ""},
		{"while supplies last", "Deadline", "June 1 until supplies last", "2026-05-20", ""},
		// A range contributes its end.
		{"from to", "Promotion Period", "From April 6 to June 7, 2026", "2026-03-28", "2026-06-07"},
		{"numeric range", "Promotion Period", "6/1/2026 - 6/30/2026", "2026-05-20", "2026-06-30"},
		{"range to a numeric date", "Promotion Period", "March 1 - 07/01/2026", "2026-05-20", "2026-07-01"},
		{"over the new year", "Promotion Period", "December 17, 2026 - January 5, 2027", "2026-12-01", "2027-01-05"},
	})
}

// A time or an amount after a date is not a day, and not the end of a range
// within the month: "June 1 - 11:59 PM" is not June 1 to June 11.
func TestEndDate_TimeAfterDateIsNotADay(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"dash, time with minutes", "Deadline", "June 30 - 11:59 PM ET", "2026-06-01", "2026-06-30"},
		{"early day, dash, time", "Deadline", "June 1 - 11:59 PM ET", "2026-05-20", "2026-06-01"},
		{"en dash, lower-case pm", "Deadline", "June 30 – 11:59pm ET", "2026-06-01", "2026-06-30"},
		{"through a time", "Deadline", "June 30 through 11:59 PM", "2026-06-01", "2026-06-30"},
		{"dash, hour only", "Deadline", "June 3 - 5 PM ET", "2026-06-01", "2026-06-03"},
		{"dotted p.m.", "Deadline", "June 3 - 5 p.m. PT", "2026-06-01", "2026-06-03"},
		{"time zone", "Deadline", "June 3 – 10 ET", "2026-06-01", "2026-06-03"},
		{"noon", "Deadline", "June 3 - 12 noon ET", "2026-06-01", "2026-06-03"},
		{"dotted time", "Deadline", "June 3 - 11.59pm ET", "2026-06-01", "2026-06-03"},
		{"points", "Deadline", "June 3 - 10,000 points", "2026-06-01", "2026-06-03"},
		{"at a time", "Deadline", "June 30, 2026 at 11:59 PM PT", "2026-06-10", "2026-06-30"},
		{"month first, 24-hour dotted", "Deadline", "June 30, 2026 23.59 BST", "2026-06-10", "2026-06-30"},
		{"iso timestamp", "Deadline", "2026-09-30T23:59:00-04:00", "2026-07-01", "2026-09-30"},
		{"iso and a time", "Deadline", "2026-09-30 23:59 ET", "2026-07-01", "2026-09-30"},
		// Day first, the number after the month is the hour: no date is
		// read rather than a wrong one.
		{"day first, 12-hour", "Deadline", "30 June 11:59pm BST", "2026-06-10", ""},
		{"day first, 24-hour", "Deadline", "30 June 23:59", "2026-06-10", ""},
		{"day first, dotted", "Deadline", "30 June 23.59", "2026-06-10", ""},
		{"hour only", "Deadline", "June 11 PM", "2026-06-10", ""},
		// Real ranges still read.
		{"real range", "Booking Window", "July 7-10, 2026", "2026-07-01", "2026-07-10"},
		{"real range with to", "Promotion Period", "July 7 to 10", "2026-07-01", "2026-07-10"},
	})
}

// Live articles often drop the year ("Deadline: June 30"). Offers are written
// about while they run, so the date is its next occurrence after the article
// was written -- or one that passed shortly before, for a story written just
// after an offer closed.
func TestEndDate_YearlessDateTakesItsNextOccurrence(t *testing.T) {
	cases := []struct {
		value, anchor, want string
	}{
		{"June 30", "2026-05-08", "2026-06-30"},
		// Written two days after the deadline: last April, not next April.
		{"April 26", "2026-04-28", "2026-04-26"},
		// Written in November about a January deadline: next January.
		{"January 15", "2026-11-10", "2027-01-15"},
		// Written in early January about a December deadline: the one just gone.
		{"December 31", "2027-01-05", "2026-12-31"},
		{"December 31", "2026-05-08", "2026-12-31"},
		// A date that passed up to 14 days before the article is the one
		// just gone; an older one is read as next year's.
		{"March 1", "2026-03-15", "2026-03-01"},
		{"March 1", "2026-03-16", "2027-03-01"},
		{"November 30", "2027-01-20", "2027-11-30"},
		{"December 17 - January 5", "2026-12-01", "2027-01-05"},
		// February 29 in the coming leap year.
		{"February 29", "2027-12-01", "2028-02-29"},
		// No February 29 anywhere near: left empty rather than guessed.
		{"February 29", "2026-06-01", ""},
	}
	for _, c := range cases {
		got := EndDate(details(t, "Deadline", c.value), day(c.anchor))
		if got != c.want {
			t.Errorf("EndDate(Deadline: %q, written %s) = %q, want %q", c.value, c.anchor, got, c.want)
		}
	}
}

// A four-digit number after a date is only its year when it is a plausible
// one: "December 31, 1000 bonus points" is December 31, not the year 1000.
// A year too far ahead is no date at all.
func TestEndDate_ImplausibleYears(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"points after the date", "Deadline", "December 31, 1000 bonus points", "2026-05-08", "2026-12-31"},
		{"far-future number", "Deadline", "March 31, 2500 points", "2026-03-01", ""},
		{"two years back is a year", "Deadline", "September 19, 2024", "2026-03-01", "2024-09-19"},
		{"five years ahead is a year", "Deadline", "December 31, 2031", "2026-03-01", "2031-12-31"},
		{"six years ahead is no date", "Deadline", "December 31, 2032", "2026-03-01", ""},
		{"2099 expiration", "Expiration Date", "December 31, 2099", "2026-06-10", ""},
		{"2099 valid through", "Valid Through", "December 31, 2099", "2026-06-10", ""},
		{"far-future range end", "Promotion Period", "June 1 - December 31, 2099", "2026-05-20", ""},
		// A far-future date beside a real one does not hide it.
		{"beside a real date", "Deadline", "June 30, 2026 (card valid through December 31, 2099)", "2026-05-20", "2026-06-30"},
		// An ISO or numeric date with an implausible year is no date.
		{"iso", "Deadline", "1000-12-31", "2026-03-01", ""},
		{"numeric", "Deadline", "6/30/1999", "2026-03-01", ""},
		{"numeric far ahead", "Deadline", "6/30/2500", "2026-03-01", ""},
	})
}

// More ways the generator writes a date.
func TestEndDate_MoreDateFormats(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"abbreviation with no space", "Deadline", "Sept.30, 2026", "2026-07-01", "2026-09-30"},
		{"abbreviation with no space, no year", "Deadline", "Sept.30", "2026-07-01", "2026-09-30"},
		{"no space after the comma", "Deadline", "September 30,2026", "2026-07-01", "2026-09-30"},
		{"numeric", "Deadline", "6/30/2026", "2026-06-01", "2026-06-30"},
		{"numeric is month first", "Deadline", "1/5/2027", "2026-06-10", "2027-01-05"},
		{"numeric that can only be day first", "Deadline", "30/06/2026", "2026-06-10", "2026-06-30"},
		// Without a four-digit year a slash is not a date: "5/24" is Chase's
		// rule, and "4/15" is too short to trust.
		{"numeric without year", "Deadline", "Ends 4/15", "2026-04-01", ""},
		{"not a real day", "Deadline", "2/30/2026", "2026-01-01", ""},
		{"not a real month-name day", "Deadline", "February 30, 2026", "2026-01-01", ""},
		// Month and year only: the last day would be a guess.
		{"month and year", "Deadline", "October 2026", "2026-07-01", ""},
		{"no date", "Deadline", "24 hours", "2026-07-01", ""},
	})
}

// The generator writes typographic spaces and dashes inside dates. They must
// read exactly like their ASCII forms: reading only the first half of a
// range would end a live offer early.
func TestEndDate_UnicodeSpacesAndDashes(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"narrow no-break spaces", "Deadline", "July 29, 2026", "2026-07-22", "2026-07-29"},
		{"no-break space", "Deadline", "July 29, 2026", "2026-07-22", "2026-07-29"},
		{"thin and figure spaces", "Deadline", "Sep 30 2026", "2026-07-22", "2026-09-30"},
		{"non-breaking hyphen across months", "Deadline", "June 15‑July 15, 2026", "2026-06-10", "2026-07-15"},
		{"non-breaking hyphen within a month", "Promotion Period", "July 7‑10, 2026", "2026-07-01", "2026-07-10"},
		{"minus sign", "Booking Window", "July 7−10, 2026", "2026-07-01", "2026-07-10"},
		{"en dash with spaces", "Promotion Period", "April 6 – June 7, 2026", "2026-03-28", "2026-06-07"},
		{"arrow", "Deadline", "June 30 → July 31, 2026", "2026-06-10", "2026-07-31"},
	})
}

func TestEndDate_BadInput(t *testing.T) {
	for _, in := range []string{"", "[]", "null", "{", "not json", `{"Label":"Deadline"}`, `[{"Label":"Deadline","Value":""}]`} {
		if got := EndDate(in, day("2026-04-01")); got != "" {
			t.Errorf("EndDate(%q) = %q, want empty", in, got)
		}
	}
}

// Rows written from model JSON use lower-case keys; rows written by Go use
// capitalised ones. Both are in production.
func TestEndDate_BothKeySpellings(t *testing.T) {
	lower := `[{"label":"Bonus","value":"20%"},{"label":"Deadline","value":"September 19, 2026"}]`
	upper := `[{"Label":"Bonus","Value":"20%"},{"Label":"Deadline","Value":"September 19, 2026"}]`
	for _, in := range []string{lower, upper} {
		if got := EndDate(in, day("2026-09-10")); got != "2026-09-19" {
			t.Errorf("EndDate(%s) = %q, want 2026-09-19", in, got)
		}
	}
}

// The last day is still live; the offer has ended from the next day.
func TestEnded(t *testing.T) {
	cases := []struct {
		endsOn, today string
		want          bool
	}{
		{"2026-07-29", "2026-07-28", false},
		{"2026-07-29", "2026-07-29", false},
		{"2026-07-29", "2026-07-30", true},
		{"", "2026-07-30", false},
	}
	for _, c := range cases {
		if got := Ended(c.endsOn, c.today); got != c.want {
			t.Errorf("Ended(%q, %q) = %v, want %v", c.endsOn, c.today, got, c.want)
		}
	}
}

// An offer ending on a date is live until that date is over in Hawaii, the
// westernmost US time zone, so a Pacific-time deadline is not called ended
// while it is still that day in California.
func TestToday_JudgedInHawaiiTime(t *testing.T) {
	cases := []struct {
		name  string
		now   time.Time
		today string
	}{
		{"11pm Jul 29 New York", time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC), "2026-07-29"},
		{"12:30am Jul 30 New York, 9:30pm Jul 29 Los Angeles", time.Date(2026, 7, 30, 4, 30, 0, 0, time.UTC), "2026-07-29"},
		{"2:59am Jul 30 New York, 11:59pm Jul 29 Los Angeles", time.Date(2026, 7, 30, 6, 59, 0, 0, time.UTC), "2026-07-29"},
		{"11:59pm Jul 29 Honolulu", time.Date(2026, 7, 30, 9, 59, 0, 0, time.UTC), "2026-07-29"},
		{"midnight Jul 30 Honolulu", time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC), "2026-07-30"},
		// Hawaii keeps no daylight saving time: the same in winter.
		{"midnight Jan 15 Honolulu", time.Date(2027, 1, 15, 10, 0, 0, 0, time.UTC), "2027-01-15"},
		{"11:59pm Jan 14 Honolulu", time.Date(2027, 1, 15, 9, 59, 0, 0, time.UTC), "2027-01-14"},
	}
	for _, c := range cases {
		if got := Today(c.now); got != c.today {
			t.Errorf("%s: Today = %q, want %q", c.name, got, c.today)
		}
	}
	// The weekly newsletter goes out at 8 AM New York time; by then an offer
	// that ended the day before has ended in Hawaii too.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	for _, send := range []time.Time{
		time.Date(2026, 7, 30, 8, 0, 0, 0, ny), // daylight saving time
		time.Date(2027, 1, 16, 8, 0, 0, 0, ny), // standard time
	} {
		yesterday := send.AddDate(0, 0, -1).Format(DateLayout)
		if !Ended(yesterday, Today(send)) {
			t.Errorf("at 8 AM New York on %s, an offer that ended on %s should have ended", send.Format(DateLayout), yesterday)
		}
	}
}

func TestLocation_FallsBackToUTC(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, name := range []string{"", "Not/AZone"} {
		if got := Location(name); got != time.UTC {
			t.Errorf("Location(%q) = %v, want UTC", name, got)
		}
	}
	if got := Location("America/New_York"); got.String() != "America/New_York" {
		t.Errorf("Location(America/New_York) = %v", got)
	}
	// An unset zone is UTC by design; a misspelt one is a mistake worth a
	// warning, since it shifts every end date by the UTC offset.
	if n := strings.Count(logged.String(), "level=WARN"); n != 1 || !strings.Contains(logged.String(), "Not/AZone") {
		t.Errorf("want one warning naming the unknown zone, got:\n%s", logged.String())
	}
}

func TestDisplay(t *testing.T) {
	if got := Display("2026-07-29"); got != "July 29, 2026" {
		t.Errorf("Display = %q, want July 29, 2026", got)
	}
	for _, bad := range []string{"", "2026-13-01", "July 29"} {
		if got := Display(bad); got != "" {
			t.Errorf("Display(%q) = %q, want empty", bad, got)
		}
	}
}

// A detail that changes the deadline ("Extended Deadline", "New Deadline")
// is not read, so the article gets no end at all rather than the old one.
func TestEndDate_ChangedDeadlineLeavesNoEnd(t *testing.T) {
	for _, label := range []string{"Extended Deadline", "New Deadline", "Deadline (Extended)", "Updated Deadline", "Extension"} {
		for _, value := range []string{"July 31, 2026", "Until further notice"} {
			got := EndDate(details(t, "Deadline", "June 30, 2026", label, value), day("2026-06-10"))
			if got != "" {
				t.Errorf("Deadline: June 30 + %s: %q = %q, want no end", label, value, got)
			}
		}
	}
}

// More ways of saying the end is not known: a past date beside them is
// history, not this offer's end.
func TestEndDate_MoreOpenEndedWording(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"unannounced", "Deadline", "Unannounced. The last similar offer ended June 30, 2025.", "2026-06-10", ""},
		{"unknown", "Deadline", "Unknown; the 2025 version ended September 30, 2025", "2026-06-10", ""},
		{"not specified", "Deadline", "Not specified (last year's ended May 31)", "2026-06-10", ""},
		{"not announced", "Deadline", "Not announced; the previous offer ran through January 31, 2026", "2026-06-10", ""},
		{"none", "Deadline", "None; previously June 30, 2026", "2026-06-10", ""},
		{"never expires", "Deadline", "Never expires (was December 31, 2026)", "2026-06-10", ""},
		{"n/a", "Deadline", "N/A - ran through May 31 last year", "2026-06-10", ""},
	})
}

// A readable date followed by words is not the start of a window when it
// can be read as an end, and a window that opens after every readable end
// runs on to an end that cannot be read.
func TestEndDate_TrailingWordsDoNotMakeAnEarlierDateWin(t *testing.T) {
	// June 30 or no end are both right; the earlier registration date is not.
	for _, value := range []string{
		"Register by May 15; complete stays by June 30 to earn the bonus",
		"Register by May 15, 2026; stays through June 30, 2026 – targeted offer",
		"Apply by May 31, 2026, spend by June 30, 2026 through the app",
	} {
		if got := EndDate(details(t, "Deadline", value), day("2026-05-10")); got != "" && got < "2026-06-30" {
			t.Errorf("EndDate(Deadline: %q) = %q, want 2026-06-30 or later, or no end", value, got)
		}
	}
	if got := EndDate(details(t, "Registration Deadline", "May 20", "Promotion Period", "June 1 - Labor Day"), day("2026-05-10")); got != "" {
		t.Errorf("a promotion window opening after the registration deadline = %q, want no end", got)
	}
}

// IsEndLabel applies EndDate's label rule to one label, for the article page.
func TestIsEndLabel(t *testing.T) {
	for label, want := range map[string]bool{
		"Deadline":           true,
		"Booking  Deadline:": true,
		"book by":            true,
		"Promotion Period":   true,
		"Travel Dates":       false,
		"Blackout Dates":     false,
		"Extended Deadline":  false,
		"Bonus":              false,
	} {
		if got := IsEndLabel(label); got != want {
			t.Errorf("IsEndLabel(%q) = %v, want %v", label, got, want)
		}
	}
}
