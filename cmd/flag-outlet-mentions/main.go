// Command flag-outlet-mentions lists stored articles that name or link to a
// third-party news site or blog -- the outlets articles are written from --
// or refer to one by number ("Source 1 notes..."), so the admin can fix them.
//
// The site's rule is that articles never name those outlets, with two narrow
// exceptions (the outlet is itself the subject of the story, or an unconfirmed
// report only that outlet carries). The pipeline now enforces it on new
// articles; this tool finds the ones written before. It only reports: prose
// cannot be fixed deterministically, and only a person can judge whether an
// exception applies. Fix an article with a revision request or an edit.
//
// It never writes. The database is opened read-only (mode=ro), and the tool
// has no apply mode.
//
// Outlets checked are every row of the sources table plus the built-in list
// and abbreviations (TPG, OMAAT, DoC, VFTW, FM); see ai.OutletMatcher.
//
// Usage:
//
//	flag-outlet-mentions -db /opt/milesahead/data/milesahead.db
//	flag-outlet-mentions -db ./data/milesahead.db -status approved,pending
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/svg"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "flag-outlet-mentions: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("flag-outlet-mentions", flag.ContinueOnError)
	dbPath := fs.String("db", "./data/milesahead.db", "path to the SQLite database (opened read-only)")
	statuses := fs.String("status", "", "optional comma-separated statuses to limit the report to (default: all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	conn, err := openReadOnly(*dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()

	outlets, err := tableOutlets(conn)
	if err != nil {
		return err
	}
	m := ai.NewOutletMatcher(outlets)
	hits, err := scan(conn, m, parseList(*statuses))
	if err != nil {
		return err
	}
	report(w, m, hits)
	return nil
}

// openReadOnly opens the database so that no statement can write to it. A
// missing path is an error rather than a new empty database.
func openReadOnly(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("database %s: %w", path, err)
	}
	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	return conn, nil
}

// tableOutlets reads every row of the sources table, active or not.
func tableOutlets(conn *sql.DB) ([]ai.Outlet, error) {
	rows, err := conn.Query(`SELECT COALESCE(name,''), COALESCE(url,'') FROM sources ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	var out []ai.Outlet
	for rows.Next() {
		var o ai.Outlet
		if err := rows.Scan(&o.Name, &o.URL); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// hit is one mention in one field of one article.
type hit struct {
	ID     int64
	Status string
	Title  string
	Field  string
	Outlet string
	// Text is the sentence the mention is in, or the link's text and URL.
	Text string
}

// scan checks every article's title, TLDR, key details, body, Scout's Take,
// links and the words of its infographic.
func scan(conn *sql.DB, m *ai.OutletMatcher, only map[string]bool) ([]hit, error) {
	rows, err := conn.Query(`SELECT id, status, COALESCE(title,''), COALESCE(tldr,''),
		COALESCE(key_details,''), COALESCE(content,''), COALESCE(scouts_take,''), COALESCE(links,''),
		COALESCE(infographic_svg,'')
		FROM generated_articles ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list articles: %w", err)
	}
	defer rows.Close()
	var hits []hit
	for rows.Next() {
		var id int64
		var status, title, tldr, details, content, take, links, chart string
		if err := rows.Scan(&id, &status, &title, &tldr, &details, &content, &take, &links, &chart); err != nil {
			return nil, fmt.Errorf("scan article: %w", err)
		}
		if len(only) > 0 && !only[status] {
			continue
		}
		add := func(field, outlet, text string) {
			hits = append(hits, hit{ID: id, Status: status, Title: title, Field: field, Outlet: outlet, Text: text})
		}
		check := func(field, text string) {
			for _, mn := range m.Find(text) {
				add(field, mn.Outlet, mn.Sentence)
			}
			// "Source 1 notes..." refers to an outlet too, by number.
			for _, ref := range ai.FindSourceRefs(text) {
				add(field, "numbered reference", ref)
			}
		}
		check("title", title)
		check("tldr", tldr)
		for _, d := range keyDetailLines(details) {
			check("key_details", d)
		}
		check("content", content)
		for _, l := range ai.HTMLLinks(content) {
			if name, ok := m.OutletForURL(l.URL); ok {
				add("content link", name, fmt.Sprintf("%s -> %s", l.Text, l.URL))
			}
		}
		check("scouts_take", take)
		if chart != "" && chart != db.NoInfographic {
			// A chart that is not well-formed is never shown, so there is
			// nothing a reader could see to check.
			if words, err := svg.Text(chart); err == nil {
				for _, line := range strings.Split(words, "\n") {
					check("infographic", line)
				}
			}
		}
		for _, l := range storedLinks(links) {
			check("links", l.Text)
			if name, ok := m.OutletForURL(l.URL); ok {
				add("links", name, fmt.Sprintf("%s -> %s", l.Text, l.URL))
			}
		}
	}
	return hits, rows.Err()
}

// keyDetailLines renders stored key details as "Label: Value" lines. A value
// that is not the expected JSON is checked as it is.
func keyDetailLines(raw string) []string {
	var details []ai.KeyDetail
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		if strings.TrimSpace(raw) == "" || raw == "null" {
			return nil
		}
		return []string{raw}
	}
	out := make([]string, len(details))
	for i, d := range details {
		out[i] = d.Label + ": " + d.Value
	}
	return out
}

func storedLinks(raw string) []ai.ArticleLink {
	var links []ai.ArticleLink
	_ = json.Unmarshal([]byte(raw), &links)
	return links
}

// report prints the outlets checked, then each article with its mentions,
// then a count by status.
func report(w io.Writer, m *ai.OutletMatcher, hits []hit) {
	fmt.Fprintf(w, "Read-only: nothing is written. Outlets checked (plus TPG, OMAAT, DoC, VFTW, FM):\n  %s\n\n",
		strings.Join(m.Names(), ", "))
	if len(hits) == 0 {
		fmt.Fprintln(w, "No article names, links to or numbers a news site.")
		return
	}
	byStatus := map[string]int{}
	var last int64 = -1
	articles := 0
	for _, h := range hits {
		if h.ID != last {
			last = h.ID
			articles++
			byStatus[h.Status]++
			fmt.Fprintf(w, "id=%d status=%s title=%q\n", h.ID, h.Status, h.Title)
		}
		fmt.Fprintf(w, "    %s [%s]: %s\n", h.Field, h.Outlet, h.Text)
	}
	statuses := make([]string, 0, len(byStatus))
	for s := range byStatus {
		statuses = append(statuses, s)
	}
	sort.Strings(statuses)
	parts := make([]string, len(statuses))
	for i, s := range statuses {
		parts[i] = fmt.Sprintf("%d %s", byStatus[s], s)
	}
	fmt.Fprintf(w, "\n%d article(s) name, link to or number a news site: %s.\n", articles, strings.Join(parts, ", "))
	fmt.Fprintln(w, "Each needs a person to judge the two exceptions and fix the text by revision request or edit.")
}

func parseList(s string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out[part] = true
		}
	}
	return out
}
