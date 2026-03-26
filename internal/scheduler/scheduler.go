package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler wraps a cron scheduler and manages periodic background jobs.
type Scheduler struct {
	cron *cron.Cron
}

// New creates a new Scheduler.
func New() *Scheduler {
	return &Scheduler{
		cron: cron.New(cron.WithSeconds()),
	}
}

// AddScanJob adds a periodic scan job at the given interval.
// Runs immediately on first call, then every interval.
func (s *Scheduler) AddScanJob(interval time.Duration, scanFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), scanFunc)
	// Run first scan immediately in background
	go scanFunc()
}

// AddMailPollJob adds a periodic mail polling job at the given interval.
func (s *Scheduler) AddMailPollJob(interval time.Duration, pollFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), pollFunc)
}

// AddNewsletterJob adds a daily newsletter job at the configured time and timezone.
// sendTime should be in "HH:MM" format, timezone should be a valid IANA timezone name.
func (s *Scheduler) AddNewsletterJob(sendTime, timezone string, sendFunc func()) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}

	var hour, min int
	fmt.Sscanf(sendTime, "%d:%d", &hour, &min)

	spec := fmt.Sprintf("CRON_TZ=%s %d %d * * *", loc.String(), min, hour)
	s.cron.AddFunc(spec, sendFunc)
}

// AddRevisionJob adds a periodic revision processing job at the given interval.
func (s *Scheduler) AddRevisionJob(interval time.Duration, revisionFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), revisionFunc)
}

// Start begins running all scheduled jobs.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops the scheduler, waiting for running jobs to complete.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}
