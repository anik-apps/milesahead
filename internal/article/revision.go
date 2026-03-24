package article

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// ProcessRevision loads an article with revision_requested status, retrieves
// feedback and source articles, re-generates via the AI pipeline with feedback,
// and stores the new content with an incremented revision number.
func (m *Manager) ProcessRevision(articleID int64) error {
	// Load the article.
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	if article.Status != db.StatusRevisionRequested {
		return fmt.Errorf("article status is %q, expected %q", article.Status, db.StatusRevisionRequested)
	}

	// Get the latest feedback.
	feedbackList, err := m.db.ListFeedbackForArticle(articleID)
	if err != nil {
		return fmt.Errorf("listing feedback: %w", err)
	}
	if len(feedbackList) == 0 {
		return fmt.Errorf("no feedback found for article %d", articleID)
	}
	latestFeedback := feedbackList[0] // Ordered by created_at DESC.

	// Get latest verification issues.
	var criticIssues string
	verification, err := m.db.GetLatestVerification(articleID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("getting verification: %w", err)
	}
	if verification != nil {
		criticIssues = verification.IssuesJSON
	}

	// Load source raw articles via article_sources linkage.
	// We need to query the raw article IDs linked to this generated article.
	sources, err := m.loadSourceArticles(articleID)
	if err != nil {
		return fmt.Errorf("loading sources: %w", err)
	}

	// Call pipeline with feedback.
	result, err := m.pipeline.ProcessWithFeedback(sources, article.Content, latestFeedback.FeedbackText, criticIssues)
	if err != nil {
		return fmt.Errorf("processing revision: %w", err)
	}

	// Transition from revision_requested -> draft first (valid transition).
	if err := m.db.UpdateArticleStatus(articleID, db.StatusDraft); err != nil {
		return fmt.Errorf("transitioning to draft: %w", err)
	}

	// Store new content and increment revision number.
	newRevisionNumber := article.RevisionNumber + 1
	if err := m.db.UpdateArticleContent(articleID, result.Title, result.Content, newRevisionNumber); err != nil {
		return fmt.Errorf("updating content: %w", err)
	}

	// Create revision record.
	_, err = m.db.CreateArticleRevision(&db.ArticleRevision{
		GeneratedArticleID: articleID,
		RevisionNumber:     newRevisionNumber,
		PreviousContent:    article.Content,
		NewContent:         result.Content,
		FeedbackID:         latestFeedback.ID,
	})
	if err != nil {
		return fmt.Errorf("creating revision record: %w", err)
	}

	// Store new verification result.
	issuesJSON := "[]"
	if result.Verification != nil && len(result.Verification.Issues) > 0 {
		b, err := json.Marshal(result.Verification.Issues)
		if err != nil {
			return fmt.Errorf("marshaling issues: %w", err)
		}
		issuesJSON = string(b)
	}

	_, err = m.db.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: articleID,
		RevisionNumber:     newRevisionNumber,
		Score:              result.Verification.Score,
		IssuesJSON:         issuesJSON,
		FactualCheck:       boolToString(result.Verification.FactualCheck),
		AttributionCheck:   boolToString(result.Verification.AttributionCheck),
		CompletenessCheck:  boolToString(result.Verification.CompletenessCheck),
		HallucinationCheck: boolToString(result.Verification.HallucinationCheck),
	})
	if err != nil {
		return fmt.Errorf("storing verification: %w", err)
	}

	// If result passes threshold, transition draft -> pending.
	if result.PassedThreshold {
		if err := m.db.UpdateArticleStatus(articleID, db.StatusPending); err != nil {
			return fmt.Errorf("transitioning to pending: %w", err)
		}
	}

	return nil
}

// loadSourceArticles retrieves source articles linked to a generated article
// and converts them to ai.SourceArticle format.
func (m *Manager) loadSourceArticles(articleID int64) ([]ai.SourceArticle, error) {
	// Query article_sources to get raw article IDs.
	rawIDs, err := m.getLinkedRawArticleIDs(articleID)
	if err != nil {
		return nil, err
	}

	if len(rawIDs) == 0 {
		return nil, nil
	}

	rawArticles, err := m.db.GetRawArticlesByIDs(rawIDs)
	if err != nil {
		return nil, fmt.Errorf("getting raw articles: %w", err)
	}

	sources := make([]ai.SourceArticle, len(rawArticles))
	for i, ra := range rawArticles {
		sources[i] = ai.SourceArticle{
			Title:   ra.Title,
			URL:     ra.URL,
			Content: ra.Content,
		}
	}
	return sources, nil
}

// getLinkedRawArticleIDs queries the article_sources table for raw article IDs
// linked to the given generated article.
func (m *Manager) getLinkedRawArticleIDs(articleID int64) ([]int64, error) {
	rows, err := m.db.DB().Query(
		`SELECT raw_article_id FROM article_sources WHERE generated_article_id = ?`, articleID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
