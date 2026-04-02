package article

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// Manager coordinates article lifecycle between the AI pipeline and database.
type Manager struct {
	db       *db.Database
	pipeline *ai.Pipeline
}

// NewManager creates a new article Manager.
func NewManager(database *db.Database, pipeline *ai.Pipeline) *Manager {
	return &Manager{db: database, pipeline: pipeline}
}

// CreateFromPipeline creates a generated article from an AI pipeline result,
// links its sources, and stores the verification result.
func (m *Manager) CreateFromPipeline(result *ai.PipelineResult, rawArticleIDs []int64, articleType string) (int64, error) {
	status := db.StatusPending
	if !result.PassedThreshold {
		status = db.StatusDraft
	}

	issuesJSON, err := json.Marshal(result.Verification.Issues)
	if err != nil {
		return 0, fmt.Errorf("marshaling issues: %w", err)
	}

	keyDetailsJSON, kdErr := json.Marshal(result.KeyDetails)
	if kdErr != nil {
		keyDetailsJSON = []byte("[]")
	}
	linksJSON, lErr := json.Marshal(result.Links)
	if lErr != nil {
		linksJSON = []byte("[]")
	}

	relatedCardsJSON, rcErr := json.Marshal(result.RelatedCards)
	if rcErr != nil {
		relatedCardsJSON = []byte("[]")
	}

	tagsJSON, tErr := json.Marshal(result.Tags)
	if tErr != nil || len(result.Tags) == 0 {
		tagsJSON = []byte("[]")
	}

	article := &db.GeneratedArticle{
		Title:            result.Title,
		Slug:             GenerateSlug(result.Title),
		Content:          result.Content,
		Type:             articleType,
		Status:           status,
		SourcesJSON:      "[]",
		RevisionNumber:   1,
		TLDR:             result.TLDR,
		KeyDetails:       string(keyDetailsJSON),
		Links:            string(linksJSON),
		ImageURL:         result.ImageURL,
		ImageThumbnail:   result.ImageThumbnail,
		ImageAttribution: result.ImageAttribution,
		ScoutsTake:       result.ScoutsTake,
		RelatedCardsJSON: string(relatedCardsJSON),
		TagsJSON:         string(tagsJSON),
	}

	articleID, err := m.db.CreateGeneratedArticle(article)
	if err != nil {
		return 0, fmt.Errorf("creating article: %w", err)
	}

	if err := m.db.LinkArticleSources(articleID, rawArticleIDs); err != nil {
		return 0, fmt.Errorf("linking sources: %w", err)
	}

	verification := &db.VerificationResult{
		GeneratedArticleID: articleID,
		RevisionNumber:     1,
		Score:              result.Verification.Score,
		IssuesJSON:         string(issuesJSON),
		FactualCheck:       boolToString(result.Verification.FactualCheck),
		AttributionCheck:   boolToString(result.Verification.AttributionCheck),
		CompletenessCheck:  boolToString(result.Verification.CompletenessCheck),
		HallucinationCheck: boolToString(result.Verification.HallucinationCheck),
	}

	if _, err := m.db.CreateVerificationResult(verification); err != nil {
		return 0, fmt.Errorf("storing verification: %w", err)
	}

	metrics.ArticlesGeneratedTotal.WithLabelValues(articleType, status).Inc()

	return articleID, nil
}

// Approve transitions an article from pending to approved and sets published_at.
func (m *Manager) Approve(articleID int64) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	allowed := map[string]bool{db.StatusPending: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true, db.StatusDraft: true}
	if !allowed[article.Status] {
		return fmt.Errorf("cannot approve article with status %q", article.Status)
	}
	if err := m.db.UpdateArticleStatus(articleID, db.StatusApproved); err != nil {
		return err
	}
	slog.Info("article approved", "article_id", articleID)
	metrics.ArticlesApprovedTotal.Inc()
	return nil
}

// Reject transitions an article from pending to rejected.
func (m *Manager) Reject(articleID int64) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	allowed := map[string]bool{db.StatusPending: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true, db.StatusDraft: true}
	if !allowed[article.Status] {
		return fmt.Errorf("cannot reject article with status %q", article.Status)
	}
	if err := m.db.UpdateArticleStatus(articleID, db.StatusRejected); err != nil {
		return err
	}
	slog.Info("article rejected", "article_id", articleID)
	metrics.ArticlesRejectedTotal.Inc()
	return nil
}

// RequestRevision transitions an article from pending to revision_requested
// and stores admin feedback.
func (m *Manager) RequestRevision(articleID int64, feedback string, source string) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	allowed := map[string]bool{db.StatusPending: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true, db.StatusDraft: true}
	if !allowed[article.Status] {
		return fmt.Errorf("cannot request revision for article with status %q", article.Status)
	}

	if err := m.db.UpdateArticleStatus(articleID, db.StatusRevisionRequested); err != nil {
		return fmt.Errorf("updating status: %w", err)
	}

	_, err = m.db.CreateAdminFeedback(&db.AdminFeedback{
		GeneratedArticleID: articleID,
		FeedbackText:       feedback,
		Source:             source,
	})
	if err != nil {
		return fmt.Errorf("storing feedback: %w", err)
	}

	slog.Info("revision requested", "article_id", articleID, "source", source)

	return nil
}

// Unpublish transitions an approved article to unpublished.
func (m *Manager) Unpublish(articleID int64) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	if article.Status != db.StatusApproved {
		return fmt.Errorf("cannot unpublish article with status %q, must be %q", article.Status, db.StatusApproved)
	}
	return m.db.UpdateArticleStatus(articleID, db.StatusUnpublished)
}

// UnpublishAndRevise unpublishes an approved article, stores feedback, and
// sets it to revision_requested via a two-step status transition:
// UnpublishAndRevise transitions approved -> unpublished -> revision_requested,
// then stores feedback for revision processing.
func (m *Manager) UnpublishAndRevise(articleID int64, feedback string, source string) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	if article.Status != db.StatusApproved {
		return fmt.Errorf("cannot unpublish article with status %q, must be %q", article.Status, db.StatusApproved)
	}

	if err := m.db.UpdateArticleStatus(articleID, db.StatusUnpublished); err != nil {
		return fmt.Errorf("unpublishing: %w", err)
	}

	_, err = m.db.CreateAdminFeedback(&db.AdminFeedback{
		GeneratedArticleID: articleID,
		FeedbackText:       feedback,
		Source:             source,
	})
	if err != nil {
		return fmt.Errorf("storing feedback: %w", err)
	}

	if err := m.db.UpdateArticleStatus(articleID, db.StatusRevisionRequested); err != nil {
		return fmt.Errorf("setting revision_requested: %w", err)
	}

	return nil
}

// GenerateSlug creates a URL-friendly slug from a title.
// It lowercases, replaces spaces with hyphens, strips non-alphanumeric characters
// (except hyphens), truncates to 80 chars, and appends a short random suffix.
func GenerateSlug(title string) string {
	slug := strings.ToLower(title)
	slug = strings.ReplaceAll(slug, " ", "-")

	re := regexp.MustCompile(`[^a-z0-9-]`)
	slug = re.ReplaceAllString(slug, "")

	// Collapse multiple hyphens.
	re2 := regexp.MustCompile(`-+`)
	slug = re2.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")

	if len(slug) > 80 {
		slug = slug[:80]
		slug = strings.TrimRight(slug, "-")
	}

	suffix := randomSuffix(6)
	return slug + "-" + suffix
}

func randomSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func boolToString(b bool) string {
	if b {
		return "pass"
	}
	return "fail"
}
