package article

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

func setupTestDB(t *testing.T) *db.Database {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func createTestSourceAndRawArticle(t *testing.T, database *db.Database) int64 {
	t.Helper()
	srcID, err := database.CreateSource(&db.Source{
		Name:   "TestSource",
		URL:    "https://example.com",
		Active: true,
	})
	if err != nil {
		t.Fatalf("failed to create source: %v", err)
	}

	rawID, err := database.CreateRawArticle(&db.RawArticle{
		SourceID:    srcID,
		Title:       "Test Raw Article",
		URL:         "https://example.com/article",
		Content:     "Test content for the raw article.",
		PublishedAt: time.Now(),
		Hash:        "abc123hash",
	})
	if err != nil {
		t.Fatalf("failed to create raw article: %v", err)
	}
	return rawID
}

func TestManager_StateTransitions(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	// Create an article in pending status.
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Test Article",
		Slug:           "test-article-xyz123",
		Content:        "Content here",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}

	// Valid: pending -> approved
	if err := mgr.Approve(articleID); err != nil {
		t.Fatalf("expected Approve to succeed: %v", err)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusApproved {
		t.Errorf("expected status %q, got %q", db.StatusApproved, article.Status)
	}
	if article.PublishedAt == nil {
		t.Error("expected published_at to be set after approval")
	}

	// Invalid: approved -> approved (should fail)
	if err := mgr.Approve(articleID); err == nil {
		t.Error("expected error when approving already approved article")
	}

	// Valid: approved -> unpublished
	if err := mgr.Unpublish(articleID); err != nil {
		t.Fatalf("expected Unpublish to succeed: %v", err)
	}

	article, err = database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusUnpublished {
		t.Errorf("expected status %q, got %q", db.StatusUnpublished, article.Status)
	}

	// Valid: unpublished -> approved (re-publish)
	if err := mgr.Approve(articleID); err != nil {
		t.Errorf("expected unpublished article to be re-approvable, got: %v", err)
	}
}

func TestManager_Reject(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Reject Test",
		Slug:           "reject-test-abc123",
		Content:        "Content",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}

	if err := mgr.Reject(articleID); err != nil {
		t.Fatalf("expected Reject to succeed: %v", err)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusRejected {
		t.Errorf("expected status %q, got %q", db.StatusRejected, article.Status)
	}

	// Invalid: reject a draft
	draftID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Draft Article",
		Slug:           "draft-article-def456",
		Content:        "Content",
		Type:           "deal",
		Status:         db.StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err := mgr.Reject(draftID); err == nil {
		t.Error("expected error when rejecting draft article")
	}
}

func TestManager_RequestRevision(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Revision Test",
		Slug:           "revision-test-ghi789",
		Content:        "Content",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}

	if err := mgr.RequestRevision(articleID, "Please add more details", "admin"); err != nil {
		t.Fatalf("expected RequestRevision to succeed: %v", err)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusRevisionRequested {
		t.Errorf("expected status %q, got %q", db.StatusRevisionRequested, article.Status)
	}

	feedback, err := database.ListFeedbackForArticle(articleID)
	if err != nil {
		t.Fatalf("failed to list feedback: %v", err)
	}
	if len(feedback) != 1 {
		t.Fatalf("expected 1 feedback, got %d", len(feedback))
	}
	if feedback[0].FeedbackText != "Please add more details" {
		t.Errorf("unexpected feedback text: %q", feedback[0].FeedbackText)
	}
	if feedback[0].Source != "admin" {
		t.Errorf("unexpected feedback source: %q", feedback[0].Source)
	}
}

func TestManager_UnpublishOnApproved(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	// Cannot unpublish a pending article.
	pendingID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Pending Article",
		Slug:           "pending-unpub-test",
		Content:        "Content",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err := mgr.Unpublish(pendingID); err == nil {
		t.Error("expected error when unpublishing pending article")
	}
}

func TestManager_CreateFromPipeline(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	rawID := createTestSourceAndRawArticle(t, database)

	pipelineResult := &ai.PipelineResult{
		Content: "Great Deal Article\nFull content about a deal.",
		Title:   "Great Deal Article",
		Type:    "deal",
		Verification: &ai.VerificationResult{
			Score:              90,
			Issues:             []string{},
			FactualCheck:       true,
			AttributionCheck:   true,
			CompletenessCheck:  true,
			HallucinationCheck: true,
		},
		PassedThreshold: true,
	}

	articleID, err := mgr.CreateFromPipeline(pipelineResult, []int64{rawID}, "deal")
	if err != nil {
		t.Fatalf("CreateFromPipeline failed: %v", err)
	}
	if articleID == 0 {
		t.Fatal("expected non-zero article ID")
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Title != "Great Deal Article" {
		t.Errorf("expected title 'Great Deal Article', got %q", article.Title)
	}
	if article.Status != db.StatusPending {
		t.Errorf("expected status %q for passed threshold, got %q", db.StatusPending, article.Status)
	}
	if article.RevisionNumber != 1 {
		t.Errorf("expected revision_number 1, got %d", article.RevisionNumber)
	}

	// Verify verification was stored.
	v, err := database.GetLatestVerification(articleID)
	if err != nil {
		t.Fatalf("failed to get verification: %v", err)
	}
	if v.Score != 90 {
		t.Errorf("expected verification score 90, got %d", v.Score)
	}
}

func TestManager_CreateFromPipeline_BelowThreshold(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	rawID := createTestSourceAndRawArticle(t, database)

	pipelineResult := &ai.PipelineResult{
		Content: "Weak Article\nNot great content.",
		Title:   "Weak Article",
		Type:    "deal",
		Verification: &ai.VerificationResult{
			Score:              40,
			Issues:             []string{"Missing citations"},
			FactualCheck:       false,
			AttributionCheck:   false,
			CompletenessCheck:  false,
			HallucinationCheck: false,
		},
		PassedThreshold: false,
	}

	articleID, err := mgr.CreateFromPipeline(pipelineResult, []int64{rawID}, "deal")
	if err != nil {
		t.Fatalf("CreateFromPipeline failed: %v", err)
	}

	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}
	if article.Status != db.StatusDraft {
		t.Errorf("expected status %q for below threshold, got %q", db.StatusDraft, article.Status)
	}
}

func TestGenerateSlug(t *testing.T) {
	slug := GenerateSlug("Great Deal: 40% Bonus Transfer to ANA!")
	if len(slug) > 87 { // 80 + 1 hyphen + 6 suffix
		t.Errorf("slug too long: %d chars", len(slug))
	}
	// Should be lowercase, contain hyphens, no special chars.
	if strings.Contains(slug, " ") {
		t.Error("slug should not contain spaces")
	}
	if strings.Contains(slug, ":") {
		t.Error("slug should not contain colons")
	}
	if strings.Contains(slug, "!") {
		t.Error("slug should not contain exclamation marks")
	}
	if !strings.HasPrefix(slug, "great-deal-40-bonus-transfer-to-ana-") {
		t.Errorf("unexpected slug prefix: %q", slug)
	}
}

func TestGenerateSlug_LongTitle(t *testing.T) {
	longTitle := strings.Repeat("word ", 50) // 250 chars
	slug := GenerateSlug(longTitle)
	// Max 80 chars + hyphen + 6 suffix = 87
	if len(slug) > 87 {
		t.Errorf("slug too long for long title: %d chars, slug: %q", len(slug), slug)
	}
}
