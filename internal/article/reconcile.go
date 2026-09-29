package article

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// A rewrite used to run only from the one-shot goroutine the Revise button
// started. If the AI call failed, or a deploy restarted the service mid-run,
// nothing ever tried again and the article sat in "Revising" indefinitely. Email
// replies and unpublish-and-revise never started a rewrite at all. The
// reconciler below is the backstop for all of them: every revision_requested
// article is picked up on the next pass, within a bounded number of retries.

var (
	// ErrRevisionInFlight means a rewrite of the article is already running in
	// this process, so this call did nothing.
	ErrRevisionInFlight = errors.New("revision already in progress")
	// ErrNotAwaitingRevision means the article is not (or no longer) in
	// revision_requested -- already revised, or approved/rejected meanwhile.
	ErrNotAwaitingRevision = errors.New("article is not awaiting revision")
	// ErrRevisionSuperseded means newer feedback arrived while the rewrite ran,
	// so its result was discarded; the article is still waiting.
	ErrRevisionSuperseded = errors.New("revision superseded by newer feedback")
	// errRevisionNotDue means the article failed recently and is in backoff.
	errRevisionNotDue = errors.New("revision not due")
	// errRevisionExhausted means the article is out of retries.
	errRevisionExhausted = errors.New("revision retries exhausted")
	// errRevisionStateCheck means the retry state could not be read, so no
	// rewrite was attempted. Not charged, and not counted as a failed rewrite.
	errRevisionStateCheck = errors.New("checking revision retry state")
)

// isRevisionSkip reports whether err means "nothing to do" rather than a failed
// rewrite. Skips are not counted against the retry budget.
func isRevisionSkip(err error) bool {
	return errors.Is(err, ErrRevisionInFlight) ||
		errors.Is(err, ErrNotAwaitingRevision) ||
		errors.Is(err, ErrRevisionSuperseded) ||
		errors.Is(err, errRevisionNotDue) ||
		errors.Is(err, errRevisionExhausted)
}

// Retry policy for failed rewrites.
const (
	// DefaultRevisionMaxFailures is how many failed rewrites an article gets
	// before the reconciler leaves it for the admin. With the backoff below
	// that spans roughly 75 minutes -- long enough to ride out a provider blip,
	// short enough that a broken article is flagged the same afternoon.
	DefaultRevisionMaxFailures = 5
	revisionBackoffBase        = 5 * time.Minute
	revisionBackoffMax         = time.Hour
)

// RevisionBackoff is how long to wait after the given number of consecutive
// failures: 5m, 10m, 20m, 40m, then capped at an hour.
func RevisionBackoff(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	d := revisionBackoffBase
	for i := 1; i < failures; i++ {
		d *= 2
		if d >= revisionBackoffMax {
			return revisionBackoffMax
		}
	}
	return d
}

func (m *Manager) maxRevisionFailures() int {
	if m.RevisionMaxFailures > 0 {
		return m.RevisionMaxFailures
	}
	return DefaultRevisionMaxFailures
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// claimRevision marks the article as being rewritten, returning false if it
// already is. The guard is per process: there is one server process, and the
// compare-and-set in db.ApplyRevision is the second line of defence across
// processes (e.g. old and new overlapping during a deploy).
func (m *Manager) claimRevision(articleID int64) bool {
	m.revMu.Lock()
	defer m.revMu.Unlock()
	if m.revInFlight == nil {
		m.revInFlight = make(map[int64]bool)
	}
	if m.revInFlight[articleID] {
		return false
	}
	m.revInFlight[articleID] = true
	return true
}

func (m *Manager) releaseRevision(articleID int64) {
	m.revMu.Lock()
	defer m.revMu.Unlock()
	delete(m.revInFlight, articleID)
}

// StartRevision rewrites the article in the background, so a request that just
// asked for a revision gets it straight away rather than on the next reconciler
// pass. It goes through ProcessRevision's in-flight guard, so a click that lands
// while a rewrite is already running does nothing. Failures are recorded for
// the reconciler to retry.
func (m *Manager) StartRevision(articleID int64) {
	m.revBG.Add(1)
	go func() {
		defer m.revBG.Done()
		// ProcessRevision already turns a panic in the rewrite into an error;
		// this covers the bookkeeping around it. An unrecovered panic in a
		// goroutine takes the whole server down.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("revision processing panicked", "article_id", articleID, "panic", r)
			}
		}()
		// ProcessRevision logs and records any failure itself.
		_ = m.ProcessRevision(articleID)
	}()
}

// WaitRevisions blocks until rewrites started by StartRevision finish. Called
// via the web app's WaitBackground: by tests before teardown, and by main
// (bounded) at shutdown.
func (m *Manager) WaitRevisions() {
	m.revBG.Wait()
}

// RevisionPassResult summarises one reconciler pass.
type RevisionPassResult struct {
	Processed int // rewrite stored
	Failed    int // rewrite attempted and failed
	Skipped   int // already in flight, or no longer waiting
	Backoff   int // failed recently, not due yet
	Exhausted int // out of retries, left for the admin
	Errors    int // could not check whether it was due; not attempted
}

// ProcessPendingRevisions rewrites every revision_requested article that is due.
// Safe to run on a schedule: overlapping passes are skipped, articles already
// being rewritten (e.g. by the Revise button) are skipped, and articles that
// keep failing back off and eventually stop being retried.
//
// Articles are processed one at a time, and ctx is checked between them so a
// shutdown does not start another rewrite. A rewrite already running is not
// interrupted (the AI client takes no context); if the process exits under it,
// the article is still revision_requested and the next process picks it up.
func (m *Manager) ProcessPendingRevisions(ctx context.Context) RevisionPassResult {
	var res RevisionPassResult
	if !m.revPassRunning.CompareAndSwap(false, true) {
		// robfig/cron's "@every" does not skip a tick while the previous run is
		// still going, and a pass can outlast the interval when the provider is
		// slow -- each rewrite may retry for minutes.
		metrics.RevisionReconcileTotal.WithLabelValues("pass_skipped").Inc()
		slog.Warn("revision pass skipped: previous pass still running")
		return res
	}
	defer m.revPassRunning.Store(false)
	defer m.refreshRevisionsWaiting()

	pending, err := m.db.ListPendingRevisions()
	if err != nil {
		slog.Error("revision pass: could not list pending revisions", "error", err)
		return res
	}
	if len(pending) == 0 {
		return res
	}

	maxFailures := m.maxRevisionFailures()
	for _, p := range pending {
		if ctx.Err() != nil {
			slog.Info("revision pass stopped by shutdown")
			break
		}
		if p.Failures >= maxFailures {
			res.Exhausted++
			continue
		}
		if !p.RetryAfter.IsZero() && m.clock().Before(p.RetryAfter) {
			res.Backoff++
			continue
		}

		err := m.processRevisionIfDue(ctx, p.ID)
		switch {
		// The first two: failed since the list was read (e.g. a kick from a
		// request), so now in backoff or out of retries.
		case errors.Is(err, errRevisionNotDue):
			res.Backoff++
		case errors.Is(err, errRevisionExhausted):
			res.Exhausted++
		case errors.Is(err, errRevisionStateCheck):
			res.Errors++
			metrics.RevisionReconcileTotal.WithLabelValues("error").Inc()
			slog.Error("revision pass: could not check retry state", "article_id", p.ID, "error", err)
		case err == nil:
			res.Processed++
			metrics.RevisionReconcileTotal.WithLabelValues("processed").Inc()
		case isRevisionSkip(err):
			res.Skipped++
			metrics.RevisionReconcileTotal.WithLabelValues("skipped").Inc()
		default:
			// ProcessRevision has already logged the failure and scheduled
			// the retry.
			res.Failed++
			metrics.RevisionReconcileTotal.WithLabelValues("failed").Inc()
		}
	}

	slog.Info("revision pass complete", "waiting", len(pending),
		"processed", res.Processed, "failed", res.Failed, "skipped", res.Skipped,
		"backoff", res.Backoff, "exhausted", res.Exhausted, "errors", res.Errors)
	if res.Exhausted > 0 {
		slog.Warn("articles stuck in revision after exhausting retries; re-request the revision, approve or reject them",
			"count", res.Exhausted, "max_failures", maxFailures)
	}
	return res
}

// refreshRevisionsWaiting republishes the revisions-waiting gauge after a pass.
func (m *Manager) refreshRevisionsWaiting() {
	pending, err := m.db.ListPendingRevisions()
	if err != nil {
		slog.Warn("revision pass: could not count pending revisions", "error", err)
		return
	}
	var due, backoff, exhausted int
	now := m.clock()
	for _, p := range pending {
		switch {
		case p.Failures >= m.maxRevisionFailures():
			exhausted++
		case !p.RetryAfter.IsZero() && now.Before(p.RetryAfter):
			backoff++
		default:
			due++
		}
	}
	metrics.RevisionsWaiting.WithLabelValues("due").Set(float64(due))
	metrics.RevisionsWaiting.WithLabelValues("backoff").Set(float64(backoff))
	metrics.RevisionsWaiting.WithLabelValues("exhausted").Set(float64(exhausted))
}

// RevisionGaveUp reports whether an article with this many failures is past its
// retry budget, i.e. the reconciler has stopped retrying it.
func (m *Manager) RevisionGaveUp(failures int) bool {
	return failures >= m.maxRevisionFailures()
}
