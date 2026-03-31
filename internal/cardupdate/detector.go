package cardupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// sem limits concurrent DetectAndDraft goroutines.
var sem = make(chan struct{}, 2)

// keySections are the reference page sections checked for updates.
var keySections = []string{
	"overview",
	"key_benefits",
	"fees",
	"signup_bonus",
	"earning_rates",
	"travel_credits_perks",
}

// Detector checks whether a newly approved article affects any credit card
// reference pages and, if so, drafts pending section updates via AI.
type Detector struct {
	db       *db.Database
	hfClient ai.HFClientInterface
	model    string
}

// NewDetector creates a Detector.
func NewDetector(database *db.Database, hfClient ai.HFClientInterface, model string) *Detector {
	return &Detector{db: database, hfClient: hfClient, model: model}
}

// DetectAndDraft loads the article, matches it against published reference
// pages by keyword, appends a recent change entry, and uses AI to propose
// section updates where relevant.
func (d *Detector) DetectAndDraft(ctx context.Context, articleID int64) error {
	article, err := d.db.GetGeneratedArticle(articleID)
	if err != nil {
		return fmt.Errorf("loading article %d: %w", articleID, err)
	}

	pages, err := d.db.ListPublishedReferencePages("")
	if err != nil {
		return fmt.Errorf("listing reference pages: %w", err)
	}

	matched := matchCards(article, pages, 3)
	if len(matched) == 0 {
		slog.Info("card update detection: no matching cards", "article_id", articleID)
		return nil
	}

	slog.Info("card update detection: matched cards", "article_id", articleID, "cards", len(matched))

	for _, page := range matched {
		// Check for cancellation between cards.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Append recent change.
		change := db.RecentChange{
			Date:        time.Now().Format("2006-01-02"),
			Summary:     fmt.Sprintf("Article published: %s", article.Title),
			ArticleID:   article.ID,
			ArticleSlug: article.Slug,
		}
		if err := d.db.AppendRecentChange(page.ID, change); err != nil {
			slog.Error("card update: failed to append recent change", "card_id", page.ID, "error", err)
			continue
		}

		// Parse sections.
		var sections []db.ReferenceSection
		if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
			if err := json.Unmarshal([]byte(page.SectionsJSON), &sections); err != nil {
				slog.Error("card update: failed to parse sections", "card_id", page.ID, "error", err)
				continue
			}
		}

		// Build a set of key sections to check.
		keySet := make(map[string]bool, len(keySections))
		for _, k := range keySections {
			keySet[k] = true
		}

		// Parse existing pending updates.
		var pendingUpdates []db.PendingUpdate
		if page.PendingUpdatesJSON != "" && page.PendingUpdatesJSON != "[]" {
			if err := json.Unmarshal([]byte(page.PendingUpdatesJSON), &pendingUpdates); err != nil {
				slog.Error("card update: failed to parse pending updates", "card_id", page.ID, "error", err)
				pendingUpdates = nil
			}
		}

		updatesAdded := 0
		for _, sec := range sections {
			if !keySet[sec.Key] {
				continue
			}

			// Check for cancellation between AI calls.
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			prompt := ai.CardUpdateDetectionPrompt(article.Title, article.Content, page.Title, sec.Title, sec.Content)
			resp, err := d.hfClient.Generate(d.model, prompt, 1024)
			if err != nil {
				slog.Error("card update: AI call failed", "card_id", page.ID, "section", sec.Key, "error", err)
				continue
			}

			resp = strings.TrimSpace(resp)
			if resp == "NO_UPDATE" || strings.HasPrefix(resp, "NO_UPDATE") {
				continue
			}

			// Try to parse JSON response.
			type aiUpdate struct {
				UpdatedContent string `json:"updated_content"`
				Summary        string `json:"summary"`
			}
			var upd aiUpdate

			// Extract JSON if wrapped in other text.
			jsonStr := resp
			if idx := strings.Index(resp, "{"); idx != -1 {
				jsonStr = resp[idx:]
			}
			if idx := strings.LastIndex(jsonStr, "}"); idx != -1 {
				jsonStr = jsonStr[:idx+1]
			}

			if err := json.Unmarshal([]byte(jsonStr), &upd); err != nil {
				slog.Warn("card update: failed to parse AI response", "card_id", page.ID, "section", sec.Key, "response", resp)
				continue
			}

			if upd.UpdatedContent == "" {
				continue
			}

			// Determine next pending update ID.
			nextID := 1
			for _, pu := range pendingUpdates {
				if pu.ID >= nextID {
					nextID = pu.ID + 1
				}
			}

			pendingUpdates = append(pendingUpdates, db.PendingUpdate{
				ID:              nextID,
				ArticleID:       article.ID,
				ArticleTitle:    article.Title,
				SectionKey:      sec.Key,
				CurrentContent:  sec.Content,
				ProposedContent: upd.UpdatedContent,
				Summary:         upd.Summary,
				CreatedAt:       time.Now().Format(time.RFC3339),
			})
			updatesAdded++
			slog.Info("card update: drafted update", "card_id", page.ID, "section", sec.Key, "summary", upd.Summary)
		}

		// Save pending updates if any were added.
		if updatesAdded > 0 {
			data, err := json.Marshal(pendingUpdates)
			if err != nil {
				slog.Error("card update: failed to marshal pending updates", "card_id", page.ID, "error", err)
				continue
			}

			// Re-fetch page to update.
			freshPage, err := d.db.GetReferencePage(page.ID)
			if err != nil {
				slog.Error("card update: failed to re-fetch page", "card_id", page.ID, "error", err)
				continue
			}
			freshPage.PendingUpdatesJSON = string(data)
			if err := d.db.UpdateReferencePage(freshPage); err != nil {
				slog.Error("card update: failed to save pending updates", "card_id", page.ID, "error", err)
			}
		}
	}

	return nil
}

// matchCards returns up to maxMatches reference pages whose keywords appear in
// the article title or content (case-insensitive).
func matchCards(article *db.GeneratedArticle, pages []db.ReferencePage, maxMatches int) []db.ReferencePage {
	titleLower := strings.ToLower(article.Title)
	contentLower := strings.ToLower(article.Content)

	var matched []db.ReferencePage
	for _, page := range pages {
		if len(matched) >= maxMatches {
			break
		}

		var keywords []string
		if page.KeywordsJSON != "" && page.KeywordsJSON != "[]" {
			if err := json.Unmarshal([]byte(page.KeywordsJSON), &keywords); err != nil {
				continue
			}
		}

		// Also match on the card title itself.
		keywords = append(keywords, page.Title)

		found := false
		for _, kw := range keywords {
			kwLower := strings.ToLower(kw)
			if kwLower == "" {
				continue
			}
			if strings.Contains(titleLower, kwLower) || strings.Contains(contentLower, kwLower) {
				found = true
				break
			}
		}
		if found {
			matched = append(matched, page)
		}
	}

	return matched
}
