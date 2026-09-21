package scheduler

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler wraps a cron scheduler and manages periodic background jobs.
type Scheduler struct {
	cron *cron.Cron
}

// New creates a new Scheduler.
//
// Recover is explicit because cron does NOT install it by default, despite its
// own doc comment claiming "a chain that recovers panics" -- cron.New sets
// chain: NewChain() with zero wrappers (cron.go:116, chain.go:20). Without it a
// panic in any scheduled job unwinds to the goroutine top and kills the whole
// process, taking the website down with it. Restart=on-failure then brings it
// back, so a panic that reproduces on a particular article becomes a crash loop.
func New() *Scheduler {
	return &Scheduler{
		cron: cron.New(
			cron.WithSeconds(),
			cron.WithChain(cron.Recover(cron.DefaultLogger)),
		),
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

// AddWeeklyJob adds a job with a full cron expression (6-field with seconds).
func (s *Scheduler) AddWeeklyJob(name, cronExpr string, fn func()) {
	_, err := s.cron.AddFunc(cronExpr, fn)
	if err != nil {
		slog.Error("failed to add weekly job", "name", name, "error", err)
	}
}

// AddRevisionJob adds a periodic revision processing job at the given interval.
func (s *Scheduler) AddRevisionJob(interval time.Duration, revisionFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), revisionFunc)
}

// AddEnrichmentJob adds the periodic pass that fills in article fields
// generation left empty, and runs one pass shortly after startup.
//
// The startup pass matters because "@every 15m" fires first 15 minutes AFTER
// start, not immediately -- and every deploy restarts the service, so each
// deploy pushed the first reconciliation another 15 minutes out. Articles
// created in that window sit visibly incomplete, and a busy deploy day could
// postpone reconciliation indefinitely.
//
// It is delayed rather than immediate so it does not contend with the scan job,
// which robfig/cron fires straight away on Start.
func (s *Scheduler) AddEnrichmentJob(interval time.Duration, enrichFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), enrichFunc)
	go func() {
		time.Sleep(startupEnrichmentDelay)
		enrichFunc()
	}()
}

// startupEnrichmentDelay is how long after boot the first reconciliation runs.
const startupEnrichmentDelay = 90 * time.Second

// Start begins running all scheduled jobs.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops the scheduler, waiting for running jobs to complete.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done() // Wait for running jobs to finish
}
