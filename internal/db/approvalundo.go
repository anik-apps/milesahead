package db

import (
	"database/sql"
	"fmt"
	"time"
)

// ApprovalUndo is the recorded state behind an approval's Undo.
type ApprovalUndo struct {
	ArticleID int64
	// Revision is the revision that was approved; undo refuses if the
	// article has moved on from it.
	Revision int
	// PrevStatus and PrevPublishedAt are what the article had before the
	// approval, restored by the undo.
	PrevStatus      string
	PrevPublishedAt *time.Time
	ExpiresAt       time.Time
}

// undoableStatuses are the statuses an approval can be undone back to: the
// ones an article can be approved from.
var undoableStatuses = map[string]bool{
	StatusPending: true, StatusDraft: true, StatusUnpublished: true, StatusRevisionRequested: true,
}

// RecordApproval stores what an approval changed, replacing any earlier
// record for the article, so the dashboard can undo it until expires.
func (d *Database) RecordApproval(u ApprovalUndo) error {
	if !undoableStatuses[u.PrevStatus] {
		return fmt.Errorf("an approval from %q cannot be undone", u.PrevStatus)
	}
	var prevPub interface{}
	if u.PrevPublishedAt != nil {
		prevPub = u.PrevPublishedAt.UTC().Format(sqliteTime)
	}
	_, err := d.db.Exec(
		`INSERT INTO approval_undo (article_id, revision, prev_status, prev_published_at, expires_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(article_id) DO UPDATE SET revision = excluded.revision, prev_status = excluded.prev_status,
		   prev_published_at = excluded.prev_published_at, expires_at = excluded.expires_at`,
		u.ArticleID, u.Revision, u.PrevStatus, prevPub, u.ExpiresAt.UTC().Format(sqliteTime))
	return err
}

// PendingUndo returns the article's recorded approval if it can still be
// undone at now (not expired, article still approved at that revision), or
// nil.
func (d *Database) PendingUndo(articleID int64, now time.Time) (*ApprovalUndo, error) {
	u := &ApprovalUndo{ArticleID: articleID}
	var prevPub sql.NullString
	var expires string
	err := d.db.QueryRow(
		`SELECT u.revision, u.prev_status, u.prev_published_at, u.expires_at
		 FROM approval_undo u JOIN generated_articles g ON g.id = u.article_id
		 WHERE u.article_id = ? AND g.status = ? AND g.revision_number = u.revision AND u.expires_at >= ?`,
		articleID, StatusApproved, now.UTC().Format(sqliteTime),
	).Scan(&u.Revision, &u.PrevStatus, &prevPub, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if u.ExpiresAt, err = time.Parse(sqliteTime, expires); err != nil {
		return nil, err
	}
	if prevPub.Valid {
		t, err := parseDBTime(prevPub.String)
		if err != nil {
			return nil, err
		}
		u.PrevPublishedAt = &t
	}
	return u, nil
}

// UndoApproval undoes the article's recorded approval: it puts back the
// status and publish date the article had before, only while the record has
// not expired and the article is still approved at the recorded revision.
// The record is used up either way. It returns the restored status, or
// ErrUndoExpired.
func (d *Database) UndoApproval(articleID int64, now time.Time) (string, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var rev int
	var prev string
	var prevPub sql.NullString
	err = tx.QueryRow(
		`SELECT revision, prev_status, prev_published_at FROM approval_undo WHERE article_id = ? AND expires_at >= ?`,
		articleID, now.UTC().Format(sqliteTime),
	).Scan(&rev, &prev, &prevPub)
	if err == sql.ErrNoRows {
		return "", ErrUndoExpired
	}
	if err != nil {
		return "", err
	}
	if !undoableStatuses[prev] {
		return "", ErrUndoExpired
	}
	var pub interface{}
	if prevPub.Valid {
		pub = prevPub.String
	}
	res, err := tx.Exec(
		`UPDATE generated_articles SET status = ?, published_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND status = ? AND revision_number = ?`,
		prev, pub, articleID, StatusApproved, rev)
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`DELETE FROM approval_undo WHERE article_id = ?`, articleID); err != nil {
		return "", err
	}
	if n == 0 {
		// Changed since the approval: drop the stale record, undo nothing.
		if err := tx.Commit(); err != nil {
			return "", err
		}
		return "", ErrUndoExpired
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return prev, nil
}
