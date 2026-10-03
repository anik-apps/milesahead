package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/editornote"
	"github.com/kumaranik/milesahead/internal/offer"
	_ "modernc.org/sqlite"
)

// Database wraps a *sql.DB connection with application-specific methods.
type Database struct {
	db *sql.DB
}

// dsn builds a connection string carrying the per-connection PRAGMAs.
//
// journal_mode is persisted in the database file itself so it only has to be
// set once, but it is harmless and clearer to state it alongside the others.
func dsn(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join([]string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
	}, "&")
}

// Open opens a SQLite database at the given path, enables WAL mode, and runs migrations.
func Open(path string) (*Database, error) {
	// PRAGMAs go in the DSN, not in Exec calls.
	//
	// busy_timeout and foreign_keys are PER-CONNECTION settings, and Exec runs
	// on whichever pooled connection happens to be free -- so setting them that
	// way configured exactly one connection and left every other one at the
	// defaults. Verified: two connections from the same pool reported
	// foreign_keys 1 and 0 respectively. In the DSN the driver applies them to
	// every connection it opens.
	//
	// busy_timeout was therefore 0, meaning a second concurrent writer failed
	// instantly with SQLITE_BUSY rather than waiting. With enrichment UPDATEs,
	// article INSERTs and web handlers all writing, that is a real collision --
	// and if it hits the ALTER TABLE in migrate() the service refuses to boot,
	// reporting a transient lock as a fatal schema error.
	conn, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &Database{db: conn}, nil
}

// Close closes the underlying database connection.
func (d *Database) Close() error {
	return d.db.Close()
}

// DB returns the underlying *sql.DB for advanced usage.
func (d *Database) DB() *sql.DB {
	return d.db
}

// --- Source CRUD ---

func (d *Database) CreateSource(s *Source) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO sources (name, url, rss_url, scrape_selector, scan_interval, active) VALUES (?, ?, ?, ?, ?, ?)`,
		s.Name, s.URL, s.RSSURL, s.ScrapeSelector, s.ScanInterval, s.Active,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) GetSource(id int64) (*Source, error) {
	s := &Source{}
	err := d.db.QueryRow(
		`SELECT id, name, url, rss_url, scrape_selector, scan_interval, active, created_at, updated_at FROM sources WHERE id = ?`, id,
	).Scan(&s.ID, &s.Name, &s.URL, &s.RSSURL, &s.ScrapeSelector, &s.ScanInterval, &s.Active, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// ListActiveSources returns the active sources by id. The order is part of the
// contract: the scan builds each cluster's source list in this order, and
// revisions and the source-reference repair rely on reproducing it.
func (d *Database) ListActiveSources() ([]Source, error) {
	rows, err := d.db.Query(`SELECT id, name, url, rss_url, scrape_selector, scan_interval, active, created_at, updated_at FROM sources WHERE active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.RSSURL, &s.ScrapeSelector, &s.ScanInterval, &s.Active, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

func (d *Database) UpdateSource(s *Source) error {
	_, err := d.db.Exec(
		`UPDATE sources SET name = ?, url = ?, rss_url = ?, scrape_selector = ?, scan_interval = ?, active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		s.Name, s.URL, s.RSSURL, s.ScrapeSelector, s.ScanInterval, s.Active, s.ID,
	)
	return err
}

func (d *Database) DeleteSource(id int64) error {
	_, err := d.db.Exec(`DELETE FROM sources WHERE id = ?`, id)
	return err
}

// --- RawArticle CRUD ---

func (d *Database) CreateRawArticle(a *RawArticle) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO raw_articles (source_id, title, url, content, published_at, hash) VALUES (?, ?, ?, ?, ?, ?)`,
		a.SourceID, a.Title, a.URL, a.Content, a.PublishedAt, a.Hash,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CreateRawArticleIfNew inserts a raw article only if its hash doesn't already exist.
// Returns true if inserted, false if it was a duplicate.
func (d *Database) CreateRawArticleIfNew(a *RawArticle) (bool, error) {
	res, err := d.db.Exec(
		`INSERT OR IGNORE INTO raw_articles (source_id, title, url, content, published_at, hash) VALUES (?, ?, ?, ?, ?, ?)`,
		a.SourceID, a.Title, a.URL, a.Content, a.PublishedAt, a.Hash,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (d *Database) GetRawArticlesBySourceID(sourceID int64) ([]RawArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, source_id, title, url, content, published_at, hash, created_at FROM raw_articles WHERE source_id = ? ORDER BY id`, sourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []RawArticle
	for rows.Next() {
		var a RawArticle
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Title, &a.URL, &a.Content, &a.PublishedAt, &a.Hash, &a.CreatedAt); err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

func (d *Database) GetRawArticlesByIDs(ids []int64) ([]RawArticle, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `SELECT id, source_id, title, url, content, published_at, hash, created_at FROM raw_articles WHERE id IN (`
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args[i] = id
	}
	query += ")"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []RawArticle
	for rows.Next() {
		var a RawArticle
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Title, &a.URL, &a.Content, &a.PublishedAt, &a.Hash, &a.CreatedAt); err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

// --- GeneratedArticle CRUD ---

func (d *Database) CreateGeneratedArticle(a *GeneratedArticle) (int64, error) {
	// created_at is "now", the same anchor the startup backfill uses for this
	// derivation, so the two agree.
	a.ExpiresOn = offer.EndDate(a.KeyDetails, time.Now().UTC())
	a.separateEditorNotes()
	res, err := d.db.Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, expires_on, editor_notes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Slug, a.Content, a.Type, a.Status, a.SourcesJSON, a.RevisionNumber, a.Featured, a.TLDR, a.KeyDetails, a.Links, a.ImageURL, a.ImageThumbnail, a.ImageAttribution, a.ScoutsTake, a.RelatedCardsJSON, a.TagsJSON, nullIfEmpty(a.ExpiresOn), a.EditorNotes,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := d.SyncArticleFTS(id); err != nil {
		slog.Warn("FTS sync failed", "article_id", id, "error", err)
	}
	return id, nil
}

func (d *Database) GetGeneratedArticle(id int64) (*GeneratedArticle, error) {
	a := &GeneratedArticle{}
	err := d.db.QueryRow(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE id = ?`, id,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.TagsJSON, &a.LinkedCardsHeading, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt, &a.ExpiresOn, &a.EditorNotes)
	if err != nil {
		return nil, err
	}
	a.separateEditorNotes()
	return a, nil
}

// validTransitions defines allowed status transitions.
var validTransitions = map[string][]string{
	StatusDraft:    {StatusPending, StatusApproved, StatusRejected, StatusRevisionRequested},
	StatusPending:  {StatusApproved, StatusRejected, StatusRevisionRequested},
	StatusApproved: {StatusUnpublished},
	StatusRejected: {},
	// -> pending: a rewrite that passes the critic goes straight to pending.
	// ApplyRevision makes that move in its own compare-and-set, not through
	// here, but it is a real transition and belongs in the table.
	StatusRevisionRequested: {StatusDraft, StatusPending, StatusApproved, StatusRejected, StatusRevisionRequested},
	StatusUnpublished:       {StatusRevisionRequested, StatusApproved, StatusRejected},
}

// ErrStatusChanged means the article's status was no longer the one a
// transition was validated against: another writer got there first.
var ErrStatusChanged = errors.New("article status changed concurrently")

// UpdateArticleStatus moves an article from whatever status it has now.
// Callers that decided on the move from a status they read earlier should use
// TransitionArticleStatus, so the decision cannot apply to a different status.
func (d *Database) UpdateArticleStatus(id int64, newStatus string) error {
	a, err := d.GetGeneratedArticle(id)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	return d.TransitionArticleStatus(id, a.Status, newStatus)
}

// TransitionArticleStatus moves an article from status from to newStatus, and
// only if it is still in from. The write is conditional on it: a read-then-write
// let an Approve validated against revision_requested land after a rewrite had
// already moved the article to pending, publishing a rewrite nobody reviewed.
// Returns ErrStatusChanged if the status moved in the meantime.
func (d *Database) TransitionArticleStatus(id int64, from, newStatus string) error {
	return d.transitionArticleStatus(id, from, newStatus, 0)
}

// TransitionArticleStatusAtRevision is TransitionArticleStatus that also
// requires the article to still be at revision: the one the admin read
// before deciding. A rewrite that lands between the admin opening the article
// and clicking Approve bumps the revision, so the decision cannot apply to
// text nobody reviewed. Returns ErrStatusChanged if either moved.
func (d *Database) TransitionArticleStatusAtRevision(id int64, from, newStatus string, revision int) error {
	if revision <= 0 {
		return fmt.Errorf("invalid revision %d", revision)
	}
	return d.transitionArticleStatus(id, from, newStatus, revision)
}

// transitionArticleStatus is the conditional write behind both; revision 0
// leaves the revision unchecked.
func (d *Database) transitionArticleStatus(id int64, from, newStatus string, revision int) error {
	valid := false
	for _, s := range validTransitions[from] {
		if s == newStatus {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid status transition from %q to %q", from, newStatus)
	}

	query := `UPDATE generated_articles SET status = ?, updated_at = CURRENT_TIMESTAMP`
	args := []interface{}{newStatus}
	switch {
	case newStatus == StatusApproved && from == StatusUnpublished:
		// Republishing an article that was taken down puts the same text
		// back: it keeps its original publish date rather than jumping to
		// the top of the feed (and into this week's newsletter) as new.
		query += `, published_at = COALESCE(published_at, CURRENT_TIMESTAMP)`
	case newStatus == StatusApproved:
		query += `, published_at = CURRENT_TIMESTAMP`
	}
	query += ` WHERE id = ? AND status = ?`
	args = append(args, id, from)
	if revision > 0 {
		query += ` AND revision_number = ?`
		args = append(args, revision)
	}

	res, err := d.db.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if revision > 0 {
			return fmt.Errorf("%w: expected %q at revision %d", ErrStatusChanged, from, revision)
		}
		return fmt.Errorf("%w: expected %q", ErrStatusChanged, from)
	}
	return nil
}

// ErrUndoExpired means an approval can no longer be undone: there is no
// recorded approval, its window has passed, or the article has changed since
// (no longer approved, or at another revision).
var ErrUndoExpired = errors.New("approval can no longer be undone")

func (d *Database) ListArticlesByStatus(status string) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE status = ? ORDER BY created_at DESC`, status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// ListPublishedArticles returns approved articles, most recently published
// first.
func (d *Database) ListPublishedArticles(limit, offset int) ([]GeneratedArticle, error) {
	return d.ListPublishedArticlesSorted(false, limit, offset)
}

// ListPublishedArticlesSorted is ListPublishedArticles in either order:
// oldestFirst lists the earliest published first. Ties go by id, the same
// way round, so the order is stable.
func (d *Database) ListPublishedArticlesSorted(oldestFirst bool, limit, offset int) ([]GeneratedArticle, error) {
	order := "published_at DESC, id DESC"
	if oldestFirst {
		order = "published_at ASC, id ASC"
	}
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE status = ? ORDER BY `+order+` LIMIT ? OFFSET ?`,
		StatusApproved, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// ListLivePublishedArticles is ListPublishedArticles without the articles
// whose offer has ended. today is the site's current date as "2006-01-02";
// an offer is still live on its last day, so only a strictly earlier
// expires_on is excluded. Articles with no end date are always live.
func (d *Database) ListLivePublishedArticles(today string, limit, offset int) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles
		WHERE status = ? AND (expires_on IS NULL OR expires_on >= ?)
		ORDER BY published_at DESC LIMIT ? OFFSET ?`,
		StatusApproved, today, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// OfferEnd is an article's offer end date and its type, which says how an
// ended one is labelled.
type OfferEnd struct {
	EndsOn      string // "2006-01-02"
	ArticleType string
}

// OfferEndsBySlug returns the end date and type of the approved articles
// among slugs that have an end date, keyed by slug. Pages that link to
// articles (a card's recent changes) use it to mark the ones whose offer has
// ended.
func (d *Database) OfferEndsBySlug(slugs []string) (map[string]OfferEnd, error) {
	out := map[string]OfferEnd{}
	if len(slugs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(slugs))
	args := make([]interface{}, 0, len(slugs)+1)
	args = append(args, StatusApproved)
	for i, s := range slugs {
		placeholders[i] = "?"
		args = append(args, s)
	}
	rows, err := d.db.Query(
		`SELECT slug, expires_on, type FROM generated_articles
		WHERE status = ? AND expires_on IS NOT NULL AND slug IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		var end OfferEnd
		if err := rows.Scan(&slug, &end.EndsOn, &end.ArticleType); err != nil {
			return nil, err
		}
		out[slug] = end
	}
	return out, rows.Err()
}

// nullIfEmpty stores "" as NULL, so "no end date" is one value in SQL.
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanGeneratedArticles(rows *sql.Rows) ([]GeneratedArticle, error) {
	var articles []GeneratedArticle
	for rows.Next() {
		var a GeneratedArticle
		if err := rows.Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.TagsJSON, &a.LinkedCardsHeading, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt, &a.ExpiresOn, &a.EditorNotes); err != nil {
			return nil, err
		}
		a.separateEditorNotes()
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

func (d *Database) LinkArticleSources(generatedID int64, rawIDs []int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO article_sources (generated_article_id, raw_article_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, rawID := range rawIDs {
		if _, err := stmt.Exec(generatedID, rawID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- VerificationResult CRUD ---

func (d *Database) CreateVerificationResult(v *VerificationResult) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO verification_results (generated_article_id, revision_number, score, issues_json, factual_check, attribution_check, completeness_check, hallucination_check) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		v.GeneratedArticleID, v.RevisionNumber, v.Score, v.IssuesJSON, v.FactualCheck, v.AttributionCheck, v.CompletenessCheck, v.HallucinationCheck,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) GetLatestVerification(articleID int64) (*VerificationResult, error) {
	v := &VerificationResult{}
	err := d.db.QueryRow(
		`SELECT id, generated_article_id, revision_number, score, issues_json, factual_check, attribution_check, completeness_check, hallucination_check, verified_at FROM verification_results WHERE generated_article_id = ? ORDER BY revision_number DESC LIMIT 1`, articleID,
	).Scan(&v.ID, &v.GeneratedArticleID, &v.RevisionNumber, &v.Score, &v.IssuesJSON, &v.FactualCheck, &v.AttributionCheck, &v.CompletenessCheck, &v.HallucinationCheck, &v.VerifiedAt)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// --- AdminFeedback CRUD ---

func (d *Database) CreateAdminFeedback(f *AdminFeedback) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO admin_feedback (generated_article_id, feedback_text, source) VALUES (?, ?, ?)`,
		f.GeneratedArticleID, f.FeedbackText, f.Source,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListFeedbackForArticle returns an article's feedback, newest first. id breaks
// ties because created_at has one-second resolution: two notes sent in the same
// second came back in arbitrary order, so a revision could answer the older one.
func (d *Database) ListFeedbackForArticle(articleID int64) ([]AdminFeedback, error) {
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, feedback_text, source, created_at FROM admin_feedback WHERE generated_article_id = ? ORDER BY created_at DESC, id DESC`, articleID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var feedback []AdminFeedback
	for rows.Next() {
		var f AdminFeedback
		if err := rows.Scan(&f.ID, &f.GeneratedArticleID, &f.FeedbackText, &f.Source, &f.CreatedAt); err != nil {
			return nil, err
		}
		feedback = append(feedback, f)
	}
	return feedback, rows.Err()
}

// UpdateArticleContent updates the content, title, and revision_number of a
// generated article. Editor notes in content are stored apart from it, and
// replace the article's previous notes: they described the old content.
func (d *Database) UpdateArticleContent(id int64, title, content string, revisionNumber int) error {
	content, notes := editornote.Split(content)
	_, err := d.db.Exec(
		`UPDATE generated_articles SET title = ?, content = ?, editor_notes = ?, revision_number = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		title, content, notes, revisionNumber, id,
	)
	return err
}

// UpdateArticleLinkedCards atomically updates the related_cards_json and
// linked_cards_heading fields, plus updated_at. The relatedCardsJSON value
// must already be a valid JSON array string (pass "[]" for empty).
func (d *Database) UpdateArticleLinkedCards(id int64, relatedCardsJSON, heading string) error {
	_, err := d.db.Exec(
		`UPDATE generated_articles SET related_cards_json = ?, linked_cards_heading = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		relatedCardsJSON, heading, id,
	)
	return err
}

// --- ArticleRevision CRUD ---

func (d *Database) CreateArticleRevision(r *ArticleRevision) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO article_revisions (generated_article_id, revision_number, previous_content, new_content, feedback_id) VALUES (?, ?, ?, ?, ?)`,
		r.GeneratedArticleID, r.RevisionNumber, r.PreviousContent, r.NewContent, r.FeedbackID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) ListRevisionsForArticle(articleID int64) ([]ArticleRevision, error) {
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, revision_number, previous_content, new_content, feedback_id, created_at FROM article_revisions WHERE generated_article_id = ? ORDER BY revision_number ASC`, articleID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var revisions []ArticleRevision
	for rows.Next() {
		var r ArticleRevision
		if err := rows.Scan(&r.ID, &r.GeneratedArticleID, &r.RevisionNumber, &r.PreviousContent, &r.NewContent, &r.FeedbackID, &r.CreatedAt); err != nil {
			return nil, err
		}
		revisions = append(revisions, r)
	}
	return revisions, rows.Err()
}

// --- ArticleVote CRUD ---

func (d *Database) CreateVote(v *ArticleVote) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO article_votes (article_id, vote, ip_hash) VALUES (?, ?, ?)`,
		v.ArticleID, v.Vote, v.IPHash,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// VoteCounts holds aggregated vote counts for an article.
type VoteCounts struct {
	Upvotes   int
	Downvotes int
}

func (d *Database) GetVoteCounts(articleID int64) (*VoteCounts, error) {
	return d.GetVoteCountsSince(articleID, time.Time{})
}

// GetVoteCountsSince counts an article's votes cast at or after since; a
// zero since counts them all.
func (d *Database) GetVoteCountsSince(articleID int64, since time.Time) (*VoteCounts, error) {
	vc := &VoteCounts{}
	// created_at is SQLite's CURRENT_TIMESTAMP: UTC, "2006-01-02 15:04:05".
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN vote > 0 THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN vote < 0 THEN 1 ELSE 0 END), 0)
		FROM article_votes WHERE article_id = ? AND created_at >= ?`,
		articleID, since.UTC().Format(sqliteTime),
	).Scan(&vc.Upvotes, &vc.Downvotes)
	if err != nil {
		return nil, err
	}
	return vc, nil
}

func (d *Database) HasVoted(articleID int64, ipHash string) (bool, error) {
	var count int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM article_votes WHERE article_id = ? AND ip_hash = ?`, articleID, ipHash,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// --- Newsletter CRUD ---

// CreateNewsletter stores n. Its CreatedAt is now unless n sets it, and n
// is left holding the CreatedAt stored.
func (d *Database) CreateNewsletter(n *Newsletter) (int64, error) {
	created := time.Now().UTC()
	if n.CreatedAt != nil {
		created = n.CreatedAt.UTC()
	}
	res, err := d.db.Exec(
		`INSERT INTO newsletters (date, subject, html_content, status, article_count, card_of_week_id, scouts_pick_article_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Date, n.Subject, n.HTMLContent, n.Status, n.ArticleCount, n.CardOfWeekID, n.ScoutsPickArticleID, created,
	)
	if err != nil {
		return 0, err
	}
	n.CreatedAt = &created
	return res.LastInsertId()
}

func (d *Database) GetNewsletterByDate(date string) (*Newsletter, error) {
	n := &Newsletter{}
	err := d.db.QueryRow(
		`SELECT id, date, subject, html_content, status, article_count, card_of_week_id, scouts_pick_article_id, sent_at, created_at FROM newsletters WHERE date = ?`, date,
	).Scan(&n.ID, &n.Date, &n.Subject, &n.HTMLContent, &n.Status, &n.ArticleCount, &n.CardOfWeekID, &n.ScoutsPickArticleID, &n.SentAt, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (d *Database) MarkNewsletterSent(id int64) error {
	_, err := d.db.Exec(`UPDATE newsletters SET status = 'sent', sent_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// ClaimNewsletterSend marks newsletter id sent before it goes out, if its
// status is still from, and reports whether it did. Recording the send first
// means a failure to record it can never leave an issue that went out
// looking unsent, to be sent again; and of two sends at once only one claims
// the issue.
func (d *Database) ClaimNewsletterSend(id int64, from string) (bool, error) {
	res, err := d.db.Exec(`UPDATE newsletters SET status = 'sent', sent_at = ? WHERE id = ? AND status = ?`, time.Now().UTC(), id, from)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// ReleaseNewsletterSend puts newsletter id, claimed by ClaimNewsletterSend
// for a send that failed, back to status.
func (d *Database) ReleaseNewsletterSend(id int64, status string) error {
	_, err := d.db.Exec(`UPDATE newsletters SET status = ?, sent_at = NULL WHERE id = ? AND status = 'sent'`, status, id)
	return err
}

// ListNewsletters returns all newsletters ordered by date descending.
func (d *Database) ListNewsletters() ([]Newsletter, error) {
	rows, err := d.db.Query(
		`SELECT id, date, subject, html_content, status, article_count, card_of_week_id, scouts_pick_article_id, sent_at, created_at FROM newsletters ORDER BY date DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var newsletters []Newsletter
	for rows.Next() {
		var n Newsletter
		if err := rows.Scan(&n.ID, &n.Date, &n.Subject, &n.HTMLContent, &n.Status, &n.ArticleCount, &n.CardOfWeekID, &n.ScoutsPickArticleID, &n.SentAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		newsletters = append(newsletters, n)
	}
	return newsletters, rows.Err()
}

// --- Subscriber CRUD ---

// Subscriber lookup and state errors.
var (
	ErrSubscriberNotFound = errors.New("subscriber not found")
	ErrAlreadyConfirmed   = errors.New("subscriber already confirmed")
	ErrUnsubscribed       = errors.New("subscriber has unsubscribed")
)

const subscriberColumns = `id, email, confirmed, token, subscribed_at, confirmed_at, unsubscribed_at`

func scanSubscriber(row interface{ Scan(...any) error }) (Subscriber, error) {
	var s Subscriber
	err := row.Scan(&s.ID, &s.Email, &s.Confirmed, &s.Token, &s.SubscribedAt, &s.ConfirmedAt, &s.UnsubscribedAt)
	return s, err
}

func (d *Database) querySubscribers(query string, args ...any) ([]Subscriber, error) {
	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscribers []Subscriber
	for rows.Next() {
		s, err := scanSubscriber(rows)
		if err != nil {
			return nil, err
		}
		subscribers = append(subscribers, s)
	}
	return subscribers, rows.Err()
}

func (d *Database) getSubscriber(where string, arg any) (*Subscriber, error) {
	s, err := scanSubscriber(d.db.QueryRow(`SELECT `+subscriberColumns+` FROM subscribers WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSubscriberNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *Database) CreateSubscriber(s *Subscriber) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO subscribers (email, token) VALUES (?, ?)`,
		s.Email, s.Token,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetSubscriberByToken returns the subscriber with this token, or
// ErrSubscriberNotFound.
func (d *Database) GetSubscriberByToken(token string) (*Subscriber, error) {
	return d.getSubscriber(`token = ?`, token)
}

// GetSubscriberByEmail returns the subscriber with this address, compared
// without regard to case, or ErrSubscriberNotFound.
func (d *Database) GetSubscriberByEmail(email string) (*Subscriber, error) {
	return d.getSubscriber(`lower(email) = lower(?)`, email)
}

// ConfirmSubscriber confirms the subscription with this token. A second
// confirmation is not an error a reader should see, so it returns
// ErrAlreadyConfirmed for the caller to tell apart; an unsubscribed row is
// left alone (an old confirmation link must not undo an unsubscribe) and
// returns ErrUnsubscribed.
func (d *Database) ConfirmSubscriber(token string) error {
	res, err := d.db.Exec(
		`UPDATE subscribers SET confirmed = 1, confirmed_at = ? WHERE token = ? AND confirmed = 0 AND unsubscribed_at IS NULL`, time.Now().UTC(), token,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	s, err := d.GetSubscriberByToken(token)
	if err != nil {
		return err
	}
	if s.State() == SubscriberUnsubscribed {
		return ErrUnsubscribed
	}
	return ErrAlreadyConfirmed
}

func (d *Database) ListConfirmedSubscribers() ([]Subscriber, error) {
	return d.querySubscribers(
		`SELECT ` + subscriberColumns + ` FROM subscribers WHERE confirmed = 1 AND unsubscribed_at IS NULL`,
	)
}

// Unsubscribe marks the subscription with this token unsubscribed. The row
// is kept so the token still resolves (for Resubscribe, and for links in
// emails already sent). Unsubscribing twice is not an error.
func (d *Database) Unsubscribe(token string) error {
	res, err := d.db.Exec(
		`UPDATE subscribers SET confirmed = 0, unsubscribed_at = COALESCE(unsubscribed_at, ?) WHERE token = ?`,
		time.Now().UTC(), token,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSubscriberNotFound
	}
	return nil
}

// Resubscribe makes the subscription with this token active again. The token
// only ever reaches its owner's inbox, so holding it is proof enough; no new
// confirmation is needed.
func (d *Database) Resubscribe(token string) error {
	res, err := d.db.Exec(
		`UPDATE subscribers SET confirmed = 1, confirmed_at = ?, unsubscribed_at = NULL WHERE token = ?`,
		time.Now().UTC(), token,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrSubscriberNotFound
	}
	return nil
}

// ReopenSubscriber turns an unsubscribed address back into a pending one,
// for someone who signs up again through the form: they must confirm again
// before anything is sent. The old confirmed_at goes too, so the stored row
// reads as pending everywhere (the subscriber list, the CSV export).
func (d *Database) ReopenSubscriber(id int64) error {
	_, err := d.db.Exec(
		`UPDATE subscribers SET confirmed = 0, confirmed_at = NULL, unsubscribed_at = NULL WHERE id = ? AND unsubscribed_at IS NOT NULL`, id,
	)
	return err
}

// ClaimConfirmationSend records that a confirmation email is about to go to
// subscriber id, unless one went within cooldown. It reports whether the
// caller should send. The check and the write are one statement, so two
// requests at once cannot both send.
func (d *Database) ClaimConfirmationSend(id int64, now time.Time, cooldown time.Duration) (bool, error) {
	res, err := d.db.Exec(
		`UPDATE subscribers SET confirmation_sent_unix = ? WHERE id = ? AND confirmation_sent_unix <= ?`,
		now.Unix(), id, now.Add(-cooldown).Unix(),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ReleaseConfirmationSend clears a claim whose email failed to send, so the
// reader can ask again straight away.
func (d *Database) ReleaseConfirmationSend(id int64) error {
	_, err := d.db.Exec(`UPDATE subscribers SET confirmation_sent_unix = 0 WHERE id = ?`, id)
	return err
}

// --- AdminSession CRUD ---

func (d *Database) CreateSession(s *AdminSession) error {
	_, err := d.db.Exec(
		`INSERT INTO admin_sessions (token, expires_at) VALUES (?, ?)`,
		s.Token, s.ExpiresAt,
	)
	return err
}

func (d *Database) GetSession(token string) (*AdminSession, error) {
	s := &AdminSession{}
	err := d.db.QueryRow(
		`SELECT token, expires_at, created_at FROM admin_sessions WHERE token = ? AND expires_at > ?`, token, time.Now().UTC(),
	).Scan(&s.Token, &s.ExpiresAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (d *Database) DeleteExpiredSessions() error {
	_, err := d.db.Exec(`DELETE FROM admin_sessions WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

// DeleteSession removes a specific session by token.
func (d *Database) DeleteSession(token string) error {
	_, err := d.db.Exec(`DELETE FROM admin_sessions WHERE token = ?`, token)
	return err
}

// ListAllSources returns all sources regardless of active status.
func (d *Database) ListAllSources() ([]Source, error) {
	rows, err := d.db.Query(`SELECT id, name, url, rss_url, scrape_selector, scan_interval, active, created_at, updated_at FROM sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.Name, &s.URL, &s.RSSURL, &s.ScrapeSelector, &s.ScanInterval, &s.Active, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

// ToggleSourceActive flips the active status of a source.
func (d *Database) ToggleSourceActive(id int64) error {
	_, err := d.db.Exec(`UPDATE sources SET active = NOT active, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ListAllSubscribers returns all subscribers regardless of confirmation status.
func (d *Database) ListAllSubscribers() ([]Subscriber, error) {
	return d.querySubscribers(`SELECT ` + subscriberColumns + ` FROM subscribers ORDER BY subscribed_at DESC`)
}

// CountArticleSources returns the number of raw article sources linked to a generated article.
func (d *Database) CountArticleSources(articleID int64) (int, error) {
	var count int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM article_sources WHERE generated_article_id = ?`, articleID).Scan(&count)
	return count, err
}

// GetGeneratedArticleBySlug fetches a generated article by its slug.
func (d *Database) GetGeneratedArticleBySlug(slug string) (*GeneratedArticle, error) {
	a := &GeneratedArticle{}
	err := d.db.QueryRow(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE slug = ?`, slug,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.TagsJSON, &a.LinkedCardsHeading, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt, &a.ExpiresOn, &a.EditorNotes)
	if err != nil {
		return nil, err
	}
	a.separateEditorNotes()
	return a, nil
}

// ListPublishedArticlesFiltered returns approved articles, newest first,
// narrowed to articleType and to articles carrying tag in their tags_json
// array. An empty articleType or tag leaves that filter off, so both empty
// lists every approved article. Ended offers are included: filtered views
// list them, marked as ended.
func (d *Database) ListPublishedArticlesFiltered(articleType, tag string, limit, offset int) ([]GeneratedArticle, error) {
	query := `SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles
		WHERE status = ?`
	args := []any{StatusApproved}
	if articleType != "" {
		query += ` AND type = ?`
		args = append(args, articleType)
	}
	if tag != "" {
		// A malformed tags_json would make json_each fail the whole query,
		// and valid JSON that is not an array (a bare string, an object)
		// would be expanded into values that look like tags. Both count as
		// no tags.
		query += ` AND EXISTS (SELECT 1 FROM json_each(CASE WHEN json_valid(generated_articles.tags_json) AND json_type(generated_articles.tags_json) = 'array' THEN generated_articles.tags_json ELSE '[]' END) WHERE json_each.value = ?)`
		args = append(args, tag)
	}
	query += ` ORDER BY published_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// ListPublishedArticleTypes returns the distinct types of approved articles,
// in no particular order. The homepage offers a type filter only for types
// that have something to show.
func (d *Database) ListPublishedArticleTypes() ([]string, error) {
	rows, err := d.db.Query(`SELECT DISTINCT type FROM generated_articles WHERE status = ?`, StatusApproved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	return types, rows.Err()
}

// ListApprovedArticlesSince returns approved articles published since the given time.
func (d *Database) ListApprovedArticlesSince(since time.Time) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles
		WHERE status = ? AND published_at >= ?
		ORDER BY featured DESC, published_at DESC`,
		StatusApproved, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// GetFeaturedArticle returns the most recent featured approved article, or the most recent approved article if none is featured.
func (d *Database) GetFeaturedArticle() (*GeneratedArticle, error) {
	a := &GeneratedArticle{}
	err := d.db.QueryRow(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE status = ? AND featured = 1 ORDER BY published_at DESC LIMIT 1`, StatusApproved,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.TagsJSON, &a.LinkedCardsHeading, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt, &a.ExpiresOn, &a.EditorNotes)
	if err != nil {
		// Fallback to most recent approved.
		err = d.db.QueryRow(
			`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles WHERE status = ? ORDER BY published_at DESC LIMIT 1`, StatusApproved,
		).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.TLDR, &a.KeyDetails, &a.Links, &a.ImageURL, &a.ImageThumbnail, &a.ImageAttribution, &a.ScoutsTake, &a.RelatedCardsJSON, &a.TagsJSON, &a.LinkedCardsHeading, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt, &a.ExpiresOn, &a.EditorNotes)
		if err != nil {
			return nil, err
		}
	}
	a.separateEditorNotes()
	return a, nil
}

// GetLinkedRawArticles returns the raw articles linked to a generated article.
func (d *Database) GetLinkedRawArticles(generatedArticleID int64) ([]RawArticle, error) {
	rows, err := d.db.Query(
		`SELECT r.id, r.source_id, r.title, r.url, r.content, r.published_at, r.hash, r.created_at
		FROM raw_articles r
		JOIN article_sources a ON r.id = a.raw_article_id
		WHERE a.generated_article_id = ?`, generatedArticleID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var articles []RawArticle
	for rows.Next() {
		var a RawArticle
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Title, &a.URL, &a.Content, &a.PublishedAt, &a.Hash, &a.CreatedAt); err != nil {
			return nil, err
		}
		articles = append(articles, a)
	}
	return articles, rows.Err()
}

// ListRelatedArticles returns up to limit approved articles, other than
// excludeID, that carry at least one of tags: the related stories at the end
// of an article page. Offers still live on today (as ListLivePublishedArticles
// judges it) come first; then articles carrying topic; then those sharing
// more of tags; then the newest. topic may be "" and need not be in tags.
func (d *Database) ListRelatedArticles(excludeID int64, topic string, tags []string, today string, limit int) ([]GeneratedArticle, error) {
	if len(tags) == 0 || limit <= 0 {
		return nil, nil
	}
	// A malformed or non-array tags_json counts as no tags, as in
	// ListPublishedArticlesFiltered.
	const articleTags = `json_each(CASE WHEN json_valid(generated_articles.tags_json) AND json_type(generated_articles.tags_json) = 'array' THEN generated_articles.tags_json ELSE '[]' END)`
	in := strings.TrimSuffix(strings.Repeat("?,", len(tags)), ",")
	// DISTINCT and EXISTS, so a tag repeated in tags_json counts once.
	shared := `(SELECT COUNT(DISTINCT json_each.value) FROM ` + articleTags + ` WHERE json_each.value IN (` + in + `))`
	hasTopic := `EXISTS (SELECT 1 FROM ` + articleTags + ` WHERE json_each.value = ?)`

	args := []any{StatusApproved, excludeID}
	for _, t := range tags {
		args = append(args, t)
	}
	args = append(args, today, topic)
	for _, t := range tags {
		args = append(args, t)
	}
	args = append(args, limit)

	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, tldr, key_details, links, image_url, image_thumbnail, image_attribution, scouts_take, related_cards_json, tags_json, linked_cards_heading, created_at, updated_at, published_at, COALESCE(expires_on, ''), editor_notes FROM generated_articles
		WHERE status = ? AND id != ? AND `+shared+` > 0
		ORDER BY CASE WHEN expires_on IS NULL OR expires_on >= ? THEN 0 ELSE 1 END,
			`+hasTopic+` DESC, `+shared+` DESC, published_at DESC, id DESC
		LIMIT ?`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

// --- ReferencePage CRUD ---

func (d *Database) CreateReferencePage(page *ReferencePage) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO reference_pages (slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		page.Slug, page.Title, page.Category, page.Issuer, page.SectionsJSON, page.RecentChangesJSON, page.PendingUpdatesJSON, page.ScoutsVerdict, page.KeywordsJSON, page.ImageURL, page.ApplyURL, page.AnnualFeeAmount, page.AnnualFeeNote, page.WelcomeBonusText, page.WelcomeBonusVerifiedAt, page.EarningRatesJSON, page.CreditsJSON, page.BenefitsJSON, page.ProtectionsJSON, page.FlaggedFieldsJSON, page.Status,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := d.SyncCardFTS(id); err != nil {
		slog.Warn("FTS sync failed", "card_id", id, "error", err)
	}
	return id, nil
}

func (d *Database) GetReferencePage(id int64) (*ReferencePage, error) {
	p := &ReferencePage{}
	err := d.db.QueryRow(
		`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE id = ?`, id,
	).Scan(&p.ID, &p.Slug, &p.Title, &p.Category, &p.Issuer, &p.SectionsJSON, &p.RecentChangesJSON, &p.PendingUpdatesJSON, &p.ScoutsVerdict, &p.KeywordsJSON, &p.ImageURL, &p.ApplyURL, &p.AnnualFeeAmount, &p.AnnualFeeNote, &p.WelcomeBonusText, &p.WelcomeBonusVerifiedAt, &p.EarningRatesJSON, &p.CreditsJSON, &p.BenefitsJSON, &p.ProtectionsJSON, &p.FlaggedFieldsJSON, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (d *Database) GetReferencePageBySlug(slug string) (*ReferencePage, error) {
	p := &ReferencePage{}
	err := d.db.QueryRow(
		`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE slug = ?`, slug,
	).Scan(&p.ID, &p.Slug, &p.Title, &p.Category, &p.Issuer, &p.SectionsJSON, &p.RecentChangesJSON, &p.PendingUpdatesJSON, &p.ScoutsVerdict, &p.KeywordsJSON, &p.ImageURL, &p.ApplyURL, &p.AnnualFeeAmount, &p.AnnualFeeNote, &p.WelcomeBonusText, &p.WelcomeBonusVerifiedAt, &p.EarningRatesJSON, &p.CreditsJSON, &p.BenefitsJSON, &p.ProtectionsJSON, &p.FlaggedFieldsJSON, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ListReferencePages returns all reference pages, optionally filtered by category.
func (d *Database) ListReferencePages(category string) ([]ReferencePage, error) {
	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = d.db.Query(
			`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE category = ? ORDER BY issuer, title`, category,
		)
	} else {
		rows, err = d.db.Query(
			`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages ORDER BY issuer, title`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferencePages(rows)
}

// ListPublishedReferencePages returns published reference pages, optionally filtered by category.
func (d *Database) ListPublishedReferencePages(category string) ([]ReferencePage, error) {
	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = d.db.Query(
			`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE status = 'published' AND category = ? ORDER BY issuer, title`, category,
		)
	} else {
		rows, err = d.db.Query(
			`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE status = 'published' ORDER BY issuer, title`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferencePages(rows)
}

func (d *Database) UpdateReferencePage(page *ReferencePage) error {
	_, err := d.db.Exec(
		`UPDATE reference_pages SET slug = ?, title = ?, category = ?, issuer = ?, sections_json = ?, recent_changes_json = ?, pending_updates_json = ?, scouts_verdict = ?, keywords_json = ?, image_url = ?, apply_url = ?, annual_fee_amount = ?, annual_fee_note = ?, welcome_bonus_text = ?, welcome_bonus_verified_at = ?, earning_rates_json = ?, credits_json = ?, benefits_json = ?, protections_json = ?, flagged_fields_json = ?, status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		page.Slug, page.Title, page.Category, page.Issuer, page.SectionsJSON, page.RecentChangesJSON, page.PendingUpdatesJSON, page.ScoutsVerdict, page.KeywordsJSON, page.ImageURL, page.ApplyURL, page.AnnualFeeAmount, page.AnnualFeeNote, page.WelcomeBonusText, page.WelcomeBonusVerifiedAt, page.EarningRatesJSON, page.CreditsJSON, page.BenefitsJSON, page.ProtectionsJSON, page.FlaggedFieldsJSON, page.Status, page.ID,
	)
	if err != nil {
		return err
	}
	if err := d.SyncCardFTS(page.ID); err != nil {
		slog.Warn("FTS sync failed", "card_id", page.ID, "error", err)
	}
	return nil
}

// AppendRecentChange prepends a new RecentChange to a reference page's
// recent_changes_json, keeping at most 10 entries.
func (d *Database) AppendRecentChange(cardID int64, change RecentChange) error {
	page, err := d.GetReferencePage(cardID)
	if err != nil {
		return fmt.Errorf("getting reference page: %w", err)
	}

	var changes []RecentChange
	if page.RecentChangesJSON != "" && page.RecentChangesJSON != "[]" {
		if err := json.Unmarshal([]byte(page.RecentChangesJSON), &changes); err != nil {
			return fmt.Errorf("unmarshaling recent changes: %w", err)
		}
	}

	// Prepend
	changes = append([]RecentChange{change}, changes...)

	// Keep max 10
	if len(changes) > 10 {
		changes = changes[:10]
	}

	data, err := json.Marshal(changes)
	if err != nil {
		return fmt.Errorf("marshaling recent changes: %w", err)
	}

	_, err = d.db.Exec(`UPDATE reference_pages SET recent_changes_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(data), cardID)
	return err
}

func (d *Database) DeleteReferencePage(id int64) error {
	_, err := d.db.Exec(`DELETE FROM reference_pages WHERE id = ?`, id)
	return err
}

// GetReferencePagesByKeyword returns published reference pages whose keywords_json contains the given keyword (case-insensitive).
func (d *Database) GetReferencePagesByKeyword(keyword string) ([]ReferencePage, error) {
	pattern := "%" + strings.ToLower(keyword) + "%"
	rows, err := d.db.Query(
		`SELECT id, slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, flagged_fields_json, status, created_at, updated_at FROM reference_pages WHERE status = 'published' AND LOWER(keywords_json) LIKE ? ORDER BY title`, pattern,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanReferencePages(rows)
}

func scanReferencePages(rows *sql.Rows) ([]ReferencePage, error) {
	var pages []ReferencePage
	for rows.Next() {
		var p ReferencePage
		if err := rows.Scan(&p.ID, &p.Slug, &p.Title, &p.Category, &p.Issuer, &p.SectionsJSON, &p.RecentChangesJSON, &p.PendingUpdatesJSON, &p.ScoutsVerdict, &p.KeywordsJSON, &p.ImageURL, &p.ApplyURL, &p.AnnualFeeAmount, &p.AnnualFeeNote, &p.WelcomeBonusText, &p.WelcomeBonusVerifiedAt, &p.EarningRatesJSON, &p.CreditsJSON, &p.BenefitsJSON, &p.ProtectionsJSON, &p.FlaggedFieldsJSON, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

// ComparisonCard holds the structured data needed to render a side-by-side comparison.
type ComparisonCard struct {
	ID                     int64
	Slug                   string
	Title                  string
	Issuer                 string
	ImageURL               string
	AnnualFeeAmount        int
	AnnualFeeNote          string
	WelcomeBonusText       string
	WelcomeBonusVerifiedAt *time.Time
	EarningRatesJSON       string
	CreditsJSON            string
	BenefitsJSON           string
	ProtectionsJSON        string
	ScoutsVerdict          string
}

// GetComparisonData returns structured comparison data for the given slugs
// (published cards only),
// sorted to match the input slug order.
func (d *Database) GetComparisonData(slugs []string) ([]ComparisonCard, error) {
	if len(slugs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(slugs))
	args := make([]interface{}, len(slugs))
	for i, s := range slugs {
		placeholders[i] = "?"
		args[i] = s
	}

	query := `SELECT id, slug, title, issuer, image_url, annual_fee_amount, annual_fee_note, welcome_bonus_text, welcome_bonus_verified_at, earning_rates_json, credits_json, benefits_json, protections_json, scouts_verdict FROM reference_pages WHERE status = 'published' AND slug IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cardMap := make(map[string]ComparisonCard)
	for rows.Next() {
		var c ComparisonCard
		if err := rows.Scan(&c.ID, &c.Slug, &c.Title, &c.Issuer, &c.ImageURL, &c.AnnualFeeAmount, &c.AnnualFeeNote, &c.WelcomeBonusText, &c.WelcomeBonusVerifiedAt, &c.EarningRatesJSON, &c.CreditsJSON, &c.BenefitsJSON, &c.ProtectionsJSON, &c.ScoutsVerdict); err != nil {
			return nil, err
		}
		cardMap[c.Slug] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Return in the same order as the input slugs.
	result := make([]ComparisonCard, 0, len(slugs))
	for _, s := range slugs {
		if c, ok := cardMap[s]; ok {
			result = append(result, c)
		}
	}
	return result, nil
}

// FlaggedField represents a single flagged field entry.
type FlaggedField struct {
	Field     string `json:"field"`
	Note      string `json:"note"`
	FlaggedAt string `json:"flagged_at"`
}

// AppendFlaggedField adds a flagged-field entry to a reference page's flagged_fields_json.
func (d *Database) AppendFlaggedField(cardID int64, field, note string) error {
	var raw string
	err := d.db.QueryRow(`SELECT flagged_fields_json FROM reference_pages WHERE id = ?`, cardID).Scan(&raw)
	if err != nil {
		return fmt.Errorf("loading flagged fields: %w", err)
	}

	var flags []FlaggedField
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &flags); err != nil {
			return fmt.Errorf("unmarshaling flagged fields: %w", err)
		}
	}

	flags = append(flags, FlaggedField{
		Field:     field,
		Note:      note,
		FlaggedAt: time.Now().UTC().Format(time.RFC3339),
	})

	data, err := json.Marshal(flags)
	if err != nil {
		return fmt.Errorf("marshaling flagged fields: %w", err)
	}

	_, err = d.db.Exec(`UPDATE reference_pages SET flagged_fields_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(data), cardID)
	return err
}

// ResolveFlaggedField removes all flagged-field entries matching the given field name.
func (d *Database) ResolveFlaggedField(cardID int64, field string) error {
	var raw string
	err := d.db.QueryRow(`SELECT flagged_fields_json FROM reference_pages WHERE id = ?`, cardID).Scan(&raw)
	if err != nil {
		return fmt.Errorf("loading flagged fields: %w", err)
	}

	var flags []FlaggedField
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &flags); err != nil {
			return fmt.Errorf("unmarshaling flagged fields: %w", err)
		}
	}

	var kept []FlaggedField
	for _, f := range flags {
		if f.Field != field {
			kept = append(kept, f)
		}
	}
	if kept == nil {
		kept = []FlaggedField{}
	}

	data, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("marshaling flagged fields: %w", err)
	}

	_, err = d.db.Exec(`UPDATE reference_pages SET flagged_fields_json = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, string(data), cardID)
	return err
}

// --- FTS Sync ---

// SyncArticleFTS updates the FTS index for a single article.
func (d *Database) SyncArticleFTS(id int64) error {
	var title, tldr, content string
	err := d.db.QueryRow(`SELECT title, tldr, content FROM generated_articles WHERE id = ?`, id).Scan(&title, &tldr, &content)
	if err != nil {
		return err
	}
	// Delete existing entry, then insert.
	d.db.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id)
	_, err = d.db.Exec(
		`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`,
		id, stripHTMLForFTS(title), stripHTMLForFTS(tldr), articleFTSContent(content),
	)
	return err
}

// SyncArticleFTSTx is SyncArticleFTS inside the caller's transaction, so an
// article rewrite and its index entry commit or roll back together. Unlike
// SyncArticleFTS it reports a failed delete too: a stale entry left behind
// next to the new one is exactly what the transaction is there to prevent.
func SyncArticleFTSTx(tx *sql.Tx, id int64) error {
	var title, tldr, content string
	err := tx.QueryRow(`SELECT title, COALESCE(tldr,''), content FROM generated_articles WHERE id = ?`, id).Scan(&title, &tldr, &content)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`,
		id, stripHTMLForFTS(title), stripHTMLForFTS(tldr), articleFTSContent(content),
	)
	return err
}

// articleFTSContent is an article's content as the search index holds it:
// without editor notes, which search results must never show, and without
// markup.
func articleFTSContent(content string) string {
	return stripHTMLForFTS(editornote.Strip(content))
}

// SyncCardFTS updates the FTS index for a single reference page.
func (d *Database) SyncCardFTS(id int64) error {
	var title, sectionsJSON, verdict string
	err := d.db.QueryRow(`SELECT title, sections_json, scouts_verdict FROM reference_pages WHERE id = ?`, id).Scan(&title, &sectionsJSON, &verdict)
	if err != nil {
		return err
	}
	d.db.Exec(`DELETE FROM cards_fts WHERE rowid = ?`, id)
	_, err = d.db.Exec(
		`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (?, ?, ?, ?)`,
		id, stripHTMLForFTS(title), extractSectionsText(sectionsJSON), stripHTMLForFTS(verdict),
	)
	return err
}

// DeleteArticleFTS removes an article from the FTS index.
func (d *Database) DeleteArticleFTS(id int64) error {
	_, err := d.db.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id)
	return err
}

// DeleteCardFTS removes a card from the FTS index.
func (d *Database) DeleteCardFTS(id int64) error {
	_, err := d.db.Exec(`DELETE FROM cards_fts WHERE rowid = ?`, id)
	return err
}

// --- Newsletter Candidate CRUD ---

// CreateNewsletterCandidate inserts a newsletter candidate record.
func (d *Database) CreateNewsletterCandidate(c *NewsletterCandidate) error {
	_, err := d.db.Exec(
		`INSERT INTO newsletter_candidates (newsletter_id, article_id, rank, featured) VALUES (?, ?, ?, ?)`,
		c.NewsletterID, c.ArticleID, c.Rank, c.Featured,
	)
	return err
}

// ListCandidatesForNewsletter returns all candidates for a newsletter ordered by rank.
func (d *Database) ListCandidatesForNewsletter(newsletterID int64) ([]NewsletterCandidate, error) {
	rows, err := d.db.Query(
		`SELECT id, newsletter_id, article_id, rank, featured, created_at FROM newsletter_candidates WHERE newsletter_id = ? ORDER BY rank ASC`,
		newsletterID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []NewsletterCandidate
	for rows.Next() {
		var c NewsletterCandidate
		if err := rows.Scan(&c.ID, &c.NewsletterID, &c.ArticleID, &c.Rank, &c.Featured, &c.CreatedAt); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	return candidates, rows.Err()
}

// SetFeaturedCandidate unsets all featured flags for a newsletter, then sets the specified article as featured.
func (d *Database) SetFeaturedCandidate(newsletterID, articleID int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE newsletter_candidates SET featured = 0 WHERE newsletter_id = ?`, newsletterID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE newsletter_candidates SET featured = 1 WHERE newsletter_id = ? AND article_id = ?`, newsletterID, articleID); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdateNewsletterStatus updates the status of a newsletter.
func (d *Database) UpdateNewsletterStatus(id int64, status string) error {
	_, err := d.db.Exec(`UPDATE newsletters SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateNewsletterHTML persists rendered HTML content to the newsletter record.
func (d *Database) UpdateNewsletterHTML(id int64, htmlContent string) error {
	_, err := d.db.Exec(`UPDATE newsletters SET html_content = ? WHERE id = ?`, htmlContent, id)
	return err
}

// UpdateNewsletterCardOfWeek sets the card_of_week_id for a newsletter.
func (d *Database) UpdateNewsletterCardOfWeek(id int64, cardID int64) error {
	_, err := d.db.Exec(`UPDATE newsletters SET card_of_week_id = ? WHERE id = ?`, cardID, id)
	return err
}

// UpdateNewsletterScoutsPick sets the scouts_pick_article_id for a newsletter.
func (d *Database) UpdateNewsletterScoutsPick(id int64, articleID int64) error {
	_, err := d.db.Exec(`UPDATE newsletters SET scouts_pick_article_id = ? WHERE id = ?`, articleID, id)
	return err
}
