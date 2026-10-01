package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
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

// LatestVerifications returns each article's latest critic result (highest
// revision), keyed by article id, in one query.
func (d *Database) LatestVerifications(ids []int64) (map[int64]VerificationResult, error) {
	out := map[int64]VerificationResult{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, revision_number, score, issues_json, factual_check, attribution_check, completeness_check, hallucination_check, verified_at
		 FROM verification_results WHERE generated_article_id IN (`+in+`) ORDER BY revision_number ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v VerificationResult
		if err := rows.Scan(&v.ID, &v.GeneratedArticleID, &v.RevisionNumber, &v.Score, &v.IssuesJSON, &v.FactualCheck, &v.AttributionCheck, &v.CompletenessCheck, &v.HallucinationCheck, &v.VerifiedAt); err != nil {
			return nil, err
		}
		out[v.GeneratedArticleID] = v // ascending, so the last one wins
	}
	return out, rows.Err()
}

// LinkedSource is one source post an article was written from, with the
// source's name.
type LinkedSource struct {
	SourceName, Title, URL string
}

// LinkedSourcesForArticles returns the source posts of each article, keyed by
// article id, in one query.
func (d *Database) LinkedSourcesForArticles(ids []int64) (map[int64][]LinkedSource, error) {
	out := map[int64][]LinkedSource{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(
		`SELECT a.generated_article_id, COALESCE(s.name, 'Unknown'), r.title, r.url
		 FROM article_sources a
		 JOIN raw_articles r ON r.id = a.raw_article_id
		 LEFT JOIN sources s ON s.id = r.source_id
		 WHERE a.generated_article_id IN (`+in+`) ORDER BY a.generated_article_id, r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ls LinkedSource
		if err := rows.Scan(&id, &ls.SourceName, &ls.Title, &ls.URL); err != nil {
			return nil, err
		}
		out[id] = append(out[id], ls)
	}
	return out, rows.Err()
}

// CardWorkCount is the number of pending card updates plus flagged card
// fields across all cards: the Cards tab's count.
func (d *Database) CardWorkCount() (int, error) {
	var n int
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(
		   CASE WHEN json_valid(pending_updates_json) THEN json_array_length(pending_updates_json) ELSE 0 END +
		   CASE WHEN json_valid(flagged_fields_json) THEN json_array_length(flagged_fields_json) ELSE 0 END), 0)
		 FROM reference_pages`).Scan(&n)
	return n, err
}

// StaleSourceCount counts active sources that have stored no article since
// cutoff (or never): the Sources tab's count. See SourceHealth.Stale.
func (d *Database) StaleSourceCount(cutoff time.Time) (int, error) {
	var n int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM sources s
		 WHERE s.active = 1 AND COALESCE(
		   (SELECT MAX(r.created_at) FROM raw_articles r WHERE r.source_id = s.id), '') < ?`,
		cutoff.UTC().Format(sqliteTime)).Scan(&n)
	return n, err
}

// UpcomingNewsletter returns the earliest unsent issue dated today or later
// ("2006-01-02"), or nil. An unsent issue dated in the past will never go
// out (SendWeekly only sends its own Saturday's), so it is not "upcoming".
func (d *Database) UpcomingNewsletter(today string) (*Newsletter, error) {
	n := &Newsletter{}
	err := d.db.QueryRow(
		`SELECT id, date, status FROM newsletters WHERE status != 'sent' AND date >= ? ORDER BY date ASC LIMIT 1`, today,
	).Scan(&n.ID, &n.Date, &n.Status)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return n, nil
}
