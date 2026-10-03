package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The states of an admin's request for a new image (article_image_requests).
// A request that succeeded has no row: the new image is the article's.
const (
	ImageRequestPending = "pending"
	ImageRequestFailed  = "failed"
)

// MaxImageHintRunes caps the admin's search words for a new image. An image
// search query longer than this finds nothing anyway.
const MaxImageHintRunes = 100

// ImageRequest is an admin's request for a new image for an article.
type ImageRequest struct {
	ArticleID int64
	State     string // ImageRequestPending or ImageRequestFailed
	Hint      string // the admin's search words; "" for none
	Seq       int64
	// RequestedAt is when it was asked for; FinishedAt when it failed, or
	// the zero time while it is pending.
	RequestedAt, FinishedAt time.Time
	// LastError says why it failed, for the admin.
	LastError string
}

// Pending reports whether the request is still being worked on.
func (r ImageRequest) Pending() bool { return r.State == ImageRequestPending }

// Failed reports whether the search gave up without a new image.
func (r ImageRequest) Failed() bool { return r.State == ImageRequestFailed }

// ErrNoImageRequest means an article cannot have a new image: it does not
// exist, or it was rejected (the reconciler never touches those).
var ErrNoImageRequest = errors.New("article cannot have a new image")

// CleanImageHint tidies the admin's search words: one line, single spaces,
// at most MaxImageHintRunes.
func CleanImageHint(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxImageHintRunes {
		s = strings.TrimSpace(string([]rune(s)[:MaxImageHintRunes]))
	}
	return s
}

// RequestNewImage asks the enrichment reconciler for a new image for article
// id, searched for with hint ("" lets it choose its own words).
//
// The article's current image is recorded as turned down, so it is not
// picked again, but it stays on the article until a replacement is found:
// readers of a published article never see it without one. The request gets
// a fresh enrichment budget, so an article whose attempts were used up long
// ago is searched again, and it is put first in the next pass.
//
// Asking again while a request is pending replaces it (new words, a new
// budget); asking after one failed starts over.
func (d *Database) RequestNewImage(id int64, hint string) (ImageRequest, error) {
	hint = CleanImageHint(hint)
	tx, err := d.db.Begin()
	if err != nil {
		return ImageRequest{}, err
	}
	defer tx.Rollback()

	var status, imageURL, thumb string
	err = tx.QueryRow(`SELECT status, COALESCE(image_url, ''), COALESCE(image_thumbnail, '')
		FROM generated_articles WHERE id = ?`, id).Scan(&status, &imageURL, &thumb)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status == StatusRejected) {
		return ImageRequest{}, ErrNoImageRequest
	}
	if err != nil {
		return ImageRequest{}, fmt.Errorf("reading article %d: %w", id, err)
	}

	now := time.Now().Unix()
	for _, u := range []string{imageURL, thumb} {
		if u == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO article_rejected_images (article_id, image_url, rejected_at)
			VALUES (?, ?, ?)`, id, u, now); err != nil {
			return ImageRequest{}, fmt.Errorf("recording the turned-down image: %w", err)
		}
	}
	if _, err := tx.Exec(`
		INSERT INTO article_image_requests (article_id, state, hint, seq, requested_at, finished_at, last_error)
		VALUES (?, ?, ?, 1, ?, 0, '')
		ON CONFLICT(article_id) DO UPDATE SET
			state = excluded.state, hint = excluded.hint, seq = seq + 1,
			requested_at = excluded.requested_at, finished_at = 0, last_error = ''`,
		id, ImageRequestPending, hint, now); err != nil {
		return ImageRequest{}, fmt.Errorf("recording the image request: %w", err)
	}
	// A fresh budget. updated_at is left alone, as by enrichment: the text
	// has not changed.
	if _, err := tx.Exec(`UPDATE generated_articles SET enrichment_attempts = 0 WHERE id = ?`, id); err != nil {
		return ImageRequest{}, fmt.Errorf("resetting the enrichment budget: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ImageRequest{}, err
	}
	reqs, err := d.ImageRequestsFor([]int64{id})
	if err != nil {
		return ImageRequest{}, err
	}
	return reqs[id], nil
}

// ImageRequestsFor returns the image requests of the given articles, pending
// or failed, keyed by article id, in one query.
func (d *Database) ImageRequestsFor(ids []int64) (map[int64]ImageRequest, error) {
	out := map[int64]ImageRequest{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(`SELECT article_id, state, hint, seq, requested_at, finished_at, last_error
		FROM article_image_requests WHERE article_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing image requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ImageRequest
		var requested, finished int64
		if err := rows.Scan(&r.ArticleID, &r.State, &r.Hint, &r.Seq, &requested, &finished, &r.LastError); err != nil {
			return nil, err
		}
		r.RequestedAt = time.Unix(requested, 0)
		if finished > 0 {
			r.FinishedAt = time.Unix(finished, 0)
		}
		out[r.ArticleID] = r
	}
	return out, rows.Err()
}

// RejectedImageURLs returns the images an admin has turned down for article
// id, display images and thumbnails alike.
func (d *Database) RejectedImageURLs(id int64) ([]string, error) {
	rows, err := d.db.Query(`SELECT image_url FROM article_rejected_images WHERE article_id = ? ORDER BY rejected_at, image_url`, id)
	if err != nil {
		return nil, fmt.Errorf("listing turned-down images: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// FailImageRequest marks request seq for article id as failed, with reason
// for the admin, and leaves the article's image as it is. A request made
// since (another seq) is left pending.
func (d *Database) FailImageRequest(id, seq int64, reason string) error {
	_, err := d.db.Exec(`UPDATE article_image_requests SET state = ?, finished_at = ?, last_error = ?
		WHERE article_id = ? AND seq = ? AND state = ?`,
		ImageRequestFailed, time.Now().Unix(), reason, id, seq, ImageRequestPending)
	if err != nil {
		return fmt.Errorf("failing image request for article %d: %w", id, err)
	}
	return nil
}
