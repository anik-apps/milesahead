package db

import (
	"strings"
)

// The admin dashboard shows each listed article's revision history: the
// feedback sent, the rewrites that answered it and the critic's score for each.
// These load that for all listed articles at once, one query per table, rather
// than three queries per article.

// idPlaceholders returns "?,?,?" and the ids as query args.
func idPlaceholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// ListFeedbackForArticles returns the feedback for each of the given articles,
// oldest first by id -- the order it was sent in, and the definition of
// "newest" the rewrite uses (created_at has one-second resolution).
func (d *Database) ListFeedbackForArticles(ids []int64) (map[int64][]AdminFeedback, error) {
	out := map[int64][]AdminFeedback{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, feedback_text, source, created_at
		 FROM admin_feedback WHERE generated_article_id IN (`+in+`) ORDER BY id ASC`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f AdminFeedback
		if err := rows.Scan(&f.ID, &f.GeneratedArticleID, &f.FeedbackText, &f.Source, &f.CreatedAt); err != nil {
			return nil, err
		}
		out[f.GeneratedArticleID] = append(out[f.GeneratedArticleID], f)
	}
	return out, rows.Err()
}

// ListRevisionsForArticles returns the stored rewrites for each of the given
// articles, in revision order.
func (d *Database) ListRevisionsForArticles(ids []int64) (map[int64][]ArticleRevision, error) {
	out := map[int64][]ArticleRevision{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, revision_number, previous_content, new_content, feedback_id, created_at
		 FROM article_revisions WHERE generated_article_id IN (`+in+`) ORDER BY revision_number ASC, id ASC`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r ArticleRevision
		if err := rows.Scan(&r.ID, &r.GeneratedArticleID, &r.RevisionNumber, &r.PreviousContent, &r.NewContent, &r.FeedbackID, &r.CreatedAt); err != nil {
			return nil, err
		}
		out[r.GeneratedArticleID] = append(out[r.GeneratedArticleID], r)
	}
	return out, rows.Err()
}

// ListVerificationsForArticles returns, for each of the given articles, the
// critic's result per revision number. If a revision was verified more than
// once, the latest result wins.
func (d *Database) ListVerificationsForArticles(ids []int64) (map[int64]map[int]VerificationResult, error) {
	out := map[int64]map[int]VerificationResult{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idPlaceholders(ids)
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, revision_number, score, issues_json, factual_check, attribution_check, completeness_check, hallucination_check, verified_at
		 FROM verification_results WHERE generated_article_id IN (`+in+`) ORDER BY id ASC`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v VerificationResult
		if err := rows.Scan(&v.ID, &v.GeneratedArticleID, &v.RevisionNumber, &v.Score, &v.IssuesJSON, &v.FactualCheck, &v.AttributionCheck, &v.CompletenessCheck, &v.HallucinationCheck, &v.VerifiedAt); err != nil {
			return nil, err
		}
		if out[v.GeneratedArticleID] == nil {
			out[v.GeneratedArticleID] = map[int]VerificationResult{}
		}
		out[v.GeneratedArticleID][v.RevisionNumber] = v
	}
	return out, rows.Err()
}
