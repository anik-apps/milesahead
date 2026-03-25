package db

import "database/sql"

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
	return err
}
