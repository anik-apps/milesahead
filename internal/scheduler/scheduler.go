package scheduler

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
)

// Scheduler wraps a cron scheduler and manages periodic background jobs.
type Scheduler struct {
	cron *cron.Cron
	// stopCh is closed by Stop so work scheduled outside cron -- currently the
	// delayed startup pass -- can abandon its wait instead of waking up after
	// the database has been closed.
	stopCh   chan struct{}
	stopOnce sync.Once
	// offCron tracks that same work, so Stop can join it rather than leaving a
	// goroutine running past shutdown.
	offCron sync.WaitGroup
	// startupDelay is how long after registration the first enrichment pass
	// runs. A field rather than a constant so tests can exercise the cancel and
	// join paths -- at the production value a test would have to wait 90s, and
	// a test that skips instead is coverage in name only.
	startupDelay time.Duration
	// revisionStartupDelay is the same for the first revision pass.
	revisionStartupDelay time.Duration
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
		stopCh:               make(chan struct{}),
		startupDelay:         startupEnrichmentDelay,
		revisionStartupDelay: startupRevisionDelay,
	}
}

// AddScanJob adds a periodic scan job at the given interval.
// Runs immediately on first call, then every interval.
//
// A scan that is still running when the next one is due makes that one skip.
// Two concurrent scans can both pass the duplicate-article check before
// either saves, and publish the same story twice. A scan can outlast its
// interval: with a slow critic each article takes minutes to verify, and the
// first scan after an outage works through two days of backlog.
func (s *Scheduler) AddScanJob(interval time.Duration, scanFunc func()) {
	scan := skipIfRunning("scan", scanFunc)
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), scan)
	// Run first scan immediately in background
	go scan()
}

// skipIfRunning wraps fn so a call made while an earlier one is still running
// returns at once instead of running alongside it. Unlike cron's
// SkipIfStillRunning it also covers calls made outside cron, such as the
// startup scan.
func skipIfRunning(name string, fn func()) func() {
	var running atomic.Bool
	return func() {
		if !running.CompareAndSwap(false, true) {
			slog.Warn("previous run still going; skipping this one", "job", name)
			return
		}
		defer running.Store(false)
		fn()
	}
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

// AddRevisionJob adds the periodic revision reconciler at the given interval,
// and runs one pass shortly after startup.
//
// The startup pass is what resumes a rewrite a deploy interrupted: the restart
// kills the goroutine mid-rewrite and the article is left revision_requested.
// Without it, "@every" would not look until a full interval after boot. It is
// delayed a little so it does not contend with the scan job cron fires on Start,
// and runs before the enrichment startup pass so the two do not share a tick.
func (s *Scheduler) AddRevisionJob(interval time.Duration, revisionFunc func()) {
	s.cron.AddFunc(fmt.Sprintf("@every %s", interval), revisionFunc)
	s.runAfterStartup("revision", s.revisionStartupDelay, revisionFunc)
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
	s.runAfterStartup("enrichment", s.startupDelay, enrichFunc)
}

// AddIntervalJob runs fn every interval, and once startupDelay after it is
// added. A run still going when the next is due is skipped, not stacked.
func (s *Scheduler) AddIntervalJob(name string, interval, startupDelay time.Duration, fn func()) {
	job := skipIfRunning(name, fn)
	if _, err := s.cron.AddFunc(fmt.Sprintf("@every %s", interval), job); err != nil {
		slog.Error("failed to add interval job", "name", name, "error", err)
		return
	}
	s.runAfterStartup(name, startupDelay, job)
}

// AddStartupJob runs fn once, startupDelay after registration, and never again.
// Like every other job, Stop cancels it if it has not begun and joins it if it
// has, so it does not run on against a database main is about to close.
func (s *Scheduler) AddStartupJob(name string, startupDelay time.Duration, fn func()) {
	s.runAfterStartup(name, startupDelay, fn)
}

// runAfterStartup runs fn once, delay after registration, unless Stop comes
// first. Stop joins it if it has already begun.
func (s *Scheduler) runAfterStartup(name string, delay time.Duration, fn func()) {
	// cron.WithChain only wraps jobs cron itself runs. This goroutine calls
	// fn directly, so it needs the same Recover wrapper applied explicitly --
	// otherwise a panic in the very first pass after boot still kills the
	// process, which is precisely what the chain was added to stop.
	startupJob := cron.Recover(cron.DefaultLogger)(cron.FuncJob(fn))

	s.offCron.Add(1)
	go func() {
		defer s.offCron.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			startupJob.Run()
		case <-s.stopCh:
			// Shutdown began before the pass was due. Waking up here would run
			// a full pass against a database main is about to close.
			slog.Info("startup pass cancelled by shutdown", "job", name)
		}
	}()
}

// SetStartupDelay overrides the delay before the first enrichment and revision
// passes. Intended for tests; a non-positive value is ignored.
func (s *Scheduler) SetStartupDelay(d time.Duration) {
	if d > 0 {
		s.startupDelay = d
		s.revisionStartupDelay = d
	}
}

// startupEnrichmentDelay is how long after boot the first reconciliation runs.
const startupEnrichmentDelay = 90 * time.Second

// startupRevisionDelay is how long after boot the first revision pass runs.
const startupRevisionDelay = 45 * time.Second

// Start begins running all scheduled jobs.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop stops the scheduler, waiting for running jobs to complete.
func (s *Scheduler) Stop() {
	// Signal non-cron work first, so it can abandon its wait while cron drains.
	s.stopOnce.Do(func() { close(s.stopCh) })

	ctx := s.cron.Stop()
	<-ctx.Done() // Wait for running jobs to finish
	s.offCron.Wait()
}
