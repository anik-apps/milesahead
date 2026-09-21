package scheduler

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	s := New()
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.cron == nil {
		t.Fatal("cron field is nil")
	}
}

func TestStartStop(t *testing.T) {
	s := New()
	s.Start()
	s.Stop()
	// Should not panic.
}

func TestAddScanJob(t *testing.T) {
	s := New()
	var called int32
	s.AddScanJob(1*time.Second, func() {
		atomic.AddInt32(&called, 1)
	})
	s.Start()
	time.Sleep(1500 * time.Millisecond)
	s.Stop()
	if atomic.LoadInt32(&called) < 1 {
		t.Error("scan job was not called")
	}
}

func TestAddMailPollJob(t *testing.T) {
	s := New()
	var called int32
	s.AddMailPollJob(1*time.Second, func() {
		atomic.AddInt32(&called, 1)
	})
	s.Start()
	time.Sleep(1500 * time.Millisecond)
	s.Stop()
	if atomic.LoadInt32(&called) < 1 {
		t.Error("mail poll job was not called")
	}
}

func TestAddRevisionJob(t *testing.T) {
	s := New()
	var called int32
	s.AddRevisionJob(1*time.Second, func() {
		atomic.AddInt32(&called, 1)
	})
	s.Start()
	time.Sleep(1500 * time.Millisecond)
	s.Stop()
	if atomic.LoadInt32(&called) < 1 {
		t.Error("revision job was not called")
	}
}

func TestAddWeeklyJob(t *testing.T) {
	s := New()
	// Valid 6-field cron expression — should register without error.
	s.AddWeeklyJob("newsletter-build", "CRON_TZ=America/New_York 0 0 20 * * 5", func() {})
	s.Start()
	s.Stop()
}

func TestAddWeeklyJob_InvalidExpr(t *testing.T) {
	s := New()
	// Invalid cron expression — should log error but not panic.
	s.AddWeeklyJob("bad-job", "not a cron expression", func() {})
	s.Start()
	s.Stop()
}

func TestMultipleJobs(t *testing.T) {
	s := New()
	var scanCalled, pollCalled int32
	s.AddScanJob(1*time.Second, func() {
		atomic.AddInt32(&scanCalled, 1)
	})
	s.AddMailPollJob(1*time.Second, func() {
		atomic.AddInt32(&pollCalled, 1)
	})
	s.Start()
	time.Sleep(1500 * time.Millisecond)
	s.Stop()
	if atomic.LoadInt32(&scanCalled) < 1 {
		t.Error("scan job was not called")
	}
	if atomic.LoadInt32(&pollCalled) < 1 {
		t.Error("poll job was not called")
	}
}

// TestAddEnrichmentJob_RunsSoonAfterStartup guards the startup pass.
//
// robfig/cron's "@every d" schedules the FIRST run one interval after start,
// not immediately. Since every deploy restarts the service, without a startup
// pass each deploy pushed the first reconciliation another full interval out,
// and articles created in that window stayed visibly incomplete.
func TestAddEnrichmentJob_RunsSoonAfterStartup(t *testing.T) {
	if startupEnrichmentDelay > 5*time.Minute {
		t.Fatalf("startup delay %v is too long to be useful after a deploy",
			startupEnrichmentDelay)
	}
	if startupEnrichmentDelay <= 0 {
		t.Fatal("startup pass must be delayed, not immediate, to avoid " +
			"contending with the scan job cron fires on Start")
	}
}
