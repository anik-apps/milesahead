package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/kumaranik/milesahead/internal/editornote"
)

// maxRevisionErrorLen bounds the stored error. It is shown on the dashboard, and
// a provider error can carry a whole response body.
const maxRevisionErrorLen = 500

// PendingRevision is an article waiting for its AI rewrite, with the retry
// bookkeeping the revision reconciler needs to decide whether it is due.
type PendingRevision struct {
	ID         int64
	Title      string
	Failures   int
	RetryAfter time.Time // zero means due now
	LastError  string
}

// ListPendingRevisions returns every article in revision_requested, oldest
// request first, including ones in backoff or past their retry budget -- the
// caller decides what to do with those, and needs them to report them.
func (d *Database) ListPendingRevisions() ([]PendingRevision, error) {
	rows, err := d.db.Query(
		`SELECT id, title, revision_failures, revision_retry_after, revision_last_error
		 FROM generated_articles WHERE status = ? ORDER BY updated_at ASC, id ASC`,
		StatusRevisionRequested,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PendingRevision
	for rows.Next() {
		var p PendingRevision
		var retryAfter int64
		if err := rows.Scan(&p.ID, &p.Title, &p.Failures, &retryAfter, &p.LastError); err != nil {
			return nil, err
		}
		if retryAfter > 0 {
			p.RetryAfter = time.Unix(retryAfter, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordRevisionFailure counts a failed rewrite and schedules the next attempt.
// nextAttempt receives the new failure count and returns when the article is
// next due.
//
// It only touches articles still in revision_requested: a failure after the new
// content was stored (say, writing the verification row) is not a failed
// rewrite, and must not paint a "revision failed" badge on a draft. Returns the
// new failure count, or 0 if the article had left revision_requested.
func (d *Database) RecordRevisionFailure(id int64, errMsg string, nextAttempt func(failures int) time.Time) (int, error) {
	if len(errMsg) > maxRevisionErrorLen {
		errMsg = errMsg[:maxRevisionErrorLen]
	}

	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var failures int
	err = tx.QueryRow(
		`UPDATE generated_articles
		 SET revision_failures = revision_failures + 1, revision_last_error = ?
		 WHERE id = ? AND status = ?
		 RETURNING revision_failures`,
		errMsg, id, StatusRevisionRequested,
	).Scan(&failures)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("counting revision failure: %w", err)
	}

	if _, err := tx.Exec(
		`UPDATE generated_articles SET revision_retry_after = ? WHERE id = ?`,
		nextAttempt(failures).Unix(), id,
	); err != nil {
		return 0, fmt.Errorf("scheduling revision retry: %w", err)
	}
	return failures, tx.Commit()
}

// ResetRevisionFailures gives an article a fresh retry budget. Called when new
// feedback arrives: the admin asking again is the signal to try again.
func (d *Database) ResetRevisionFailures(id int64) error {
	_, err := d.db.Exec(
		`UPDATE generated_articles
		 SET revision_failures = 0, revision_retry_after = 0, revision_last_error = ''
		 WHERE id = ?`, id,
	)
	return err
}

// AppliedRevision is a finished rewrite ready to be stored.
type AppliedRevision struct {
	ArticleID int64
	// BaseRevision is the revision_number the rewrite started from.
	BaseRevision int
	// FeedbackID is the feedback the rewrite answered, which must still be the
	// latest for the article.
	FeedbackID      int64
	Title           string
	PreviousContent string
	NewContent      string
	// EditorNotes are the rewrite's remarks for the admin. Notes still inside
	// NewContent are split out and added to them; together they replace the
	// article's notes, which described the content being replaced.
	EditorNotes string
	// Status is where the article goes: StatusPending if the rewrite passed
	// the critic, otherwise StatusDraft. Empty means StatusDraft.
	Status string
	// Verification, if set, is the critic's result for the new revision; its
	// GeneratedArticleID and RevisionNumber are filled in here.
	Verification *VerificationResult
}

// ApplyRevision stores a rewrite and moves the article out of
// revision_requested (to draft, or pending if it passed the critic),
// atomically, and only if nothing changed underneath it.
//
// The UPDATE is a compare-and-set on three things, each closing a way a rewrite
// could land on an article it no longer belongs to:
//   - status is still revision_requested: another run already applied a
//     rewrite, or an admin approved/rejected meanwhile. Previously each step
//     was a separate read-then-write, so two concurrent runs could both pass
//     the status check and both write revision N+1 with two history rows.
//   - revision_number is unchanged: the rewrite was based on this content.
//   - the answered feedback is still the newest: if the admin sent more notes
//     mid-rewrite, storing this result would move the article to draft and
//     silently drop them. Instead it is discarded and the article stays
//     revision_requested for the newer feedback.
//
// Content, final status, the history row, the verification row and the retry
// bookkeeping commit together. So a crash cannot leave an article in draft with
// its old content, and there is no window after the store in which a separate
// draft -> pending step could overwrite (and drop) feedback that just arrived.
// Returns false, with no error, when the compare-and-set did not match.
func (d *Database) ApplyRevision(r AppliedRevision) (bool, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	status := r.Status
	if status == "" {
		status = StatusDraft
	}
	if status != StatusDraft && status != StatusPending {
		return false, fmt.Errorf("revision cannot move an article to %q", status)
	}

	// Neither the article nor its history stores editor notes in content.
	newContent, notes := editornote.Split(r.NewContent)
	notes = editornote.Join(r.EditorNotes, notes)
	previousContent := editornote.Strip(r.PreviousContent)

	newRevision := r.BaseRevision + 1
	res, err := tx.Exec(
		`UPDATE generated_articles
		 SET status = ?, title = ?, content = ?, editor_notes = ?, revision_number = ?,
		     revision_failures = 0, revision_retry_after = 0, revision_last_error = '',
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = ? AND revision_number = ?
		   AND COALESCE((SELECT MAX(id) FROM admin_feedback WHERE generated_article_id = ?), 0) = ?`,
		status, r.Title, newContent, notes, newRevision,
		r.ArticleID, StatusRevisionRequested, r.BaseRevision,
		r.ArticleID, r.FeedbackID,
	)
	if err != nil {
		return false, fmt.Errorf("storing revision: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}

	if _, err := tx.Exec(
		`INSERT INTO article_revisions (generated_article_id, revision_number, previous_content, new_content, feedback_id) VALUES (?, ?, ?, ?, ?)`,
		r.ArticleID, newRevision, previousContent, newContent, r.FeedbackID,
	); err != nil {
		return false, fmt.Errorf("creating revision record: %w", err)
	}

	if v := r.Verification; v != nil {
		if _, err := tx.Exec(
			`INSERT INTO verification_results (generated_article_id, revision_number, score, issues_json, factual_check, attribution_check, completeness_check, hallucination_check) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ArticleID, newRevision, v.Score, v.IssuesJSON, v.FactualCheck, v.AttributionCheck, v.CompletenessCheck, v.HallucinationCheck,
		); err != nil {
			return false, fmt.Errorf("storing verification: %w", err)
		}
	}
	return true, tx.Commit()
}

// GetRevisionRetryState returns an article's current revision bookkeeping, for
// checking it is still due just before a rewrite starts. ok is false if the
// article is no longer in revision_requested.
func (d *Database) GetRevisionRetryState(id int64) (p PendingRevision, ok bool, err error) {
	var retryAfter int64
	err = d.db.QueryRow(
		`SELECT id, title, revision_failures, revision_retry_after, revision_last_error
		 FROM generated_articles WHERE id = ? AND status = ?`,
		id, StatusRevisionRequested,
	).Scan(&p.ID, &p.Title, &p.Failures, &retryAfter, &p.LastError)
	if err == sql.ErrNoRows {
		return PendingRevision{}, false, nil
	}
	if err != nil {
		return PendingRevision{}, false, err
	}
	if retryAfter > 0 {
		p.RetryAfter = time.Unix(retryAfter, 0)
	}
	return p, true, nil
}
