package newsletter

import (
	"testing"
	"time"

	"github.com/robfig/cron/v3"
)

// What readers and the admin are told matches when the weekly really goes
// out: the scheduler's cron line, parsed as the scheduler parses it
// (cron.WithSeconds).
func TestWeeklySendWordingMatchesTheSchedule(t *testing.T) {
	sched, err := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor).Parse(WeeklySendSchedule)
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	next := sched.Next(time.Date(2026, 10, 1, 12, 0, 0, 0, ny)).In(ny)
	if got := next.Weekday().String(); got != WeeklySendDay {
		t.Errorf("the weekly goes out on %s; WeeklySendDay says %s", got, WeeklySendDay)
	}
	if got := next.Format("3:04 PM") + " ET"; got != WeeklySendLabel {
		t.Errorf("the weekly goes out at %s; WeeklySendLabel says %s", got, WeeklySendLabel)
	}
	if WeeklySendWhen != "every Saturday at 8:00 AM ET" {
		t.Errorf("WeeklySendWhen = %q", WeeklySendWhen)
	}
}
