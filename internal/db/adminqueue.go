package db

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// QueueStatuses are the statuses the admin's review queue shows.
var QueueStatuses = []string{StatusPending, StatusDraft, StatusUnpublished, StatusRevisionRequested}

// QueueFingerprint is a short hash of the review queue's state: which
// articles are in it, in what status and revision, and their rewrite
// failures. The dashboard polls it to tell the admin when the queue changed
// (a rewrite landed, a new article arrived) without reloading the page.
func (d *Database) QueueFingerprint() (string, error) {
	rows, err := d.db.Query(
		`SELECT id, status, revision_number, revision_failures, updated_at
		 FROM generated_articles WHERE status IN (?, ?, ?, ?) ORDER BY id`,
		QueueStatuses[0], QueueStatuses[1], QueueStatuses[2], QueueStatuses[3])
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var id int64
		var status string
		var rev, failures int
		var updated interface{}
		if err := rows.Scan(&id, &status, &rev, &failures, &updated); err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d|%s|%d|%d|%v\n", id, status, rev, failures, updated)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}
