package db

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testNote       = "Could not find a link for: the promotion page"
	testNoteHTML   = `<div class="editor-note">` + testNote + `</div>`
	testBody       = "<p>Body text.</p>"
	testWithNote   = testBody + "\n" + testNoteHTML
	searchNotePhr  = `"Could not find a link"`
	searchBodyPhr  = `"Body text"`
	storedColumnsQ = `SELECT content, editor_notes FROM generated_articles WHERE id = ?`
)

// stored reads a row's columns as written, bypassing the read-time split.
func stored(t *testing.T, d *Database, id int64) (content, notes string) {
	t.Helper()
	if err := d.db.QueryRow(storedColumnsQ, id).Scan(&content, &notes); err != nil {
		t.Fatal(err)
	}
	return content, notes
}

func searchHits(t *testing.T, d *Database, phrase string) int {
	t.Helper()
	res, err := d.SearchArticles(phrase, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(res)
}

func TestCreateGeneratedArticle_SplitsEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "s", Content: testWithNote,
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
		EditorNotes: "from the pipeline"})
	if err != nil {
		t.Fatal(err)
	}
	content, notes := stored(t, d, id)
	if content != testBody {
		t.Errorf("stored content %q", content)
	}
	if notes != "from the pipeline\n"+testNote {
		t.Errorf("stored notes %q", notes)
	}
	if searchHits(t, d, searchNotePhr) != 0 {
		t.Error("note text indexed for search")
	}
	if searchHits(t, d, searchBodyPhr) != 1 {
		t.Error("body not indexed")
	}
}

// Rows stored before the split, and anything written around this package,
// still load with clean content and their notes in EditorNotes, through every
// way an article is loaded.
func TestLoadingSeparatesLegacyEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "legacy", Content: "x",
		Type: "deal", Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE generated_articles SET content = ?, featured = 1 WHERE id = ?`, testWithNote, id); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateArticleStatus(id, StatusApproved); err != nil {
		t.Fatal(err)
	}

	check := func(how string, a *GeneratedArticle) {
		t.Helper()
		if a.Content != testBody || a.EditorNotes != testNote {
			t.Errorf("%s: content %q notes %q", how, a.Content, a.EditorNotes)
		}
	}
	a, err := d.GetGeneratedArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	check("by id", a)
	if a, err = d.GetGeneratedArticleBySlug("legacy"); err != nil {
		t.Fatal(err)
	}
	check("by slug", a)
	if a, err = d.GetFeaturedArticle(); err != nil {
		t.Fatal(err)
	}
	check("featured", a)
	lists := map[string]func() ([]GeneratedArticle, error){
		"by status": func() ([]GeneratedArticle, error) { return d.ListArticlesByStatus(StatusApproved) },
		"published": func() ([]GeneratedArticle, error) { return d.ListPublishedArticles(10, 0) },
		"live":      func() ([]GeneratedArticle, error) { return d.ListLivePublishedArticles("2000-01-01", 10, 0) },
		"filtered":  func() ([]GeneratedArticle, error) { return d.ListPublishedArticlesFiltered("deal", "", 10, 0) },
	}
	for how, list := range lists {
		got, err := list()
		if err != nil || len(got) != 1 {
			t.Fatalf("%s: %v %v", how, got, err)
		}
		check(how, &got[0])
	}

	// No JSON rendering of an article can carry the notes.
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Could not find") {
		t.Errorf("notes in JSON: %s", b)
	}
}

func TestSyncArticleFTS_StripsLegacyEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "s", Content: "x",
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, testWithNote, id); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncArticleFTS(id); err != nil {
		t.Fatal(err)
	}
	if searchHits(t, d, searchNotePhr) != 0 || searchHits(t, d, searchBodyPhr) != 1 {
		t.Error("SyncArticleFTS indexed the note, or lost the body")
	}

	tx, err := d.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncArticleFTSTx(tx, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if searchHits(t, d, searchNotePhr) != 0 || searchHits(t, d, searchBodyPhr) != 1 {
		t.Error("SyncArticleFTSTx indexed the note, or lost the body")
	}
}

// The startup backfill of an empty index skips notes too.
func TestPopulateFTS_StripsEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "s", Content: "x",
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, testWithNote, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`DELETE FROM articles_fts`); err != nil {
		t.Fatal(err)
	}
	if err := populateFTS(d.db); err != nil {
		t.Fatal(err)
	}
	if searchHits(t, d, searchNotePhr) != 0 || searchHits(t, d, searchBodyPhr) != 1 {
		t.Error("populateFTS indexed the note, or lost the body")
	}
}

func TestApplyRevision_SplitsEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "rev")
	if _, err := d.db.Exec(`UPDATE generated_articles SET editor_notes = 'stale note' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "T2", PreviousContent: "old" + testNoteHTML, NewContent: testWithNote})
	if err != nil || !ok {
		t.Fatalf("apply: %v %v", ok, err)
	}
	content, notes := stored(t, d, id)
	if content != testBody {
		t.Errorf("stored content %q", content)
	}
	// The rewrite's notes replace the old ones, which described old content.
	if notes != testNote {
		t.Errorf("stored notes %q", notes)
	}
	revs, err := d.ListRevisionsForArticle(id)
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions %v %v", revs, err)
	}
	if revs[0].NewContent != testBody || revs[0].PreviousContent != "old" {
		t.Errorf("history stored notes: new %q previous %q", revs[0].NewContent, revs[0].PreviousContent)
	}

	// A rewrite with no notes clears them.
	if err := d.UpdateArticleStatus(id, StatusRevisionRequested); err != nil {
		t.Fatal(err)
	}
	fid2, err := d.CreateAdminFeedback(&AdminFeedback{GeneratedArticleID: id, FeedbackText: "again", Source: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 2, FeedbackID: fid2,
		Title: "T3", PreviousContent: testBody, NewContent: "<p>New.</p>"}); err != nil || !ok {
		t.Fatalf("second apply: %v %v", ok, err)
	}
	if _, notes := stored(t, d, id); notes != "" {
		t.Errorf("notes not cleared: %q", notes)
	}
}

func TestUpdateArticleContent_SplitsEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id := createTestArticle(t, d)
	if err := d.UpdateArticleContent(id, "T", testWithNote, 2); err != nil {
		t.Fatal(err)
	}
	if content, notes := stored(t, d, id); content != testBody || notes != testNote {
		t.Errorf("stored content %q notes %q", content, notes)
	}
}

// Enrichment writes public fields from the content, so it must not see notes.
func TestListArticlesNeedingEnrichment_StripsEditorNotes(t *testing.T) {
	d := openTestDB(t)
	id := createTestArticle(t, d)
	if _, err := d.db.Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, testWithNote, id); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Content != testBody {
		t.Errorf("enrichment input %q", got[0].Content)
	}
}

// An index entry built before notes were stripped is rebuilt at startup,
// without waiting for the repair tool; the content itself is left alone.
func TestStartupReindexesLegacyEditorNoteRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "T", Slug: "s", Content: "x",
		Type: "deal", Status: StatusApproved, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE generated_articles SET content = '` + testWithNote + `' WHERE id = 1`,
		`DELETE FROM articles_fts`,
		`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (1, 'T', '', 'Body text. Could not find a link for: the promotion page')`,
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if searchHits(t, d, searchNotePhr) != 1 {
		t.Fatal("fixture: legacy index entry should match the note")
	}
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if searchHits(t, d, searchNotePhr) != 0 || searchHits(t, d, searchBodyPhr) != 1 {
		t.Error("startup did not rebuild the index entry without the note")
	}
	if content, _ := stored(t, d, id); content != testWithNote {
		t.Errorf("startup changed stored content to %q", content)
	}
}
