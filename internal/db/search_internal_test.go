package db

import (
	"database/sql"
	"errors"
	"testing"
)

// A rebuild that fails part-way leaves the old index exactly as it was.
func TestRebuildFTS_FailureKeepsOldIndex(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.db.Exec(`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (7, 'Old Row', '', '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE search_index_versions SET version = 1 WHERE name = 'cards_fts'`); err != nil {
		t.Fatal(err)
	}
	var before string
	d.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'cards_fts'`).Scan(&before)

	failing := func(tx *sql.Tx) (int, error) {
		if _, err := tx.Exec(`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (8, 'Half Done', '', '')`); err != nil {
			return 0, err
		}
		return 0, errors.New("disk on fire")
	}
	if _, err := rebuildFTS(d.db, "cards_fts", ftsTables[1].schema, "reference_pages", failing); err == nil {
		t.Fatal("want the fill error")
	}
	var after string
	var rows, version int
	d.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'cards_fts'`).Scan(&after)
	d.db.QueryRow(`SELECT COUNT(*) FROM cards_fts WHERE cards_fts MATCH '"old row"'`).Scan(&rows)
	d.db.QueryRow(`SELECT version FROM search_index_versions WHERE name = 'cards_fts'`).Scan(&version)
	var half int
	d.db.QueryRow(`SELECT COUNT(*) FROM cards_fts WHERE rowid = 8`).Scan(&half)
	if after != before || rows != 1 || half != 0 || version != 1 {
		t.Errorf("old index not restored: schema same %v, old row %d, half row %d, version %d", after == before, rows, half, version)
	}

	// A mismatched row count also refuses to commit.
	short := func(tx *sql.Tx) (int, error) { return 0, nil }
	if _, err := d.db.Exec(`INSERT INTO reference_pages (slug, title, category, issuer, sections_json, recent_changes_json, pending_updates_json, scouts_verdict, keywords_json, image_url, apply_url, status) VALUES ('x', 'X', 'travel', 'Chase', '[]', '[]', '[]', '', '[]', '', '', 'published')`); err != nil {
		t.Fatal(err)
	}
	if _, err := rebuildFTS(d.db, "cards_fts", ftsTables[1].schema, "reference_pages", short); err == nil {
		t.Fatal("a rebuild missing rows must not commit")
	}
	d.db.QueryRow(`SELECT COUNT(*) FROM cards_fts WHERE cards_fts MATCH '"old row"'`).Scan(&rows)
	if rows != 1 {
		t.Error("the old index should survive a short rebuild")
	}
}
