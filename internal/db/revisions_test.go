package db

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// newRevisionRequested creates an article in revision_requested with one piece
// of feedback, returning its id and the feedback id.
func newRevisionRequested(t *testing.T, d *Database, slug string) (int64, int64) {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "T", Slug: slug, Content: "old", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := d.UpdateArticleStatus(id, StatusRevisionRequested); err != nil {
		t.Fatalf("status: %v", err)
	}
	fid, err := d.CreateAdminFeedback(&AdminFeedback{GeneratedArticleID: id, FeedbackText: "fix", Source: "admin"})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	return id, fid
}

func revisionCount(t *testing.T, d *Database, id int64) int {
	t.Helper()
	revs, err := d.ListRevisionsForArticle(id)
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	return len(revs)
}

// TestApplyRevision_AppliesOnce is the cross-process backstop: two rewrites
// based on the same revision cannot both be stored.
func TestApplyRevision_AppliesOnce(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "apply-once")

	r := AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new"}
	ok, err := d.ApplyRevision(r)
	if err != nil || !ok {
		t.Fatalf("first apply = %v, %v; want applied", ok, err)
	}
	ok, err = d.ApplyRevision(r)
	if err != nil || ok {
		t.Fatalf("second apply = %v, %v; want not applied", ok, err)
	}

	a, _ := d.GetGeneratedArticle(id)
	if a.Status != StatusDraft || a.RevisionNumber != 2 || a.Content != "new" || a.Title != "New" {
		t.Errorf("article after apply = status %q rev %d content %q title %q",
			a.Status, a.RevisionNumber, a.Content, a.Title)
	}
	if n := revisionCount(t, d, id); n != 1 {
		t.Errorf("history rows = %d, want 1", n)
	}
}

// TestApplyRevision_RefusesSupersededFeedback: newer feedback mid-rewrite means
// the result is discarded and the article keeps waiting.
func TestApplyRevision_RefusesSupersededFeedback(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "superseded")
	if _, err := d.CreateAdminFeedback(&AdminFeedback{GeneratedArticleID: id, FeedbackText: "more", Source: "admin"}); err != nil {
		t.Fatalf("feedback: %v", err)
	}

	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new"})
	if err != nil || ok {
		t.Fatalf("apply against stale feedback = %v, %v; want not applied", ok, err)
	}
	a, _ := d.GetGeneratedArticle(id)
	if a.Status != StatusRevisionRequested || a.Content != "old" {
		t.Errorf("article changed by a discarded rewrite: status %q content %q", a.Status, a.Content)
	}
	if n := revisionCount(t, d, id); n != 0 {
		t.Errorf("history rows = %d, want 0", n)
	}
}

// TestApplyRevision_RefusesChangedStatus: an article approved mid-rewrite is not
// dragged back to draft.
func TestApplyRevision_RefusesChangedStatus(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "approved-meanwhile")
	if err := d.UpdateArticleStatus(id, StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new"})
	if err != nil || ok {
		t.Fatalf("apply to an approved article = %v, %v; want not applied", ok, err)
	}
	if a, _ := d.GetGeneratedArticle(id); a.Status != StatusApproved {
		t.Errorf("status = %q, want approved", a.Status)
	}
}

func TestRecordRevisionFailure(t *testing.T) {
	d := openTestDB(t)
	id, _ := newRevisionRequested(t, d, "failing")
	at := time.Unix(1_800_000_000, 0)

	for want := 1; want <= 2; want++ {
		n, err := d.RecordRevisionFailure(id, "provider returned 503", func(f int) time.Time {
			return at.Add(time.Duration(f) * time.Minute)
		})
		if err != nil || n != want {
			t.Fatalf("RecordRevisionFailure = %d, %v; want %d", n, err, want)
		}
	}
	pending, err := d.ListPendingRevisions()
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPendingRevisions = %v, %v", pending, err)
	}
	p := pending[0]
	if p.Failures != 2 || !p.RetryAfter.Equal(at.Add(2*time.Minute)) || p.LastError != "provider returned 503" {
		t.Errorf("pending = %+v", p)
	}

	if err := d.ResetRevisionFailures(id); err != nil {
		t.Fatalf("reset: %v", err)
	}
	pending, _ = d.ListPendingRevisions()
	if p := pending[0]; p.Failures != 0 || !p.RetryAfter.IsZero() || p.LastError != "" {
		t.Errorf("after reset = %+v", p)
	}
}

// TestRecordRevisionFailure_IgnoresArticlesNoLongerWaiting: a failure after the
// rewrite was stored must not mark a draft as a failed revision.
func TestRecordRevisionFailure_IgnoresArticlesNoLongerWaiting(t *testing.T) {
	d := openTestDB(t)
	id, _ := newRevisionRequested(t, d, "moved-on")
	if err := d.UpdateArticleStatus(id, StatusDraft); err != nil {
		t.Fatalf("draft: %v", err)
	}
	n, err := d.RecordRevisionFailure(id, "boom", func(int) time.Time { return time.Now() })
	if err != nil || n != 0 {
		t.Errorf("RecordRevisionFailure on a draft = %d, %v; want 0, nil", n, err)
	}
}

// TestApplyRevision_StoresStatusAndVerificationTogether: the final status and
// the critic's result land in the same transaction as the content, so there is
// no separate draft -> pending step that could overwrite feedback arriving in
// between.
func TestApplyRevision_StoresStatusAndVerificationTogether(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "passes-critic")

	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new", Status: StatusPending,
		Verification: &VerificationResult{Score: 91, IssuesJSON: "[]", FactualCheck: "pass",
			AttributionCheck: "pass", CompletenessCheck: "pass", HallucinationCheck: "pass"}})
	if err != nil || !ok {
		t.Fatalf("apply = %v, %v", ok, err)
	}
	if a, _ := d.GetGeneratedArticle(id); a.Status != StatusPending {
		t.Errorf("status = %q, want pending", a.Status)
	}
	v, err := d.GetLatestVerification(id)
	if err != nil || v.RevisionNumber != 2 || v.Score != 91 {
		t.Errorf("verification = %+v, %v; want revision 2 score 91", v, err)
	}

	if _, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 2, FeedbackID: fid,
		Status: StatusApproved}); err == nil {
		t.Error("ApplyRevision accepted a target status other than draft/pending")
	}
}

func TestGetRevisionRetryState(t *testing.T) {
	d := openTestDB(t)
	id, _ := newRevisionRequested(t, d, "retry-state")
	at := time.Unix(1_800_000_000, 0)
	if _, err := d.RecordRevisionFailure(id, "boom", func(int) time.Time { return at }); err != nil {
		t.Fatalf("record: %v", err)
	}
	p, ok, err := d.GetRevisionRetryState(id)
	if err != nil || !ok || p.Failures != 1 || !p.RetryAfter.Equal(at) {
		t.Errorf("state = %+v, %v, %v", p, ok, err)
	}
	if err := d.UpdateArticleStatus(id, StatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, ok, err := d.GetRevisionRetryState(id); ok || err != nil {
		t.Errorf("rejected article reported as waiting: ok=%v err=%v", ok, err)
	}
}

// TestApplyRevision_RefusesChangedRevisionNumber: the base revision alone is
// enough to refuse, even with status and feedback matching -- the rewrite was
// made from content that has since been replaced.
func TestApplyRevision_RefusesChangedRevisionNumber(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "stale-base")
	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 7, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new"})
	if err != nil || ok {
		t.Fatalf("apply on a stale base = %v, %v; want not applied", ok, err)
	}
	if a, _ := d.GetGeneratedArticle(id); a.RevisionNumber != 1 || a.Content != "old" {
		t.Errorf("article changed: rev %d content %q", a.RevisionNumber, a.Content)
	}
}

// TestTransitionArticleStatus_ConditionalOnValidatedStatus: an Approve validated
// against revision_requested must not land once a rewrite has moved the article
// to pending, or unreviewed content would be published.
func TestTransitionArticleStatus_ConditionalOnValidatedStatus(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "approve-race")

	// The rewrite lands first.
	if ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "unreviewed", Status: StatusPending}); err != nil || !ok {
		t.Fatalf("apply = %v, %v", ok, err)
	}
	// Then the Approve decided from the earlier read.
	err := d.TransitionArticleStatus(id, StatusRevisionRequested, StatusApproved)
	if !errors.Is(err, ErrStatusChanged) {
		t.Fatalf("TransitionArticleStatus = %v, want ErrStatusChanged", err)
	}
	if a, _ := d.GetGeneratedArticle(id); a.Status != StatusPending || a.PublishedAt != nil {
		t.Errorf("status %q published_at %v; the stale approve must not apply", a.Status, a.PublishedAt)
	}

	// From the status it really has, it works.
	if err := d.TransitionArticleStatus(id, StatusPending, StatusApproved); err != nil {
		t.Errorf("approve from the current status: %v", err)
	}
}

func TestValidTransitions_RevisionRequestedToPending(t *testing.T) {
	d := openTestDB(t)
	id, _ := newRevisionRequested(t, d, "rr-to-pending")
	if err := d.UpdateArticleStatus(id, StatusPending); err != nil {
		t.Errorf("revision_requested -> pending rejected: %v", err)
	}
}

// A rewrite refreshes what is derived from the content: its own key details
// replace the old ones (with the offer end date they state), the chart is
// cleared to be drawn again from them, and the attempts are reset so
// enrichment picks the article up.
func TestApplyRevision_RefreshesKeyDetailsAndChart(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "refresh")
	if _, err := d.DB().Exec(`UPDATE generated_articles SET key_details = ?, infographic_json = ?, infographic_note = 'n', enrichment_attempts = 3 WHERE id = ?`,
		`[{"Label":"Annual fee","Value":"$99"}]`, `{"kind":"figures"}`, id); err != nil {
		t.Fatal(err)
	}

	next := time.Now().Year() + 1
	details := fmt.Sprintf(`[{"Label":"Annual fee","Value":"$95"},{"Label":"Deadline","Value":"December 31, %d"}]`, next)
	ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new", KeyDetails: details})
	if err != nil || !ok {
		t.Fatalf("apply = %v, %v", ok, err)
	}
	var kd, chart, note, expires string
	var attempts int
	if err := d.DB().QueryRow(`SELECT key_details, COALESCE(infographic_json,''), infographic_note, COALESCE(expires_on,''), enrichment_attempts FROM generated_articles WHERE id = ?`, id).
		Scan(&kd, &chart, &note, &expires, &attempts); err != nil {
		t.Fatal(err)
	}
	if kd != details || chart != "" || attempts != 0 {
		t.Errorf("after the rewrite: key details %q, chart %q, attempts %d; want the rewrite's details, no chart, 0", kd, chart, attempts)
	}
	if want := fmt.Sprintf("%d-12-31", next); expires != want {
		t.Errorf("expires_on = %q, want the end date the new key details state", expires)
	}
	// The admin's note on the old chart is kept for the next draw.
	if note != "n" {
		t.Errorf("redraw note = %q, want it kept", note)
	}
}

// A rewrite that gave no key details clears the old ones, for enrichment to
// extract from the new text; a chart that was removed, or found nothing to
// chart, stays so.
func TestApplyRevision_WithoutKeyDetailsClearsThemAndKeepsARemovedChart(t *testing.T) {
	d := openTestDB(t)
	id, fid := newRevisionRequested(t, d, "no-details")
	if _, err := d.DB().Exec(`UPDATE generated_articles SET key_details = ?, infographic_json = ? WHERE id = ?`,
		`[{"Label":"Annual fee","Value":"$99"}]`, NoInfographic, id); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ApplyRevision(AppliedRevision{ArticleID: id, BaseRevision: 1, FeedbackID: fid,
		Title: "New", PreviousContent: "old", NewContent: "new"}); err != nil || !ok {
		t.Fatalf("apply = %v, %v", ok, err)
	}
	var kd, chart string
	if err := d.DB().QueryRow(`SELECT key_details, infographic_json FROM generated_articles WHERE id = ?`, id).Scan(&kd, &chart); err != nil {
		t.Fatal(err)
	}
	if kd != "" || chart != NoInfographic {
		t.Errorf("key details %q, chart %q; want none, and the removed chart left removed", kd, chart)
	}
	arts, err := d.ListArticlesNeedingEnrichmentWithInfographics(3, 10)
	if err != nil || len(arts) != 1 || !arts[0].MissingKeyDetails || arts[0].MissingInfographic {
		t.Errorf("listed %+v, %v; want the article, for its key details only", arts, err)
	}
}
