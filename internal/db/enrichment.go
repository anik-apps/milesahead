package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// ArticleNeedingEnrichment is an article with at least one enrichment field
// missing, along with the inputs needed to fill it.
type ArticleNeedingEnrichment struct {
	ID       int64
	Title    string
	TLDR     string
	Content  string
	Attempts int

	MissingScoutsTake bool
	MissingKeyDetails bool
	MissingImage      bool
	MissingTags       bool
}

// AnyMissing reports whether there is anything left to fill.
func (a ArticleNeedingEnrichment) AnyMissing() bool {
	return a.MissingScoutsTake || a.MissingKeyDetails || a.MissingImage || a.MissingTags
}

// Missing names the gaps, for logging.
func (a ArticleNeedingEnrichment) Missing() string {
	var m []string
	if a.MissingScoutsTake {
		m = append(m, "scouts_take")
	}
	if a.MissingKeyDetails {
		m = append(m, "key_details")
	}
	if a.MissingImage {
		m = append(m, "image")
	}
	if a.MissingTags {
		m = append(m, "tags")
	}
	return strings.Join(m, ",")
}

// emptyKeyDetails matches every way key_details has been written when the
// extraction failed. It is a TEXT column holding JSON, and a nil struct
// marshalled unguarded produces the four-character string "null" -- which is
// neither NULL nor ” and so slips past the obvious checks. Rows carrying it
// exist in production from 2026-07-01 onward.
const emptyKeyDetails = `(key_details IS NULL OR key_details IN ('', '[]', 'null'))`

const emptyTags = `(tags_json IS NULL OR tags_json IN ('', '[]', 'null'))`

// ListArticlesNeedingEnrichment returns articles with at least one enrichment
// field missing that have not yet exhausted their attempts.
//
// Rejected articles are skipped: spending model calls to enrich something an
// editor has already thrown out is pure waste.
func (d *Database) ListArticlesNeedingEnrichment(maxAttempts, limit int) ([]ArticleNeedingEnrichment, error) {
	query := fmt.Sprintf(`
		SELECT id, title, COALESCE(tldr,''), content, enrichment_attempts,
		       (scouts_take IS NULL OR scouts_take = '') AS need_take,
		       %s AS need_details,
		       (image_url IS NULL OR image_url = '') AS need_image,
		       %s AS need_tags
		FROM generated_articles
		WHERE status != ?
		  AND enrichment_attempts < ?
		  AND (
		        (scouts_take IS NULL OR scouts_take = '')
		     OR %s
		     OR (image_url IS NULL OR image_url = '')
		     OR %s
		  )
		ORDER BY created_at DESC
		LIMIT ?`, emptyKeyDetails, emptyTags, emptyKeyDetails, emptyTags)

	rows, err := d.db.Query(query, StatusRejected, maxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("listing articles needing enrichment: %w", err)
	}
	defer rows.Close()

	var out []ArticleNeedingEnrichment
	for rows.Next() {
		var a ArticleNeedingEnrichment
		if err := rows.Scan(&a.ID, &a.Title, &a.TLDR, &a.Content, &a.Attempts,
			&a.MissingScoutsTake, &a.MissingKeyDetails, &a.MissingImage, &a.MissingTags); err != nil {
			return nil, fmt.Errorf("scanning article needing enrichment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// EnrichmentUpdate carries whatever a reconciliation pass managed to produce.
// A nil field means "not filled this pass" and leaves the column untouched, so
// a partial success is kept rather than discarded.
type EnrichmentUpdate struct {
	ScoutsTake       *string
	KeyDetails       *string
	ImageURL         *string
	ImageThumbnail   *string
	ImageAttribution *string
	TagsJSON         *string

	// Uncounted leaves enrichment_attempts alone. It is for a pass in which
	// nothing failed and the only work left undone was put off, not tried:
	// the pass ran out of time before the image search.
	Uncounted bool
}

// ApplyEnrichment writes whatever was produced and records the attempt.
//
// The attempt counter increments even when nothing was filled -- that is the
// whole point of it. Otherwise an article the model can never enrich would be
// retried on every pass forever. The one exception is u.Uncounted.
func (d *Database) ApplyEnrichment(id int64, u EnrichmentUpdate) error {
	sets := []string{"enrichment_last_at = CURRENT_TIMESTAMP"}
	if !u.Uncounted {
		sets = append(sets, "enrichment_attempts = enrichment_attempts + 1")
	}
	var args []any

	add := func(col string, val *string) {
		if val != nil {
			sets = append(sets, col+" = ?")
			args = append(args, *val)
		}
	}
	add("scouts_take", u.ScoutsTake)
	add("key_details", u.KeyDetails)
	add("image_url", u.ImageURL)
	add("image_thumbnail", u.ImageThumbnail)
	add("image_attribution", u.ImageAttribution)
	add("tags_json", u.TagsJSON)

	// updated_at is deliberately NOT touched. It is used to tell when an
	// article's content last changed, and a background enrichment pass is not
	// a content edit.
	args = append(args, id)
	_, err := d.db.Exec(
		"UPDATE generated_articles SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if err != nil {
		return fmt.Errorf("applying enrichment to article %d: %w", id, err)
	}
	return nil
}

// CountDegradedArticles reports how many articles are missing enrichment,
// split by whether the reconciler has given up on them. Exposed so the gap is
// a number on a dashboard rather than something nobody notices for months.
func (d *Database) CountDegradedArticles(maxAttempts int) (pending, exhausted int, err error) {
	query := fmt.Sprintf(`
		SELECT
		  SUM(CASE WHEN enrichment_attempts <  ? THEN 1 ELSE 0 END),
		  SUM(CASE WHEN enrichment_attempts >= ? THEN 1 ELSE 0 END)
		FROM generated_articles
		WHERE status != ?
		  AND ( (scouts_take IS NULL OR scouts_take = '')
		     OR %s
		     OR (image_url IS NULL OR image_url = '')
		     OR %s )`, emptyKeyDetails, emptyTags)

	var p, e sql.NullInt64
	if err := d.db.QueryRow(query, maxAttempts, maxAttempts, StatusRejected).Scan(&p, &e); err != nil {
		return 0, 0, fmt.Errorf("counting degraded articles: %w", err)
	}
	return int(p.Int64), int(e.Int64), nil
}
