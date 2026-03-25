package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Database wraps a *sql.DB connection with application-specific methods.
type Database struct {
	db *sql.DB
}

// Open opens a SQLite database at the given path, enables WAL mode, and runs migrations.
func Open(path string) (*Database, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	// Enable foreign keys.
	if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
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

func (d *Database) ListActiveSources() ([]Source, error) {
	rows, err := d.db.Query(`SELECT id, name, url, rss_url, scrape_selector, scan_interval, active, created_at, updated_at FROM sources WHERE active = 1`)
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
		`SELECT id, source_id, title, url, content, published_at, hash, created_at FROM raw_articles WHERE source_id = ?`, sourceID,
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
	res, err := d.db.Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number, featured) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Title, a.Slug, a.Content, a.Type, a.Status, a.SourcesJSON, a.RevisionNumber, a.Featured,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) GetGeneratedArticle(id int64) (*GeneratedArticle, error) {
	a := &GeneratedArticle{}
	err := d.db.QueryRow(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE id = ?`, id,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// validTransitions defines allowed status transitions.
var validTransitions = map[string][]string{
	StatusDraft:             {StatusPending},
	StatusPending:           {StatusApproved, StatusRejected, StatusRevisionRequested},
	StatusApproved:          {StatusUnpublished},
	StatusRejected:          {},
	StatusRevisionRequested: {StatusDraft},
	StatusUnpublished:       {StatusRevisionRequested, StatusApproved},
}

func (d *Database) UpdateArticleStatus(id int64, newStatus string) error {
	a, err := d.GetGeneratedArticle(id)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}

	allowed := validTransitions[a.Status]
	valid := false
	for _, s := range allowed {
		if s == newStatus {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid status transition from %q to %q", a.Status, newStatus)
	}

	query := `UPDATE generated_articles SET status = ?, updated_at = CURRENT_TIMESTAMP`
	args := []interface{}{newStatus}
	if newStatus == StatusApproved {
		query += `, published_at = CURRENT_TIMESTAMP`
	}
	query += ` WHERE id = ?`
	args = append(args, id)

	_, err = d.db.Exec(query, args...)
	return err
}

func (d *Database) ListArticlesByStatus(status string) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE status = ? ORDER BY created_at DESC`, status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

func (d *Database) ListPublishedArticles(limit, offset int) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE status = ? ORDER BY published_at DESC LIMIT ? OFFSET ?`,
		StatusApproved, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanGeneratedArticles(rows)
}

func scanGeneratedArticles(rows *sql.Rows) ([]GeneratedArticle, error) {
	var articles []GeneratedArticle
	for rows.Next() {
		var a GeneratedArticle
		if err := rows.Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt); err != nil {
			return nil, err
		}
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

func (d *Database) ListFeedbackForArticle(articleID int64) ([]AdminFeedback, error) {
	rows, err := d.db.Query(
		`SELECT id, generated_article_id, feedback_text, source, created_at FROM admin_feedback WHERE generated_article_id = ? ORDER BY created_at DESC`, articleID,
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

// UpdateArticleContent updates the content, title, and revision_number of a generated article.
func (d *Database) UpdateArticleContent(id int64, title, content string, revisionNumber int) error {
	_, err := d.db.Exec(
		`UPDATE generated_articles SET title = ?, content = ?, revision_number = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		title, content, revisionNumber, id,
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
	vc := &VoteCounts{}
	err := d.db.QueryRow(
		`SELECT COALESCE(SUM(CASE WHEN vote > 0 THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN vote < 0 THEN 1 ELSE 0 END), 0) FROM article_votes WHERE article_id = ?`, articleID,
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

func (d *Database) CreateNewsletter(n *Newsletter) (int64, error) {
	res, err := d.db.Exec(
		`INSERT INTO newsletters (date, subject, html_content, status, article_count) VALUES (?, ?, ?, ?, ?)`,
		n.Date, n.Subject, n.HTMLContent, n.Status, n.ArticleCount,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *Database) GetNewsletterByDate(date string) (*Newsletter, error) {
	n := &Newsletter{}
	err := d.db.QueryRow(
		`SELECT id, date, subject, html_content, status, article_count, sent_at FROM newsletters WHERE date = ?`, date,
	).Scan(&n.ID, &n.Date, &n.Subject, &n.HTMLContent, &n.Status, &n.ArticleCount, &n.SentAt)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func (d *Database) MarkNewsletterSent(id int64) error {
	_, err := d.db.Exec(`UPDATE newsletters SET status = 'sent', sent_at = ? WHERE id = ?`, time.Now().UTC(), id)
	return err
}

// ListNewsletters returns all newsletters ordered by date descending.
func (d *Database) ListNewsletters() ([]Newsletter, error) {
	rows, err := d.db.Query(
		`SELECT id, date, subject, html_content, status, article_count, sent_at FROM newsletters ORDER BY date DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var newsletters []Newsletter
	for rows.Next() {
		var n Newsletter
		if err := rows.Scan(&n.ID, &n.Date, &n.Subject, &n.HTMLContent, &n.Status, &n.ArticleCount, &n.SentAt); err != nil {
			return nil, err
		}
		newsletters = append(newsletters, n)
	}
	return newsletters, rows.Err()
}

// --- Subscriber CRUD ---

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

func (d *Database) ConfirmSubscriber(token string) error {
	res, err := d.db.Exec(
		`UPDATE subscribers SET confirmed = 1, confirmed_at = ? WHERE token = ? AND confirmed = 0`, time.Now().UTC(), token,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("subscriber not found or already confirmed")
	}
	return nil
}

func (d *Database) ListConfirmedSubscribers() ([]Subscriber, error) {
	rows, err := d.db.Query(
		`SELECT id, email, confirmed, token, subscribed_at, confirmed_at FROM subscribers WHERE confirmed = 1`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscribers []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.ID, &s.Email, &s.Confirmed, &s.Token, &s.SubscribedAt, &s.ConfirmedAt); err != nil {
			return nil, err
		}
		subscribers = append(subscribers, s)
	}
	return subscribers, rows.Err()
}

func (d *Database) Unsubscribe(token string) error {
	res, err := d.db.Exec(`DELETE FROM subscribers WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("subscriber not found")
	}
	return nil
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
	rows, err := d.db.Query(
		`SELECT id, email, confirmed, token, subscribed_at, confirmed_at FROM subscribers ORDER BY subscribed_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscribers []Subscriber
	for rows.Next() {
		var s Subscriber
		if err := rows.Scan(&s.ID, &s.Email, &s.Confirmed, &s.Token, &s.SubscribedAt, &s.ConfirmedAt); err != nil {
			return nil, err
		}
		subscribers = append(subscribers, s)
	}
	return subscribers, rows.Err()
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
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE slug = ?`, slug,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ListPublishedArticlesByType returns approved articles filtered by type.
func (d *Database) ListPublishedArticlesByType(articleType string, limit, offset int) ([]GeneratedArticle, error) {
	rows, err := d.db.Query(
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE status = ? AND type = ? ORDER BY published_at DESC LIMIT ? OFFSET ?`,
		StatusApproved, articleType, limit, offset,
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
		`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE status = ? AND featured = 1 ORDER BY published_at DESC LIMIT 1`, StatusApproved,
	).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt)
	if err != nil {
		// Fallback to most recent approved.
		err = d.db.QueryRow(
			`SELECT id, title, slug, content, type, status, sources_json, revision_number, featured, created_at, updated_at, published_at FROM generated_articles WHERE status = ? ORDER BY published_at DESC LIMIT 1`, StatusApproved,
		).Scan(&a.ID, &a.Title, &a.Slug, &a.Content, &a.Type, &a.Status, &a.SourcesJSON, &a.RevisionNumber, &a.Featured, &a.CreatedAt, &a.UpdatedAt, &a.PublishedAt)
		if err != nil {
			return nil, err
		}
	}
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
