package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

const (
	note57     = "Could not find a link for: EVA Air promotional image from Source 2 (LoyaltyLobby)"
	legacy57   = "<p>EVA Air bonus body.</p>\n<div class=\"editor-note\">\n" + note57 + "\n</div>"
	note60     = "Availability is limited and varies by departure city."
	legacy60   = `<p>Delta sale body.</p><div class="editor-note">` + note60 + `</div>`
	textOnly   = "<p>The editor-note class is mentioned here only as text.</p>"
	cleanBody  = "<p>Nothing to repair.</p>"
	ftsLeakSQL = `SELECT rowid FROM articles_fts WHERE articles_fts MATCH '"Could not find a link"' OR articles_fts MATCH '"Availability is limited"' ORDER BY rowid`
)

type fixture struct {
	path     string
	database *db.Database
	ids      map[string]int64
}

// newFixture builds a database holding legacy rows: content with an editor
// note inline and a search index entry carrying its text, as production had.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &fixture{path: path, database: database, ids: map[string]int64{}}

	article := func(key, status, content string) {
		id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: "T " + key, Slug: key,
			Content: "<p>placeholder</p>", Type: "deal", Status: status, SourcesJSON: "[]", RevisionNumber: 2})
		if err != nil {
			t.Fatal(err)
		}
		// Stored the way articles were before notes were split out on
		// write, bypassing the db package that now prevents it.
		conn := database.DB()
		if _, err := conn.Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, content, id); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`DELETE FROM articles_fts WHERE rowid = ?`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, '', ?)`,
			id, "T "+key, stripTags(content)); err != nil {
			t.Fatal(err)
		}
		f.ids[key] = id
	}
	article("a57", db.StatusApproved, legacy57)
	article("a60", db.StatusPending, legacy60)
	article("text-only", db.StatusApproved, textOnly)
	article("clean", db.StatusApproved, cleanBody)
	return f
}

func stripTags(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// raw reads a row's stored content and notes, bypassing the db package's
// read-time split.
func (f *fixture) raw(t *testing.T, key string) (content, notes, updatedAt string) {
	t.Helper()
	err := f.database.DB().QueryRow(`SELECT content, editor_notes, updated_at FROM generated_articles WHERE id = ?`,
		f.ids[key]).Scan(&content, &notes, &updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return content, notes, updatedAt
}

func (f *fixture) snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, key := range []string{"a57", "a60", "text-only", "clean"} {
		c, n, u := f.raw(t, key)
		fmt.Fprintf(&b, "%s|%s|%s\n", c, n, u)
	}
	rows, err := f.database.DB().Query(`SELECT rowid, content FROM articles_fts ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c string
		if err := rows.Scan(&id, &c); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "fts %d|%s\n", id, c)
	}
	return b.String()
}

func (f *fixture) leakingFTS(t *testing.T) []int64 {
	t.Helper()
	rows, err := f.database.DB().Query(ftsLeakSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot(t)

	var out bytes.Buffer
	if err := run([]string{"-db", f.path}, &out); err != nil {
		t.Fatal(err)
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("dry run changed the database:\nbefore %s\nafter  %s", before, after)
	}
	s := out.String()
	for _, want := range []string{
		"BACKUP FIRST",
		"sudo systemctl start milesahead-backup.service",
		fmt.Sprintf("[would move notes out of] id=%d status=approved", f.ids["a57"]),
		"note: " + note57,
		fmt.Sprintf("[would move notes out of] id=%d status=pending", f.ids["a60"]),
		"note: " + note60,
		"Dry run: nothing was written",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	for _, key := range []string{"text-only", "clean"} {
		if strings.Contains(s, fmt.Sprintf("id=%d ", f.ids[key])) {
			t.Errorf("%s listed, but has no note:\n%s", key, s)
		}
	}
}

// A dry run ahead of the deploy that adds editor_notes must still work.
func TestDryRunBeforeMigration(t *testing.T) {
	f := newFixture(t)
	if _, err := f.database.DB().Exec(`ALTER TABLE generated_articles DROP COLUMN editor_notes`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"-db", f.path}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "note: "+note57) {
		t.Errorf("notes not listed:\n%s", out.String())
	}
}

func TestApplyMovesNotesAndReindexes(t *testing.T) {
	f := newFixture(t)
	if got := f.leakingFTS(t); len(got) != 2 {
		t.Fatalf("fixture: index should leak two notes, got %v", got)
	}
	_, _, updatedBefore := f.raw(t, "a57")
	textBefore, _, _ := f.raw(t, "text-only")

	var out bytes.Buffer
	if err := run([]string{"-db", f.path, "-apply"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Applied 2 article(s); 0 left unchanged.") {
		t.Errorf("unexpected summary:\n%s", out.String())
	}

	content, notes, updatedAt := f.raw(t, "a57")
	if content != "<p>EVA Air bonus body.</p>" || notes != note57 {
		t.Errorf("a57 stored content %q notes %q", content, notes)
	}
	if updatedAt != updatedBefore {
		t.Errorf("updated_at changed from %s to %s; what readers see did not change", updatedBefore, updatedAt)
	}
	if content, notes, _ := f.raw(t, "a60"); content != "<p>Delta sale body.</p>" || notes != note60 {
		t.Errorf("a60 stored content %q notes %q", content, notes)
	}
	if after, _, _ := f.raw(t, "text-only"); after != textBefore {
		t.Errorf("text-only content changed to %q", after)
	}

	if got := f.leakingFTS(t); len(got) != 0 {
		t.Errorf("search index still holds notes for %v", got)
	}
	// The rest of the article is still searchable.
	res, err := f.database.SearchArticles(`"EVA Air bonus body"`, "", "2000-01-01", 10, 0)
	if err != nil || len(res) != 1 || res[0].ID != f.ids["a57"] {
		t.Errorf("article body no longer searchable: %v %v", res, err)
	}

	// The admin sees the notes through the model; the content carries none.
	a, err := f.database.GetGeneratedArticle(f.ids["a57"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(a.Content, "editor-note") || a.EditorNotes != note57 {
		t.Errorf("loaded content %q notes %q", a.Content, a.EditorNotes)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	f := newFixture(t)
	if err := run([]string{"-db", f.path, "-apply"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	once := f.snapshot(t)
	var out bytes.Buffer
	if err := run([]string{"-db", f.path, "-apply"}, &out); err != nil {
		t.Fatal(err)
	}
	if twice := f.snapshot(t); twice != once {
		t.Errorf("second run changed the database:\nonce  %s\ntwice %s", once, twice)
	}
	if !strings.Contains(out.String(), "No article content holds an editor note.") ||
		!strings.Contains(out.String(), "Applied 0 article(s)") {
		t.Errorf("second run output:\n%s", out.String())
	}
}

func TestIDsFilter(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	if err := run([]string{"-db", f.path, "-apply", "-ids", fmt.Sprint(f.ids["a60"])}, &out); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := f.raw(t, "a57"); c != legacy57 {
		t.Errorf("a57 changed though not selected: %q", c)
	}
	if c, _, _ := f.raw(t, "a60"); strings.Contains(c, "editor-note") {
		t.Errorf("a60 not repaired: %q", c)
	}
	if _, err := parseIDs("1,x"); err == nil {
		t.Error("bad -ids accepted")
	}
}

// A row edited between reading and writing is left alone.
func TestApplyIsCompareAndSet(t *testing.T) {
	f := newFixture(t)
	plans, err := buildPlans(f.database.DB(), map[int64]bool{f.ids["a57"]: true})
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans %v, err %v", plans, err)
	}
	edited := legacy57 + "<p>edited meanwhile</p>"
	if _, err := f.database.DB().Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, edited, f.ids["a57"]); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	applied, skipped := applyPlans(&out, f.database.DB(), plans, db.SyncArticleFTSTx)
	if applied != 0 || skipped != 1 || !strings.Contains(out.String(), "changed since it was read") {
		t.Errorf("applied %d skipped %d:\n%s", applied, skipped, out.String())
	}
	if c, _, _ := f.raw(t, "a57"); c != edited {
		t.Errorf("edited row overwritten: %q", c)
	}
}

// The split and the index rebuild commit together or not at all.
func TestIndexFailureRollsBack(t *testing.T) {
	f := newFixture(t)
	before := f.snapshot(t)
	plans, err := buildPlans(f.database.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	failing := func(*sql.Tx, int64) error { return errors.New("index unavailable") }
	var out bytes.Buffer
	applied, skipped := applyPlans(&out, f.database.DB(), plans, failing)
	if applied != 0 || skipped != 2 || !strings.Contains(out.String(), "rolled back: search index update: index unavailable") {
		t.Errorf("applied %d skipped %d:\n%s", applied, skipped, out.String())
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("failed index update left changes:\nbefore %s\nafter  %s", before, after)
	}
}

func TestMissingDatabase(t *testing.T) {
	err := run([]string{"-db", filepath.Join(t.TempDir(), "missing.db")}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a missing database should be an error, not a new empty one")
	}
}
