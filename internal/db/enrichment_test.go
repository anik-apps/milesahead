package db

import (
	"testing"
)

func makeArticle(t *testing.T, d *Database, slug string) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "T " + slug, Slug: slug, Content: "Body", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create %s: %v", slug, err)
	}
	return id
}

func setFields(t *testing.T, d *Database, id int64, sql string, args ...any) {
	t.Helper()
	args = append(args, id)
	if _, err := d.DB().Exec("UPDATE generated_articles SET "+sql+" WHERE id = ?", args...); err != nil {
		t.Fatalf("set fields: %v", err)
	}
}

// TestListArticlesNeedingEnrichment_DetectsNullKeyDetails is the case that
// mattered in production. key_details is a TEXT column holding JSON, and a nil
// value marshalled unguarded lands as the four-character string "null" — which
// is neither NULL nor ”, so the obvious emptiness checks miss it. Every
// article degraded from 2026-07-01 onward carries exactly that.
func TestListArticlesNeedingEnrichment_DetectsNullKeyDetails(t *testing.T) {
	d := openTestDB(t)

	complete := makeArticle(t, d, "complete")
	setFields(t, d, complete,
		"scouts_take = 'take', key_details = '[{\"Label\":\"a\"}]', image_url = 'u', tags_json = '[\"deals\"]'")

	nullDetails := makeArticle(t, d, "null-details")
	setFields(t, d, nullDetails,
		"scouts_take = 'take', key_details = 'null', image_url = 'u', tags_json = '[\"deals\"]'")

	got, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected only the 'null' row, got %d rows", len(got))
	}
	if got[0].ID != nullDetails {
		t.Errorf("returned article %d, want %d", got[0].ID, nullDetails)
	}
	if !got[0].MissingKeyDetails {
		t.Error("key_details = 'null' must count as missing")
	}
	if got[0].MissingScoutsTake || got[0].MissingImage || got[0].MissingTags {
		t.Errorf("only key_details should be missing, got %q", got[0].Missing())
	}
}

// TestListArticlesNeedingEnrichment_RespectsAttemptBudget verifies the
// reconciler gives up. Without a bound, an article the model simply cannot
// enrich would be retried on every pass forever.
func TestListArticlesNeedingEnrichment_RespectsAttemptBudget(t *testing.T) {
	d := openTestDB(t)

	fresh := makeArticle(t, d, "fresh")
	exhausted := makeArticle(t, d, "exhausted")
	setFields(t, d, exhausted, "enrichment_attempts = 3")

	got, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != fresh {
		t.Fatalf("expected only the un-exhausted article %d, got %+v", fresh, got)
	}

	pending, done, err := d.CountDegradedArticles(3)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if pending != 1 || done != 1 {
		t.Errorf("counts pending=%d exhausted=%d, want 1 and 1", pending, done)
	}
}

// TestListArticlesNeedingEnrichment_SkipsRejected — spending model calls on an
// article an editor already threw out is pure waste.
func TestListArticlesNeedingEnrichment_SkipsRejected(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "rejected")
	if err := d.UpdateArticleStatus(id, StatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}
	got, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("rejected articles must not be enriched, got %d", len(got))
	}
}

// TestApplyEnrichment_KeepsPartialSuccessAndCountsAttempt covers the two
// properties that make reconciliation work: a field that succeeded is kept even
// when a sibling failed, and the attempt is recorded either way so a hopeless
// article eventually stops being retried.
func TestApplyEnrichment_KeepsPartialSuccessAndCountsAttempt(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "partial")

	take := "A considered take."
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{ScoutsTake: &take}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var gotTake, gotDetails string
	var attempts int
	err := d.DB().QueryRow(
		"SELECT scouts_take, COALESCE(key_details,''), enrichment_attempts FROM generated_articles WHERE id = ?",
		id).Scan(&gotTake, &gotDetails, &attempts)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if gotTake != take {
		t.Errorf("scouts_take = %q, want %q", gotTake, take)
	}
	if attempts != 1 {
		t.Errorf("enrichment_attempts = %d, want 1", attempts)
	}

	// A pass that fills nothing must still consume an attempt.
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{}); err != nil {
		t.Fatalf("apply empty: %v", err)
	}
	if err := d.DB().QueryRow(
		"SELECT enrichment_attempts FROM generated_articles WHERE id = ?", id).Scan(&attempts); err != nil {
		t.Fatalf("read back attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("a pass that filled nothing must still count: attempts = %d, want 2", attempts)
	}

	// An uncounted update writes its fields and leaves the counter alone.
	tags := `["hotels"]`
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{TagsJSON: &tags, Uncounted: true}); err != nil {
		t.Fatalf("apply uncounted: %v", err)
	}
	var gotTags string
	if err := d.DB().QueryRow(
		"SELECT tags_json, enrichment_attempts FROM generated_articles WHERE id = ?", id).Scan(&gotTags, &attempts); err != nil {
		t.Fatalf("read back uncounted: %v", err)
	}
	if gotTags != tags || attempts != 2 {
		t.Errorf("uncounted update: tags %q, attempts %d; want %q and 2", gotTags, attempts, tags)
	}
}

// The infographic is a gap only when the caller asks for it, and the sentinel
// counts as filled. InfographicsFor returns what is stored, sentinel included,
// and nothing for an article without a chart.
func TestInfographics_ListingAndLoading(t *testing.T) {
	d := openTestDB(t)
	complete := "scouts_take = 'take', key_details = '[{\"Label\":\"a\"}]', image_url = 'u', tags_json = '[\"deals\"]'"

	none := makeArticle(t, d, "no-chart")
	setFields(t, d, none, complete)
	drawn := makeArticle(t, d, "drawn")
	setFields(t, d, drawn, complete+", infographic_json = ?", `{"kind":"figures"}`)
	nothing := makeArticle(t, d, "nothing-to-chart")
	setFields(t, d, nothing, complete+", infographic_json = ?", NoInfographic)

	if got, err := d.ListArticlesNeedingEnrichment(3, 10); err != nil || len(got) != 0 {
		t.Errorf("without infographics listed %d (%v); a missing chart is not a gap", len(got), err)
	}
	got, err := d.ListArticlesNeedingEnrichmentWithInfographics(3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != none || !got[0].MissingInfographic || got[0].Missing() != "infographic" {
		t.Errorf("with infographics listed %+v; want only the article with no chart", got)
	}
	if len(got) == 1 && got[0].KeyDetails != `[{"Label":"a"}]` {
		t.Errorf("KeyDetails = %q; the chart is drawn from them", got[0].KeyDetails)
	}

	charts, err := d.InfographicsFor([]int64{none, drawn, nothing})
	if err != nil {
		t.Fatal(err)
	}
	if len(charts) != 2 || charts[drawn].JSON != `{"kind":"figures"}` || charts[nothing].JSON != NoInfographic {
		t.Errorf("InfographicsFor = %v", charts)
	}
	if empty, err := d.InfographicsFor(nil); err != nil || len(empty) != 0 {
		t.Errorf("InfographicsFor(nil) = %v, %v", empty, err)
	}
}

// Redraw chart with a note: the chart is cleared, the attempts reset and the
// note kept for the next draw, which clears it once it stores a chart or
// "nothing to chart". A draw that fails keeps the note for the retry.
func TestRedrawInfographic_NoteLastsUntilAChartIsStored(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "redraw-note")
	setFields(t, d, id, "infographic_json = ?, enrichment_attempts = 3", `{"kind":"figures"}`)

	if err := d.RedrawInfographic(id, "The cap is $1,500"); err != nil {
		t.Fatal(err)
	}
	charts, err := d.InfographicsFor([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if got := charts[id]; got.JSON != "" || got.Note != "The cap is $1,500" {
		t.Errorf("after redraw: %+v; want no chart and the note", got)
	}
	arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(3, 10)
	if err != nil || len(arts) != 1 || arts[0].InfographicNote != "The cap is $1,500" {
		t.Fatalf("listed %+v, %v; want the article with its note", arts, err)
	}

	// A failed draw stores nothing and keeps the note.
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{}); err != nil {
		t.Fatal(err)
	}
	if charts, _ := d.InfographicsFor([]int64{id}); charts[id].Note == "" {
		t.Error("a failed draw dropped the note")
	}

	chart := `{"kind":"figures","headline":"H"}`
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{InfographicJSON: &chart}); err != nil {
		t.Fatal(err)
	}
	if got := mustInfographics(t, d, id); got.JSON != chart || got.Note != "" {
		t.Errorf("after a chart is stored: %+v; want the chart and no note", got)
	}

	// Redraw without a note replaces an old one with nothing.
	if err := d.RedrawInfographic(id, "old note"); err != nil {
		t.Fatal(err)
	}
	if err := d.RedrawInfographic(id, ""); err != nil {
		t.Fatal(err)
	}
	if got := mustInfographics(t, d, id); got.Note != "" {
		t.Errorf("note = %q after a redraw without one", got.Note)
	}
}

func mustInfographics(t *testing.T, d *Database, id int64) StoredInfographic {
	t.Helper()
	charts, err := d.InfographicsFor([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	return charts[id]
}
