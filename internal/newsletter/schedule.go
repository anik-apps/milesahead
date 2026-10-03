package newsletter

import (
	"fmt"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
)

// Schedule is when the weekly issue is built and sent, from
// newsletter.send_day, send_time and timezone. The scheduler's cron lines
// and what readers and the admin are told ("every Saturday at 8:00 AM ET")
// both come from it, so they cannot disagree.
type Schedule struct {
	Day          time.Weekday
	Hour, Minute int
	// Zone is the IANA name the day and time are read in.
	Zone string
	loc  *time.Location
}

// buildHour is the hour of the day before the send that the issue is
// built: the evening before, so the admin can look it over.
const buildHour = 20

// ScheduleFor reads the schedule from cfg, with config's defaults for
// anything unset. config.Load has already checked the values, so an error
// here means a config that did not come through Load.
func ScheduleFor(cfg config.NewsletterConfig) (Schedule, error) {
	day, at, zone := cfg.SendDay, cfg.SendTime, strings.TrimSpace(cfg.Timezone)
	if strings.TrimSpace(day) == "" {
		day = config.DefaultSendDay
	}
	if strings.TrimSpace(at) == "" {
		at = config.DefaultSendTime
	}
	if zone == "" {
		zone = config.DefaultTimezone
	}
	wd, err := config.ParseWeekday(day)
	if err != nil {
		return Schedule{}, fmt.Errorf("newsletter.send_day: %w", err)
	}
	h, m, err := config.ParseClock(at)
	if err != nil {
		return Schedule{}, fmt.Errorf("newsletter.send_time: %w", err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Schedule{}, fmt.Errorf("newsletter.timezone: %w", err)
	}
	return Schedule{Day: wd, Hour: h, Minute: m, Zone: zone, loc: loc}, nil
}

// DefaultSchedule is the schedule with nothing configured: Saturday at
// 08:00 New York time, which is also what production sets.
func DefaultSchedule() Schedule {
	s, err := ScheduleFor(config.NewsletterConfig{})
	if err != nil {
		// The defaults are constants and the zone database is built in.
		panic(err)
	}
	return s
}

// Location is the zone the schedule is read in.
func (s Schedule) Location() *time.Location {
	if s.loc == nil {
		return time.UTC
	}
	return s.loc
}

// SendCron is the scheduler's line for the send (seconds first, as
// scheduler.AddWeeklyJob parses it).
func (s Schedule) SendCron() string {
	return fmt.Sprintf("CRON_TZ=%s 0 %d %d * * %d", s.Zone, s.Minute, s.Hour, int(s.Day))
}

// BuildDay is the day the issue is built: the day before the send.
func (s Schedule) BuildDay() time.Weekday {
	return (s.Day + 6) % 7
}

// BuildCron is the scheduler's line for the build, at 20:00 the day
// before the send.
func (s Schedule) BuildCron() string {
	return fmt.Sprintf("CRON_TZ=%s 0 0 %d * * %d", s.Zone, buildHour, int(s.BuildDay()))
}

// DayName is the send day for readers: "Saturday".
func (s Schedule) DayName() string { return s.Day.String() }

// Label is the send time for readers, with its zone: "8:00 AM ET".
func (s Schedule) Label() string {
	t := time.Date(2000, 1, 1, s.Hour, s.Minute, 0, 0, time.UTC)
	return t.Format("3:04 PM") + " " + zoneLabel(s.Zone)
}

// When is how often the newsletter comes, for the sign-up page and the
// confirmation email: "every Saturday at 8:00 AM ET".
func (s Schedule) When() string {
	return "every " + s.DayName() + " at " + s.Label()
}

// usZones are the zones readers know by a short name. Any other zone is
// named after its city ("London time").
var usZones = map[string]string{
	"America/New_York": "ET", "US/Eastern": "ET", "America/Detroit": "ET",
	"America/Chicago": "CT", "US/Central": "CT",
	"America/Denver": "MT", "US/Mountain": "MT", "America/Phoenix": "MT",
	"America/Los_Angeles": "PT", "US/Pacific": "PT",
	"America/Anchorage": "AKT", "Pacific/Honolulu": "HT",
	"UTC": "UTC", "Etc/UTC": "UTC",
}

func zoneLabel(zone string) string {
	if l, ok := usZones[zone]; ok {
		return l
	}
	city := zone[strings.LastIndex(zone, "/")+1:]
	return strings.ReplaceAll(city, "_", " ") + " time"
}
