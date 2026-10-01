package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

type fixture struct {
	path     string
	database *db.Database
	ids      map[string]int64
}

// newFixture builds a database with sources in id order One Mile at a Time
// (1), Upgraded Points (2), Loyalty Lobby (3), and articles covering each
// mapping rule.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	f := &fixture{path: path, database: database, ids: map[string]int64{}}

	src := map[string]int64{}
	for _, name := range []string{"One Mile at a Time", "Upgraded Points", "Loyalty Lobby"} {
		id, err := database.CreateSource(&db.Source{Name: name, URL: "https://" + name, Active: true})
		if err != nil {
			t.Fatal(err)
		}
		src[name] = id
	}
	n := 0
	raw := func(source string) int64 {
		n++
		id, err := database.CreateRawArticle(&db.RawArticle{SourceID: src[source], Title: "raw",
			URL: fmt.Sprintf("https://x/%d", n), Content: "c", PublishedAt: time.Now(), Hash: fmt.Sprint("h", n)})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	article := func(key, title, content, tldr, details, take string, revision int, raws ...int64) {
		id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: title, Slug: key,
			Content: content, Type: "deal", Status: db.StatusApproved, SourcesJSON: "[]",
			RevisionNumber: revision, TLDR: tldr, KeyDetails: details, ScoutsTake: take})
		if err != nil {
			t.Fatal(err)
		}
		if err := database.LinkArticleSources(id, raws); err != nil {
			t.Fatal(err)
		}
		if err := database.SyncArticleFTS(id); err != nil {
			t.Fatal(err)
		}
		f.ids[key] = id
	}

	// Raw ids put Loyalty Lobby first, source ids put One Mile at a Time
	// first: the generation order and the old revision order disagree.
	ll, omaat := raw("Loyalty Lobby"), raw("One Mile at a Time")
	article("unrevised", "Source 1 Says It Is Rare",
		`<p>Source 1 notes this offer "doesn't come around often."</p>`,
		"Source 2 confirms 20%.", `[{"Label":"Bonus","Value":"20% (Source 2)"}]`, "", 1, ll, omaat)
	article("revised-ambiguous", "T", "<p>Source 1 notes it.</p>", "", "[]", "", 2, ll, omaat)
	article("revised-explicit", "T",
		`<p>ok</p><div class="editor-note">image from Source 2 (LoyaltyLobby)</div>`, "", "[]", "", 2, ll, omaat)

	// Here both orders agree, so a revised article is still mappable.
	omaat2, up := raw("One Mile at a Time"), raw("Upgraded Points")
	article("revised-agree", "T", "<p>Source 2 also notes the Companion Pass.</p>", "", "[]",
		"Source 1 is right.", 2, omaat2, up)
	article("out-of-range", "T", "<p>Source 5 notes it. Source 1 too.</p>", "", "[]", "", 1, omaat2, up)
	article("clean", "T", "<p>Sources say it is rare; open source too.</p>", "", "[]", "", 1, omaat2, up)
	return f
}

func (f *fixture) get(t *testing.T, key string) *db.GeneratedArticle {
	t.Helper()
	a, err := f.database.GetGeneratedArticle(f.ids[key])
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (f *fixture) snapshot(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, key := range []string{"unrevised", "revised-ambiguous", "revised-explicit", "revised-agree", "out-of-range", "clean"} {
		a := f.get(t, key)
		fmt.Fprintf(&b, "%s|%s|%s|%s|%s|%s\n", a.Title, a.Content, a.TLDR, a.KeyDetails, a.ScoutsTake, a.UpdatedAt)
	}
	return b.String()
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
		fmt.Sprintf("[would rewrite] id=%d", f.ids["unrevised"]),
		"Source 1 -> One Mile at a Time",
		"Needs a manual revision request",
		fmt.Sprintf("id=%d status=approved", f.ids["revised-ambiguous"]),
		"unresolved content: Source 5",
		"Dry run: nothing was written",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, fmt.Sprintf("id=%d ", f.ids["clean"])) {
		t.Errorf("clean article listed:\n%s", s)
	}
}

func TestApplyRewritesOnlyUnambiguousRowsAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ambiguous := f.get(t, "revised-ambiguous")
	outOfRange := f.get(t, "out-of-range")
	clean := f.get(t, "clean")

	var out bytes.Buffer
	if err := run([]string{"-db", f.path, "-apply"}, &out); err != nil {
		t.Fatal(err)
	}

	a := f.get(t, "unrevised")
	if a.Title != "One Mile at a Time Says It Is Rare" ||
		a.Content != `<p>One Mile at a Time notes this offer "doesn't come around often."</p>` ||
		a.TLDR != "Loyalty Lobby confirms 20%." ||
		a.KeyDetails != `[{"Label":"Bonus","Value":"20% (Loyalty Lobby)"}]` {
		t.Errorf("unrevised article not rewritten by generation order: %+v", a)
	}
	if a.Slug != "unrevised" {
		t.Errorf("slug changed to %q", a.Slug)
	}
	if got := f.get(t, "revised-explicit").Content; got != `<p>ok</p><div class="editor-note">image from Loyalty Lobby</div>` {
		t.Errorf("explicit outlet not used: %q", got)
	}
	agree := f.get(t, "revised-agree")
	if agree.Content != "<p>Upgraded Points also notes the Companion Pass.</p>" || agree.ScoutsTake != "One Mile at a Time is right." {
		t.Errorf("revised article with agreeing orders not rewritten: %q / %q", agree.Content, agree.ScoutsTake)
	}

	// Ambiguous and partly unmappable articles are left entirely alone.
	for key, before := range map[string]*db.GeneratedArticle{"revised-ambiguous": ambiguous, "out-of-range": outOfRange, "clean": clean} {
		after := f.get(t, key)
		if after.Content != before.Content || after.UpdatedAt != before.UpdatedAt {
			t.Errorf("%s was modified: %q -> %q", key, before.Content, after.Content)
		}
	}

	// The search index follows the rewrite.
	var indexed string
	if err := f.database.DB().QueryRow(`SELECT content FROM articles_fts WHERE rowid = ?`, a.ID).Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexed, "One Mile at a Time notes") {
		t.Errorf("search index not updated: %q", indexed)
	}
	if !strings.Contains(out.String(), "Applied 3 article(s)") {
		t.Errorf("output:\n%s", out.String())
	}

	// A second run finds nothing left to rewrite and changes nothing.
	before := f.snapshot(t)
	out.Reset()
	if err := run([]string{"-db", f.path, "-apply"}, &out); err != nil {
		t.Fatal(err)
	}
	if after := f.snapshot(t); after != before {
		t.Errorf("second -apply changed the database")
	}
	if !strings.Contains(out.String(), "Applied 0 article(s)") {
		t.Errorf("second run output:\n%s", out.String())
	}
}

func TestApplyLeavesConcurrentlyEditedRowAlone(t *testing.T) {
	f := newFixture(t)
	conn := f.database.DB()
	plans, err := buildPlans(conn, map[int64]bool{f.ids["unrevised"]: true})
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %v, %v", plans, err)
	}
	// An admin edit lands between reading and writing.
	if _, err := conn.Exec(`UPDATE generated_articles SET content = 'edited' WHERE id = ?`, f.ids["unrevised"]); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	applied, _ := applyPlans(&out, conn, plans, f.database.SyncArticleFTS)
	if applied != 0 || f.get(t, "unrevised").Content != "edited" {
		t.Errorf("stale plan overwrote an edit (applied=%d):\n%s", applied, out.String())
	}
}

func TestIDsFlagLimitsTheRepair(t *testing.T) {
	f := newFixture(t)
	var out bytes.Buffer
	if err := run([]string{"-db", f.path, "-apply", "-ids", fmt.Sprint(f.ids["revised-agree"])}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.get(t, "revised-agree").Content, "Source") {
		t.Error("selected article not rewritten")
	}
	if !strings.Contains(f.get(t, "unrevised").Content, "Source 1") {
		t.Error("article outside -ids was rewritten")
	}
}

func TestMissingDatabaseIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")
	if err := run([]string{"-db", path}, &bytes.Buffer{}); err == nil {
		t.Error("want an error for a missing database, not a new empty one")
	}
}
