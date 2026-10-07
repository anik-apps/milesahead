package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/kumaranik/milesahead/internal/editornote"
	"github.com/kumaranik/milesahead/internal/offer"
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
	// MissingInfographic is only ever set when the caller asked for
	// infographics (see ListArticlesNeedingEnrichmentWithInfographics).
	MissingInfographic bool

	// KeyDetails is the chart's only source of numbers, so an article that
	// still lacks it cannot have an infographic drawn yet.
	KeyDetails string

	// ImageRequestSeq is the admin's pending request for a new image (see
	// RequestNewImage), or 0 for none. With one, MissingImage is set even
	// though the article has an image: it is to be replaced, and kept until
	// it is. ImageHint is the admin's search words for it.
	ImageRequestSeq int64
	ImageHint       string
}

// AnyMissing reports whether there is anything left to fill.
func (a ArticleNeedingEnrichment) AnyMissing() bool {
	return a.MissingScoutsTake || a.MissingKeyDetails || a.MissingImage || a.MissingTags ||
		a.MissingInfographic
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
	if a.MissingImage && a.ImageRequestSeq > 0 {
		m = append(m, "new_image")
	} else if a.MissingImage {
		m = append(m, "image")
	}
	if a.MissingTags {
		m = append(m, "tags")
	}
	if a.MissingInfographic {
		m = append(m, "infographic")
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

// NoInfographic is written to infographic_json when a model has looked at an
// article and found nothing worth charting. It distinguishes "asked, and the
// answer was no" from "not asked yet", which is the difference between one
// model call and one per pass forever.
const NoInfographic = "none"

const emptyInfographic = `(g.infographic_json IS NULL OR g.infographic_json = '')`

// ListArticlesNeedingEnrichmentWithInfographics is ListArticlesNeedingEnrichment
// with a missing infographic counted as a gap. It is separate because the
// infographic is opt-in (ai.enrichment_infographics): with it off, an article
// that lacks only a chart is complete and must not be picked up, charged an
// attempt or held out of the image pass.
func (d *Database) ListArticlesNeedingEnrichmentWithInfographics(maxAttempts, limit int) ([]ArticleNeedingEnrichment, error) {
	return d.listArticlesNeedingEnrichment(maxAttempts, limit, true)
}

// ListArticlesNeedingEnrichment returns articles with at least one enrichment
// field missing that have not yet exhausted their attempts.
//
// Rejected articles are skipped: spending model calls to enrich something an
// editor has already thrown out is pure waste.
func (d *Database) ListArticlesNeedingEnrichment(maxAttempts, limit int) ([]ArticleNeedingEnrichment, error) {
	return d.listArticlesNeedingEnrichment(maxAttempts, limit, false)
}

func (d *Database) listArticlesNeedingEnrichment(maxAttempts, limit int, infographics bool) ([]ArticleNeedingEnrichment, error) {
	needInfographic := "0"
	if infographics {
		needInfographic = emptyInfographic
	}
	// An admin's pending request for a new image counts as a missing image,
	// and is served first: someone is waiting on it.
	query := fmt.Sprintf(`
		SELECT g.id, g.title, COALESCE(g.tldr,''), g.content, g.enrichment_attempts,
		       COALESCE(g.key_details,''),
		       (g.scouts_take IS NULL OR g.scouts_take = '') AS need_take,
		       %s AS need_details,
		       (g.image_url IS NULL OR g.image_url = '' OR r.article_id IS NOT NULL) AS need_image,
		       %s AS need_tags,
		       %s AS need_infographic,
		       COALESCE(r.seq, 0), COALESCE(r.hint, '')
		FROM generated_articles g
		LEFT JOIN article_image_requests r ON r.article_id = g.id AND r.state = ?
		WHERE g.status != ?
		  AND g.enrichment_attempts < ?
		  AND (
		        (g.scouts_take IS NULL OR g.scouts_take = '')
		     OR %s
		     OR (g.image_url IS NULL OR g.image_url = '')
		     OR r.article_id IS NOT NULL
		     OR %s
		     OR %s
		  )
		ORDER BY (r.article_id IS NOT NULL) DESC, g.created_at DESC
		LIMIT ?`, emptyKeyDetails, emptyTags, needInfographic,
		emptyKeyDetails, emptyTags, needInfographic)

	rows, err := d.db.Query(query, ImageRequestPending, StatusRejected, maxAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("listing articles needing enrichment: %w", err)
	}
	defer rows.Close()

	var out []ArticleNeedingEnrichment
	for rows.Next() {
		var a ArticleNeedingEnrichment
		if err := rows.Scan(&a.ID, &a.Title, &a.TLDR, &a.Content, &a.Attempts, &a.KeyDetails,
			&a.MissingScoutsTake, &a.MissingKeyDetails, &a.MissingImage, &a.MissingTags,
			&a.MissingInfographic, &a.ImageRequestSeq, &a.ImageHint); err != nil {
			return nil, fmt.Errorf("scanning article needing enrichment: %w", err)
		}
		// The Scout's Take, key details and tags are written from the
		// content, and are public: no editor note may feed them.
		a.Content = editornote.Strip(a.Content)
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
	InfographicJSON  *string

	// ImageRequestSeq is the admin's request this pass's image answers (see
	// ArticleNeedingEnrichment), or 0. The image is written only if that is
	// still the current request, and then closes it; a request made while
	// the pass ran supersedes the image and stays pending (see applyImage).
	ImageRequestSeq int64

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
	if u.KeyDetails != nil {
		// New key details can state an offer end date, so it is derived in
		// the same write, anchored on created_at as the startup backfill is.
		var createdAt any
		if err := d.db.QueryRow(`SELECT created_at FROM generated_articles WHERE id = ?`, id).Scan(&createdAt); err != nil {
			return fmt.Errorf("reading created_at for article %d: %w", id, err)
		}
		sets = append(sets, "expires_on = ?")
		args = append(args, nullIfEmpty(offer.EndDate(*u.KeyDetails, writtenAt(createdAt))))
	}
	add("tags_json", u.TagsJSON)
	add("infographic_json", u.InfographicJSON)

	// updated_at is deliberately NOT touched. It is used to tell when an
	// article's content last changed, and a background enrichment pass is not
	// a content edit.
	args = append(args, id)
	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("applying enrichment to article %d: %w", id, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		"UPDATE generated_articles SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...); err != nil {
		return fmt.Errorf("applying enrichment to article %d: %w", id, err)
	}
	if u.ImageURL != nil {
		if err := applyImage(tx, id, u); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("applying enrichment to article %d: %w", id, err)
	}
	return nil
}

// applyImage writes the image a pass found, inside ApplyEnrichment's
// transaction, but only if the admin's request it answers is still the
// current one: still pending at u.ImageRequestSeq, or, for a pass that served
// no request (seq 0), none pending. A request made while the pass ran
// supersedes it -- the admin may have turned down the very image the pass was
// replacing, or asked for something else -- so its image is dropped and the
// newer request is left pending for the next pass. The other fields of the
// update still apply.
//
// The check is part of the UPDATE, and the transaction already holds the
// write lock from the UPDATE before it, so no request can slip in between.
// When the image lands, the request it answered is closed in the same
// transaction, and so is an earlier failed one: the article has a new image.
func applyImage(tx *sql.Tx, id int64, u EnrichmentUpdate) error {
	sets := []string{"image_url = ?"}
	args := []any{*u.ImageURL}
	if u.ImageThumbnail != nil {
		sets = append(sets, "image_thumbnail = ?")
		args = append(args, *u.ImageThumbnail)
	}
	if u.ImageAttribution != nil {
		sets = append(sets, "image_attribution = ?")
		args = append(args, *u.ImageAttribution)
	}
	current := `NOT EXISTS (SELECT 1 FROM article_image_requests WHERE article_id = ? AND state = ?)`
	args = append(args, id, id, ImageRequestPending)
	if u.ImageRequestSeq > 0 {
		current = `EXISTS (SELECT 1 FROM article_image_requests WHERE article_id = ? AND state = ? AND seq = ?)`
		args = append(args, u.ImageRequestSeq)
	}
	res, err := tx.Exec("UPDATE generated_articles SET "+strings.Join(sets, ", ")+
		" WHERE id = ? AND "+current, args...)
	if err != nil {
		return fmt.Errorf("applying the image to article %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("applying the image to article %d: %w", id, err)
	} else if n == 0 {
		slog.Info("enrichment: image dropped, a newer image request superseded the pass",
			"article_id", id, "pass_request_seq", u.ImageRequestSeq)
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM article_image_requests
		WHERE article_id = ? AND (seq = ? OR state = ?)`, id, u.ImageRequestSeq, ImageRequestFailed); err != nil {
		return fmt.Errorf("closing the image request for article %d: %w", id, err)
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

// InfographicsFor returns the stored infographic_json of each of ids that has
// one, sentinel included. The list queries do not carry the column -- only
// single-article views show a chart -- so the review queue and Published load
// it for their rows in one query.
func (d *Database) InfographicsFor(ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(`SELECT id, infographic_json FROM generated_articles
		WHERE id IN (`+in+`) AND infographic_json IS NOT NULL AND infographic_json != ''`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing infographics: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var chart string
		if err := rows.Scan(&id, &chart); err != nil {
			return nil, err
		}
		out[id] = chart
	}
	return out, rows.Err()
}

// ErrNoChart means a chart action named an article that does not exist or
// was rejected.
var ErrNoChart = errors.New("no such article, or it was rejected")

// RemoveInfographic is the admin's "Remove chart": the article's chart is
// replaced by the sentinel, so it is not shown and not drawn again until
// RedrawInfographic.
func (d *Database) RemoveInfographic(id int64) error {
	return d.setInfographic(id, `infographic_json = ?`, NoInfographic)
}

// RedrawInfographic is the admin's "Redraw chart": the chart (or the
// sentinel) is cleared so the reconciler draws a new one, and the article's
// attempts are reset so it is picked up even if earlier passes used them all
// -- as a new-image request gets a fresh budget. Any other field still
// missing is retried with that budget too.
func (d *Database) RedrawInfographic(id int64) error {
	return d.setInfographic(id, `infographic_json = '', enrichment_attempts = 0`)
}

func (d *Database) setInfographic(id int64, set string, args ...any) error {
	// updated_at is not touched: the article's content did not change.
	res, err := d.db.Exec(`UPDATE generated_articles SET `+set+` WHERE id = ? AND status != ?`,
		append(args, id, StatusRejected)...)
	if err != nil {
		return fmt.Errorf("updating the chart of article %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNoChart
	}
	return nil
}
