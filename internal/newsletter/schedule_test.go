package newsletter

import (
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/robfig/cron/v3"
)

func parseCron(t *testing.T, line string) cron.Schedule {
	t.Helper()
	sched, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(line)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	return sched
}

// The schedule production has always had: before send_day, send_time and
// timezone drove it, these lines and words were constants.
func TestDefaultScheduleIsTheOldFixedOne(t *testing.T) {
	for name, s := range map[string]Schedule{
		"defaults": DefaultSchedule(),
		// What the deploy config sets.
		"production": mustSchedule(t, config.NewsletterConfig{SendTime: "08:00", Timezone: "America/New_York"}),
	} {
		if got := s.SendCron(); got != "CRON_TZ=America/New_York 0 0 8 * * 6" {
			t.Errorf("%s: SendCron = %q", name, got)
		}
		if got := s.BuildCron(); got != "CRON_TZ=America/New_York 0 0 20 * * 5" {
			t.Errorf("%s: BuildCron = %q", name, got)
		}
		if got := s.When(); got != "every Saturday at 8:00 AM ET" {
			t.Errorf("%s: When = %q", name, got)
		}
		if got := s.Label(); got != "8:00 AM ET" {
			t.Errorf("%s: Label = %q", name, got)
		}
		if s.DayName() != "Saturday" || s.BuildDay() != time.Friday {
			t.Errorf("%s: day %s, build day %s", name, s.DayName(), s.BuildDay())
		}
	}
}

func mustSchedule(t *testing.T, cfg config.NewsletterConfig) Schedule {
	t.Helper()
	s, err := ScheduleFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// What readers and the admin are told matches when the cron lines really
// fire, for the default and for configured schedules.
func TestScheduleWordingMatchesTheCron(t *testing.T) {
	cases := []struct {
		cfg              config.NewsletterConfig
		when             string
		buildDay         time.Weekday
		sendHour, sendMn int
	}{
		{config.NewsletterConfig{}, "every Saturday at 8:00 AM ET", time.Friday, 8, 0},
		{config.NewsletterConfig{SendDay: "Sun", SendTime: "18:30", Timezone: "America/Los_Angeles"}, "every Sunday at 6:30 PM PT", time.Saturday, 18, 30},
		{config.NewsletterConfig{SendDay: "TUESDAY", SendTime: "7:05", Timezone: "Europe/London"}, "every Tuesday at 7:05 AM London time", time.Monday, 7, 5},
		{config.NewsletterConfig{SendDay: "sunday", SendTime: "00:00", Timezone: "UTC"}, "every Sunday at 12:00 AM UTC", time.Saturday, 0, 0},
	}
	for _, c := range cases {
		s := mustSchedule(t, c.cfg)
		if s.When() != c.when {
			t.Errorf("%+v: When = %q, want %q", c.cfg, s.When(), c.when)
		}
		start := time.Date(2026, 10, 1, 12, 0, 0, 0, s.Location())
		send := parseCron(t, s.SendCron()).Next(start).In(s.Location())
		if send.Weekday() != s.Day || send.Hour() != c.sendHour || send.Minute() != c.sendMn {
			t.Errorf("%+v: send fires %s", c.cfg, send)
		}
		build := parseCron(t, s.BuildCron()).Next(start).In(s.Location())
		if build.Weekday() != c.buildDay || build.Hour() != 20 {
			t.Errorf("%+v: build fires %s", c.cfg, build)
		}
		// Built the evening before the send it is for.
		want := 4*time.Hour + time.Duration(60*c.sendHour+c.sendMn)*time.Minute
		if next := parseCron(t, s.SendCron()).Next(build); next.Sub(build) != want {
			t.Errorf("%+v: build %s is not the evening before the send %s", c.cfg, build, next)
		}
	}
}

func TestScheduleForRejectsWhatItCannotRead(t *testing.T) {
	for _, cfg := range []config.NewsletterConfig{
		{SendDay: "someday"},
		{SendTime: "8am"},
		{SendTime: "24:00"},
		{Timezone: "Mars/Olympus"},
	} {
		if _, err := ScheduleFor(cfg); err == nil {
			t.Errorf("%+v: no error", cfg)
		}
	}
}

// The service dates issues on the configured send day: BuildWeekly the next
// one, SendWeekly the current one.
func TestServiceDatesIssuesOnTheSendDay(t *testing.T) {
	svc := NewService(nil, &mockSender{}, config.NewsletterConfig{SendDay: "wednesday", Timezone: "UTC"})
	// Tuesday 2026-10-06, 20:00 UTC.
	svc.now = func() time.Time { return time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC) }
	if got := svc.nextSendDate(); got != "2026-10-07" {
		t.Errorf("nextSendDate on the Tuesday = %s, want the Wednesday", got)
	}
	if got := svc.thisSendDate(); got != "2026-09-30" {
		t.Errorf("thisSendDate on the Tuesday = %s, want the Wednesday before", got)
	}
	svc.now = func() time.Time { return time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC) }
	if svc.nextSendDate() != "2026-10-07" || svc.thisSendDate() != "2026-10-07" {
		t.Errorf("on the Wednesday itself: next %s, this %s", svc.nextSendDate(), svc.thisSendDate())
	}
}
