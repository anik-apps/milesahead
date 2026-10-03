package main

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/newsletter"
)

// The deploy config's newsletter keys give the schedule production had when
// it was fixed in code: built Friday 20:00 and sent Saturday 08:00, New York
// time. And config.example.yaml documents the same one.
func TestDeployAndExampleConfig_WeeklyScheduleUnchanged(t *testing.T) {
	stubReachable(t, false)
	example, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*config.Config{"deploy": deployConfig(t, nil), "example": example} {
		s, err := newsletter.ScheduleFor(cfg.Newsletter)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.SendCron() != "CRON_TZ=America/New_York 0 0 8 * * 6" || s.BuildCron() != "CRON_TZ=America/New_York 0 0 20 * * 5" {
			t.Errorf("%s: send %q, build %q", name, s.SendCron(), s.BuildCron())
		}
		if s.When() != "every Saturday at 8:00 AM ET" {
			t.Errorf("%s: When = %q", name, s.When())
		}
	}
}
