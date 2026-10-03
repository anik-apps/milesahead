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

// A scan still running when the next is due must not run alongside it: two
// concurrent scans can publish the same story twice. The startup scan blocks
// here past two cron ticks, so any second call is an overlap.
func TestAddScanJob_SkipsWhileAScanIsStillRunning(t *testing.T) {
	s := New()
	var calls, concurrent, maxConcurrent int32
	release := make(chan struct{})
	s.AddScanJob(1*time.Second, func() {
		atomic.AddInt32(&calls, 1)
		n := atomic.AddInt32(&concurrent, 1)
		defer atomic.AddInt32(&concurrent, -1)
		for {
			m := atomic.LoadInt32(&maxConcurrent)
			if n <= m || atomic.CompareAndSwapInt32(&maxConcurrent, m, n) {
				break
			}
		}
		<-release
	})
	s.Start()
	time.Sleep(2500 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("scan ran %d times while the first was still running; want 1", got)
	}
	close(release)
	s.Stop()
	if got := atomic.LoadInt32(&maxConcurrent); got != 1 {
		t.Errorf("max concurrent scans = %d; want 1", got)
	}
}

// Once a scan finishes, the next tick runs normally.
func TestSkipIfRunning_RunsAgainAfterTheFirstFinishes(t *testing.T) {
	var calls int32
	f := skipIfRunning("test", func() { atomic.AddInt32(&calls, 1) })
	f()
	f()
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls = %d; want 2 (sequential calls must both run)", got)
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

// TestStop_CancelsPendingStartupPass verifies shutdown during the startup delay
// abandons the pass instead of waking into a closed database.
//
// cron.WithChain only wraps jobs cron runs; the startup pass calls its function
// directly, so it needs both the Recover wrapper and a cancellation path of its
// own. Without the latter, Stop returned while this goroutine was still
// sleeping, and it then ran a full enrichment pass against a database main was
// about to close.
func TestStop_CancelsPendingStartupPass(t *testing.T) {
	s := New()
	s.SetStartupDelay(10 * time.Second) // long enough that Stop lands first
	var ran atomic.Bool
	s.AddEnrichmentJob(time.Hour, func() { ran.Store(true) })
	s.Start()

	s.Stop() // shutdown begins well before the pass is due

	if ran.Load() {
		t.Error("startup pass ran despite shutdown beginning before it was due")
	}
}

// TestStop_JoinsStartupPass verifies Stop waits rather than racing: once the
// pass has begun, shutdown must not return until it finishes, or it would run
// on against a database main is about to close.
func TestStop_JoinsStartupPass(t *testing.T) {
	s := New()
	s.SetStartupDelay(time.Millisecond)

	started := make(chan struct{})
	var finished atomic.Bool
	s.AddEnrichmentJob(time.Hour, func() {
		close(started)
		time.Sleep(200 * time.Millisecond)
		finished.Store(true)
	})
	s.Start()

	<-started // the pass is now running
	s.Stop()

	if !finished.Load() {
		t.Error("Stop returned while the startup pass was still running")
	}
}

// TestStartupPass_PanicDoesNotKillProcess covers a gap that cron.WithChain
// leaves open.
//
// cron.Recover only wraps jobs cron itself runs. The startup pass calls its
// function directly from a goroutine, so without an explicit wrapper a panic
// there unwinds to the goroutine top and terminates the whole process — taking
// the website down and, with Restart=on-failure, turning a reproducible panic
// into a crash loop.
//
// A panic in a goroutine cannot be recovered by the caller, so if the wrapper
// were missing this test binary would die outright rather than fail.
func TestStartupPass_PanicDoesNotKillProcess(t *testing.T) {
	s := New()
	s.SetStartupDelay(time.Millisecond)

	panicked := make(chan struct{})
	s.AddEnrichmentJob(time.Hour, func() {
		close(panicked)
		panic("enrichment pass exploded")
	})
	s.Start()

	select {
	case <-panicked:
	case <-time.After(2 * time.Second):
		t.Fatal("startup pass never ran")
	}

	// Stop must still join cleanly; the panicking goroutine has to have
	// completed rather than left the WaitGroup permanently held.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung after a panicking startup pass")
	}
}

// TestAddRevisionJob_RunsSoonAfterStartup guards the revision startup pass.
// Deploys restart the service, killing any rewrite in progress and leaving its
// article in revision_requested; this pass is what resumes it, rather than a
// full interval after boot.
func TestAddRevisionJob_RunsSoonAfterStartup(t *testing.T) {
	if startupRevisionDelay <= 0 || startupRevisionDelay > 5*time.Minute {
		t.Fatalf("revision startup delay %v should be short but not immediate", startupRevisionDelay)
	}

	s := New()
	s.SetStartupDelay(time.Millisecond)
	ran := make(chan struct{}, 1)
	s.AddRevisionJob(time.Hour, func() { ran <- struct{}{} }) // cron tick far away
	s.Start()
	defer s.Stop()

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("revision job did not run after startup")
	}
}

// TestStop_CancelsPendingRevisionStartupPass: shutdown before the pass is due
// must abandon it, not wake it against a closed database.
func TestStop_CancelsPendingRevisionStartupPass(t *testing.T) {
	s := New()
	s.SetStartupDelay(10 * time.Second)
	var ran atomic.Bool
	s.AddRevisionJob(time.Hour, func() { ran.Store(true) })
	s.Start()
	s.Stop()

	if ran.Load() {
		t.Error("revision startup pass ran despite shutdown beginning first")
	}
}
