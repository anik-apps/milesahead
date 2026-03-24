package article

import (
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// mockHFClient is a simple mock for ai.HFClientInterface.
type mockHFClient struct {
	responses []string
	callIdx   int
}

func (m *mockHFClient) Generate(model, prompt string, maxTokens int) (string, error) {
	if m.callIdx >= len(m.responses) {
		return m.responses[len(m.responses)-1], nil
	}
	resp := m.responses[m.callIdx]
	m.callIdx++
	return resp, nil
}

func TestProcessRevision(t *testing.T) {
	database := setupTestDB(t)

	// Create a mock HF client that returns different responses for generator and critic.
	// The pipeline calls generator first, then critic.
	mock := &mockHFClient{
		responses: []string{
			// Generator response (revision).
			"Revised Title\nRevised content with better details [Source]",
			// Critic response.
			`{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
		},
	}

	gen := ai.NewGenerator(mock, "test-model", nil)
	critic := ai.NewCritic(mock, "test-model")
	pipeline := ai.NewPipeline(gen, critic, 80)

	mgr := NewManager(database, pipeline)

	// Set up source + raw article.
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
		Title:       "Source Article",
		URL:         "https://example.com/source",
		Content:     "Source content about a deal.",
		PublishedAt: time.Now(),
		Hash:        "revision-test-hash",
	})
	if err != nil {
		t.Fatalf("failed to create raw article: %v", err)
	}

	// Create a generated article in pending status.
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Original Title",
		Slug:           "original-title-abc123",
		Content:        "Original content.",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("failed to create article: %v", err)
	}

	// Link the source.
	if err := database.LinkArticleSources(articleID, []int64{rawID}); err != nil {
		t.Fatalf("failed to link sources: %v", err)
	}

	// Store initial verification.
	_, err = database.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: articleID,
		RevisionNumber:     1,
		Score:              60,
		IssuesJSON:         `["Missing details"]`,
		FactualCheck:       "pass",
		AttributionCheck:   "fail",
		CompletenessCheck:  "fail",
		HallucinationCheck: "pass",
	})
	if err != nil {
		t.Fatalf("failed to create verification: %v", err)
	}

	// Request revision (pending -> revision_requested).
	if err := mgr.RequestRevision(articleID, "Please add more specific details and citations", "admin"); err != nil {
		t.Fatalf("RequestRevision failed: %v", err)
	}

	// Process revision.
	if err := mgr.ProcessRevision(articleID); err != nil {
		t.Fatalf("ProcessRevision failed: %v", err)
	}

	// Verify the article was updated.
	article, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("failed to get article: %v", err)
	}

	if article.Title != "Revised Title" {
		t.Errorf("expected title 'Revised Title', got %q", article.Title)
	}
	if article.Content != "Revised Title\nRevised content with better details [Source]" {
		t.Errorf("unexpected content: %q", article.Content)
	}
	if article.RevisionNumber != 2 {
		t.Errorf("expected revision_number 2, got %d", article.RevisionNumber)
	}
	// Score 92 >= threshold 80, so should be pending.
	if article.Status != db.StatusPending {
		t.Errorf("expected status %q (passed threshold), got %q", db.StatusPending, article.Status)
	}

	// Verify revision history was created.
	revisions, err := database.ListRevisionsForArticle(articleID)
	if err != nil {
		t.Fatalf("failed to list revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("expected 1 revision, got %d", len(revisions))
	}
	if revisions[0].RevisionNumber != 2 {
		t.Errorf("expected revision number 2, got %d", revisions[0].RevisionNumber)
	}
	if revisions[0].PreviousContent != "Original content." {
		t.Errorf("unexpected previous content: %q", revisions[0].PreviousContent)
	}
	if revisions[0].NewContent != "Revised Title\nRevised content with better details [Source]" {
		t.Errorf("unexpected new content: %q", revisions[0].NewContent)
	}

	// Verify new verification was stored.
	v, err := database.GetLatestVerification(articleID)
	if err != nil {
		t.Fatalf("failed to get verification: %v", err)
	}
	if v.RevisionNumber != 2 {
		t.Errorf("expected verification revision_number 2, got %d", v.RevisionNumber)
	}
	if v.Score != 92 {
		t.Errorf("expected verification score 92, got %d", v.Score)
	}
}

func TestProcessRevision_BelowThreshold(t *testing.T) {
	database := setupTestDB(t)

	mock := &mockHFClient{
		responses: []string{
			"Revised But Weak\nStill not great",
			`{"score": 50, "issues": ["Still missing citations"], "factual_check": false, "attribution_check": false, "completeness_check": false, "hallucination_check": true}`,
		},
	}

	gen := ai.NewGenerator(mock, "test-model", nil)
	critic := ai.NewCritic(mock, "test-model")
	pipeline := ai.NewPipeline(gen, critic, 80)

	mgr := NewManager(database, pipeline)

	srcID, _ := database.CreateSource(&db.Source{Name: "S", URL: "https://s.com", Active: true})
	rawID, _ := database.CreateRawArticle(&db.RawArticle{
		SourceID: srcID, Title: "T", URL: "https://s.com/a", Content: "C", PublishedAt: time.Now(), Hash: "h1",
	})

	articleID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Orig", Slug: "orig-slug-111", Content: "Orig content", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	database.LinkArticleSources(articleID, []int64{rawID})
	database.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: articleID, RevisionNumber: 1, Score: 50,
		IssuesJSON: `["bad"]`, FactualCheck: "fail", AttributionCheck: "fail",
		CompletenessCheck: "fail", HallucinationCheck: "pass",
	})

	mgr.RequestRevision(articleID, "Fix it", "admin")

	if err := mgr.ProcessRevision(articleID); err != nil {
		t.Fatalf("ProcessRevision failed: %v", err)
	}

	article, _ := database.GetGeneratedArticle(articleID)
	// Score 50 < threshold 80, should stay as draft.
	if article.Status != db.StatusDraft {
		t.Errorf("expected status %q (below threshold), got %q", db.StatusDraft, article.Status)
	}
	if article.RevisionNumber != 2 {
		t.Errorf("expected revision_number 2, got %d", article.RevisionNumber)
	}
}

func TestProcessRevision_InvalidStatus(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)

	articleID, _ := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Draft", Slug: "draft-rev-test", Content: "C", Type: "deal",
		Status: db.StatusDraft, SourcesJSON: "[]", RevisionNumber: 1,
	})

	if err := mgr.ProcessRevision(articleID); err == nil {
		t.Error("expected error when processing revision on draft article")
	}
}
