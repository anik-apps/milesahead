package db

import (
	"database/sql"
	"strings"
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
	}
	for _, stmt := range alterStatements {
		_, err := db.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}

	return nil
}
