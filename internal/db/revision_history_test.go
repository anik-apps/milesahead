package db

import "testing"

// The batch loaders behind the dashboard's revision history: each returns the
// rows for only the requested articles, grouped by article and in order.
func TestRevisionHistoryLoaders(t *testing.T) {
	d := openTestDB(t)
	a := createArticle(t, d, "a", StatusPending)
	b := createArticle(t, d, "b", StatusPending)
	other := createArticle(t, d, "other", StatusPending)

	feedback := func(article int64, text string) int64 {
		t.Helper()
		id, err := d.CreateAdminFeedback(&AdminFeedback{GeneratedArticleID: article, FeedbackText: text, Source: "admin"})
		if err != nil {
			t.Fatalf("CreateAdminFeedback: %v", err)
		}
		return id
	}
	a1 := feedback(a, "a first")
	feedback(b, "b only")
	feedback(a, "a second")
	feedback(other, "not listed")

	for _, r := range []ArticleRevision{
		{GeneratedArticleID: a, RevisionNumber: 3, PreviousContent: "p", NewContent: "n3", FeedbackID: a1},
		{GeneratedArticleID: a, RevisionNumber: 2, PreviousContent: "p", NewContent: "n2", FeedbackID: a1},
		{GeneratedArticleID: other, RevisionNumber: 2, PreviousContent: "p", NewContent: "x", FeedbackID: a1},
	} {
		if _, err := d.CreateArticleRevision(&r); err != nil {
			t.Fatalf("CreateArticleRevision: %v", err)
		}
	}
	for _, v := range []VerificationResult{
		{GeneratedArticleID: a, RevisionNumber: 2, Score: 70},
		{GeneratedArticleID: a, RevisionNumber: 2, Score: 88}, // re-verified: latest wins
		{GeneratedArticleID: b, RevisionNumber: 1, Score: 91},
		{GeneratedArticleID: other, RevisionNumber: 1, Score: 10},
	} {
		v.IssuesJSON = "[]"
		v.FactualCheck, v.AttributionCheck, v.CompletenessCheck, v.HallucinationCheck = "pass", "pass", "pass", "pass"
		if _, err := d.CreateVerificationResult(&v); err != nil {
			t.Fatalf("CreateVerificationResult: %v", err)
		}
	}

	ids := []int64{a, b}
	fb, err := d.ListFeedbackForArticles(ids)
	if err != nil {
		t.Fatalf("ListFeedbackForArticles: %v", err)
	}
	if len(fb) != 2 || len(fb[a]) != 2 || len(fb[b]) != 1 {
		t.Fatalf("feedback grouping = %+v", fb)
	}
	if fb[a][0].FeedbackText != "a first" || fb[a][1].FeedbackText != "a second" {
		t.Errorf("feedback for a not oldest first: %q, %q", fb[a][0].FeedbackText, fb[a][1].FeedbackText)
	}

	revs, err := d.ListRevisionsForArticles(ids)
	if err != nil {
		t.Fatalf("ListRevisionsForArticles: %v", err)
	}
	if len(revs) != 1 || len(revs[a]) != 2 || revs[a][0].RevisionNumber != 2 || revs[a][1].RevisionNumber != 3 {
		t.Errorf("revisions = %+v, want a's revisions 2 then 3 only", revs)
	}

	ver, err := d.ListVerificationsForArticles(ids)
	if err != nil {
		t.Fatalf("ListVerificationsForArticles: %v", err)
	}
	if len(ver) != 2 || ver[a][2].Score != 88 || ver[b][1].Score != 91 {
		t.Errorf("verifications = %+v, want a rev 2 = 88 and b rev 1 = 91", ver)
	}

	for name, err := range map[string]error{
		"feedback":      func() error { _, err := d.ListFeedbackForArticles(nil); return err }(),
		"revisions":     func() error { _, err := d.ListRevisionsForArticles(nil); return err }(),
		"verifications": func() error { _, err := d.ListVerificationsForArticles(nil); return err }(),
	} {
		if err != nil {
			t.Errorf("%s with no ids: %v", name, err)
		}
	}
}
