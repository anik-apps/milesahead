package article

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// Manager coordinates article lifecycle between the AI pipeline and database.
type Manager struct {
	db       *db.Database
	pipeline *ai.Pipeline

	// RevisionMaxFailures is how many failed rewrites an article gets before
	// the revision reconciler stops retrying it. Zero means
	// DefaultRevisionMaxFailures.
	RevisionMaxFailures int

	// revInFlight holds the articles being rewritten right now, so the Revise
	// button, the other kicks and the reconciler never rewrite one article
	// concurrently.
	revMu       sync.Mutex
	revInFlight map[int64]bool
	// revPassRunning stops reconciler passes overlapping.
	revPassRunning atomic.Bool
	// revBG tracks rewrites started by StartRevision.
	revBG sync.WaitGroup
	// now is the clock for retry backoff; nil means time.Now. Tests override it.
	now func() time.Time
	// applyRevision replaces db.ApplyRevision when set. Tests only: it is how
	// they make the store miss or interleave feedback with it.
	applyRevision func(db.AppliedRevision) (bool, error)
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
	return m.decide(articleID, db.StatusApproved, 0)
}

// Reject transitions an article from pending to rejected.
func (m *Manager) Reject(articleID int64) error {
	return m.decide(articleID, db.StatusRejected, 0)
}

// ApproveRevision approves the article only if it is still at revision, the
// one the admin reviewed. See ErrArticleChanged.
func (m *Manager) ApproveRevision(articleID int64, revision int) error {
	return m.decide(articleID, db.StatusApproved, revision)
}

// RejectRevision rejects the article only if it is still at revision.
func (m *Manager) RejectRevision(articleID int64, revision int) error {
	return m.decide(articleID, db.StatusRejected, revision)
}

// ErrArticleChanged means the article is no longer the revision the admin
// decided on: a rewrite landed after the page was loaded. Revision is the
// article's revision now.
type ErrArticleChanged struct {
	Revision int
}

func (e *ErrArticleChanged) Error() string {
	return fmt.Sprintf("article changed: it is now revision %d", e.Revision)
}

// decidable are the statuses an admin can approve or reject from.
var decidable = map[string]bool{db.StatusPending: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true, db.StatusDraft: true}

// decide moves the article to approved or rejected. revision > 0 makes the
// move conditional on the article still being at that revision, in the same
// write as the status check, so a rewrite landing in between cannot be
// approved unseen; 0 (the email path, which acts on the latest text) skips it.
func (m *Manager) decide(articleID int64, to string, revision int) error {
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	verb := "approve"
	if to == db.StatusRejected {
		verb = "reject"
	}
	if revision > 0 && article.RevisionNumber != revision {
		return &ErrArticleChanged{Revision: article.RevisionNumber}
	}
	if !decidable[article.Status] {
		return fmt.Errorf("cannot %s article with status %q", verb, article.Status)
	}
	// Conditional on the status (and revision) just checked: if a rewrite
	// landed in between, this must not decide on content nobody has reviewed.
	if revision > 0 {
		err = m.db.TransitionArticleStatusAtRevision(articleID, article.Status, to, revision)
		if errors.Is(err, db.ErrStatusChanged) {
			if now, gerr := m.db.GetGeneratedArticle(articleID); gerr == nil && now.RevisionNumber != revision {
				return &ErrArticleChanged{Revision: now.RevisionNumber}
			}
		}
	} else {
		err = m.db.TransitionArticleStatus(articleID, article.Status, to)
	}
	if err != nil {
		return err
	}
	if to == db.StatusApproved {
		// Record what the approval changed, for the dashboard's Undo. A
		// failure here only costs the undo, not the approval.
		if err := m.db.RecordApproval(db.ApprovalUndo{
			ArticleID: articleID, Revision: article.RevisionNumber,
			PrevStatus: article.Status, PrevPublishedAt: article.PublishedAt,
			ExpiresAt: time.Now().Add(ApprovalUndoWindow),
		}); err != nil {
			slog.Warn("could not record approval for undo", "article_id", articleID, "error", err)
		}
		slog.Info("article approved", "article_id", articleID, "revision", article.RevisionNumber)
		metrics.ArticlesApprovedTotal.Inc()
	} else {
		slog.Info("article rejected", "article_id", articleID, "revision", article.RevisionNumber)
		metrics.ArticlesRejectedTotal.Inc()
	}
	return nil
}

// ApprovalUndoWindow is how long after an approval its Undo still works.
const ApprovalUndoWindow = 30 * time.Second

// UndoApprove undoes the article's last approval, if it is recent enough and
// the article has not changed since: it goes back to the status and publish
// date it had before, as recorded when it was approved. Returns the restored
// status, or db.ErrUndoExpired.
func (m *Manager) UndoApprove(articleID int64) (string, error) {
	prev, err := m.db.UndoApproval(articleID, time.Now())
	if err != nil {
		return "", err
	}
	slog.Info("article approval undone", "article_id", articleID, "status", prev)
	return prev, nil
}

// RequestRevision stores admin feedback and moves the article to
// revision_requested.
//
// The feedback is stored before the status changes, so anything that sees the
// article in revision_requested -- a reconciler pass, a kick -- also sees the
// feedback it is meant to answer. The other order let a rewrite start against
// the previous note (or none).
func (m *Manager) RequestRevision(articleID int64, feedback string, source string) error {
	allowed := map[string]bool{db.StatusPending: true, db.StatusUnpublished: true, db.StatusRevisionRequested: true, db.StatusDraft: true}
	article, err := m.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("getting article: %w", err)
	}
	if !allowed[article.Status] {
		return fmt.Errorf("cannot request revision for article with status %q", article.Status)
	}

	_, err = m.db.CreateAdminFeedback(&db.AdminFeedback{
		GeneratedArticleID: articleID,
		FeedbackText:       feedback,
		Source:             source,
	})
	if err != nil {
		return fmt.Errorf("storing feedback: %w", err)
	}
	m.resetRevisionBudget(articleID)

	// A rewrite may store between the read above and this write (e.g.
	// revision_requested -> pending). Re-read and retry once: the request is
	// still valid from the new status, and the feedback is already stored.
	for attempt := 1; ; attempt++ {
		err = m.db.TransitionArticleStatus(articleID, article.Status, db.StatusRevisionRequested)
		if err == nil || !errors.Is(err, db.ErrStatusChanged) || attempt == 2 {
			break
		}
		if article, err = m.db.GetGeneratedArticle(articleID); err != nil {
			return fmt.Errorf("getting article: %w", err)
		}
		if !allowed[article.Status] {
			return fmt.Errorf("cannot request revision for article with status %q", article.Status)
		}
	}
	if err != nil {
		return fmt.Errorf("updating status: %w", err)
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
	return m.db.TransitionArticleStatus(articleID, db.StatusApproved, db.StatusUnpublished)
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

	if err := m.db.TransitionArticleStatus(articleID, db.StatusApproved, db.StatusUnpublished); err != nil {
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

	m.resetRevisionBudget(articleID)

	if err := m.db.TransitionArticleStatus(articleID, db.StatusUnpublished, db.StatusRevisionRequested); err != nil {
		return fmt.Errorf("setting revision_requested: %w", err)
	}

	slog.Info("revision requested", "article_id", articleID, "source", source, "unpublished", true)

	return nil
}

// resetRevisionBudget gives the article a fresh set of rewrite retries. New
// feedback is the admin asking again, and is also the only way an article that
// exhausted its retries gets another try. Best effort: failing to reset leaves
// at worst an old failure count, which a later request resets.
func (m *Manager) resetRevisionBudget(articleID int64) {
	if err := m.db.ResetRevisionFailures(articleID); err != nil {
		slog.Warn("could not reset revision retry budget", "article_id", articleID, "error", err)
	}
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
