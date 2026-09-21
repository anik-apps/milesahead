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
}
