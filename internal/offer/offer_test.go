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

// Every value below is copied from a live article's Key Details or the dev
// database, so the parser is tested against what the generator really writes.
func TestEndDate_FormatsSeenInRealData(t *testing.T) {
	cases := []struct {
		name   string
		label  string
		value  string
		anchor string
		want   string
	}{
		{"full date", "Deadline", "July 29, 2026", "2026-07-22", "2026-07-29"},
		{"time and zone after the date", "Deadline", "August 22, 2026, 23:59 ET", "2026-07-22", "2026-08-22"},
		{"time before the date", "Deadline", "Midnight ET on March 26, 2026", "2026-03-27", "2026-03-26"},
		{"through", "Promotion Period", "Through June 9, 2026", "2026-04-04", "2026-06-09"},
		{"range across months", "Promotion Period", "April 6 - June 7, 2026", "2026-03-28", "2026-06-07"},
		{"range with to", "Promotion Period", "April 6 to May 18, 2026", "2026-04-07", "2026-05-18"},
		{"range repeating the month", "Booking Period", "May 26 - May 29, 2026", "2026-05-27", "2026-05-29"},
		{"range within one month, en dash", "Booking Window", "July 7–10, 2026", "2026-07-08", "2026-07-10"},
		{"qualified deadline label", "Marriott Booking Deadline", "June 2, 2026", "2026-05-27", "2026-06-02"},
		{"expiration date label", "Expiration date", "May 21, 2026", "2026-04-02", "2026-05-21"},
		{"two deadlines in one value: the earlier", "Deadline", "July 7, 2026 (stay completion) and July 6, 2026 (booking)", "2026-05-12", "2026-07-06"},
		{"abbreviated month, no comma", "Deadline", "Sep 30 2026", "2026-07-22", "2026-09-30"},
		{"ordinal day", "Deadline", "September 19th, 2026", "2026-09-10", "2026-09-19"},
		{"before means the day before", "Southwest Booking Deadline", "Book before April 9 to lock in current fees", "2026-04-10", "2026-04-08"},
		{"iso date", "Deadline", "2026-07-29", "2026-07-22", "2026-07-29"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EndDate(details(t, c.label, c.value), day(c.anchor))
			if got != c.want {
				t.Errorf("EndDate(%s: %q) = %q, want %q", c.label, c.value, got, c.want)
			}
		})
	}
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
		// "Through December 31" written in spring or summer means the coming
		// December. The nearest December is the one gone, which would mark a
		// live offer ended the moment it was published.
		{"December 31", "2026-05-08", "2026-12-31"},
		{"December 31", "2026-06-15", "2026-12-31"},
		{"October 1", "2026-03-01", "2026-10-01"},
		{"July 7", "2026-01-05", "2026-07-07"},
		// A date that passed up to 14 days before the article is the one
		// just gone; an older one is read as next year's.
		{"March 1", "2026-03-15", "2026-03-01"},
		{"March 1", "2026-03-16", "2027-03-01"},
		// Written in January about a November deadline: this November. A
		// wider grace read it as last November and ended a live offer.
		{"November 30", "2027-01-20", "2027-11-30"},
		{"Deadline: November 30", "2027-01-20", "2027-11-30"},
		// A range whose end carries the year lends it to its start, stepping
		// back across the new year.
		{"December 17 - January 5, 2027", "2026-12-01", "2027-01-05"},
		// With no year anywhere, the range ends on the next January 5.
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

// When one article states several ends, the earliest is when a reader can no
// longer take the offer: registering closes before travel has to finish.
func TestEndDate_EarliestEndAcrossDetails(t *testing.T) {
	kd := details(t,
		"Bonus", "Double points",
		"Registration Deadline", "April 12",
		"Travel Deadline", "July 1",
	)
	if got := EndDate(kd, day("2026-04-11")); got != "2026-04-12" {
		t.Errorf("got %q, want the registration deadline 2026-04-12", got)
	}

	kd = details(t,
		"Promotion Period", "April 6 - June 7, 2026",
		"Registration Deadline", "May 31, 2026",
	)
	if got := EndDate(kd, day("2026-04-01")); got != "2026-05-31" {
		t.Errorf("got %q, want 2026-05-31", got)
	}
}

// Dates that are not the end of the offer must not become one, and values
// with no date at all must leave it empty rather than guess.
func TestEndDate_NoEndDate(t *testing.T) {
	cases := []struct{ label, value string }{
		// No date in the value.
		{"Deadline", "No specific deadline, offers tend to disappear without warning"},
		{"Deadline", "Limited-time offer, no hard deadline specified"},
		{"Deadline", "24 hours"},
		{"Offer Duration", "Limited time, no end date specified"},
		{"Offer Duration", "About a month and a half to two months"},
		// Month and year only: the last day would be a guess.
		{"Deadline", "October 2026"},
		// Dates that are not the offer's end.
		{"Effective Date", "April 1, 2026"},
		{"Effective Date for Existing Cardholders", "October 1, 2026"},
		{"Closure Date", "May 9, 2027"},
		{"Launch Date", "May 15, 2026"},
		{"Lounge Access End Date", "October 1, 2026"},
		{"Free Days Expiration", "December 31, 2026"},
		{"Volare Program Expiration", "December 31, 2026"},
		{"Expiration", "5 years"},
		{"Travel Period", "October 1 through December 10, 2026"},
		{"Travel Dates", "Flexible until September 30"},
		{"Eligible Stay Dates", "May 22 - August 29/31, 2026"},
		{"Blackout Dates", "September 7, November 19-30, December 17 - January 5"},
		// A window that only says when it opens.
		{"Promotion Period", "Starting May 1, 2026"},
		// Not a real day.
		{"Deadline", "February 30, 2026"},
	}
	for _, c := range cases {
		if got := EndDate(details(t, c.label, c.value), day("2026-04-01")); got != "" {
			t.Errorf("EndDate(%s: %q) = %q, want no end date", c.label, c.value, got)
		}
	}
}

// A value can name dates that are not its end: the deadline an extension
// replaced, or when the text was updated. Those give way to the real end.
func TestEndDate_SupersededAndIncidentalDates(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"extension, old date after", "Extended to October 31, 2026 (was September 30, 2026)", "2026-10-31"},
		{"extension, old date first", "Previously September 30, now October 31, 2026", "2026-10-31"},
		{"originally", "October 31, 2026 (originally September 30)", "2026-10-31"},
		{"formerly", "October 31, 2026, formerly September 30, 2026", "2026-10-31"},
		{"last updated", "September 30, 2026 (last updated May 1, 2026)", "2026-09-30"},
		{"announced on", "September 30, 2026; announced on August 3, 2026", "2026-09-30"},
		{"posted", "Posted August 3, 2026: register by September 30, 2026", "2026-09-30"},
		// A past-tense deadline with nothing replacing it is still the end:
		// dropping it would leave an ended offer looking current.
		{"only a past-tense date", "Was September 19, 2026 (offer has ended)", "2026-09-19"},
		// An update date alone is never the end.
		{"only an update date", "Not announced (last updated May 1, 2026)", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EndDate(details(t, "Deadline", c.value), day("2026-07-01")); got != c.want {
				t.Errorf("EndDate(Deadline: %q) = %q, want %q", c.value, got, c.want)
			}
		})
	}
}

// ISO dates with a time attached ("2026-09-30T23:59") are read as their date.
func TestEndDate_ISOTimestamp(t *testing.T) {
	for _, v := range []string{"2026-09-30T23:59", "2026-09-30T23:59:00-04:00", "Ends 2026-09-30T23:59Z", "2026-09-30 23:59 ET"} {
		if got := EndDate(details(t, "Deadline", v), day("2026-07-01")); got != "2026-09-30" {
			t.Errorf("EndDate(Deadline: %q) = %q, want 2026-09-30", v, got)
		}
	}
}

// The generator writes typographic spaces and dashes inside dates: 7 of the
// 87 live titles do ("Ends July\u202f29,\u202f2026", "June\u202f15\u2011July\u202f15"), and
// Key Details values carry U+202F too. They must read exactly like their
// ASCII forms: missing one leaves an ended offer looking current, and reading
// only the first half of a range ends a live offer early.
func TestEndDate_UnicodeSpacesAndDashes(t *testing.T) {
	cases := []struct {
		name, label, value, anchor, want string
	}{
		{"narrow no-break spaces", "Deadline", "July\u202f29,\u202f2026", "2026-07-22", "2026-07-29"},
		{"no-break space", "Deadline", "July\u00a029, 2026", "2026-07-22", "2026-07-29"},
		{"thin and figure spaces", "Deadline", "Sep\u200930\u20072026", "2026-07-22", "2026-09-30"},
		// Read as a range, June 15 opens it. Misread, June 15 was taken as
		// the deadline, a month early.
		{"non-breaking hyphen across months", "Deadline", "June\u202f15\u2011July\u202f15,\u202f2026", "2026-06-10", "2026-07-15"},
		{"non-breaking hyphen in a period", "Promotion Period", "June 15\u2011July 15, 2026", "2026-06-10", "2026-07-15"},
		{"non-breaking hyphen within a month", "Promotion Period", "July 7\u201110, 2026", "2026-07-01", "2026-07-10"},
		{"hyphen U+2010", "Booking Window", "July 7\u201010, 2026", "2026-07-01", "2026-07-10"},
		{"minus sign", "Booking Window", "July 7\u221210, 2026", "2026-07-01", "2026-07-10"},
		{"en dash with spaces", "Promotion Period", "April 6 \u2013 June 7, 2026", "2026-03-28", "2026-06-07"},
		{"no-break space in the label", "Offer\u00a0ends", "May 21, 2026", "2026-04-02", "2026-05-21"},
		{"narrow no-break space in the label", "Promotion\u202fPeriod", "April 6 - June 7, 2026", "2026-03-28", "2026-06-07"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EndDate(details(t, c.label, c.value), day(c.anchor)); got != c.want {
				t.Errorf("EndDate(%q: %q) = %q, want %q", c.label, c.value, got, c.want)
			}
		})
	}
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

// A time written after a year-less date is not the end of a range within the
// month: "June 30 - 11:59 PM ET" was read as June 30 to June 11, and June 11
// ended the offer nineteen days early.
func TestEndDate_TimeAfterDateIsNotARange(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"dash, time with minutes", "Deadline", "June 30 - 11:59 PM ET", "2026-06-01", "2026-06-30"},
		{"en dash, lower-case pm", "Deadline", "June 30 – 11:59pm ET", "2026-06-01", "2026-06-30"},
		{"through a time", "Deadline", "June 30 through 11:59 PM", "2026-06-01", "2026-06-30"},
		{"dash, hour only", "Deadline", "June 30 - 5 PM ET", "2026-06-01", "2026-06-30"},
		{"dotted p.m.", "Deadline", "June 30 - 5 p.m. PT", "2026-06-01", "2026-06-30"},
		{"a percentage", "Deadline", "June 30 - 10% bonus", "2026-06-01", "2026-06-30"},
		// A same-month range ends after it starts; "June 30 - 5" is not one.
		{"end day before start day", "Deadline", "June 30 - 5 tiers", "2026-06-01", "2026-06-30"},
		// Real ranges still read.
		{"real range", "Booking Window", "July 7-10, 2026", "2026-07-01", "2026-07-10"},
		{"real range with to", "Promotion Period", "July 7 to 10", "2026-07-01", "2026-07-10"},
	})
}

// A date that says when the offer started, or how an earlier run of it went,
// is not when it ends -- though it is the earliest date in the value.
func TestEndDate_StartsAndHistoryAreNotTheEnd(t *testing.T) {
	// Written before every date below, so no date is dropped for predating
	// the article: the wording alone has to rule the earlier date out.
	const written = "2026-05-20"
	runEndDateCases(t, []endDateCase{
		{"launched", "Deadline", "July 31, 2026 (launched June 1)", written, "2026-07-31"},
		{"started", "Deadline", "Started June 1; ends July 31, 2026", written, "2026-07-31"},
		{"began", "Deadline", "Began June 1; ends July 31, 2026", written, "2026-07-31"},
		{"live since", "Deadline", "Live since June 1; ends July 31, 2026", written, "2026-07-31"},
		{"opened", "Deadline", "Opened June 1, ends July 31, 2026", written, "2026-07-31"},
		{"kicked off", "Deadline", "Kicked off June 1; runs through July 31, 2026", written, "2026-07-31"},
		{"went live", "Deadline", "Went live June 1; ends July 31, 2026", written, "2026-07-31"},
		{"returned", "Deadline", "Returned June 1; ends July 31, 2026", written, "2026-07-31"},
		{"last year's run", "Deadline", "July 31, 2026 (last year's version ended June 30, 2025)", written, "2026-07-31"},
		{"prior offer", "Deadline", "July 31, 2026; prior offer expired May 31", written, "2026-07-31"},
		// The history is left behind for this run.
		{"unlike the previous offer", "Deadline", "Unlike the previous offer, this one ends June 30, 2026", written, "2026-06-30"},
		// Every date in the history clause, including a range's end.
		{"prior offer's range", "Deadline", "Not announced; prior offer ran June 1 - June 30", written, ""},
		{"prior offer's month range", "Deadline", "Not announced; prior offer ran June 1 through July", written, ""},
	})
}

// An extension names the old date and the new one; the new one is the end.
func TestEndDate_ExtensionsTakeTheNewDate(t *testing.T) {
	const written = "2026-05-20"
	runEndDateCases(t, []endDateCase{
		{"instead of", "Deadline", "July 31, 2026 instead of June 30", written, "2026-07-31"},
		{"new and old deadline", "Deadline", "New deadline July 31, 2026; old deadline June 30, 2026", written, "2026-07-31"},
		{"now extended through", "Deadline", "June 30, 2026, now extended through July 31, 2026", written, "2026-07-31"},
		{"extended to", "Deadline", "June 30, 2026 (extended to July 31, 2026)", written, "2026-07-31"},
		{"not", "Deadline", "July 31, 2026, not June 30", written, "2026-07-31"},
		{"now", "Deadline", "June 30, now July 31, 2026", written, "2026-07-31"},
		{"replaced by", "Deadline", "June 30, 2026 (replaced by July 31, 2026)", written, "2026-07-31"},
		// "Book now through" is not an extension: the registration date
		// before it still stands.
		{"book now through", "Deadline", "Register by June 15, 2026; book now through June 30, 2026", written, "2026-06-15"},
	})
}

// A superseded date on its own says the offer no longer ends then. Unless the
// value says the offer is over, there is no end date.
func TestEndDate_LoneSupersededDateIsNoEnd(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"extended indefinitely", "Deadline", "Extended indefinitely (was June 30, 2026)", "2026-05-20", ""},
		{"until further notice", "Deadline", "Until further notice (originally June 30, 2026)", "2026-05-20", ""},
		{"none", "Deadline", "None; previously June 30, 2026", "2026-05-20", ""},
		{"old deadline only", "Deadline", "Old deadline: June 30, 2026", "2026-05-20", ""},
		// Said to be over: the date is when it ended.
		{"expired", "Deadline", "Expired June 30, 2026", "2026-07-02", "2026-06-30"},
		{"ended", "Deadline", "Ended on June 30, 2026", "2026-07-02", "2026-06-30"},
	})
}

// A window with no stated end has no end date: its only date is when it
// opened. "Through July" runs to the end of July.
func TestEndDate_OpenEndedPeriods(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"TBD", "Promotion Period", "June 1 - TBD", "2026-05-20", ""},
		{"ongoing", "Promotion Period", "June 1 – ongoing", "2026-05-20", ""},
		{"until further notice", "Promotion Period", "June 1, 2026 until further notice", "2026-05-20", ""},
		{"onward", "Promotion Period", "June 1 onward", "2026-05-20", ""},
		{"present", "Offer Dates", "June 1 - present", "2026-06-20", ""},
		{"and beyond", "Deadline", "June 1 and beyond", "2026-05-20", ""},
		{"dangling dash", "Promotion Period", "June 1 -", "2026-05-20", ""},
		{"while supplies last", "Deadline", "June 1 until supplies last", "2026-05-20", ""},
		{"through a month", "Promotion Period", "June 1 through July", "2026-05-20", "2026-07-31"},
		{"through a month and year", "Promotion Period", "June 1 - July 2026", "2026-05-20", "2026-07-31"},
		{"through a month, start has the year", "Promotion Period", "December 1, 2026 through January", "2026-11-20", "2027-01-31"},
		{"through February in a leap year", "Promotion Period", "January 5, 2028 through February", "2027-12-20", "2028-02-29"},
		// A remark after a dash, or a channel after "through", is not an
		// open end: the date is still the deadline.
		{"remark after a dash", "Deadline", "September 30, 2026 - act fast", "2026-09-01", "2026-09-30"},
		{"may end early", "Deadline", "September 30, 2026 - may end early", "2026-09-01", "2026-09-30"},
		{"and after that", "Deadline", "September 30, 2026, and after that the bonus drops", "2026-09-01", "2026-09-30"},
		{"through a channel", "Deadline", "June 30 through the Chase portal", "2026-06-01", "2026-06-30"},
		{"between and", "Promotion Period", "Between June 1 and June 30, 2026", "2026-05-20", "2026-06-30"},
	})
}

// "On or before June 30" includes June 30; only "before June 30" stops at
// June 29.
func TestEndDate_OnOrBefore(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"on or before", "Deadline", "Enroll on or before June 30, 2026", "2026-06-01", "2026-06-30"},
		{"by or before", "Deadline", "Register by or before June 30, 2026", "2026-06-01", "2026-06-30"},
		{"before", "Deadline", "Enroll before June 30, 2026", "2026-06-01", "2026-06-29"},
	})
}

// The article describes an offer that is running when it is written. A date
// before that, next to one on or after it, is the offer's past.
func TestEndDate_DatesBeforeTheArticleGiveWay(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"earlier date unmarked", "Deadline", "June 1, 2026 and July 31, 2026", "2026-06-15", "2026-07-31"},
		{"yearless earlier date", "Deadline", "June 10 (registration), July 31 (travel)", "2026-06-15", "2026-07-31"},
		// Both still ahead: the earlier one is the deadline.
		{"both ahead", "Deadline", "June 20, 2026 and July 31, 2026", "2026-06-15", "2026-06-20"},
		// Written after the offer closed: the date stands.
		{"only an earlier date", "Deadline", "June 1, 2026", "2026-06-15", "2026-06-01"},
	})
}

// "Deadline" in a label does not always mean the offer's end. An old
// deadline has been replaced, and a deadline to use or earn a benefit (a
// credit, a discount, a spend) comes and goes while the offer stays open.
func TestEndDate_LabelsThatAreNotTheOffersEnd(t *testing.T) {
	for _, label := range []string{
		"Original Deadline", "Previous Deadline", "Old Deadline", "Former Deadline", "Initial Deadline", "Prior Deadline",
		"Statement Credit Deadline", "Discount Deadline", "Redemption Deadline", "Spend Deadline",
		"Minimum Spend Deadline", "Deadline to Use Credit", "Deadline to Redeem", "Credit Deadline",
	} {
		if got := EndDate(details(t, label, "September 30, 2026"), day("2026-04-02")); got != "" {
			t.Errorf("EndDate(%s: September 30, 2026) = %q, want no end date", label, got)
		}
	}
	// The BofA Atmos Rewards Summit article: its only dated detail is when
	// the first-year flight discount must be used. The 100K offer may run
	// on past it, so it must not end the offer on October 1.
	atmos := details(t,
		"Sign-up Bonus", "100,000 points",
		"Spend Requirement", "$6,500 in 90 days",
		"Discount Deadline", "September 30, 2026",
	)
	if got := EndDate(atmos, day("2026-04-02")); got != "" {
		t.Errorf("Atmos Summit end = %q, want none", got)
	}
	// With the current deadline beside the original one, the current one is
	// the end even though it is later.
	both := details(t,
		"Original Deadline", "June 30, 2026",
		"Deadline", "July 31, 2026",
	)
	if got := EndDate(both, day("2026-05-20")); got != "2026-07-31" {
		t.Errorf("EndDate with an original deadline = %q, want 2026-07-31", got)
	}
	// Deadlines about taking the offer still count.
	for _, label := range []string{"Deadline", "Booking Deadline", "Registration Deadline", "Deadline for sharing ThankYou points", "Extended Deadline", "New Deadline"} {
		if got := EndDate(details(t, label, "September 30, 2026"), day("2026-04-02")); got != "2026-09-30" {
			t.Errorf("EndDate(%s: September 30, 2026) = %q, want 2026-09-30", label, got)
		}
	}
}

// A four-digit number after a date is only its year when it is a plausible
// one: "December 31, 1000 bonus points" is December 31, not the year 1000.
// A year too far ahead is no date at all: reading it as this year's would
// end an offer that the text says runs for years.
func TestEndDate_ImplausibleYears(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"points after the date", "Deadline", "December 31, 1000 bonus points", "2026-05-08", "2026-12-31"},
		{"far-future number", "Deadline", "March 31, 2500 points", "2026-03-01", ""},
		{"two years back is a year", "Deadline", "September 19, 2024", "2026-03-01", "2024-09-19"},
		{"five years ahead is a year", "Deadline", "December 31, 2031", "2026-03-01", "2031-12-31"},
		{"six years ahead is no date", "Deadline", "December 31, 2032", "2026-03-01", ""},
		// Read as this year's, these ended the offer on January 1.
		{"2099 expiration", "Expiration Date", "December 31, 2099", "2026-06-10", ""},
		{"2099 deadline", "Deadline", "December 31, 2099", "2026-06-10", ""},
		{"2099 listed as no expiration", "Deadline", "No expiration (listed as December 31, 2099)", "2026-06-10", ""},
		{"2032 valid through", "Valid Through", "December 31, 2032", "2026-06-10", ""},
		{"far-future range end", "Promotion Period", "June 1 - December 31, 2099", "2026-05-20", ""},
		// An ISO or numeric date with an implausible year is no date.
		{"iso", "Deadline", "1000-12-31", "2026-03-01", ""},
		{"numeric", "Deadline", "6/30/1999", "2026-03-01", ""},
		{"numeric far ahead", "Deadline", "6/30/2500", "2026-03-01", ""},
		// Nor does a bogus year lend itself to a yearless date before it.
		{"no lending", "Promotion Period", "June 1 - June 30, 1000", "2026-05-20", "2026-06-30"},
	})
}

// More ways the generator labels an offer's end.
func TestEndDate_MoreEndLabels(t *testing.T) {
	for _, label := range []string{"Book By", "Apply By", "Register By", "Ends On", "Offer Valid Through", "Promotion End", "Offer Ends", "Offer ends:"} {
		if got := EndDate(details(t, label, "September 30, 2026"), day("2026-07-01")); got != "2026-09-30" {
			t.Errorf("EndDate(%s: September 30, 2026) = %q, want 2026-09-30", label, got)
		}
	}
}

// More ways the generator writes a date.
func TestEndDate_MoreDateFormats(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"abbreviation with no space", "Deadline", "Sept.30, 2026", "2026-07-01", "2026-09-30"},
		{"abbreviation with no space, no year", "Deadline", "Sept.30", "2026-07-01", "2026-09-30"},
		{"no space after the comma", "Deadline", "September 30,2026", "2026-07-01", "2026-09-30"},
		{"numeric", "Deadline", "6/30/2026", "2026-06-01", "2026-06-30"},
		{"numeric range", "Promotion Period", "6/1/2026 - 6/30/2026", "2026-05-20", "2026-06-30"},
		// Without a four-digit year a slash is not a date: "5/24" is Chase's
		// rule, and "4/15" is too short to trust.
		{"numeric without year", "Deadline", "Ends 4/15", "2026-04-01", ""},
		{"not a real day", "Deadline", "2/30/2026", "2026-01-01", ""},
	})
}

// A numeric date is month first, but one written day first must not end an
// offer the day it is published: read month first, "01/07/2026" in June is
// January 7, long gone, so it is July 1.
func TestEndDate_NumericDayFirst(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"day first, zero-padded", "Deadline", "01/07/2026", "2026-06-10", "2026-07-01"},
		{"day first, with a UK time", "Deadline", "1/7/2026 11:59pm BST", "2026-06-10", "2026-07-01"},
		{"only day first is a date", "Deadline", "30/06/2026", "2026-06-10", "2026-06-30"},
		// Month first when that is still ahead, or only just gone.
		{"month first ahead", "Deadline", "1/5/2027", "2026-06-10", "2027-01-05"},
		{"month first just gone", "Deadline", "6/1/2026", "2026-06-10", "2026-06-01"},
		{"same both ways", "Deadline", "12/12/2026", "2026-06-10", "2026-12-12"},
		// Both readings long gone: the later.
		{"both gone", "Deadline", "03/04/2026", "2026-06-10", "2026-04-03"},
		// Month first just gone, day first still ahead: day first.
		{"just gone month first, ahead day first", "Deadline", "06/07/2026", "2026-06-10", "2026-07-06"},
		{"September", "Deadline", "09/10/2026", "2026-09-20", "2026-10-09"},
		{"November", "Deadline", "11/12/2026", "2026-11-20", "2026-12-11"},
		// Beside an end still ahead, the date just gone is the offer's
		// past: read day first, it would end the offer sooner.
		{"beside a later end", "Deadline", "July 31, 2026 (ran through 3/4/2026)", "2026-03-18", "2026-07-31"},
		// The "07" of a numeric date is not the end of a range from March 1.
		{"range to a numeric date", "Promotion Period", "March 1 - 07/01/2026", "2026-05-20", "2026-07-01"},
	})
}

// An hour right after a day-first date is not a day: "30 June 11:59pm" is
// not June 11. The day-first date itself is not read, which leaves no date
// rather than a wrong one.
func TestEndDate_TimeAfterDayFirstDate(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"BST", "Deadline", "30 June 11:59pm BST", "2026-06-10", ""},
		{"weekday", "Deadline", "Sat 27 June 11:59pm", "2026-06-10", ""},
		{"abbreviated month", "Deadline", "30 Sept 11:59pm", "2026-06-10", ""},
		{"24-hour time", "Deadline", "30 June 23:59 BST", "2026-06-10", ""},
		{"dotted time", "Deadline", "30 June 11.59pm", "2026-06-10", ""},
		// British times are dotted, often with no am or pm.
		{"dotted 24-hour time", "Deadline", "30 June 23.59", "2026-06-10", ""},
		{"dotted time, BST", "Deadline", "30 June 11.59 BST", "2026-06-10", ""},
		{"month first, dotted time", "Deadline", "June 30, 2026 23.59 BST", "2026-06-10", "2026-06-30"},
		{"hour only", "Deadline", "June 11 PM", "2026-06-10", ""},
		// A time after a month-first date is still just a time.
		{"month first", "Deadline", "June 12 11:59 PM PT", "2026-06-10", "2026-06-12"},
		{"month first with year", "Deadline", "June 30, 2026 at 11:59 PM PT", "2026-06-10", "2026-06-30"},
	})
}

// A number after a dash is not a range's end when it is a time or an amount.
// Reading the amount as the end left June 30 as a range's start, and the
// deadline was lost.
func TestEndDate_NumberAfterDashIsNotARange(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"points", "Deadline", "June 30 - 75,000 bonus points after $5K spend", "2026-06-10", "2026-06-30"},
		{"small amount", "Deadline", "June 3 - 10,000 points", "2026-06-01", "2026-06-03"},
		{"nights", "Deadline", "July 7 – 15 nights", "2026-06-10", "2026-07-07"},
		{"multiplier", "Deadline", "June 1 - 5x points", "2026-05-20", "2026-06-01"},
		{"time zone", "Deadline", "June 13 – 10 ET", "2026-06-10", "2026-06-13"},
		{"noon", "Deadline", "June 3 - 12 noon ET", "2026-06-01", "2026-06-03"},
		{"dotted time", "Deadline", "June 13 - 11.59pm ET", "2026-06-10", "2026-06-13"},
	})
}

// A date without a year may borrow one from a later date in the value, but
// never to read earlier than it would alone. A start, a replaced date or an
// earlier run's date is no guide to the end's year: borrowing "May 1, 2026"
// made "June 30" June 30, 2025, ended on publication.
func TestEndDate_BorrowedYearNeverReadsEarlier(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"launch date", "Deadline", "June 30 (launched May 1, 2026)", "2026-06-10", "2026-06-30"},
		{"last year's version", "Deadline", "July 31 (last year's version ended June 30, 2025)", "2026-06-10", "2026-07-31"},
		{"last year's offer, same day", "Deadline", "September 30 (last year's offer ended September 30, 2025)", "2026-09-01", "2026-09-30"},
		{"last year", "Deadline", "March 31 (last year: March 31, 2025)", "2026-03-01", "2026-03-31"},
		{"a year's version", "Deadline", "June 30; the 2025 version ran June 1 - July 15, 2025", "2026-06-10", "2026-06-30"},
		{"2025's version", "Deadline", "March 31 (2025's version ended February 28, 2025)", "2026-03-01", "2026-03-31"},
		{"originally", "Deadline", "January 15 (originally December 31, 2026)", "2026-12-10", "2027-01-15"},
		{"extended from", "Deadline", "January 31 (extended from December 31, 2026)", "2026-12-10", "2027-01-31"},
		{"launched in December", "Deadline", "February 15 (launched December 1, 2026)", "2026-12-10", "2027-02-15"},
		{"prior offer", "Deadline", "January 15 (prior offer expired May 31, 2026)", "2026-12-01", "2027-01-15"},
		{"replaced range", "Promotion Period", "Extended through January 31 (was December 1 - December 31, 2026)", "2026-12-10", "2027-01-31"},
		// Borrowing still reads a later year when that is what it gives.
		{"later lender", "Deadline", "December 31 (launched January 15, 2027)", "2026-01-10", "2026-12-31"},
	})
}

// A range's end falls after its start. Written in June, "September 30 -
// mid-July" runs to next July; read alone, July 31 had the offer end before
// it began.
func TestEndDate_RangeEndFollowsItsStart(t *testing.T) {
	runEndDateCases(t, []endDateCase{
		{"crosses the new year", "Promotion Period", "Sept. 30 - mid-July", "2026-06-10", "2027-07-31"},
		{"within a year, running", "Promotion Period", "May 15 – June 15", "2026-06-10", "2026-06-15"},
		{"over the holidays, written after it began", "Promotion Period", "December 17 - January 5", "2027-01-02", "2027-01-05"},
		{"over the holidays, written before it began", "Promotion Period", "December 17 - January 5", "2026-12-01", "2027-01-05"},
	})
}

// An extension replaces the dates it pushes back, never a later one. Marking
// every earlier-written date as replaced cut periods short: "Through July
// 31, 2026 (originally extended to June 30)" ended on June 30.
func TestEndDate_ExtensionOnlyReplacesEarlierDates(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"originally extended to", "Promotion Period", "Through July 31, 2026 (originally extended to June 30)", written, "2026-07-31"},
		{"first moved to", "Offer Period", "April 1 – July 31, 2026 (extended; the offer was first moved to June 30)", written, "2026-07-31"},
		{"now through, for some", "Promotion Period", "June 1 – August 31, 2026; now through July 15 for double points", written, "2026-08-31"},
		{"now through, for non-members", "Sale Period", "June 1 - July 31, 2026 (now through June 30 for non-members)", written, "2026-07-31"},
		{"extended twice, newest first", "Deadline", "Extended to July 31, 2026 (after it was first extended to June 30)", written, "2026-07-31"},
		{"extended twice, older one after", "Deadline", "Extended again: July 31, 2026 (after being extended once to June 30)", written, "2026-07-31"},
		{"was, then extended to", "Deadline", "July 31, 2026 (was June 30; before that it had been extended to June 15)", "2026-06-01", "2026-07-31"},
		{"newest extension last", "Deadline", "June 30, 2026, extended to July 15, 2026, now extended to August 31, 2026", written, "2026-08-31"},
	})
}

// More ways of saying a deadline was extended. With no new date given there
// is no end date; with one, the new date is the end.
func TestEndDate_MoreExtensionWordings(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"extended indefinitely, after", "Deadline", "June 30, 2026 (extended indefinitely)", written, ""},
		{"has been extended indefinitely", "Deadline", "June 30, 2026 has been extended indefinitely", written, ""},
		{"extended past", "Deadline", "Extended past June 30, 2026", written, ""},
		{"extended beyond", "Deadline", "Extended beyond June 30 (new date not announced)", written, ""},
		{"beyond the deadline", "Deadline", "Extended indefinitely beyond the June 30, 2026 deadline", written, ""},
		{"no longer", "Deadline", "No longer June 30 — extended until further notice", written, ""},
		{"extended, now", "Deadline", "June 30, 2026 — extended! Now July 15, 2026", written, "2026-07-15"},
		{"arrow", "Deadline", "June 30, 2026 → July 31, 2026", written, "2026-07-31"},
		{"ascii arrow", "Deadline", "June 30, 2026 -> July 31, 2026", written, "2026-07-31"},
		{"after the original", "Deadline", "Extended through July 31, 2026, after the original June 30 deadline", written, "2026-07-31"},
		{"was scheduled to end", "Deadline", "Now ends July 31, 2026 (was scheduled to end June 30)", written, "2026-07-31"},
		{"the original end date", "Deadline", "July 31, 2026 (the original June 30 end date was extended)", written, "2026-07-31"},
		{"had been set to expire", "Deadline", "Extended to July 31, 2026; the offer had been set to expire June 30", written, "2026-07-31"},
		{"extended it to", "Deadline", "June 30, 2026, but Chase has extended it to July 31, 2026", written, "2026-07-31"},
		{"earlier reports", "Deadline", "July 31, 2026 (earlier reports said June 30)", written, "2026-07-31"},
		{"later than the usual", "Deadline", "July 31, 2026, a month later than the usual June 30", written, "2026-07-31"},
		{"not as previously reported", "Deadline", "July 31, 2026 (not June 30 as previously reported)", written, "2026-07-31"},
		// "Extended from" names the old date: the date before it is the new one.
		{"extended from", "Deadline", "June 30 (extended from May 31)", written, "2026-06-30"},
		{"was extended from", "Deadline", "June 30 was extended from May 31", written, "2026-06-30"},
	})
}

// An extension in its own detail replaces the deadline beside it.
func TestEndDate_ReplacementLabels(t *testing.T) {
	for _, label := range []string{"Extended Deadline", "New Deadline", "New End Date", "Extended To", "Revised Deadline"} {
		kd := details(t, "Deadline", "June 30, 2026", label, "July 31, 2026")
		if got := EndDate(kd, day("2026-06-10")); got != "2026-07-31" {
			t.Errorf("EndDate(Deadline: June 30, 2026; %s: July 31, 2026) = %q, want 2026-07-31", label, got)
		}
	}
	kd := details(t, "Deadline", "June 30, 2026 (extended)", "Extended To", "July 31, 2026")
	if got := EndDate(kd, day("2026-06-10")); got != "2026-07-31" {
		t.Errorf("EndDate with Extended To = %q, want 2026-07-31", got)
	}
	// "New" as part of an ordinary label is not a replacement.
	kd = details(t, "New Cardmember Deadline", "June 30, 2026", "Travel Deadline", "July 31, 2026")
	if got := EndDate(kd, day("2026-06-10")); got != "2026-06-30" {
		t.Errorf("EndDate with New Cardmember Deadline = %q, want 2026-06-30", got)
	}
}

// A lone past-tense date is kept only when the value says this offer is
// over. "Ended" about another offer, next to "ongoing" or "no end date", or
// denied ("hasn't ended"), keeps nothing; nor does a date long before the
// article.
func TestEndDate_LoneEndedDateAboutAnotherOffer(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"ongoing, another offer ended", "Deadline", "Ongoing (the 60K offer ended June 1; the current 75K has no end date)", written, ""},
		{"unannounced, last similar offer", "Deadline", "Unannounced. The last similar offer ended June 30, 2025.", written, ""},
		{"unknown, a year's version", "Deadline", "Unknown; the 2025 version ended September 30, 2025", written, ""},
		{"no end date, previously ended", "Deadline", "No end date; previously ended June 30, 2025", written, ""},
		{"hasn't ended", "Deadline", "Originally June 30, 2026 — but the offer hasn't ended yet", written, ""},
		{"ongoing, a similar promo", "Deadline", "Ongoing; a similar promo expired March 31", written, ""},
		{"never expires", "Deadline", "Never expires (was December 31, 2026)", written, ""},
		{"a year before the article", "Deadline", "Expired June 30, 2025", written, ""},
		// Said to be over, recently: the date is when it ended.
		{"expired recently", "Deadline", "Expired May 20, 2026", written, "2026-05-20"},
		{"ended recently", "Deadline", "Ended June 5, 2026", written, "2026-06-05"},
		// Ended early: the day it ended, not the day it was meant to.
		{"ended earlier than planned", "Deadline", "Offer ended June 5, 2026 (earlier than the planned June 30)", written, "2026-06-05"},
		{"ended early, was scheduled", "Deadline", "Ended early: June 5, 2026 (was scheduled through June 30, 2026)", written, "2026-06-05"},
	})
}

// A window with a start and nothing readable after its joiner has no end
// date. In a period that holds whatever follows the joiner; in a deadline
// only explicit words do ("September 30, 2026 - act fast" still ends).
func TestEndDate_PeriodsWithNoReadableEnd(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"mid-month", "Promotion Period", "June 1 - mid-July", written, "2026-07-31"},
		{"end of a month", "Promotion Period", "June 1 – end of July", written, "2026-07-31"},
		{"early month", "Promotion Period", "June 1 – early July", written, "2026-07-31"},
		{"holiday", "Promotion Period", "June 1 – Labor Day", written, ""},
		{"limited time", "Promotion Period", "June 1 – limited time", written, ""},
		{"no end date announced", "Promotion Period", "June 1 - (no end date announced)", written, ""},
		{"end date unknown", "Promotion Period", "June 1 – end date unknown", written, ""},
		{"unannounced", "Promotion Period", "June 1 – unannounced", written, ""},
		{"question mark", "Promotion Period", "June 1 - ?", written, ""},
		{"N/A", "Promotion Period", "June 1 – N/A", written, ""},
		{"start date", "Promotion Period", "June 1 (start date; no end announced)", written, ""},
		{"start", "Promotion Period", "June 1 start, no end date", written, ""},
		{"until withdrawn", "Promotion Period", "June 1 until the promotion is withdrawn", written, ""},
		{"until pulled", "Promotion Period", "June 1 until Chase pulls it", written, ""},
		{"and ongoing", "Promotion Period", "June 1 and ongoing", written, ""},
		// A date that already closes the window stays the end.
		{"range then remark", "Promotion Period", "April 6 - June 7, 2026 - register now", "2026-04-01", "2026-06-07"},
		{"through then remark", "Promotion Period", "Through June 9, 2026 - hurry", "2026-04-01", "2026-06-09"},
		{"deadline remark", "Deadline", "September 30, 2026 - limited time", "2026-09-01", "2026-09-30"},
	})
}

// A history marker used to compare ("Unlike last year", "Same as last year")
// does not make this year's date history.
func TestEndDate_ComparedWithLastYear(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"unlike last year", "Deadline", "Unlike last year, the offer runs through September 30, 2026", written, "2026-09-30"},
		{"same as last year", "Deadline", "Same as last year: September 30, 2026", written, "2026-09-30"},
		// A past date in the history is still history.
		{"last year's date", "Deadline", "Not announced; last year's ran through September 30, 2025", written, ""},
		// Nor is an earlier run's date still ahead this run's end when the
		// value says this run has none yet.
		{"not announced, previous offer ahead", "Deadline", "Not announced; the previous offer ran through January 31, 2026", "2026-01-10", ""},
		{"ongoing, last year's version ahead", "Deadline", "Ongoing; last year's version runs until January 31, 2026", "2026-01-10", ""},
		// And a future-dated "history" never cuts a real deadline short.
		{"future-dated history beside the end", "Deadline", "January 15, 2027; prior offer ran June 30, 2026", "2026-01-10", "2027-01-15"},
	})
}

// A benefit word in a deadline's label does not make it a benefit's deadline
// when the label is about taking the offer.
func TestEndDate_TakingTheOfferDespiteABenefitWord(t *testing.T) {
	for _, label := range []string{"Credit Card Application Deadline", "Promo Code Use Deadline", "Discount Code Deadline", "Statement Credit Offer Deadline"} {
		if got := EndDate(details(t, label, "June 30, 2026"), day("2026-06-10")); got != "2026-06-30" {
			t.Errorf("EndDate(%s: June 30, 2026) = %q, want 2026-06-30", label, got)
		}
	}
	for _, label := range []string{"Offer Spend Deadline", "Deadline to Use Credit", "Discount Deadline"} {
		if got := EndDate(details(t, label, "June 30, 2026"), day("2026-06-10")); got != "" {
			t.Errorf("EndDate(%s: June 30, 2026) = %q, want no end date", label, got)
		}
	}
}

// An extension written after a later date still pushes back the deadline it
// extends. Dropping it as an older extension left the pre-extension
// deadline in force, and the offer ended a month early.
func TestEndDate_ExtensionAfterALaterDate(t *testing.T) {
	const written = "2026-06-10"
	runEndDateCases(t, []endDateCase{
		{"booking deadline extended, travel later", "Deadline", "Book by June 15, 2026 for travel through December 31, 2026 (extended to July 15, 2026)", written, "2026-07-15"},
		{"stay later, booking extended", "Deadline", "Stay by December 31, 2026; book by June 30, 2026, now extended to July 15, 2026", written, "2026-07-15"},
		// Pushing back no end, it is still an older extension.
		{"older extension", "Deadline", "July 31, 2026; now through July 15 for double points", written, "2026-07-31"},
	})
}

// Booking with a credit or entering a code to redeem is using a benefit, not
// taking the offer. Letting "booking" or "code" override every benefit word
// ended a November offer on its hotel credit's June booking deadline.
func TestEndDate_BookingWithABenefitIsUsingIt(t *testing.T) {
	for _, label := range []string{
		"Hotel Credit Booking Deadline", "Travel Credit Booking Deadline", "Deadline to Use Booking Credit",
		"Deadline to Redeem Code", "Discount Code Redemption Deadline", "Certificate Booking Deadline",
		"Voucher Code Deadline",
	} {
		kd := details(t, "Offer Ends", "November 5, 2026", label, "June 30, 2026")
		if got := EndDate(kd, day("2026-06-10")); got != "2026-11-05" {
			t.Errorf("EndDate(Offer Ends: November 5, 2026; %s: June 30, 2026) = %q, want 2026-11-05", label, got)
		}
	}
	// A code or a booking beside a use or a discount is still taking it.
	for _, label := range []string{"Promo Code Use Deadline", "Discount Code Deadline", "Discount Booking Deadline", "Booking Deadline"} {
		if got := EndDate(details(t, label, "June 30, 2026"), day("2026-06-10")); got != "2026-06-30" {
			t.Errorf("EndDate(%s: June 30, 2026) = %q, want 2026-06-30", label, got)
		}
	}
}

// A deadline value that tells how the deadline changed takes its latest
// date. There are too many ways to say "it used to be June 30" to catch
// each; taking the earliest read every one of these as ending June 30.
func TestEndDate_RevisedDeadlineTakesTheLatestDate(t *testing.T) {
	const written = "2026-06-10"
	var cases []endDateCase
	for _, v := range []string{
		"July 31, 2026 (originally through June 30)",
		"July 31, 2026 (originally scheduled to end June 30)",
		"July 31, 2026 (originally set to expire June 30)",
		"July 31, 2026 (initially set for June 30)",
		"July 31, 2026 (initially slated to end on June 30)",
		"July 31, 2026 (previously through June 30)",
		"July 31, 2026 (had been June 30)",
		"July 31, 2026 (used to be June 30)",
		"July 31, 2026 (June 30 previously)",
		"July 31, 2026 (the June 30 deadline was extended)",
		"July 31, 2026; June 30 was the old deadline",
		"June 30, 2026 (now ends July 31, 2026)",
		"June 30, 2026 (now runs through July 31, 2026)",
		"June 30, 2026 (now valid until July 31, 2026)",
		"June 30, 2026 - update: July 31, 2026",
		"June 30, 2026 (extended — July 31, 2026)",
		"June 30, 2026, extended by a month to July 31, 2026",
		"June 30, 2026, extended 30 days to July 31, 2026",
	} {
		cases = append(cases, endDateCase{v, "Deadline", v, written, "2026-07-31"})
	}
	cases = append(cases,
		// No revision: the earlier deadline is the one to meet.
		endDateCase{"two deadlines", "Deadline", "June 30, 2026 (registration) and July 31, 2026 (travel)", written, "2026-06-30"},
		endDateCase{"book now through", "Deadline", "Register by June 15, 2026; book now through June 30, 2026", written, "2026-06-15"},
		// An extension's own date already set the old one aside.
		endDateCase{"extension beside a later end", "Deadline", "Book by June 15, 2026 for travel through December 31, 2026 (extended to July 15, 2026)", written, "2026-07-15"},
	)
	runEndDateCases(t, cases)
}

// A replacement is any label that revises the end, not only a handful of
// exact ones. With no new date and word that the offer runs on, it leaves no
// end; and it never moves the end earlier than the deadline beside it.
func TestEndDate_MoreReplacementLabels(t *testing.T) {
	const written = "2026-06-10"
	for _, label := range []string{
		"Extended Offer Deadline", "New Application Deadline", "Deadline (Extended)", "Deadline Extended To",
		"Extended Promotion Period", "Extension", "Updated End Date", "Extended",
	} {
		kd := details(t, "Deadline", "June 30, 2026", label, "July 31, 2026")
		if got := EndDate(kd, day(written)); got != "2026-07-31" {
			t.Errorf("EndDate(Deadline: June 30, 2026; %s: July 31, 2026) = %q, want 2026-07-31", label, got)
		}
	}
	for _, c := range []struct{ label, value string }{
		{"Extended Deadline", "Until further notice"},
		{"New Deadline", "TBA"},
		{"Extension", "Extended indefinitely"},
		{"Deadline (Extended)", "New date not yet announced"},
		{"Extension", "Extended (the original June 30, 2026 deadline has expired)"},
	} {
		kd := details(t, "Deadline", "June 30, 2026", c.label, c.value)
		if got := EndDate(kd, day(written)); got != "" {
			t.Errorf("EndDate(Deadline: June 30, 2026; %s: %s) = %q, want no end date", c.label, c.value, got)
		}
	}
	// A replacement earlier than the deadline beside it does not cut it short.
	kd := details(t, "Deadline", "July 31, 2026", "Extended Deadline", "June 30, 2026")
	if got := EndDate(kd, day(written)); got != "2026-07-31" {
		t.Errorf("EndDate(Deadline: July 31; Extended Deadline: June 30) = %q, want 2026-07-31", got)
	}
	// Of two replacements, the later: "Extended Offer Deadline" was an
	// ordinary deadline before, and a newly recognised replacement beside
	// it must not end the offer sooner.
	kd = details(t, "Extended Promotion Period", "June 30, 2026", "Extended Offer Deadline", "July 15, 2026")
	if got := EndDate(kd, day(written)); got != "2026-07-15" {
		t.Errorf("EndDate with two replacements = %q, want the later, 2026-07-15", got)
	}
	// An extended period is a period: its last date is the end.
	kd = details(t, "Deadline", "June 30, 2026", "Extended Promotion Period", "June 1 - August 31, 2026")
	if got := EndDate(kd, day(written)); got != "2026-08-31" {
		t.Errorf("EndDate with Extended Promotion Period = %q, want 2026-08-31", got)
	}
	// Not replacements: a deadline for new cardmembers, a hotel's extended
	// stay, a travel window, or a dateless value with no word of running on.
	for _, c := range []struct{ label, value, want string }{
		{"New Cardmember Deadline", "June 30, 2026", "2026-06-30"},
		{"Extended Stay Booking Deadline", "June 30, 2026", "2026-06-30"},
		{"Extended Travel Period", "Through December 31, 2026", "2026-07-31"},
		{"Extended Deadline", "None", "2026-07-31"},
		{"Extended Deadline", "None (not extended)", "2026-07-31"},
	} {
		kd := details(t, "Deadline", "July 31, 2026", c.label, c.value)
		if got := EndDate(kd, day(written)); got != c.want {
			t.Errorf("EndDate(Deadline: July 31, 2026; %s: %s) = %q, want %s", c.label, c.value, got, c.want)
		}
	}
}

// A lone replaced date is not when the offer ended if the value says it was
// extended, or only that something "ended up" happening.
func TestEndDate_ExtendedIsNotEnded(t *testing.T) {
	const written = "2026-07-05"
	runEndDateCases(t, []endDateCase{
		{"original deadline has expired", "Deadline", "Extended (original June 30, 2026 deadline has expired)", written, ""},
		{"another offer ended", "Deadline", "Extended (was June 30, 2026); Amex's similar offer ended last week", written, ""},
		{"ended up extended", "Deadline", "Previously June 30, 2026 — the deal ended up being extended", written, ""},
		{"ended up running longer", "Deadline", "Previously June 30, 2026 — the deal ended up running longer", written, ""},
		// Still said to be over: the date is when it ended.
		{"ended", "Deadline", "Ended June 30, 2026", written, "2026-06-30"},
	})
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
