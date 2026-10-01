// Command repair-editor-notes moves editor notes out of stored article content
// into the article's admin-only editor_notes column.
//
// An editor note is an element with the class "editor-note", a remark for the
// admin ("Could not find a link for: ..."). The revision prompt briefly asked
// for them in March 2026 and they were stored inside the content, hidden from
// readers only by CSS: the text was still in the page source and in the search
// index, where search snippets showed it. Articles are now stored without them
// (see package editornote); this one-off tool repairs articles stored before
// that.
//
// It is a dry run unless -apply is given: the database is opened read-only and
// every change it would make is printed. With -apply each article is one
// transaction: a compare-and-set UPDATE of content and editor_notes, and the
// article's search index entry rebuilt, committed together. Re-running it is a
// no-op. updated_at is left alone: what readers see does not change. It takes
// no backup itself; take one first (see the reminder it prints).
//
// The revision history (article_revisions) is left as it is: it is shown only
// in the admin preview.
//
// Usage:
//
//	repair-editor-notes -db /opt/milesahead/data/milesahead.db          # dry run
//	repair-editor-notes -db /opt/milesahead/data/milesahead.db -apply   # write
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/editornote"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "repair-editor-notes: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("repair-editor-notes", flag.ContinueOnError)
	dbPath := fs.String("db", "./data/milesahead.db", "path to the SQLite database")
	apply := fs.Bool("apply", false, "write the changes (default is a read-only dry run)")
	idList := fs.String("ids", "", "optional comma-separated article ids to limit the repair to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	only, err := parseIDs(*idList)
	if err != nil {
		return err
	}
	if _, err := os.Stat(*dbPath); err != nil {
		// Opening a missing path would create an empty database.
		return fmt.Errorf("database %s: %w", *dbPath, err)
	}

	fmt.Fprintln(w, "BACKUP FIRST: this tool takes no backup. Before -apply, snapshot the database,")
	fmt.Fprintln(w, "e.g. on the server: sudo systemctl start milesahead-backup.service")
	fmt.Fprintln(w, "(or /opt/milesahead/backup.sh) and confirm the new file in /opt/milesahead/backups.")
	fmt.Fprintln(w)

	if !*apply {
		conn, err := sql.Open("sqlite", "file:"+*dbPath+"?mode=ro")
		if err != nil {
			return fmt.Errorf("open %s read-only: %w", *dbPath, err)
		}
		defer conn.Close()
		plans, err := buildPlans(conn, only)
		if err != nil {
			return err
		}
		report(w, plans, false)
		fmt.Fprintln(w, "\nDry run: nothing was written. Re-run with -apply to write.")
		return nil
	}

	// db.Open runs the migrations, which add the editor_notes column if the
	// server has not yet.
	database, err := db.Open(*dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", *dbPath, err)
	}
	defer database.Close()
	plans, err := buildPlans(database.DB(), only)
	if err != nil {
		return err
	}
	report(w, plans, true)
	applied, skipped := applyPlans(w, database.DB(), plans, db.SyncArticleFTSTx)
	fmt.Fprintf(w, "\nApplied %d article(s); %d left unchanged.\n", applied, skipped)
	return nil
}

// plan is what the tool would do to one article.
type plan struct {
	ID     int64
	Status string
	Title  string

	oldContent, oldNotes string
	newContent, newNotes string
	// Notes are the notes moved out of the content, one per entry.
	Notes []string
}

// buildPlans finds every article whose content holds an editor note and works
// out the split. It only reads.
func buildPlans(conn *sql.DB, only map[int64]bool) ([]plan, error) {
	// Before the server's migration has run (a dry run ahead of a deploy)
	// the column does not exist yet; every article's notes are then empty.
	notesCol := "''"
	has, err := hasColumn(conn, "generated_articles", "editor_notes")
	if err != nil {
		return nil, err
	}
	if has {
		notesCol = "COALESCE(editor_notes,'')"
	}
	rows, err := conn.Query(`SELECT id, status, COALESCE(title,''), COALESCE(content,''), ` + notesCol + `
		FROM generated_articles WHERE content LIKE '%` + editornote.Class + `%' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}
	defer rows.Close()

	var plans []plan
	for rows.Next() {
		var p plan
		if err := rows.Scan(&p.ID, &p.Status, &p.Title, &p.oldContent, &p.oldNotes); err != nil {
			return nil, fmt.Errorf("scan article: %w", err)
		}
		if len(only) > 0 && !only[p.ID] {
			continue
		}
		clean, notes := editornote.Split(p.oldContent)
		if clean == p.oldContent {
			continue // the class name only appears as text
		}
		p.newContent = clean
		p.newNotes = editornote.Join(p.oldNotes, notes)
		p.Notes = editornote.Lines(notes)
		plans = append(plans, p)
	}
	return plans, rows.Err()
}

func hasColumn(conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("columns of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// report prints every affected article and the notes that would move.
func report(w io.Writer, plans []plan, applying bool) {
	if len(plans) == 0 {
		fmt.Fprintln(w, "No article content holds an editor note.")
		return
	}
	verb := "would move notes out of"
	if applying {
		verb = "moving notes out of"
	}
	for _, p := range plans {
		fmt.Fprintf(w, "[%s] id=%d status=%s title=%q\n", verb, p.ID, p.Status, p.Title)
		for _, n := range p.Notes {
			fmt.Fprintf(w, "    note: %s\n", n)
		}
		if len(p.Notes) == 0 {
			fmt.Fprintln(w, "    (an empty note element; removed)")
		}
		fmt.Fprintf(w, "    content: %d -> %d bytes\n", len(p.oldContent), len(p.newContent))
	}
}

// applyPlans writes the plans, one transaction per article, and reports each.
func applyPlans(w io.Writer, conn *sql.DB, plans []plan, syncFTS func(*sql.Tx, int64) error) (applied, skipped int) {
	for _, p := range plans {
		ok, err := applyOne(conn, p, syncFTS)
		switch {
		case err != nil:
			fmt.Fprintf(w, "  id=%d: not changed, rolled back: %v\n", p.ID, err)
			skipped++
		case !ok:
			fmt.Fprintf(w, "  id=%d: changed since it was read, left alone; re-run to pick it up\n", p.ID)
			skipped++
		default:
			fmt.Fprintf(w, "  id=%d: %d note(s) moved\n", p.ID, len(p.Notes))
			applied++
		}
	}
	return applied, skipped
}

// applyOne splits one article and rebuilds its index entry atomically. The
// UPDATE is a compare-and-set on the content and notes it read, so an article
// edited since then is left alone rather than overwritten with a stale copy.
// If the index update fails the split is rolled back too: otherwise the row
// would be fixed with the note still in its search entry, and a re-run,
// finding no note left in the content, would never repair the index. It
// reports false, with nothing written, when the row no longer matches.
func applyOne(conn *sql.DB, p plan, syncFTS func(*sql.Tx, int64) error) (bool, error) {
	tx, err := conn.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback() // no-op once committed

	res, err := tx.Exec(`UPDATE generated_articles SET content = ?, editor_notes = ?
		WHERE id = ? AND COALESCE(content,'') = ? AND COALESCE(editor_notes,'') = ?`,
		p.newContent, p.newNotes, p.ID, p.oldContent, p.oldNotes)
	if err != nil {
		return false, fmt.Errorf("update: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	if err := syncFTS(tx, p.ID); err != nil {
		return false, fmt.Errorf("search index update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

func parseIDs(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad -ids entry %q: %w", part, err)
		}
		out[id] = true
	}
	return out, nil
}
