package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *Database {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(%q): %v", dbPath, err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// Verify the DB file was created.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file not created")
	}

	// Verify all 12 tables exist.
	tables := []string{
		"sources", "raw_articles", "generated_articles", "article_sources",
		"verification_results", "admin_feedback", "article_revisions",
		"article_votes", "newsletters", "subscribers", "admin_sessions",
		"reference_pages", "covered_raws",
	}
	for _, table := range tables {
		var name string
		err := d.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenAndMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	d1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	d1.Close()

	// Opening again should not fail (migrations are idempotent).
	d2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	d2.Close()
}

func TestMigration_LinkedCardsHeading(t *testing.T) {
	d := openTestDB(t)
	// The migration should have created the column. Test by setting + reading a value.
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "T", Slug: "test-linked-heading", Content: "C", Type: "deal",
		Status: StatusDraft, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Direct UPDATE of just the new column to verify it exists and persists.
	_, err = d.DB().Exec(`UPDATE generated_articles SET linked_cards_heading = ? WHERE id = ?`,
		"Cards earning Membership Rewards", id)
	if err != nil {
		t.Fatalf("update column: %v", err)
	}
	var heading string
	if err := d.DB().QueryRow(`SELECT linked_cards_heading FROM generated_articles WHERE id = ?`, id).Scan(&heading); err != nil {
		t.Fatalf("select: %v", err)
	}
	if heading != "Cards earning Membership Rewards" {
		t.Errorf("got %q, want %q", heading, "Cards earning Membership Rewards")
	}
}

// --- Source CRUD ---

func TestSourceCRUD(t *testing.T) {
	d := openTestDB(t)

	rssURL := "https://example.com/rss"
	s := &Source{
		Name:   "Test Source",
		URL:    "https://example.com",
		RSSURL: &rssURL,
		Active: true,
	}

	id, err := d.CreateSource(s)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	got, err := d.GetSource(id)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if got.Name != s.Name {
		t.Errorf("Name = %q, want %q", got.Name, s.Name)
	}
	if got.RSSURL == nil || *got.RSSURL != rssURL {
		t.Errorf("RSSURL = %v, want %q", got.RSSURL, rssURL)
	}
	if !got.Active {
		t.Error("expected Active = true")
	}

	// List active sources.
	sources, err := d.ListActiveSources()
	if err != nil {
		t.Fatalf("ListActiveSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("ListActiveSources: got %d, want 1", len(sources))
	}

	// Update source to inactive.
	got.Active = false
	got.Name = "Updated Source"
	if err := d.UpdateSource(got); err != nil {
		t.Fatalf("UpdateSource: %v", err)
	}

	sources, err = d.ListActiveSources()
	if err != nil {
		t.Fatalf("ListActiveSources after update: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("ListActiveSources: got %d, want 0", len(sources))
	}

	// Delete.
	if err := d.DeleteSource(id); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	_, err = d.GetSource(id)
	if err != sql.ErrNoRows {
		t.Errorf("GetSource after delete: got %v, want sql.ErrNoRows", err)
	}
}

// --- RawArticle CRUD + dedup ---

func createTestSource(t *testing.T, d *Database) int64 {
	t.Helper()
	id, err := d.CreateSource(&Source{Name: "Test", URL: "https://test.com", Active: true})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	return id
}

func TestRawArticleCRUD(t *testing.T) {
	d := openTestDB(t)
	srcID := createTestSource(t, d)

	a := &RawArticle{
		SourceID:    srcID,
		Title:       "Test Article",
		URL:         "https://test.com/article",
		Content:     "Article body text",
		PublishedAt: time.Now().UTC().Truncate(time.Second),
		Hash:        "abc123",
	}

	id, err := d.CreateRawArticle(a)
	if err != nil {
		t.Fatalf("CreateRawArticle: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	articles, err := d.GetRawArticlesBySourceID(srcID)
	if err != nil {
		t.Fatalf("GetRawArticlesBySourceID: %v", err)
	}
	if len(articles) != 1 {
		t.Fatalf("got %d articles, want 1", len(articles))
	}
	if articles[0].Title != "Test Article" {
		t.Errorf("Title = %q, want %q", articles[0].Title, "Test Article")
	}

	// Batch fetch.
	batch, err := d.GetRawArticlesByIDs([]int64{id})
	if err != nil {
		t.Fatalf("GetRawArticlesByIDs: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("got %d, want 1", len(batch))
	}

	// Empty batch.
	empty, err := d.GetRawArticlesByIDs(nil)
	if err != nil {
		t.Fatalf("GetRawArticlesByIDs(nil): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("got %d, want 0", len(empty))
	}
}

func TestRawArticleDedup(t *testing.T) {
	d := openTestDB(t)
	srcID := createTestSource(t, d)

	a := &RawArticle{
		SourceID:    srcID,
		Title:       "Dedup Article",
		URL:         "https://test.com/dedup",
		Content:     "Content",
		PublishedAt: time.Now().UTC().Truncate(time.Second),
		Hash:        "deduphash",
	}

	inserted, err := d.CreateRawArticleIfNew(a)
	if err != nil {
		t.Fatalf("first CreateRawArticleIfNew: %v", err)
	}
	if !inserted {
		t.Error("first insert should return true")
	}

	// Same hash again.
	inserted, err = d.CreateRawArticleIfNew(a)
	if err != nil {
		t.Fatalf("second CreateRawArticleIfNew: %v", err)
	}
	if inserted {
		t.Error("second insert with same hash should return false")
	}
}

// --- GeneratedArticle CRUD + status transitions ---

func createTestArticle(t *testing.T, d *Database) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title:          "Generated Article",
		Slug:           "generated-article",
		Content:        "Generated content",
		Type:           "article",
		Status:         StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("CreateGeneratedArticle: %v", err)
	}
	return id
}

func TestGeneratedArticleCRUD(t *testing.T) {
	d := openTestDB(t)
	id := createTestArticle(t, d)

	got, err := d.GetGeneratedArticle(id)
	if err != nil {
		t.Fatalf("GetGeneratedArticle: %v", err)
	}
	if got.Status != StatusDraft {
		t.Errorf("Status = %q, want %q", got.Status, StatusDraft)
	}
	if got.PublishedAt != nil {
		t.Error("expected nil PublishedAt for draft")
	}

	// List by status.
	drafts, err := d.ListArticlesByStatus(StatusDraft)
	if err != nil {
		t.Fatalf("ListArticlesByStatus: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("got %d drafts, want 1", len(drafts))
	}
}

func TestArticleStatusTransitions(t *testing.T) {
	d := openTestDB(t)
	id := createTestArticle(t, d)

	// Valid: draft -> pending
	if err := d.UpdateArticleStatus(id, StatusPending); err != nil {
		t.Fatalf("draft->pending: %v", err)
	}

	// Valid: pending -> approved (should set published_at)
	if err := d.UpdateArticleStatus(id, StatusApproved); err != nil {
		t.Fatalf("pending->approved: %v", err)
	}
	got, _ := d.GetGeneratedArticle(id)
	if got.PublishedAt == nil {
		t.Error("expected published_at to be set after approval")
	}

	// Valid: approved -> unpublished
	if err := d.UpdateArticleStatus(id, StatusUnpublished); err != nil {
		t.Fatalf("approved->unpublished: %v", err)
	}

	// Test rejected transition.
	id2 := createTestArticleWithSlug(t, d, "test-rejected")
	if err := d.UpdateArticleStatus(id2, StatusPending); err != nil {
		t.Fatalf("draft->pending (2): %v", err)
	}
	if err := d.UpdateArticleStatus(id2, StatusRejected); err != nil {
		t.Fatalf("pending->rejected: %v", err)
	}

	// Test revision_requested transition.
	id3 := createTestArticleWithSlug(t, d, "test-revision")
	if err := d.UpdateArticleStatus(id3, StatusPending); err != nil {
		t.Fatalf("draft->pending (3): %v", err)
	}
	if err := d.UpdateArticleStatus(id3, StatusRevisionRequested); err != nil {
		t.Fatalf("pending->revision_requested: %v", err)
	}

	// Valid transition: draft -> approved
	id4 := createTestArticleWithSlug(t, d, "test-invalid")
	err := d.UpdateArticleStatus(id4, StatusApproved)
	if err != nil {
		t.Errorf("unexpected error for draft->approved: %v", err)
	}
}

func createTestArticleWithSlug(t *testing.T, d *Database, slug string) int64 {
	t.Helper()
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title:          "Article " + slug,
		Slug:           slug,
		Content:        "Content",
		Type:           "article",
		Status:         StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("CreateGeneratedArticle(%s): %v", slug, err)
	}
	return id
}

func TestListPublishedArticles(t *testing.T) {
	d := openTestDB(t)

	// Create and approve 3 articles.
	for i := 0; i < 3; i++ {
		id := createTestArticleWithSlug(t, d, "published-"+time.Now().Format("150405.000000000")+string(rune('a'+i)))
		d.UpdateArticleStatus(id, StatusPending)
		d.UpdateArticleStatus(id, StatusApproved)
	}

	all, err := d.ListPublishedArticles(10, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticles: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d, want 3", len(all))
	}

	// Pagination.
	page, err := d.ListPublishedArticles(2, 0)
	if err != nil {
		t.Fatalf("ListPublishedArticles(2,0): %v", err)
	}
	if len(page) != 2 {
		t.Errorf("got %d, want 2", len(page))
	}
}

func TestLinkArticleSources(t *testing.T) {
	d := openTestDB(t)
	srcID := createTestSource(t, d)

	rawID, err := d.CreateRawArticle(&RawArticle{
		SourceID: srcID, Title: "Raw", URL: "https://test.com/raw",
		Content: "c", PublishedAt: time.Now().UTC(), Hash: "linkhash",
	})
	if err != nil {
		t.Fatalf("CreateRawArticle: %v", err)
	}

	genID := createTestArticle(t, d)
	if err := d.LinkArticleSources(genID, []int64{rawID}); err != nil {
		t.Fatalf("LinkArticleSources: %v", err)
	}

	// Linking again should not error (INSERT OR IGNORE).
	if err := d.LinkArticleSources(genID, []int64{rawID}); err != nil {
		t.Fatalf("LinkArticleSources duplicate: %v", err)
	}
}

// --- VerificationResult ---

func TestVerificationResult(t *testing.T) {
	d := openTestDB(t)
	artID := createTestArticle(t, d)

	v := &VerificationResult{
		GeneratedArticleID: artID,
		RevisionNumber:     1,
		Score:              85,
		IssuesJSON:         "[]",
		FactualCheck:       "pass",
		AttributionCheck:   "pass",
		CompletenessCheck:  "pass",
		HallucinationCheck: "pass",
	}
	id, err := d.CreateVerificationResult(v)
	if err != nil {
		t.Fatalf("CreateVerificationResult: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	// Create a second one with higher revision.
	v2 := *v
	v2.RevisionNumber = 2
	v2.Score = 90
	d.CreateVerificationResult(&v2)

	latest, err := d.GetLatestVerification(artID)
	if err != nil {
		t.Fatalf("GetLatestVerification: %v", err)
	}
	if latest.RevisionNumber != 2 {
		t.Errorf("RevisionNumber = %d, want 2", latest.RevisionNumber)
	}
	if latest.Score != 90 {
		t.Errorf("Score = %d, want 90", latest.Score)
	}
}

// --- AdminFeedback ---

func TestAdminFeedback(t *testing.T) {
	d := openTestDB(t)
	artID := createTestArticle(t, d)

	id, err := d.CreateAdminFeedback(&AdminFeedback{
		GeneratedArticleID: artID,
		FeedbackText:       "Needs more detail on signup bonus",
		Source:             "admin",
	})
	if err != nil {
		t.Fatalf("CreateAdminFeedback: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	list, err := d.ListFeedbackForArticle(artID)
	if err != nil {
		t.Fatalf("ListFeedbackForArticle: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d, want 1", len(list))
	}
	if list[0].FeedbackText != "Needs more detail on signup bonus" {
		t.Errorf("FeedbackText = %q", list[0].FeedbackText)
	}
}

// --- ArticleRevision ---

func TestArticleRevision(t *testing.T) {
	d := openTestDB(t)
	artID := createTestArticle(t, d)

	fbID, _ := d.CreateAdminFeedback(&AdminFeedback{
		GeneratedArticleID: artID, FeedbackText: "Fix it", Source: "admin",
	})

	id, err := d.CreateArticleRevision(&ArticleRevision{
		GeneratedArticleID: artID,
		RevisionNumber:     2,
		PreviousContent:    "old",
		NewContent:         "new",
		FeedbackID:         fbID,
	})
	if err != nil {
		t.Fatalf("CreateArticleRevision: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	revisions, err := d.ListRevisionsForArticle(artID)
	if err != nil {
		t.Fatalf("ListRevisionsForArticle: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("got %d, want 1", len(revisions))
	}
	if revisions[0].NewContent != "new" {
		t.Errorf("NewContent = %q", revisions[0].NewContent)
	}
}

// --- ArticleVote ---

func TestArticleVote(t *testing.T) {
	d := openTestDB(t)
	artID := createTestArticle(t, d)

	_, err := d.CreateVote(&ArticleVote{ArticleID: artID, Vote: 1, IPHash: "hash1"})
	if err != nil {
		t.Fatalf("CreateVote: %v", err)
	}
	_, err = d.CreateVote(&ArticleVote{ArticleID: artID, Vote: 1, IPHash: "hash2"})
	if err != nil {
		t.Fatalf("CreateVote: %v", err)
	}
	_, err = d.CreateVote(&ArticleVote{ArticleID: artID, Vote: -1, IPHash: "hash3"})
	if err != nil {
		t.Fatalf("CreateVote: %v", err)
	}

	counts, err := d.GetVoteCounts(artID)
	if err != nil {
		t.Fatalf("GetVoteCounts: %v", err)
	}
	if counts.Upvotes != 2 {
		t.Errorf("Upvotes = %d, want 2", counts.Upvotes)
	}
	if counts.Downvotes != 1 {
		t.Errorf("Downvotes = %d, want 1", counts.Downvotes)
	}

	voted, err := d.HasVoted(artID, "hash1")
	if err != nil {
		t.Fatalf("HasVoted: %v", err)
	}
	if !voted {
		t.Error("expected HasVoted = true")
	}

	voted, err = d.HasVoted(artID, "nonexistent")
	if err != nil {
		t.Fatalf("HasVoted: %v", err)
	}
	if voted {
		t.Error("expected HasVoted = false")
	}
}

// --- Newsletter ---

func TestNewsletter(t *testing.T) {
	d := openTestDB(t)

	n := &Newsletter{
		Date:         "2026-03-24",
		Subject:      "Daily Miles Digest",
		HTMLContent:  "<h1>Hello</h1>",
		Status:       StatusDraft,
		ArticleCount: 5,
	}
	id, err := d.CreateNewsletter(n)
	if err != nil {
		t.Fatalf("CreateNewsletter: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	got, err := d.GetNewsletterByDate("2026-03-24")
	if err != nil {
		t.Fatalf("GetNewsletterByDate: %v", err)
	}
	if got.Subject != "Daily Miles Digest" {
		t.Errorf("Subject = %q", got.Subject)
	}
	// The generation time it defaulted is written back, as stored.
	if n.CreatedAt == nil || got.CreatedAt == nil || !n.CreatedAt.Equal(*got.CreatedAt) {
		t.Errorf("CreatedAt = %v, stored %v; want the stored time written back", n.CreatedAt, got.CreatedAt)
	}
	if got.SentAt != nil {
		t.Error("expected nil SentAt")
	}

	if err := d.MarkNewsletterSent(id); err != nil {
		t.Fatalf("MarkNewsletterSent: %v", err)
	}
	got, _ = d.GetNewsletterByDate("2026-03-24")
	if got.SentAt == nil {
		t.Error("expected SentAt to be set")
	}
	if got.Status != "sent" {
		t.Errorf("Status = %q, want 'sent'", got.Status)
	}
}

// A send claims its newsletter once: a second claim finds it sent, and a
// failed send puts it back.
func TestClaimNewsletterSend(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateNewsletter(&Newsletter{Date: "2026-03-24", Subject: "S", Status: StatusDraft})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.ClaimNewsletterSend(id, StatusDraft); err != nil || !ok {
		t.Fatalf("first claim = %v, %v; want claimed", ok, err)
	}
	got, _ := d.GetNewsletterByDate("2026-03-24")
	if got.Status != "sent" || got.SentAt == nil {
		t.Errorf("claimed: status %q, sent at %v; want sent", got.Status, got.SentAt)
	}
	if ok, err := d.ClaimNewsletterSend(id, StatusDraft); err != nil || ok {
		t.Errorf("second claim = %v, %v; want not claimed", ok, err)
	}
	if err := d.ReleaseNewsletterSend(id, StatusDraft); err != nil {
		t.Fatal(err)
	}
	got, _ = d.GetNewsletterByDate("2026-03-24")
	if got.Status != StatusDraft || got.SentAt != nil {
		t.Errorf("released: status %q, sent at %v; want draft, never sent", got.Status, got.SentAt)
	}
}

// --- Subscriber ---

func TestSubscriber(t *testing.T) {
	d := openTestDB(t)

	id, err := d.CreateSubscriber(&Subscriber{
		Email: "test@example.com",
		Token: "token123",
	})
	if err != nil {
		t.Fatalf("CreateSubscriber: %v", err)
	}
	if id < 1 {
		t.Fatal("expected positive id")
	}

	// Not yet confirmed.
	confirmed, err := d.ListConfirmedSubscribers()
	if err != nil {
		t.Fatalf("ListConfirmedSubscribers: %v", err)
	}
	if len(confirmed) != 0 {
		t.Errorf("got %d confirmed, want 0", len(confirmed))
	}

	// Confirm.
	if err := d.ConfirmSubscriber("token123"); err != nil {
		t.Fatalf("ConfirmSubscriber: %v", err)
	}

	confirmed, err = d.ListConfirmedSubscribers()
	if err != nil {
		t.Fatalf("ListConfirmedSubscribers: %v", err)
	}
	if len(confirmed) != 1 {
		t.Fatalf("got %d confirmed, want 1", len(confirmed))
	}
	if confirmed[0].ConfirmedAt == nil {
		t.Error("expected ConfirmedAt to be set")
	}

	// Confirm again should fail.
	err = d.ConfirmSubscriber("token123")
	if err == nil {
		t.Error("expected error confirming already-confirmed subscriber")
	}

	// Unsubscribe.
	if err := d.Unsubscribe("token123"); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
	confirmed, _ = d.ListConfirmedSubscribers()
	if len(confirmed) != 0 {
		t.Errorf("got %d after unsubscribe, want 0", len(confirmed))
	}

	// Unsubscribe nonexistent.
	err = d.Unsubscribe("nonexistent")
	if err == nil {
		t.Error("expected error unsubscribing nonexistent token")
	}
}

func TestUpdateArticleLinkedCards(t *testing.T) {
	d := openTestDB(t)
	id, err := d.CreateGeneratedArticle(&GeneratedArticle{
		Title: "T", Slug: "linked-cards-update-test", Content: "C", Type: "deal",
		Status: StatusDraft, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	wantJSON := `[{"slug":"amex-platinum","title":"Amex Platinum"},{"slug":"amex-gold","title":"Amex Gold"}]`
	wantHeading := "Cards earning Membership Rewards"

	if err := d.UpdateArticleLinkedCards(id, wantJSON, wantHeading); err != nil {
		t.Fatalf("update: %v", err)
	}

	a, err := d.GetGeneratedArticle(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a.RelatedCardsJSON != wantJSON {
		t.Errorf("RelatedCardsJSON: got %q, want %q", a.RelatedCardsJSON, wantJSON)
	}
	if a.LinkedCardsHeading != wantHeading {
		t.Errorf("LinkedCardsHeading: got %q, want %q", a.LinkedCardsHeading, wantHeading)
	}
}

// --- AdminSession ---

func TestAdminSession(t *testing.T) {
	d := openTestDB(t)

	s := &AdminSession{
		Token:     "session-token-abc",
		ExpiresAt: time.Now().UTC().Add(1 * time.Hour),
	}
	if err := d.CreateSession(s); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	got, err := d.GetSession("session-token-abc")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.Token != s.Token {
		t.Errorf("Token = %q, want %q", got.Token, s.Token)
	}

	// Expired session should not be returned.
	expired := &AdminSession{
		Token:     "expired-token",
		ExpiresAt: time.Now().UTC().Add(-1 * time.Hour),
	}
	d.CreateSession(expired)
	_, err = d.GetSession("expired-token")
	if err != sql.ErrNoRows {
		t.Errorf("GetSession(expired): got %v, want sql.ErrNoRows", err)
	}

	// Delete expired.
	if err := d.DeleteExpiredSessions(); err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}

	// Valid session still available.
	got, err = d.GetSession("session-token-abc")
	if err != nil {
		t.Errorf("GetSession after cleanup: %v", err)
	}
}

// TestOpen_AppliesPragmasToEveryConnection guards a subtle pooling bug.
//
// busy_timeout and foreign_keys are PER-CONNECTION. They used to be set with
// conn.Exec("PRAGMA ..."), which runs on whichever pooled connection is free —
// configuring exactly one and leaving the rest at defaults. A probe of two
// connections from the same pool previously reported foreign_keys 1 and 0.
//
// busy_timeout mattered most: at 0 a second concurrent writer fails instantly
// with SQLITE_BUSY instead of waiting, and if that lands on the ALTER TABLE in
// migrate() the service refuses to start, reporting a transient lock as a fatal
// schema error.
func TestOpen_AppliesPragmasToEveryConnection(t *testing.T) {
	d := openTestDB(t)

	// Force several distinct connections to be open simultaneously, so the
	// checks below cannot all land on the same one.
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := d.DB().Conn(context.Background())
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		t.Cleanup(func() { c.Close() })
		conns = append(conns, c)
	}

	for i, c := range conns {
		var busy, fk int
		var journal string
		if err := c.QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if err := c.QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if err := c.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		if busy <= 0 {
			t.Errorf("conn %d: busy_timeout = %d; a concurrent writer will fail "+
				"instantly instead of waiting", i, busy)
		}
		if fk != 1 {
			t.Errorf("conn %d: foreign_keys = %d, want 1", i, fk)
		}
		if journal != "wal" {
			t.Errorf("conn %d: journal_mode = %q, want wal", i, journal)
		}
	}
}
