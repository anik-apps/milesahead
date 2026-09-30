package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/offer"
)

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS sources (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		rss_url TEXT,
		scrape_selector TEXT,
		scan_interval INTEGER,
		active BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS raw_articles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_id INTEGER NOT NULL REFERENCES sources(id),
		title TEXT NOT NULL,
		url TEXT NOT NULL,
		content TEXT NOT NULL,
		published_at DATETIME NOT NULL,
		hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_raw_articles_hash ON raw_articles(hash);

	CREATE TABLE IF NOT EXISTS generated_articles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		content TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'article',
		status TEXT NOT NULL DEFAULT 'draft',
		sources_json TEXT NOT NULL DEFAULT '[]',
		revision_number INTEGER NOT NULL DEFAULT 1,
		featured BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		published_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_generated_articles_slug ON generated_articles(slug);
	CREATE INDEX IF NOT EXISTS idx_generated_articles_status ON generated_articles(status);

	CREATE TABLE IF NOT EXISTS article_sources (
		generated_article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		raw_article_id INTEGER NOT NULL REFERENCES raw_articles(id),
		PRIMARY KEY (generated_article_id, raw_article_id)
	);

	-- Raw articles an existing generated article already covers without
	-- citing them: late coverage of a story that was skipped as a duplicate.
	-- Kept apart from article_sources so an article's public source list is
	-- only ever what it was written from. The duplicate check reads both, so a
	-- covered raw cannot seed a second article about the same story after the
	-- raws it was written from leave the 48h clustering window.
	CREATE TABLE IF NOT EXISTS covered_raws (
		raw_article_id INTEGER PRIMARY KEY REFERENCES raw_articles(id),
		generated_article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		reason TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_covered_raws_article ON covered_raws(generated_article_id);

	CREATE TABLE IF NOT EXISTS verification_results (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		generated_article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		revision_number INTEGER NOT NULL,
		score INTEGER NOT NULL,
		issues_json TEXT NOT NULL DEFAULT '[]',
		factual_check TEXT NOT NULL,
		attribution_check TEXT NOT NULL,
		completeness_check TEXT NOT NULL,
		hallucination_check TEXT NOT NULL,
		verified_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS admin_feedback (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		generated_article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		feedback_text TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'admin',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS article_revisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		generated_article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		revision_number INTEGER NOT NULL,
		previous_content TEXT NOT NULL,
		new_content TEXT NOT NULL,
		feedback_id INTEGER NOT NULL REFERENCES admin_feedback(id),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS article_votes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		vote INTEGER NOT NULL,
		ip_hash TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS newsletters (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL UNIQUE,
		subject TEXT NOT NULL,
		html_content TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'draft',
		article_count INTEGER NOT NULL DEFAULT 0,
		sent_at DATETIME
	);

	CREATE TABLE IF NOT EXISTS subscribers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE,
		confirmed BOOLEAN NOT NULL DEFAULT 0,
		token TEXT NOT NULL UNIQUE,
		subscribed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		confirmed_at DATETIME
	);
	CREATE INDEX IF NOT EXISTS idx_subscribers_token ON subscribers(token);

	CREATE TABLE IF NOT EXISTS admin_sessions (
		token TEXT PRIMARY KEY,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`)
	if err != nil {
		return err
	}

	// Create reference_pages table.
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS reference_pages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		slug TEXT UNIQUE NOT NULL,
		title TEXT NOT NULL,
		category TEXT NOT NULL DEFAULT 'credit-card',
		issuer TEXT NOT NULL DEFAULT '',
		sections_json TEXT NOT NULL DEFAULT '[]',
		recent_changes_json TEXT NOT NULL DEFAULT '[]',
		pending_updates_json TEXT NOT NULL DEFAULT '[]',
		scouts_verdict TEXT NOT NULL DEFAULT '',
		keywords_json TEXT NOT NULL DEFAULT '[]',
		image_url TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'draft',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_reference_pages_slug ON reference_pages(slug);
	CREATE INDEX IF NOT EXISTS idx_reference_pages_category ON reference_pages(category);
	`)
	if err != nil {
		return err
	}

	// Create newsletter_candidates table.
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS newsletter_candidates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		newsletter_id INTEGER NOT NULL REFERENCES newsletters(id),
		article_id INTEGER NOT NULL REFERENCES generated_articles(id),
		rank INTEGER NOT NULL,
		featured BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(newsletter_id, article_id)
	);
	CREATE INDEX IF NOT EXISTS idx_newsletter_candidates_newsletter ON newsletter_candidates(newsletter_id);
	`)
	if err != nil {
		return err
	}

	// Add new columns for structured content (idempotent).
	alterStatements := []string{
		`ALTER TABLE generated_articles ADD COLUMN tldr TEXT DEFAULT ''`,
		`ALTER TABLE generated_articles ADD COLUMN key_details TEXT DEFAULT '[]'`,
		`ALTER TABLE generated_articles ADD COLUMN links TEXT DEFAULT '[]'`,
		`ALTER TABLE generated_articles ADD COLUMN image_url TEXT DEFAULT ''`,
		`ALTER TABLE generated_articles ADD COLUMN image_thumbnail TEXT DEFAULT ''`,
		`ALTER TABLE generated_articles ADD COLUMN image_attribution TEXT DEFAULT ''`,
		`ALTER TABLE generated_articles ADD COLUMN scouts_take TEXT DEFAULT ''`,
		`ALTER TABLE reference_pages ADD COLUMN apply_url TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE generated_articles ADD COLUMN related_cards_json TEXT DEFAULT '[]'`,
		`ALTER TABLE generated_articles ADD COLUMN tags_json TEXT DEFAULT '[]'`,
		`ALTER TABLE generated_articles ADD COLUMN linked_cards_heading TEXT DEFAULT ''`,
		`ALTER TABLE newsletters ADD COLUMN card_of_week_id INTEGER DEFAULT NULL`,
		`ALTER TABLE newsletters ADD COLUMN scouts_pick_article_id INTEGER DEFAULT NULL`,
		// When an issue was generated: a daily sent later is re-rendered
		// from the articles it was generated from.
		`ALTER TABLE newsletters ADD COLUMN created_at DATETIME DEFAULT NULL`,
		`ALTER TABLE reference_pages ADD COLUMN annual_fee_amount INTEGER DEFAULT 0`,
		`ALTER TABLE reference_pages ADD COLUMN annual_fee_note TEXT DEFAULT ''`,
		`ALTER TABLE reference_pages ADD COLUMN welcome_bonus_text TEXT DEFAULT ''`,
		`ALTER TABLE reference_pages ADD COLUMN welcome_bonus_verified_at DATETIME DEFAULT NULL`,
		`ALTER TABLE reference_pages ADD COLUMN earning_rates_json TEXT DEFAULT '{}'`,
		`ALTER TABLE reference_pages ADD COLUMN credits_json TEXT DEFAULT '{}'`,
		`ALTER TABLE reference_pages ADD COLUMN benefits_json TEXT DEFAULT '{}'`,
		`ALTER TABLE reference_pages ADD COLUMN protections_json TEXT DEFAULT '{}'`,
		`ALTER TABLE reference_pages ADD COLUMN flagged_fields_json TEXT DEFAULT '[]'`,
		// Enrichment bookkeeping. Without these, a reconciler cannot tell
		// "never attempted" from "attempted and the model returned nothing",
		// so it would either retry a hopeless article forever or skip a
		// recoverable one. They also make "how many articles are degraded"
		// a query rather than a guess.
		`ALTER TABLE generated_articles ADD COLUMN enrichment_attempts INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE generated_articles ADD COLUMN enrichment_last_at DATETIME DEFAULT NULL`,
		// Last day of the offer an article describes, "2006-01-02", or NULL.
		// TEXT rather than DATE: the driver would turn a DATE into a
		// time.Time at midnight UTC, and this is a calendar day in the site's
		// timezone. ISO strings compare correctly in SQL as they are.
		`ALTER TABLE generated_articles ADD COLUMN expires_on TEXT DEFAULT NULL`,
		// Revision bookkeeping for the revision reconciler. A rewrite used to
		// run once, from the goroutine the Revise button started; if it failed
		// or a deploy restarted the process mid-run, the article sat in
		// revision_requested forever. The reconciler retries, and these bound
		// it: failures counts consecutive failed rewrites, retry_after (unix
		// seconds, 0 = now) is the backoff, and last_error is shown on the
		// dashboard once the budget is spent. In the database rather than in
		// memory so a deploy neither forgets the backoff nor hands a
		// permanently failing article a fresh budget.
		`ALTER TABLE generated_articles ADD COLUMN revision_failures INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE generated_articles ADD COLUMN revision_retry_after INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE generated_articles ADD COLUMN revision_last_error TEXT NOT NULL DEFAULT ''`,
	}
	for _, stmt := range alterStatements {
		_, err := db.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}

	// Create FTS5 virtual tables for full-text search.
	ftsStatements := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS articles_fts USING fts5(title, tldr, content)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS cards_fts USING fts5(title, sections_text, scouts_verdict)`,
	}
	for _, stmt := range ftsStatements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("creating FTS table: %w", err)
		}
	}

	if err := populateFTS(db); err != nil {
		return fmt.Errorf("populating FTS: %w", err)
	}

	if err := repairRottedImages(db); err != nil {
		return fmt.Errorf("repairing rotted images: %w", err)
	}

	if n, err := backfillOfferEndDates(db); err != nil {
		return fmt.Errorf("deriving offer end dates: %w", err)
	} else if n > 0 {
		slog.Info("derived offer end dates from key details", "rows", n)
	}

	return nil
}

// backfillOfferEndDates derives expires_on from key_details for every
// article, anchored on the row's created_at exactly as CreateGeneratedArticle
// anchors on "now".
//
// It runs on every startup and writes only the rows whose stored value
// differs from the derived one, so it converges and then does nothing. That
// also means existing rows pick up any later improvement to the parser on the
// next deploy, with no manual step. It returns how many rows it changed.
func backfillOfferEndDates(conn *sql.DB) (int, error) {
	changes, err := offerEndChanges(conn)
	if err != nil || len(changes) == 0 {
		return 0, err
	}
	return applyOfferEndChanges(conn, changes)
}

// offerEndChange is one row whose stored expires_on differs from what its
// key_details give, with the values it was read with.
type offerEndChange struct {
	id      int64
	details string // key_details as read
	have    string // expires_on as read
	want    string // expires_on derived from details
}

// offerEndChanges reads every article and returns the ones whose stored end
// date is not the one their key details give.
func offerEndChanges(conn *sql.DB) ([]offerEndChange, error) {
	rows, err := conn.Query(`SELECT id, COALESCE(key_details, ''), created_at, COALESCE(expires_on, '') FROM generated_articles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var changes []offerEndChange
	for rows.Next() {
		var (
			c         offerEndChange
			createdAt any
		)
		if err := rows.Scan(&c.id, &c.details, &createdAt, &c.have); err != nil {
			return nil, err
		}
		if c.want = offer.EndDate(c.details, writtenAt(createdAt)); c.want != c.have {
			changes = append(changes, c)
		}
	}
	return changes, rows.Err()
}

// applyOfferEndChanges writes the derived end dates. Another process can be
// writing the same database meanwhile -- the running server when a command
// such as seed-structured opens it, or the outgoing container during a
// deploy -- so a row may have been rewritten since it was read: an
// enrichment pass storing new key details and their end date, say. Each
// UPDATE therefore applies only if the row still holds the key details and
// end date it was read with; a row that moved on keeps its newer value. It
// returns how many rows it changed.
func applyOfferEndChanges(conn *sql.DB, changes []offerEndChange) (int, error) {
	tx, err := conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, c := range changes {
		res, err := tx.Exec(`UPDATE generated_articles SET expires_on = ?
			WHERE id = ? AND COALESCE(expires_on, '') = ? AND COALESCE(key_details, '') = ?`,
			nullIfEmpty(c.want), c.id, c.have, c.details)
		if err != nil {
			return 0, err
		}
		if k, err := res.RowsAffected(); err == nil {
			n += int(k)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// writtenAt turns a scanned created_at into the anchor offer.EndDate needs.
// The driver returns DATETIME columns as time.Time; anything else (NULL, or
// text it could not parse) falls back to now rather than failing the
// migration, since the anchor only matters for a date written without a year.
func writtenAt(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano, "2006-01-02"} {
			if p, err := time.Parse(layout, t); err == nil {
				return p
			}
		}
	}
	return time.Now().UTC()
}

// repairRottedImages fixes article images whose stored URL can no longer be
// fetched by a browser. Both cases below were visible on the live homepage as
// broken thumbnails, because the card renders image_thumbnail.
//
// Both statements are idempotent: each one stops matching the rows it has
// already fixed, so running on every startup converges and then does nothing.
func repairRottedImages(conn *sql.DB) error {
	// Openverse thumbnails are a proxy (api.openverse.org/.../thumb/) that
	// 424s whenever the upstream fetch fails. image_url already holds the
	// provider's own stable CDN link for these rows, so promote it.
	res, err := conn.Exec(`
		UPDATE generated_articles
		SET image_thumbnail = image_url
		WHERE image_thumbnail LIKE 'https://api.openverse.org/%'
		  AND image_url IS NOT NULL AND image_url != ''`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("repaired openverse proxy thumbnails", "rows", n)
	}

	// Pixabay's hotlink URLs are signed and expire by design -- the API
	// terms require downloading rather than hotlinking -- so every one of
	// these eventually 400s with "This URL is invalid or has expired." There
	// is nothing to promote, so clear the image and reset the attempt counter
	// to hand the row back to the enrichment reconciler, which will re-pick
	// from a source that does not rot.
	res, err = conn.Exec(`
		UPDATE generated_articles
		SET image_url = '', image_thumbnail = '', image_attribution = '',
		    enrichment_attempts = 0
		WHERE image_url LIKE 'https://pixabay.com/get/%'
		   OR image_thumbnail LIKE 'https://pixabay.com/get/%'`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Info("cleared expired pixabay images for re-enrichment", "rows", n)
	}
	return nil
}

// populateFTS backfills FTS tables from existing data. It only populates
// tables that are empty, so it is safe to call on every startup.
func populateFTS(conn *sql.DB) error {
	var articleCount, cardCount int
	conn.QueryRow(`SELECT COUNT(*) FROM articles_fts`).Scan(&articleCount)
	conn.QueryRow(`SELECT COUNT(*) FROM cards_fts`).Scan(&cardCount)
	if articleCount > 0 && cardCount > 0 {
		return nil
	}

	// Populate articles_fts if empty.
	if articleCount == 0 {
		rows, err := conn.Query(`SELECT rowid, title, tldr, content FROM generated_articles`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var id int64
			var title, tldr, content string
			if err := rows.Scan(&id, &title, &tldr, &content); err != nil {
				return err
			}
			_, err := conn.Exec(
				`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`,
				id, stripHTMLForFTS(title), stripHTMLForFTS(tldr), stripHTMLForFTS(content),
			)
			if err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// Populate cards_fts if empty.
	if cardCount == 0 {
		cardRows, err := conn.Query(`SELECT rowid, title, sections_json, scouts_verdict FROM reference_pages`)
		if err != nil {
			return err
		}
		defer cardRows.Close()

		for cardRows.Next() {
			var id int64
			var title, sectionsJSON, verdict string
			if err := cardRows.Scan(&id, &title, &sectionsJSON, &verdict); err != nil {
				return err
			}
			_, err := conn.Exec(
				`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (?, ?, ?, ?)`,
				id, stripHTMLForFTS(title), extractSectionsText(sectionsJSON), stripHTMLForFTS(verdict),
			)
			if err != nil {
				return err
			}
		}
		if err := cardRows.Err(); err != nil {
			return err
		}
	}

	return nil
}

// stripHTMLForFTS removes HTML tags from a string and HTML-escapes the result
// so that FTS content is safe to render with safeHTML.
func stripHTMLForFTS(s string) string {
	var result []byte
	inTag := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '<':
			inTag = true
		case s[i] == '>':
			inTag = false
			result = append(result, ' ')
		case !inTag:
			result = append(result, s[i])
		}
	}
	return html.EscapeString(strings.TrimSpace(string(result)))
}

// extractSectionsText extracts plain text from sections_json for FTS indexing.
func extractSectionsText(sectionsJSON string) string {
	var sections []struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(sectionsJSON), &sections); err != nil {
		return ""
	}
	var parts []string
	for _, s := range sections {
		parts = append(parts, stripHTMLForFTS(s.Title))
		parts = append(parts, stripHTMLForFTS(s.Content))
	}
	return strings.Join(parts, " ")
}
