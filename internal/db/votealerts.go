package db

import (
	"database/sql"
	"errors"
	"time"
)

// VoteAlert is an article whose recent votes crossed the alert threshold,
// waiting on the admin Published page until acknowledged.
type VoteAlert struct {
	ID        int64
	ArticleID int64
	// Upvotes and Downvotes are the counts in the alert window when the
	// alert was last raised or updated.
	Upvotes, Downvotes int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// The article, for the admin's list.
	Title, Slug, Status string
}

// RecordVoteAlert opens a vote alert for an article, or, when one is already
// open, updates its counts. opened reports whether a new alert was opened:
// only then is anyone notified, so an article does not alert again on every
// further downvote.
func (d *Database) RecordVoteAlert(articleID int64, up, down int) (opened bool, err error) {
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO vote_alerts (article_id, upvotes, downvotes) VALUES (?, ?, ?)`,
		articleID, up, down)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	_, err = d.db.Exec(
		`UPDATE vote_alerts SET upvotes = ?, downvotes = ?, updated_at = CURRENT_TIMESTAMP
		WHERE article_id = ? AND acknowledged_at IS NULL`, up, down, articleID)
	return false, err
}

// ListOpenVoteAlerts returns the alerts not yet acknowledged, newest first.
func (d *Database) ListOpenVoteAlerts() ([]VoteAlert, error) {
	rows, err := d.db.Query(`
		SELECT v.id, v.article_id, v.upvotes, v.downvotes, v.created_at, v.updated_at,
		       COALESCE(a.title, ''), COALESCE(a.slug, ''), COALESCE(a.status, '')
		FROM vote_alerts v LEFT JOIN generated_articles a ON a.id = v.article_id
		WHERE v.acknowledged_at IS NULL
		ORDER BY v.updated_at DESC, v.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var alerts []VoteAlert
	for rows.Next() {
		var a VoteAlert
		if err := rows.Scan(&a.ID, &a.ArticleID, &a.Upvotes, &a.Downvotes, &a.CreatedAt, &a.UpdatedAt,
			&a.Title, &a.Slug, &a.Status); err != nil {
			return nil, err
		}
		alerts = append(alerts, a)
	}
	return alerts, rows.Err()
}

// CountOpenVoteAlerts counts the alerts not yet acknowledged.
func (d *Database) CountOpenVoteAlerts() (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM vote_alerts WHERE acknowledged_at IS NULL`).Scan(&n)
	return n, err
}

// AcknowledgeVoteAlert marks one open alert as seen at the given time. It
// reports whether there was an open alert with that id.
func (d *Database) AcknowledgeVoteAlert(id int64, at time.Time) (bool, error) {
	res, err := d.db.Exec(`UPDATE vote_alerts SET acknowledged_at = ? WHERE id = ? AND acknowledged_at IS NULL`,
		at.UTC().Format(sqliteTime), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AcknowledgeAllVoteAlerts marks every open alert as seen at the given time
// and returns how many there were.
func (d *Database) AcknowledgeAllVoteAlerts(at time.Time) (int, error) {
	res, err := d.db.Exec(`UPDATE vote_alerts SET acknowledged_at = ? WHERE acknowledged_at IS NULL`,
		at.UTC().Format(sqliteTime))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// LastVoteAlertAcknowledged is when an alert on the article was last
// acknowledged; zero if none ever was. Votes before it have been seen, so
// the next alert counts only votes after it.
func (d *Database) LastVoteAlertAcknowledged(articleID int64) (time.Time, error) {
	var s sql.NullString
	err := d.db.QueryRow(`SELECT MAX(acknowledged_at) FROM vote_alerts WHERE article_id = ?`, articleID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || !s.Valid || s.String == "" {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(sqliteTime, s.String)
}
