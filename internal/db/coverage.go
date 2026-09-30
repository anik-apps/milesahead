package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reasons a raw article is recorded in covered_raws.
const (
	// CoverReasonRawOverlap: the raw was clustered with raws an existing
	// article was written from, so the cluster was skipped.
	CoverReasonRawOverlap = "raw_overlap"
	// CoverReasonStoryMatch: the story-level check matched the raw's cluster
	// to an existing article and blocked it.
	CoverReasonStoryMatch = "story_match"
)

// CoveredRaw is a raw article an existing generated article covers without
// citing it as a source.
type CoveredRaw struct {
	RawArticleID       int64
	GeneratedArticleID int64
	Reason             string
	CreatedAt          time.Time
}

// ArticleForRawIDs returns the generated article that already covers any of
// rawIDs, either as a cited source (article_sources) or as recorded late
// coverage (covered_raws). With several, it picks the one sharing the most raw
// IDs, then the newest. found is false when none does.
//
// This replaces a yes/no check that read article_sources only. Knowing WHICH
// article matched is what lets the caller record the cluster's other raws as
// covered by it; reading covered_raws is what stops those raws seeding a
// duplicate once the article's own sources leave the 48h clustering window.
func (d *Database) ArticleForRawIDs(rawIDs []int64) (articleID int64, found bool, err error) {
	if len(rawIDs) == 0 {
		return 0, false, nil
	}
	in, args := int64Placeholders(rawIDs)
	query := fmt.Sprintf(`SELECT generated_article_id, COUNT(*) AS n FROM (
			SELECT generated_article_id, raw_article_id FROM article_sources WHERE raw_article_id IN (%[1]s)
			UNION
			SELECT generated_article_id, raw_article_id FROM covered_raws WHERE raw_article_id IN (%[1]s)
		) GROUP BY generated_article_id ORDER BY n DESC, generated_article_id DESC LIMIT 1`, in)
	var n int
	err = d.db.QueryRow(query, append(args, args...)...).Scan(&articleID, &n)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return articleID, true, nil
}

// CoverRawArticles records rawIDs as covered by generatedID, skipping any raw
// that is already a source of some article or already covered (the first
// cover wins). It returns how many rows it added.
func (d *Database) CoverRawArticles(generatedID int64, rawIDs []int64, reason string) (int, error) {
	if len(rawIDs) == 0 {
		return 0, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO covered_raws (raw_article_id, generated_article_id, reason)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM article_sources WHERE raw_article_id = ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	added := 0
	for _, rawID := range rawIDs {
		res, err := stmt.Exec(rawID, generatedID, reason, rawID)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err == nil {
			added += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return added, nil
}

// ListCoveredRaws returns the raw articles recorded as covered by an article.
func (d *Database) ListCoveredRaws(generatedID int64) ([]CoveredRaw, error) {
	rows, err := d.db.Query(`SELECT raw_article_id, generated_article_id, reason, created_at
		FROM covered_raws WHERE generated_article_id = ? ORDER BY raw_article_id`, generatedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoveredRaw
	for rows.Next() {
		var c CoveredRaw
		if err := rows.Scan(&c.RawArticleID, &c.GeneratedArticleID, &c.Reason, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StoryCandidate is a recently generated article, with the titles of the raw
// articles it was written from, for the story-level duplicate check.
type StoryCandidate struct {
	ID        int64
	Title     string
	TLDR      string
	Status    string
	CreatedAt time.Time
	RawTitles []string
}

// ListStoryCandidates returns every generated article created at or after
// since, in any status, newest first. Rejected and draft articles are
// included on purpose: a rejected duplicate is still a duplicate, and a draft
// may yet be published.
func (d *Database) ListStoryCandidates(since time.Time) ([]StoryCandidate, error) {
	// created_at is written by CURRENT_TIMESTAMP ("YYYY-MM-DD HH:MM:SS", UTC),
	// so compare against the same text form.
	rows, err := d.db.Query(`SELECT g.id, g.title, COALESCE(g.tldr, ''), g.status, g.created_at, COALESCE(r.title, '')
		FROM generated_articles g
		LEFT JOIN article_sources s ON s.generated_article_id = g.id
		LEFT JOIN raw_articles r ON r.id = s.raw_article_id
		WHERE g.created_at >= ?
		ORDER BY g.created_at DESC, g.id DESC, r.id`,
		since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StoryCandidate
	for rows.Next() {
		var c StoryCandidate
		var rawTitle string
		if err := rows.Scan(&c.ID, &c.Title, &c.TLDR, &c.Status, &c.CreatedAt, &rawTitle); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == c.ID {
			if rawTitle != "" {
				out[n-1].RawTitles = append(out[n-1].RawTitles, rawTitle)
			}
			continue
		}
		if rawTitle != "" {
			c.RawTitles = []string{rawTitle}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// int64Placeholders returns "?,?,..." and the matching arguments.
func int64Placeholders(ids []int64) (string, []interface{}) {
	ph := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return strings.Join(ph, ","), args
}
