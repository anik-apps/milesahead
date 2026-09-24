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
		// A date that passed up to 60 days before the article is the one
		// just gone; an older one is read as next year's.
		{"March 1", "2026-04-30", "2026-03-01"},
		{"March 1", "2026-05-01", "2027-03-01"},
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

// "Today" is the site's date, not UTC's: at 11pm in New York on the last day
// it is already the next day in UTC, but the offer is still live.
func TestToday_UsesSiteTimezone(t *testing.T) {
	ny := Location("America/New_York")
	now := time.Date(2026, 7, 30, 3, 0, 0, 0, time.UTC) // 11pm Jul 29 in New York
	if got := Today(now, ny); got != "2026-07-29" {
		t.Errorf("Today in New York = %q, want 2026-07-29", got)
	}
	if Ended("2026-07-29", Today(now, ny)) {
		t.Error("an offer ending Jul 29 must still be live at 11pm Jul 29 New York time")
	}
	if got := Today(now, time.UTC); got != "2026-07-30" {
		t.Errorf("Today in UTC = %q, want 2026-07-30", got)
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
