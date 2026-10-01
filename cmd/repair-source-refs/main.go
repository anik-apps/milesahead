// Command repair-source-refs rewrites numbered source references ("Source 1
// notes this offer...") in stored articles to the name of the outlet the
// number stood for.
//
// The generator prompts used to label their inputs "Source 1", "Source 2", and
// some published articles repeat the label. The pipeline now fixes this as it
// generates; this one-off tool repairs articles written before that.
//
// It is a dry run unless -apply is given: the database is opened read-only and
// every change it would make is printed. With -apply it writes only articles
// whose every reference maps to an outlet unambiguously, each with a
// compare-and-set UPDATE, and keeps the search index in step. Re-running it is
// a no-op. It takes no backup itself; take one first (see the reminder it
// prints).
//
// Which outlet "Source N" meant depends on the order the prompt listed the
// sources in, which is not stored. It is reconstructed:
//
//   - The scan has always built a cluster's source list by source id (the
//     ListActiveSources order) and then by raw article id, so text from the
//     original generation maps by that order.
//   - Revisions loaded their sources by raw article id alone, so text in an
//     article that has been revised may come from either order. Such a
//     reference is mapped only when both orders give the same outlet.
//   - An outlet the text names itself ("Source 2 (LoyaltyLobby)") wins.
//
// Anything else is listed as needing a manual revision request and left alone.
//
// Usage:
//
//	repair-source-refs -db /opt/milesahead/data/milesahead.db          # dry run
//	repair-source-refs -db /opt/milesahead/data/milesahead.db -apply   # write
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "repair-source-refs: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("repair-source-refs", flag.ContinueOnError)
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
		fmt.Fprintln(w, "\nDry run: nothing was written. Re-run with -apply to write the rewritable articles.")
		return nil
	}

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
	applied, skipped := applyPlans(w, database.DB(), plans, database.SyncArticleFTS)
	fmt.Fprintf(w, "\nApplied %d article(s); %d left unchanged (listed above).\n", applied, skipped)
	return nil
}

// fields are the stored columns a numbered reference can appear in. Slug is
// deliberately not touched: published URLs stay stable.
var fields = []string{"title", "content", "tldr", "key_details", "scouts_take"}

// plan is what the tool would do to one article.
type plan struct {
	ID     int64
	Status string
	Title  string

	old, new map[string]string
	// Changes lists each rewrite as "field: old -> new".
	Changes []string
	// Unresolved lists references that could not be mapped, as "field: ref".
	// An article with any is not written at all.
	Unresolved []string
	// Revised records that the article has been through the revision path,
	// which is why a number may be ambiguous.
	Revised bool
}

func (p plan) rewritable() bool { return len(p.Unresolved) == 0 && len(p.Changes) > 0 }

type linkedRaw struct {
	id, sourceID int64
	name         string
}

// buildPlans finds every article with a numbered source reference and works
// out its rewrite. It only reads.
func buildPlans(conn *sql.DB, only map[int64]bool) ([]plan, error) {
	rows, err := conn.Query(`SELECT id, status, revision_number,
		COALESCE(title,''), COALESCE(content,''), COALESCE(tldr,''),
		COALESCE(key_details,''), COALESCE(scouts_take,'')
		FROM generated_articles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}
	type article struct {
		id       int64
		status   string
		revision int
		vals     map[string]string
	}
	var candidates []article
	for rows.Next() {
		var a article
		var title, content, tldr, details, take string
		if err := rows.Scan(&a.id, &a.status, &a.revision, &title, &content, &tldr, &details, &take); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan article: %w", err)
		}
		if len(only) > 0 && !only[a.id] {
			continue
		}
		a.vals = map[string]string{"title": title, "content": content, "tldr": tldr,
			"key_details": details, "scouts_take": take}
		for _, f := range fields {
			if len(ai.FindSourceRefs(a.vals[f])) > 0 {
				candidates = append(candidates, a)
				break
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var plans []plan
	for _, a := range candidates {
		raws, err := linkedRaws(conn, a.id)
		if err != nil {
			return nil, err
		}
		var revisions int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM article_revisions WHERE generated_article_id = ?`,
			a.id).Scan(&revisions); err != nil {
			return nil, fmt.Errorf("count revisions of %d: %w", a.id, err)
		}
		p := plan{ID: a.id, Status: a.status, Title: a.vals["title"],
			old: a.vals, new: map[string]string{}, Revised: revisions > 0 || a.revision > 1}

		known, byNumber := resolver(raws, p.Revised)
		for _, f := range fields {
			out, rep := ai.RewriteSourceRefs(a.vals[f], known, byNumber)
			for _, u := range rep.Unresolved {
				p.Unresolved = append(p.Unresolved, f+": "+u)
			}
			if !rep.Changed() {
				continue
			}
			if f == "key_details" && !json.Valid([]byte(out)) {
				p.Unresolved = append(p.Unresolved, f+": rewrite would not be valid JSON")
				continue
			}
			for _, c := range rep.Rewritten {
				p.Changes = append(p.Changes, f+": "+c)
			}
			p.new[f] = out
		}
		if len(p.Changes) > 0 || len(p.Unresolved) > 0 {
			plans = append(plans, p)
		}
	}
	return plans, nil
}

// linkedRaws returns the raw articles an article was written from, with their
// outlet names.
func linkedRaws(conn *sql.DB, articleID int64) ([]linkedRaw, error) {
	rows, err := conn.Query(`SELECT r.id, r.source_id, COALESCE(s.name, '')
		FROM article_sources a
		JOIN raw_articles r ON r.id = a.raw_article_id
		LEFT JOIN sources s ON s.id = r.source_id
		WHERE a.generated_article_id = ?`, articleID)
	if err != nil {
		return nil, fmt.Errorf("sources of %d: %w", articleID, err)
	}
	defer rows.Close()
	var out []linkedRaw
	for rows.Next() {
		var r linkedRaw
		if err := rows.Scan(&r.id, &r.sourceID, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolver reconstructs what "Source N" meant for one article; see the package
// comment for the rules.
func resolver(raws []linkedRaw, revised bool) ([]string, func(int) (string, bool)) {
	gen := append([]linkedRaw(nil), raws...)
	sort.SliceStable(gen, func(i, j int) bool {
		if gen[i].sourceID != gen[j].sourceID {
			return gen[i].sourceID < gen[j].sourceID
		}
		return gen[i].id < gen[j].id
	})
	rev := append([]linkedRaw(nil), raws...)
	sort.SliceStable(rev, func(i, j int) bool { return rev[i].id < rev[j].id })

	genNames, revNames := names(gen), names(rev)
	byGen, byRev := ai.ByPosition(genNames), ai.ByPosition(revNames)
	return genNames, func(n int) (string, bool) {
		g, ok := byGen(n)
		if !ok || !revised {
			return g, ok
		}
		r, ok := byRev(n)
		if !ok || r != g {
			return "", false
		}
		return g, true
	}
}

func names(raws []linkedRaw) []string {
	out := make([]string, len(raws))
	for i, r := range raws {
		out[i] = r.name
	}
	return out
}

// report prints every affected article: what would change, and what needs a
// manual revision request.
func report(w io.Writer, plans []plan, applying bool) {
	if len(plans) == 0 {
		fmt.Fprintln(w, "No article contains a numbered source reference.")
		return
	}
	verb := "would rewrite"
	if applying {
		verb = "rewriting"
	}
	var manual []plan
	for _, p := range plans {
		switch {
		case p.rewritable():
			fmt.Fprintf(w, "[%s] id=%d status=%s title=%q\n", verb, p.ID, p.Status, p.Title)
			for _, c := range p.Changes {
				fmt.Fprintf(w, "    %s\n", c)
			}
		default:
			manual = append(manual, p)
		}
	}
	if len(manual) == 0 {
		return
	}
	fmt.Fprintln(w, "\nNeeds a manual revision request (left unchanged):")
	for _, p := range manual {
		why := "the number does not map to one outlet"
		if p.Revised {
			why += " (revised article: generation and revision orders disagree)"
		}
		fmt.Fprintf(w, "  id=%d status=%s title=%q: %s\n", p.ID, p.Status, p.Title, why)
		for _, u := range p.Unresolved {
			fmt.Fprintf(w, "    unresolved %s\n", u)
		}
	}
}

// applyPlans writes the rewritable plans. Each UPDATE is a compare-and-set on
// every column it reads, so an article edited since buildPlans read it is left
// alone rather than overwritten with a stale copy.
func applyPlans(w io.Writer, conn *sql.DB, plans []plan, syncFTS func(int64) error) (applied, skipped int) {
	for _, p := range plans {
		if !p.rewritable() {
			skipped++
			continue
		}
		val := func(f string) string {
			if v, ok := p.new[f]; ok {
				return v
			}
			return p.old[f]
		}
		res, err := conn.Exec(`UPDATE generated_articles
			SET title = ?, content = ?, tldr = ?, key_details = ?, scouts_take = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND COALESCE(title,'') = ? AND COALESCE(content,'') = ? AND COALESCE(tldr,'') = ?
			  AND COALESCE(key_details,'') = ? AND COALESCE(scouts_take,'') = ?`,
			val("title"), val("content"), val("tldr"), val("key_details"), val("scouts_take"),
			p.ID, p.old["title"], p.old["content"], p.old["tldr"], p.old["key_details"], p.old["scouts_take"])
		if err != nil {
			fmt.Fprintf(w, "  id=%d: update failed: %v\n", p.ID, err)
			skipped++
			continue
		}
		if n, _ := res.RowsAffected(); n != 1 {
			fmt.Fprintf(w, "  id=%d: changed since it was read, left alone; re-run to pick it up\n", p.ID)
			skipped++
			continue
		}
		if err := syncFTS(p.ID); err != nil {
			fmt.Fprintf(w, "  id=%d: rewritten, but the search index update failed: %v\n", p.ID, err)
		}
		fmt.Fprintf(w, "  id=%d: rewritten (%d change(s))\n", p.ID, len(p.Changes))
		applied++
	}
	return applied, skipped
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
