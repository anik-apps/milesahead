package db

import "fmt"

// StoredArticleImage is an article's image as stored: the display URL (hero,
// featured card, og:image) and the homepage thumbnail.
type StoredArticleImage struct {
	ID        int64
	URL       string
	Thumbnail string
}

// ListStoredArticleImages returns every non-rejected article that has an
// image, for the pass that swaps oversized files for sized ones. Deciding which
// URLs can be resized is left to the caller, which knows the hosts' schemes.
func (d *Database) ListStoredArticleImages() ([]StoredArticleImage, error) {
	rows, err := d.db.Query(`
		SELECT id, COALESCE(image_url, ''), COALESCE(image_thumbnail, '')
		FROM generated_articles
		WHERE status != ?
		  AND (COALESCE(image_url, '') != '' OR COALESCE(image_thumbnail, '') != '')
		ORDER BY id`, StatusRejected)
	if err != nil {
		return nil, fmt.Errorf("listing article images: %w", err)
	}
	defer rows.Close()

	var out []StoredArticleImage
	for rows.Next() {
		var a StoredArticleImage
		if err := rows.Scan(&a.ID, &a.URL, &a.Thumbnail); err != nil {
			return nil, fmt.Errorf("scanning article image: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReplaceArticleImage points an article at different URLs for the same
// picture. It only writes if the row still holds what the caller read, so an
// admin edit or an enrichment pick that landed in between is never overwritten;
// the returned bool reports whether it did. The attribution is untouched,
// since the picture is the same one, and so is updated_at, which tracks
// content edits.
func (d *Database) ReplaceArticleImage(old StoredArticleImage, url, thumbnail string) (bool, error) {
	res, err := d.db.Exec(`
		UPDATE generated_articles
		SET image_url = ?, image_thumbnail = ?
		WHERE id = ? AND COALESCE(image_url, '') = ? AND COALESCE(image_thumbnail, '') = ?`,
		url, thumbnail, old.ID, old.URL, old.Thumbnail)
	if err != nil {
		return false, fmt.Errorf("replacing image of article %d: %w", old.ID, err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearArticleImage removes an image whose file no longer exists and resets the
// enrichment attempt counter, handing the article back to the reconciler to
// pick a new one -- the same treatment repairRottedImages gives expired
// Pixabay links. Like ReplaceArticleImage it is a no-op if the row changed
// since the caller read it.
func (d *Database) ClearArticleImage(old StoredArticleImage) (bool, error) {
	res, err := d.db.Exec(`
		UPDATE generated_articles
		SET image_url = '', image_thumbnail = '', image_attribution = '',
		    enrichment_attempts = 0
		WHERE id = ? AND COALESCE(image_url, '') = ? AND COALESCE(image_thumbnail, '') = ?`,
		old.ID, old.URL, old.Thumbnail)
	if err != nil {
		return false, fmt.Errorf("clearing image of article %d: %w", old.ID, err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
