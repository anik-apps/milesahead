package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func expiresOn(t *testing.T, d *Database, id int64) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := d.DB().QueryRow(`SELECT expires_on FROM generated_articles WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("reading expires_on for %d: %v", id, err)
	}
	return v
}

// A database from before this change has no expires_on column and rows whose
// key details already state deadlines. Opening it must add the column and
// fill it, so production is corrected by the deploy itself.
func TestMigrate_BackfillsOfferEndDatesOnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Turn it back into the old schema.
	if _, err := d.DB().Exec(`ALTER TABLE generated_articles DROP COLUMN expires_on`); err != nil {
		t.Fatalf("dropping column: %v", err)
	}
	insert := func(slug, keyDetails, createdAt string) int64 {
		t.Helper()
		res, err := d.DB().Exec(`INSERT INTO generated_articles
			(title, slug, content, type, status, key_details, created_at)
			VALUES (?, ?, 'Body', 'deal', 'approved', ?, ?)`,
			"T "+slug, slug, keyDetails, createdAt)
		if err != nil {
			t.Fatalf("insert %s: %v", slug, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	// Both key spellings occur in production: Go-marshalled and model JSON.
	aspire := insert("aspire", `[{"Label":"Bonus","Value":"175K"},{"Label":"Deadline","Value":"July 29, 2026"}]`, "2026-07-22 10:00:00")
	ihg := insert("ihg", `[{"label":"Minimum Spend","value":"$400"},{"label":"Deadline","value":"June 30"}]`, "2026-05-08 09:00:00")
	lhw := insert("lhw", `[{"label":"Deadline","value":"September 19, 2026"}]`, "2026-09-10 09:00:00")
	// Only created_at can place this one: written in early January about the
	// December deadline just gone, it is 2025, not the coming December.
	newYear := insert("new-year", `[{"Label":"Deadline","Value":"December 31"}]`, "2026-01-05 09:00:00")
	// Written in May about the coming year end: this December, not last.
	yearEnd := insert("year-end", `[{"Label":"Deadline","Value":"December 31"}]`, "2026-05-14 09:00:00")
	noDeadline := insert("no-deadline", `[{"Label":"Deadline","Value":"Limited-time offer, no hard deadline specified"}]`, "2026-07-22 10:00:00")
	effective := insert("effective", `[{"Label":"Effective Date","Value":"April 1, 2026"}]`, "2026-03-30 10:00:00")
	nullDetails := insert("null-details", `null`, "2026-07-01 10:00:00")
	broken := insert("broken", `[{"Label":"Deadline",`, "2026-07-01 10:00:00")
	d.Close()

	d, err = Open(path)
	if err != nil {
		t.Fatalf("reopening old database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	want := map[int64]string{
		aspire:  "2026-07-29",
		ihg:     "2026-06-30", // yearless: the next June 30 after created_at
		lhw:     "2026-09-19",
		newYear: "2025-12-31",
		yearEnd: "2026-12-31",
	}
	for id, w := range want {
		if got := expiresOn(t, d, id); !got.Valid || got.String != w {
			t.Errorf("article %d expires_on = %v, want %s", id, got, w)
		}
	}
	for _, id := range []int64{noDeadline, effective, nullDetails, broken} {
		if got := expiresOn(t, d, id); got.Valid {
			t.Errorf("article %d expires_on = %q, want NULL", id, got.String)
		}
	}

	// It runs on every startup, so it must converge: a second pass changes
	// nothing.
	n, err := backfillOfferEndDates(d.DB())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second backfill pass changed %d rows, want 0", n)
	}

	// A stale stored value (say, from an older parser) is corrected, and one
	// the key details no longer support is cleared.
	setFields(t, d, aspire, "expires_on = '1999-01-01'")
	setFields(t, d, effective, "expires_on = '2026-04-01'")
	n, err = backfillOfferEndDates(d.DB())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("backfill changed %d rows, want 2", n)
	}
	if got := expiresOn(t, d, aspire); got.String != "2026-07-29" {
		t.Errorf("stale value not corrected: %v", got)
	}
	if got := expiresOn(t, d, effective); got.Valid {
		t.Errorf("unsupported value not cleared: %v", got)
	}
}

// New articles get their end date when they are generated.
func TestCreateGeneratedArticle_SetsOfferEndDate(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "Citi 25% to LHW", Slug: "citi-lhw", Content: "Body", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
		KeyDetails: `[{"Label":"Deadline","Value":"September 19, 2026"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := expiresOn(t, d, id); got.String != "2026-09-19" {
		t.Errorf("expires_on = %v, want 2026-09-19", got)
	}
	a, err := d.GetGeneratedArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExpiresOn != "2026-09-19" {
		t.Errorf("GetGeneratedArticle ExpiresOn = %q, want 2026-09-19", a.ExpiresOn)
	}

	plain := makeArticle(t, d, "no-details")
	if got := expiresOn(t, d, plain); got.Valid {
		t.Errorf("article without key details got expires_on %q", got.String)
	}
}

// Key details filled in later by the enrichment reconciler set the end date
// in the same write.
func TestApplyEnrichment_SetsOfferEndDate(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "late-details")
	setFields(t, d, id, "created_at = '2026-01-05 09:00:00'")

	kd := `[{"label":"Deadline","value":"December 31"}]`
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{KeyDetails: &kd}); err != nil {
		t.Fatal(err)
	}
	if got := expiresOn(t, d, id); got.String != "2025-12-31" {
		t.Errorf("expires_on = %v, want 2025-12-31 (yearless, anchored on created_at, not today)", got)
	}

	// A pass that fills something else leaves the end date alone.
	take := "Worth it."
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{ScoutsTake: &take}); err != nil {
		t.Fatal(err)
	}
	if got := expiresOn(t, d, id); got.String != "2025-12-31" {
		t.Errorf("expires_on changed by an unrelated pass: %v", got)
	}
}

func TestWrittenAt(t *testing.T) {
	want := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	for _, v := range []any{want, "2026-01-05 09:00:00", "2026-01-05T09:00:00Z"} {
		if got := writtenAt(v); !got.Equal(want) {
			t.Errorf("writtenAt(%#v) = %v, want %v", v, got, want)
		}
	}
	// Unreadable values fall back to now instead of failing the migration.
	for _, v := range []any{nil, "garbage", int64(7)} {
		if got := writtenAt(v); time.Since(got) > time.Minute {
			t.Errorf("writtenAt(%#v) = %v, want about now", v, got)
		}
	}
}

func approvedWithDeadline(t *testing.T, d *Database, slug, deadline string) int64 {
	t.Helper()
	kd := ""
	if deadline != "" {
		kd = `[{"Label":"Deadline","Value":"` + deadline + `"}]`
	}
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "T " + slug, Slug: slug, Content: "Chase body", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1, KeyDetails: kd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateArticleStatus(id, StatusApproved); err != nil {
		t.Fatal(err)
	}
	return id
}

// The home feed's query: an offer is live through its last day.
func TestListLivePublishedArticles(t *testing.T) {
	d := openTestDB(t)
	approvedWithDeadline(t, d, "ended", "July 29, 2026")
	approvedWithDeadline(t, d, "ends-today", "September 23, 2026")
	approvedWithDeadline(t, d, "future", "September 30, 2026")
	approvedWithDeadline(t, d, "no-deadline", "")

	got, err := d.ListLivePublishedArticles("2026-09-23", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]bool{}
	for _, a := range got {
		slugs[a.Slug] = true
	}
	if slugs["ended"] {
		t.Error("an ended offer was listed as live")
	}
	for _, s := range []string{"ends-today", "future", "no-deadline"} {
		if !slugs[s] {
			t.Errorf("live article %q missing", s)
		}
	}

	all, err := d.ListPublishedArticles(20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("ListPublishedArticles returned %d, want all 4", len(all))
	}
}

func TestOfferEndsBySlugAndSearch(t *testing.T) {
	d := openTestDB(t)
	approvedWithDeadline(t, d, "ended", "July 29, 2026")
	approvedWithDeadline(t, d, "no-deadline", "")

	m, err := d.OfferEndsBySlug([]string{"ended", "no-deadline", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (OfferEnd{EndsOn: "2026-07-29", ArticleType: "deal"}); len(m) != 1 || m["ended"] != want {
		t.Errorf("OfferEndsBySlug = %v, want only ended=%v", m, want)
	}

	results, err := d.SearchArticles(`"Chase"`, "", "2026-01-01", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range results {
		got[r.Slug] = r.ExpiresOn
		if r.ArticleType != "deal" {
			t.Errorf("search result %s ArticleType = %q, want deal", r.Slug, r.ArticleType)
		}
	}
	if got["ended"] != "2026-07-29" || got["no-deadline"] != "" || len(got) != 2 {
		t.Errorf("search ExpiresOn = %v", got)
	}
}

// Another process can write the database while the backfill runs (the
// server, when a command opens the same file). A row rewritten between the
// backfill's read and its write -- by an enrichment pass storing new key
// details and their end date -- keeps the newer value rather than getting
// one derived from the details it replaced.
func TestBackfillOfferEndDates_KeepsRowsWrittenSinceTheRead(t *testing.T) {
	d := openTestDB(t)
	moved := approvedWithDeadline(t, d, "moved", "July 29, 2026")
	enriched := approvedWithDeadline(t, d, "enriched", "July 29, 2026")
	stale := approvedWithDeadline(t, d, "stale", "July 29, 2026")
	// Wrong stored values, as an older parser would have left them.
	for _, id := range []int64{moved, enriched, stale} {
		setFields(t, d, id, "expires_on = '1999-01-01'")
	}

	changes, err := offerEndChanges(d.DB())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("want 3 rows to correct, got %d", len(changes))
	}

	// Between the read and the write: one row's end date is rewritten, and
	// another's key details are replaced while its end date is left as it
	// was, which a check on the end date alone would miss.
	setFields(t, d, moved, "expires_on = '2026-08-15'")
	setFields(t, d, enriched, `key_details = '[{"Label":"Deadline","Value":"No deadline announced"}]'`)

	n, err := applyOfferEndChanges(d.DB(), changes)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("applied %d changes, want only the untouched row's", n)
	}
	if got := expiresOn(t, d, moved); got.String != "2026-08-15" {
		t.Errorf("a newer end date was overwritten: %v", got)
	}
	if got := expiresOn(t, d, enriched); got.String != "1999-01-01" {
		t.Errorf("an end date was derived from key details that had been replaced: %v", got)
	}
	if got := expiresOn(t, d, stale); got.String != "2026-07-29" {
		t.Errorf("the untouched row was not corrected: %v", got)
	}
}
