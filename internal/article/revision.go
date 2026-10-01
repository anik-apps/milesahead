package article

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// ProcessRevision rewrites an article in revision_requested status: it loads
// the latest feedback and the source articles, re-generates via the AI pipeline
// with that feedback, and stores the new content with an incremented revision
// number.
//
// Every path that revises goes through here -- the Revise button, email
// replies, unpublish-and-revise, and the reconciler -- so the guards live here:
//   - at most one rewrite per article runs at a time in this process; a second
//     call returns ErrRevisionInFlight without touching anything;
//   - the result is stored with a compare-and-set (db.ApplyRevision), so a
//     rewrite that raced with another writer is discarded rather than stored
//     twice or on top of newer feedback;
//   - a failure is counted against the article's retry budget, with backoff,
//     so the reconciler does not retry a hopeless article forever.
//
// ProcessRevision ignores the retry backoff: it is what an explicit request
// calls. The reconciler goes through processRevisionIfDue instead.
func (m *Manager) ProcessRevision(articleID int64) error {
	return m.processGuarded(context.Background(), articleID, false)
}

// processRevisionIfDue is ProcessRevision for the reconciler: after claiming the
// article it re-reads the retry state, because the pass works from a list read
// before earlier rewrites in the same pass ran -- minutes ago, possibly. A kick
// that failed in the meantime would otherwise be retried with no backoff.
func (m *Manager) processRevisionIfDue(ctx context.Context, articleID int64) error {
	return m.processGuarded(ctx, articleID, true)
}

// processGuarded runs a rewrite under the in-flight guard. ctx stops it from
// starting a rerun during shutdown; a rewrite already running is not
// interrupted (the AI client takes no context).
func (m *Manager) processGuarded(ctx context.Context, articleID int64, respectBackoff bool) error {
	if !m.claimRevision(articleID) {
		slog.Info("revision already in progress, not starting another", "article_id", articleID)
		return ErrRevisionInFlight
	}
	defer m.releaseRevision(articleID)

	if respectBackoff {
		p, ok, err := m.db.GetRevisionRetryState(articleID)
		switch {
		case err != nil:
			// Not a failed rewrite, so not charged against the budget.
			return fmt.Errorf("%w: %w", errRevisionStateCheck, err)
		case !ok:
			return fmt.Errorf("%w: left revision_requested before its turn", ErrNotAwaitingRevision)
		case p.Failures >= m.maxRevisionFailures():
			return errRevisionExhausted
		case !p.RetryAfter.IsZero() && m.clock().Before(p.RetryAfter):
			return errRevisionNotDue
		}
	}

	var err error
	var lastSuperseded int64 // feedback id the previous discarded attempt answered
	for attempt := 1; ; attempt++ {
		err = m.runRevision(articleID)

		var sup *supersededError
		if errors.As(err, &sup) {
			if sup.answered == lastSuperseded {
				// Discarded twice answering the same feedback, so it was not
				// newer feedback that made the store miss -- the store
				// condition itself does not match what the rewrite reads.
				// Charge it as a failure: left as a skip, every kick and pass
				// would spend AI calls on this article, uncounted, forever.
				err = fmt.Errorf("rewrite discarded twice answering feedback %d with no newer feedback; "+
					"its result cannot be stored", sup.answered)
				break
			}
			lastSuperseded = sup.answered
		} else {
			lastSuperseded = 0
		}

		if attempt >= maxSupersededRetries || ctx.Err() != nil {
			break
		}
		if sup != nil {
			// Newer feedback arrived while the rewrite ran, so its result was
			// discarded. Go again straight away rather than leaving the newer
			// notes for the next reconciler pass: the Revise click that sent
			// them was turned away as in-flight, so nothing else is about to
			// pick them up.
			slog.Info("newer feedback arrived during the rewrite, revising again",
				"article_id", articleID, "attempt", attempt+1)
			continue
		}
		if err == nil && m.revisionRequestedAgain(articleID) {
			// Same, for feedback that landed just after the rewrite was stored
			// but while this call still held the in-flight guard.
			slog.Info("feedback arrived as the rewrite was stored, revising again",
				"article_id", articleID, "attempt", attempt+1)
			continue
		}
		break
	}

	if err == nil || isRevisionSkip(err) {
		return err
	}

	metrics.RevisionsTotal.WithLabelValues("failed").Inc()
	failures, recErr := m.db.RecordRevisionFailure(articleID, err.Error(), func(n int) time.Time {
		return m.clock().Add(RevisionBackoff(n))
	})
	switch {
	case recErr != nil:
		slog.Error("revision failed and the failure could not be recorded",
			"article_id", articleID, "error", err, "record_error", recErr)
	case failures >= m.maxRevisionFailures():
		// ERROR, and worded for the admin: from here the reconciler leaves the
		// article alone, and only a new revision request (which resets the
		// budget) or an approve/reject moves it on.
		slog.Error("revision failed too many times, giving up until the admin acts",
			"article_id", articleID, "failures", failures, "error", err)
	case failures > 0:
		// Failures up to the budget are expected; the reconciler retries.
		slog.Warn("revision failed, will retry",
			"article_id", articleID, "failures", failures,
			"max_failures", m.maxRevisionFailures(),
			"retry_in", RevisionBackoff(failures), "error", err)
	default:
		// The article left revision_requested while the rewrite ran, so there
		// is nothing to retry.
		slog.Error("revision failed after the article left revision_requested",
			"article_id", articleID, "error", err)
	}
	return err
}

// maxSupersededRetries bounds how many times ProcessRevision re-runs because
// feedback kept arriving mid-rewrite. Past it the reconciler picks it up.
const maxSupersededRetries = 3

// runRevision is one rewrite attempt. A panic becomes an error so it is counted
// against the retry budget like any other failure: otherwise an article that
// reliably panics would be retried on every reconciler pass, forever.
func (m *Manager) runRevision(articleID int64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("revision panicked: %v", r)
		}
	}()
	return m.processRevision(articleID)
}

func (m *Manager) processRevision(articleID int64) error {
	slog.Info("revision processing", "article_id", articleID)

	// Load the article.
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	if article.Status != db.StatusRevisionRequested {
		return fmt.Errorf("%w: status is %q, expected %q",
			ErrNotAwaitingRevision, article.Status, db.StatusRevisionRequested)
	}

	// Get the latest feedback.
	feedbackList, err := m.db.ListFeedbackForArticle(articleID)
	if err != nil {
		return fmt.Errorf("listing feedback: %w", err)
	}
	if len(feedbackList) == 0 {
		return fmt.Errorf("no feedback found for article %d", articleID)
	}
	// Newest by id, the same definition ApplyRevision's compare-and-set uses.
	// Not by created_at: if the clock stepped back between two notes, the
	// newest by time is the older note, the store would never match, and the
	// article would be rewritten again and again without being charged.
	latestFeedback := feedbackList[0]
	for _, f := range feedbackList[1:] {
		if f.ID > latestFeedback.ID {
			latestFeedback = f
		}
	}

	// Get latest verification issues.
	var criticIssues string
	verification, err := m.db.GetLatestVerification(articleID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("getting verification: %w", err)
	}
	if verification != nil {
		criticIssues = verification.IssuesJSON
	}

	// Load source raw articles via article_sources linkage.
	// We need to query the raw article IDs linked to this generated article.
	sources, err := m.loadSourceArticles(articleID)
	if err != nil {
		return fmt.Errorf("loading sources: %w", err)
	}

	// Call pipeline with feedback.
	if m.pipeline == nil {
		return fmt.Errorf("pipeline not configured")
	}
	result, err := m.pipeline.ProcessWithFeedback(sources, article.Content, latestFeedback.FeedbackText, criticIssues)
	if err != nil {
		return fmt.Errorf("processing revision: %w", err)
	}

	issuesJSON := "[]"
	if result.Verification != nil && len(result.Verification.Issues) > 0 {
		b, err := json.Marshal(result.Verification.Issues)
		if err != nil {
			return fmt.Errorf("marshaling issues: %w", err)
		}
		issuesJSON = string(b)
	}

	// Pending if the rewrite passes the critic, otherwise back to draft.
	newStatus := db.StatusDraft
	if result.PassedThreshold {
		newStatus = db.StatusPending
	}

	// Store the new content, final status, history row and verification in one
	// compare-and-set, so a concurrent writer cannot double-apply and there is
	// no gap after the store for newly arrived feedback to be overwritten in.
	apply := m.db.ApplyRevision
	if m.applyRevision != nil {
		apply = m.applyRevision
	}
	applied, err := apply(db.AppliedRevision{
		ArticleID:       articleID,
		BaseRevision:    article.RevisionNumber,
		FeedbackID:      latestFeedback.ID,
		Title:           result.Title,
		PreviousContent: article.Content,
		NewContent:      result.Content,
		EditorNotes:     result.EditorNotes,
		Status:          newStatus,
		Verification: &db.VerificationResult{
			Score:              result.Verification.Score,
			IssuesJSON:         issuesJSON,
			FactualCheck:       boolToString(result.Verification.FactualCheck),
			AttributionCheck:   boolToString(result.Verification.AttributionCheck),
			CompletenessCheck:  boolToString(result.Verification.CompletenessCheck),
			HallucinationCheck: boolToString(result.Verification.HallucinationCheck),
		},
	})
	if err != nil {
		return err
	}
	if !applied {
		return m.discardedRevisionError(articleID, latestFeedback.ID)
	}

	if result.PassedThreshold {
		metrics.RevisionsTotal.WithLabelValues("improved").Inc()
	} else {
		metrics.RevisionsTotal.WithLabelValues("unchanged").Inc()
	}

	slog.Info("revision completed", "article_id", articleID, "revision", article.RevisionNumber+1,
		"new_score", result.Verification.Score, "status", newStatus)

	return nil
}

// revisionRequestedAgain reports whether the article is back in
// revision_requested, i.e. new feedback arrived right after a rewrite stored.
func (m *Manager) revisionRequestedAgain(articleID int64) bool {
	a, err := m.db.GetGeneratedArticle(articleID)
	return err == nil && a.Status == db.StatusRevisionRequested
}

// discardedRevisionError explains why ApplyRevision's compare-and-set missed.
func (m *Manager) discardedRevisionError(articleID, answeredFeedback int64) error {
	current, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("revision discarded, and re-reading the article failed: %w", err)
	}
	if current.Status != db.StatusRevisionRequested {
		slog.Info("revision discarded: article changed while the rewrite ran",
			"article_id", articleID, "status", current.Status)
		return fmt.Errorf("%w: status changed to %q during the rewrite",
			ErrNotAwaitingRevision, current.Status)
	}
	return &supersededError{answered: answeredFeedback}
}

// supersededError is ErrRevisionSuperseded carrying the feedback the discarded
// rewrite answered, so a rerun can tell newer feedback from a store that will
// never match.
type supersededError struct{ answered int64 }

func (e *supersededError) Error() string {
	return fmt.Sprintf("%v (answered feedback %d)", ErrRevisionSuperseded, e.answered)
}

func (e *supersededError) Is(target error) bool { return target == ErrRevisionSuperseded }

// loadSourceArticles retrieves source articles linked to a generated article
// and converts them to ai.SourceArticle format.
func (m *Manager) loadSourceArticles(articleID int64) ([]ai.SourceArticle, error) {
	// Query article_sources to get raw article IDs.
	rawIDs, err := m.getLinkedRawArticleIDs(articleID)
	if err != nil {
		return nil, err
	}

	if len(rawIDs) == 0 {
		return nil, nil
	}

	rawArticles, err := m.db.GetRawArticlesByIDs(rawIDs)
	if err != nil {
		return nil, fmt.Errorf("getting raw articles: %w", err)
	}
	// Present the sources in the order the scan wrote the article from: by
	// source (ListActiveSources order), then by raw article. The IN query
	// returns them by raw id, so the order changed between generation and
	// revision, and a "Source 2" the old prompts let into the article named a
	// different outlet to the reviser than to the generator. The prompts no
	// longer number sources, but the pipeline still maps any "Source N" the
	// model writes by position, and that mapping should agree with the
	// article's own history.
	sort.SliceStable(rawArticles, func(i, j int) bool {
		if rawArticles[i].SourceID != rawArticles[j].SourceID {
			return rawArticles[i].SourceID < rawArticles[j].SourceID
		}
		return rawArticles[i].ID < rawArticles[j].ID
	})

	sources := make([]ai.SourceArticle, len(rawArticles))
	for i, ra := range rawArticles {
		name := "Unknown"
		if src, _ := m.db.GetSource(ra.SourceID); src != nil {
			name = src.Name
		}
		sources[i] = ai.SourceArticle{
			Name:    name,
			Title:   ra.Title,
			URL:     ra.URL,
			Content: ra.Content,
		}
	}
	return sources, nil
}

// getLinkedRawArticleIDs queries the article_sources table for raw article IDs
// linked to the given generated article.
func (m *Manager) getLinkedRawArticleIDs(articleID int64) ([]int64, error) {
	rows, err := m.db.DB().Query(
		`SELECT raw_article_id FROM article_sources WHERE generated_article_id = ?`, articleID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
