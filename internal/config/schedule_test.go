package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadYAML(t *testing.T, text string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// Unset, the schedule is the one production has always had.
func TestLoad_NewsletterScheduleDefaults(t *testing.T) {
	cfg, err := loadYAML(t, "server:\n  port: 8080\n")
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Newsletter
	if n.SendDay != "saturday" || n.SendTime != "08:00" || n.Timezone != "America/New_York" {
		t.Errorf("defaults: send_day %q, send_time %q, timezone %q", n.SendDay, n.SendTime, n.Timezone)
	}
}

// A schedule value that cannot be read fails the load, naming the key, so
// -check-config stops the deploy.
func TestLoad_NewsletterScheduleRejectsBadValues(t *testing.T) {
	for _, c := range []struct{ yaml, key string }{
		{`send_day: "someday"`, "newsletter.send_day"},
		{`send_time: "8am"`, "newsletter.send_time"},
		{`send_time: "25:00"`, "newsletter.send_time"},
		{`send_time: "08:60"`, "newsletter.send_time"},
		{`send_time: "+8:00"`, "newsletter.send_time"},
		{`timezone: "America/New_Yrok"`, "newsletter.timezone"},
	} {
		_, err := loadYAML(t, "newsletter:\n  "+c.yaml+"\n")
		if err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s: err = %v, want one naming %s", c.yaml, err, c.key)
		}
	}
}

func TestParseWeekday(t *testing.T) {
	for in, want := range map[string]time.Weekday{
		"saturday": time.Saturday, "Saturday": time.Saturday, "SAT": time.Saturday,
		" sun ": time.Sunday, "wed": time.Wednesday, "Thursday": time.Thursday,
	} {
		if got, err := ParseWeekday(in); err != nil || got != want {
			t.Errorf("ParseWeekday(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "sa", "satur", "7", "weekend"} {
		if _, err := ParseWeekday(in); err == nil {
			t.Errorf("ParseWeekday(%q): no error", in)
		}
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string][2]int{
		"08:00": {8, 0}, "8:00": {8, 0}, "00:00": {0, 0}, "23:59": {23, 59}, "18:30": {18, 30},
	} {
		h, m, err := ParseClock(in)
		if err != nil || h != want[0] || m != want[1] {
			t.Errorf("ParseClock(%q) = %d:%d, %v", in, h, m, err)
		}
	}
	for _, in := range []string{"", "8", "8:0", "24:00", "12:60", "8:00 AM", "-1:00", "08:00:00", "ab:cd"} {
		if _, _, err := ParseClock(in); err == nil {
			t.Errorf("ParseClock(%q): no error", in)
		}
	}
}
