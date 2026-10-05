package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// seed builds a database with one source outside the built-in list and
// articles that name outlets in each field, plus one clean article.
func seed(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.CreateSource(&db.Source{Name: "Some Travel Blog", URL: "https://sometravelblog.net", Active: false}); err != nil {
		t.Fatal(err)
	}
	articles := []db.GeneratedArticle{
		{Title: "American Changes Upgrades", Slug: "a", Status: db.StatusApproved,
			Content:    `<p>American confirmed to The Points Guy: the change is permanent.</p>`,
			KeyDetails: `[{"Label":"Value","Value":"~1.41 cents according to One Mile at a Time"}]`},
		{Title: "OMAAT: Hyatt Sale", Slug: "b", Status: db.StatusPending,
			TLDR:       "Hyatt is running a sale.",
			Content:    `<p>Book via <a href="https://sometravelblog.net/x">the sale page</a>.</p>`,
			ScoutsTake: "Some Travel Blog runs the math slightly differently, but book it.",
			Links:      `[{"Text":"DoC post","URL":"https://www.doctorofcredit.com/x"}]`},
		{Title: "Capital One BA Bonus", Slug: "d", Status: db.StatusRejected,
			Content: `<p>Source 1 notes this offer is rare. Terms: <a href="https://www.capitalone.com/x">https://www.capitalone.com/x</a>.</p>` +
				`<p>More at https://milestomemories.com/c1-ba and <a href="https://milestomemories.com/x">https://milestomemories.com/x</a>.</p>`},
		{Title: "Chase Raises the Sapphire Bonus", Slug: "c", Status: db.StatusDraft,
			Content: `<p>Chase, American and Hyatt all said so. Sources say more is coming.</p>`},
	}
	for i := range articles {
		a := &articles[i]
		a.Type, a.SourcesJSON, a.RevisionNumber = "deal", "[]", 1
		if _, err := database.CreateGeneratedArticle(a); err != nil {
			t.Fatal(err)
		}
	}
	// The first article's chart names an outlet in a label.
	if _, err := database.DB().Exec(`UPDATE generated_articles SET infographic_svg = ? WHERE slug = 'a'`,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 360"><title>Upgrade odds</title>`+
			`<text x="1" y="20">Odds per View from the <tspan>Wing</tspan></text></svg>`); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileHash(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestRun_ListsEveryMention(t *testing.T) {
	path := seed(t)
	var out bytes.Buffer
	if err := run([]string{"-db", path}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"Some Travel Blog", // the table's outlet is listed among those checked
		`id=1 status=approved title="American Changes Upgrades"`,
		"content [The Points Guy]: American confirmed to The Points Guy: the change is permanent.",
		"key_details [One Mile at a Time]: Value: ~1.41 cents according to One Mile at a Time",
		"infographic [View from the Wing]: Odds per View from the Wing",
		`id=2 status=pending title="OMAAT: Hyatt Sale"`,
		"title [One Mile at a Time]: OMAAT: Hyatt Sale",
		"content link [Some Travel Blog]: the sale page -> https://sometravelblog.net/x",
		"scouts_take [Some Travel Blog]: Some Travel Blog runs the math slightly differently, but book it.",
		"links [Doctor of Credit]: DoC post",
		`id=3 status=rejected title="Capital One BA Bonus"`,
		"content [numbered reference]: Source 1",
		"content [Miles to Memories]: More at https://milestomemories.com/c1-ba and https://milestomemories.com/x.",
		"3 article(s) name, link to or number a news site: 1 approved, 1 pending, 1 rejected.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "capitalone.com") {
		t.Errorf("an official URL was listed:\n%s", got)
	}
	if strings.Contains(got, "id=4 ") {
		t.Errorf("the clean article was listed:\n%s", got)
	}
}

func TestRun_StatusFilter(t *testing.T) {
	path := seed(t)
	var out bytes.Buffer
	if err := run([]string{"-db", path, "-status", "pending"}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out.String(), "id=1 ") || !strings.Contains(out.String(), "id=2 ") {
		t.Errorf("status filter not applied:\n%s", out.String())
	}
}

// TestRun_NeverWrites: the database file is byte-for-byte unchanged, and the
// connection the tool opens refuses writes.
func TestRun_NeverWrites(t *testing.T) {
	path := seed(t)
	before := fileHash(t, path)
	if err := run([]string{"-db", path}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fileHash(t, path) != before {
		t.Error("the database changed")
	}
	for _, suffix := range []string{"-wal", "-journal"} {
		if fi, err := os.Stat(path + suffix); err == nil && fi.Size() > 0 {
			t.Errorf("%s%s was written", path, suffix)
		}
	}

	conn, err := openReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`UPDATE generated_articles SET title = 'x'`); err == nil {
		t.Error("the read-only connection accepted a write")
	}
}

func TestRun_MissingDatabaseIsNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if err := run([]string{"-db", path}, &bytes.Buffer{}); err == nil {
		t.Error("want an error for a missing database")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a missing database was created")
	}
}

// A database from before infographics has no infographic_svg column; the
// tool still reports everything else rather than failing.
func TestRun_DatabaseWithoutTheInfographicColumn(t *testing.T) {
	path := seed(t)
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`ALTER TABLE generated_articles DROP COLUMN infographic_svg`); err != nil {
		t.Fatalf("drop column: %v", err)
	}
	database.Close()

	var out bytes.Buffer
	if err := run([]string{"-db", path}, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "content [The Points Guy]") || strings.Contains(got, "infographic [") {
		t.Errorf("unexpected output without the column:\n%s", got)
	}
}
