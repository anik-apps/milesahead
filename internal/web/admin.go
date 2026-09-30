package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/linker"
	"github.com/kumaranik/milesahead/internal/offer"
)

// sem limits concurrent card update detection goroutines.
var sem = make(chan struct{}, 2)

// adminSourceInfo holds source article info for admin display.
type adminSourceInfo struct {
	Name  string
	Title string
	URL   string
}

// adminCriticReport holds the critic's verification details.
type adminCriticReport struct {
	Score              int
	Issues             []string
	FactualCheck       string
	AttributionCheck   string
	CompletenessCheck  string
	HallucinationCheck string
}

// adminArticleRow holds data for a single article row in admin views.
type adminArticleRow struct {
	ID                int64
	Title             string
	Type              string
	Status            string
	Slug              string
	Content           string
	RevisionNumber    int
	VerificationScore int
	SourceCount       int
	CreatedAt         string
	UpdatedAt         string
	Upvotes           int
	Downvotes         int
	Flagged           bool
	PublishedAt       string
	Sources           []adminSourceInfo
	CriticReport      *adminCriticReport
	TLDR              string
	KeyDetails        []struct{ Label, Value string }
	ArticleLinks      []struct{ Text, URL string }
	ImageURL          string
	Tags              []string
	// ScoutsTake is shown in the admin preview so it can be reviewed before
	// approval; empty until enrichment writes it.
	ScoutsTake string
	// RevisionFailures is how many rewrites of a revision_requested article
	// have failed in a row, and RevisionLastError the latest reason. Without
	// them "Revising..." looks the same whether a rewrite is running, retrying,
	// or has been given up on.
	RevisionFailures  int
	RevisionLastError string
	// RevisionGaveUp: the reconciler has stopped retrying; the admin must
	// re-request the revision, approve or reject.
	RevisionGaveUp bool
	// RevisionThread is the article's feedback and the rewrites that answered
	// it, oldest first, for the preview; nil when no feedback was ever sent.
	RevisionThread []revisionRequest
	createdAtRaw   time.Time // for sorting only, not exposed to template
}

// adminTimeFormat is how admin views show timestamps.
const adminTimeFormat = "Jan 2, 2006 3:04 PM"

func (app *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	// Show all articles needing admin attention: pending, unpublished, and revision_requested
	var articles []db.GeneratedArticle
	for _, status := range []string{"pending", "unpublished", "revision_requested", "draft"} {
		batch, err := app.db.ListArticlesByStatus(status)
		if err != nil {
			continue
		}
		articles = append(articles, batch...)
	}

	// Retry state for revision_requested articles, in one query.
	revisionState := map[int64]db.PendingRevision{}
	if pending, err := app.db.ListPendingRevisions(); err == nil {
		for _, p := range pending {
			revisionState[p.ID] = p
		}
	} else {
		slog.Warn("dashboard: could not load revision retry state", "error", err)
	}

	var rows []adminArticleRow
	for _, a := range articles {
		row := adminArticleRow{
			ID:             a.ID,
			Title:          a.Title,
			Type:           a.Type,
			Status:         a.Status,
			Slug:           a.Slug,
			Content:        a.Content,
			RevisionNumber: a.RevisionNumber,
			TLDR:           a.TLDR,
			ImageURL:       a.ImageURL,
			ScoutsTake:     a.ScoutsTake,
			CreatedAt:      a.CreatedAt.Format(adminTimeFormat),
			UpdatedAt:      a.UpdatedAt.Format(adminTimeFormat),
			createdAtRaw:   a.CreatedAt,
		}
		if p, ok := revisionState[a.ID]; ok && a.Status == db.StatusRevisionRequested && p.Failures > 0 {
			row.RevisionFailures = p.Failures
			row.RevisionLastError = p.LastError
			row.RevisionGaveUp = app.articleManager != nil && app.articleManager.RevisionGaveUp(p.Failures)
		}

		// Parse key details and links from JSON
		if a.KeyDetails != "" && a.KeyDetails != "[]" {
			var kd []struct{ Label, Value string }
			if err := json.Unmarshal([]byte(a.KeyDetails), &kd); err != nil {
				slog.Warn("failed to parse key details", "article_id", a.ID, "error", err)
			}
			row.KeyDetails = kd
		}
		if a.Links != "" && a.Links != "[]" {
			var links []struct{ Text, URL string }
			if err := json.Unmarshal([]byte(a.Links), &links); err != nil {
				slog.Warn("failed to parse links", "article_id", a.ID, "error", err)
			}
			row.ArticleLinks = links
		}
		if a.TagsJSON != "" && a.TagsJSON != "[]" {
			var tags []string
			if err := json.Unmarshal([]byte(a.TagsJSON), &tags); err == nil {
				row.Tags = tags
			}
		}

		if v, err := app.db.GetLatestVerification(a.ID); err == nil {
			row.VerificationScore = v.Score
			var issues []string
			if v.IssuesJSON != "" && v.IssuesJSON != "[]" {
				json.Unmarshal([]byte(v.IssuesJSON), &issues)
			}
			row.CriticReport = &adminCriticReport{
				Score:              v.Score,
				Issues:             issues,
				FactualCheck:       v.FactualCheck,
				AttributionCheck:   v.AttributionCheck,
				CompletenessCheck:  v.CompletenessCheck,
				HallucinationCheck: v.HallucinationCheck,
			}
		}
		if count, err := app.db.CountArticleSources(a.ID); err == nil {
			row.SourceCount = count
		}
		if rawArticles, err := app.db.GetLinkedRawArticles(a.ID); err == nil {
			for _, ra := range rawArticles {
				src, _ := app.db.GetSource(ra.SourceID)
				name := "Unknown"
				if src != nil {
					name = src.Name
				}
				row.Sources = append(row.Sources, adminSourceInfo{
					Name:  name,
					Title: ra.Title,
					URL:   ra.URL,
				})
			}
		}

		rows = append(rows, row)
	}

	// Sort by CreatedAt DESC (most recent first) and limit to 20 for dashboard
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].createdAtRaw.After(rows[j].createdAtRaw)
	})
	if len(rows) > 20 {
		rows = rows[:20]
	}
	app.attachRevisionHistory(rows)

	data := map[string]interface{}{
		"Title":       "Dashboard",
		"Articles":    rows,
		"CSRFToken":   getCSRFToken(w, r),
		"Message":     r.URL.Query().Get("msg"),
		"PersonaName": app.cfg.Site.PersonaName,
	}
	app.renderTemplate(w, "dashboard.html", data)
}

func (app *App) handleAdminPublished(w http.ResponseWriter, r *http.Request) {
	articles, err := app.db.ListPublishedArticles(100, 0)
	if err != nil {
		http.Error(w, "Failed to load articles", http.StatusInternalServerError)
		return
	}

	threshold := app.cfg.Votes.AlertThreshold
	if threshold == 0 {
		threshold = 5
	}

	var rows []adminArticleRow
	for _, a := range articles {
		row := adminArticleRow{
			ID:             a.ID,
			Title:          a.Title,
			Type:           a.Type,
			Status:         a.Status,
			Slug:           a.Slug,
			RevisionNumber: a.RevisionNumber,
		}
		if a.PublishedAt != nil {
			row.PublishedAt = a.PublishedAt.Format("2006-01-02 15:04")
		}

		if vc, err := app.db.GetVoteCounts(a.ID); err == nil {
			row.Upvotes = vc.Upvotes
			row.Downvotes = vc.Downvotes
			if vc.Downvotes >= threshold {
				row.Flagged = true
			}
		}

		rows = append(rows, row)
	}

	data := map[string]interface{}{
		"Title":     "Published Articles",
		"Articles":  rows,
		"CSRFToken": getCSRFToken(w, r),
	}
	app.renderTemplate(w, "published.html", data)
}

func (app *App) handleApproveArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	if err := app.articleManager.Approve(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to approve: %v", err), http.StatusBadRequest)
		return
	}

	// Trigger card update detection in the background.
	if app.cardDetector != nil {
		app.bg.Add(1)
		go func() {
			defer app.bg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("card update detection panicked", "article_id", id, "panic", r)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := app.cardDetector.DetectAndDraft(app.shutdownCtx, id); err != nil {
				slog.Error("card update detection failed", "article_id", id, "error", err)
			}
		}()
	}

	http.Redirect(w, r, "/admin/dashboard?msg=approved", http.StatusSeeOther)
}

func (app *App) handleRejectArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	if err := app.articleManager.Reject(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to reject: %v", err), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/admin/dashboard?msg=rejected", http.StatusSeeOther)
}

func (app *App) handleRequestRevision(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	feedback := r.FormValue("feedback")
	if strings.TrimSpace(feedback) == "" {
		http.Error(w, "Feedback is required", http.StatusBadRequest)
		return
	}

	if err := app.articleManager.RequestRevision(id, feedback, "admin"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to request revision: %v", err), http.StatusBadRequest)
		return
	}

	app.startRevision(id)

	http.Redirect(w, r, "/admin/dashboard?msg=revision_submitted", http.StatusSeeOther)
}

// startRevision kicks off the AI rewrite in the background so the admin sees it
// soon, rather than on the next revision reconciler pass. The kick is only for
// responsiveness: if it fails, or a deploy kills it, the reconciler retries.
// It runs through the manager's in-flight guard, so it never races a rewrite
// the reconciler already has going. Tracked by the manager and joined by
// WaitBackground, which tests call before teardown and main calls, bounded, at
// shutdown.
func (app *App) startRevision(id int64) {
	if app.articleManager != nil {
		app.articleManager.StartRevision(id)
	}
}

func (app *App) handleUnpublishArticle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	feedback := r.FormValue("feedback")
	if strings.TrimSpace(feedback) != "" {
		// Unpublish and request revision with feedback.
		if err := app.articleManager.UnpublishAndRevise(id, feedback, "admin"); err != nil {
			http.Error(w, fmt.Sprintf("Failed to unpublish: %v", err), http.StatusBadRequest)
			return
		}
		app.startRevision(id)
	} else {
		if err := app.articleManager.Unpublish(id); err != nil {
			http.Error(w, fmt.Sprintf("Failed to unpublish: %v", err), http.StatusBadRequest)
			return
		}
	}

	http.Redirect(w, r, "/admin/published", http.StatusSeeOther)
}

func (app *App) handleUnpublishAndRevise(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	feedback := r.FormValue("feedback")
	if strings.TrimSpace(feedback) == "" {
		http.Error(w, "Feedback is required", http.StatusBadRequest)
		return
	}

	if err := app.articleManager.UnpublishAndRevise(id, feedback, "admin"); err != nil {
		http.Error(w, fmt.Sprintf("Failed to unpublish and revise: %v", err), http.StatusBadRequest)
		return
	}
	app.startRevision(id)

	http.Redirect(w, r, "/admin/published", http.StatusSeeOther)
}

func (app *App) handleAdminSources(w http.ResponseWriter, r *http.Request) {
	sources, err := app.db.ListAllSources()
	if err != nil {
		http.Error(w, "Failed to load sources", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":     "Sources",
		"Sources":   sources,
		"CSRFToken": getCSRFToken(w, r),
	}
	app.renderTemplate(w, "sources.html", data)
}

func (app *App) handleAddSource(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	url := r.FormValue("url")
	rssURL := r.FormValue("rss_url")

	if strings.TrimSpace(name) == "" || strings.TrimSpace(url) == "" {
		http.Error(w, "Name and URL are required", http.StatusBadRequest)
		return
	}

	source := &dbSource{
		Name:   name,
		URL:    url,
		Active: true,
	}
	if rssURL != "" {
		source.RSSURL = &rssURL
	}

	intervalStr := r.FormValue("scan_interval")
	if intervalStr != "" {
		interval, err := strconv.Atoi(intervalStr)
		if err == nil {
			source.ScanInterval = &interval
		}
	}

	if _, err := app.db.CreateSource(source); err != nil {
		http.Error(w, "Failed to create source", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/sources", http.StatusSeeOther)
}

func (app *App) handleEditSource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid source ID", http.StatusBadRequest)
		return
	}

	source, err := app.db.GetSource(id)
	if err != nil {
		http.Error(w, "Source not found", http.StatusNotFound)
		return
	}

	source.Name = r.FormValue("name")
	source.URL = r.FormValue("url")
	rssURL := r.FormValue("rss_url")
	if rssURL != "" {
		source.RSSURL = &rssURL
	} else {
		source.RSSURL = nil
	}

	intervalStr := r.FormValue("scan_interval")
	if intervalStr != "" {
		interval, err := strconv.Atoi(intervalStr)
		if err == nil {
			source.ScanInterval = &interval
		}
	}

	if err := app.db.UpdateSource(source); err != nil {
		http.Error(w, "Failed to update source", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/sources", http.StatusSeeOther)
}

func (app *App) handleToggleSource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid source ID", http.StatusBadRequest)
		return
	}

	if err := app.db.ToggleSourceActive(id); err != nil {
		http.Error(w, "Failed to toggle source", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/sources", http.StatusSeeOther)
}

func (app *App) handleAdminSubscribers(w http.ResponseWriter, r *http.Request) {
	subscribers, err := app.db.ListAllSubscribers()
	if err != nil {
		http.Error(w, "Failed to load subscribers", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":       "Subscribers",
		"Subscribers": subscribers,
		"CSRFToken":   getCSRFToken(w, r),
	}
	app.renderTemplate(w, "subscribers.html", data)
}

func (app *App) handleExportSubscribers(w http.ResponseWriter, r *http.Request) {
	subscribers, err := app.db.ListAllSubscribers()
	if err != nil {
		http.Error(w, "Failed to load subscribers", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=subscribers.csv")

	writer := csv.NewWriter(w)
	writer.Write([]string{"Email", "Confirmed", "Subscribed At", "Confirmed At"})

	for _, s := range subscribers {
		confirmed := "No"
		if s.Confirmed {
			confirmed = "Yes"
		}
		confirmedAt := ""
		if s.ConfirmedAt != nil {
			confirmedAt = s.ConfirmedAt.Format("2006-01-02 15:04:05")
		}
		writer.Write([]string{
			s.Email,
			confirmed,
			s.SubscribedAt.Format("2006-01-02 15:04:05"),
			confirmedAt,
		})
	}
	writer.Flush()
}

func (app *App) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	// Mask sensitive values.
	maskedConfig := map[string]interface{}{
		"Server": map[string]interface{}{
			"Port":   app.cfg.Server.Port,
			"Domain": app.cfg.Server.Domain,
		},
		"Admin": map[string]interface{}{
			"PasswordHash": maskString(app.cfg.Admin.PasswordHash),
		},
		"Database": map[string]interface{}{
			"Path": app.cfg.DB.Path,
		},
		"Scanner": map[string]interface{}{
			"DefaultInterval": app.cfg.Scanner.DefaultInterval,
			"UserAgent":       app.cfg.Scanner.UserAgent,
		},
		"AI": map[string]interface{}{
			"GeneratorModel": app.cfg.AI.GeneratorModel,
			// A chain, tried left to right; shown that way rather than as the
			// raw comma-separated string.
			"CriticModel":           strings.Join(ai.SplitModels(app.cfg.AI.CriticModel), " -> "),
			"CriticDisableThinking": app.cfg.AI.CriticDisableThinking,
			"HFAPIToken":            maskString(app.cfg.AI.HFAPIToken),
			"CriticThreshold":       app.cfg.AI.CriticThreshold,
			"MaxRetries":            app.cfg.AI.MaxRetries,
			"CacheTTL":              app.cfg.AI.CacheTTL,
		},
		"Mail": map[string]interface{}{
			"SMTP": map[string]interface{}{
				"Host":     app.cfg.Mail.SMTP.Host,
				"Port":     app.cfg.Mail.SMTP.Port,
				"User":     app.cfg.Mail.SMTP.User,
				"Password": maskString(app.cfg.Mail.SMTP.Password),
				"From":     app.cfg.Mail.SMTP.From,
			},
			"IMAP": map[string]interface{}{
				"Host":         app.cfg.Mail.IMAP.Host,
				"Port":         app.cfg.Mail.IMAP.Port,
				"User":         app.cfg.Mail.IMAP.User,
				"Password":     maskString(app.cfg.Mail.IMAP.Password),
				"PollInterval": app.cfg.Mail.IMAP.PollInterval,
			},
		},
		"Newsletter": map[string]interface{}{
			"SendTime": app.cfg.Newsletter.SendTime,
			"Timezone": app.cfg.Newsletter.Timezone,
		},
		"Votes": map[string]interface{}{
			"AlertThreshold": app.cfg.Votes.AlertThreshold,
			"AlertRatio":     app.cfg.Votes.AlertRatio,
			"AlertWindow":    app.cfg.Votes.AlertWindow,
		},
	}

	data := map[string]interface{}{
		"Title":    "Settings",
		"Config":   maskedConfig,
		"Sections": []string{"Server", "Admin", "Database", "Scanner", "AI", "Mail", "Newsletter", "Votes"},
	}
	app.renderTemplate(w, "settings.html", data)
}

// maskString fully masks a sensitive string, revealing no content.
func maskString(s string) string {
	if s == "" {
		return ""
	}
	return "********"
}

// adminCandidateRow holds a newsletter candidate with its article title for display.
type adminCandidateRow struct {
	db.NewsletterCandidate
	ArticleTitle string
	// Ended marks a candidate whose offer has ended since the issue was
	// built; the send leaves it out.
	Ended bool
}

// adminCardInfo holds minimal card info for the admin dropdown.
type adminCardInfo struct {
	ID    int64
	Title string
}

func (app *App) handleAdminNewsletter(w http.ResponseWriter, r *http.Request) {
	var previewHTML string
	var noContent bool
	var successMsg, errorMsg string

	if msg := r.URL.Query().Get("success"); msg != "" {
		successMsg = msg
	}
	if msg := r.URL.Query().Get("error"); msg != "" {
		errorMsg = msg
	}

	// Try to load the current week's newsletter (most recent draft/auto_approved).
	newsletters, _ := app.db.ListNewsletters()
	var currentNL *db.Newsletter
	for i := range newsletters {
		if newsletters[i].Status != "sent" {
			currentNL = &newsletters[i]
			break
		}
	}
	// If no unsent newsletter, use the most recent one for display.
	if currentNL == nil && len(newsletters) > 0 {
		currentNL = &newsletters[0]
	}

	// Load candidates for the current newsletter. The stored preview was
	// rendered when the issue was built; an offer that has ended since is
	// left out when it is sent, so an unsent issue's ended candidates are
	// marked and counted for the note above the preview.
	var candidates []adminCandidateRow
	var endedCount int
	var pickEnded bool
	if currentNL != nil {
		unsent := currentNL.Status != "sent"
		today := app.offerToday()
		rawCandidates, _ := app.db.ListCandidatesForNewsletter(currentNL.ID)
		for _, c := range rawCandidates {
			row := adminCandidateRow{NewsletterCandidate: c}
			if article, err := app.db.GetGeneratedArticle(c.ArticleID); err == nil {
				row.ArticleTitle = article.Title
				row.Ended = unsent && offer.Ended(article.ExpiresOn, today)
			}
			if row.Ended {
				endedCount++
				if currentNL.ScoutsPickArticleID != nil && *currentNL.ScoutsPickArticleID == c.ArticleID {
					pickEnded = true
				}
			}
			candidates = append(candidates, row)
		}
	}
	endedNote := ""
	switch {
	case endedCount > 0 && !app.weekHasLiveArticle(currentNL.Date):
		// SendWeekly re-renders from the week's live articles and skips the
		// issue when there are none, so there is no Scout's Pick to pass on.
		endedNote = "No article from this issue's week is still live, so it will not be sent."
	case endedCount == 1:
		endedNote = "1 offer in this preview has ended since it was built; it is left out when the issue is sent."
		if pickEnded {
			endedNote += " Scout's Pick passes to the top article still in it."
		}
	case endedCount > 1:
		endedNote = fmt.Sprintf("%d offers in this preview have ended since it was built; they are left out when the issue is sent.", endedCount)
		if pickEnded {
			endedNote += " Scout's Pick passes to the top article still in it."
		}
	}

	// Load current card of the week info.
	var currentCard *adminCardInfo
	if currentNL != nil && currentNL.CardOfWeekID != nil {
		if page, err := app.db.GetReferencePage(*currentNL.CardOfWeekID); err == nil {
			currentCard = &adminCardInfo{ID: page.ID, Title: page.Title}
		}
	}

	// Load all published cards for the dropdown.
	var allCards []adminCardInfo
	if pages, err := app.db.ListPublishedReferencePages(""); err == nil {
		for _, p := range pages {
			allCards = append(allCards, adminCardInfo{ID: p.ID, Title: p.Title})
		}
	}

	// Use stored HTML content as preview if available.
	if currentNL != nil && currentNL.HTMLContent != "" {
		previewHTML = currentNL.HTMLContent
	} else if app.newsletterSvc != nil {
		html, err := app.newsletterSvc.PreviewNext()
		if err != nil {
			noContent = true
		} else {
			previewHTML = html
		}
	} else {
		noContent = true
	}

	// Build history (exclude current newsletter from the history list).
	var history []db.Newsletter
	for _, nl := range newsletters {
		if currentNL != nil && nl.ID == currentNL.ID {
			continue
		}
		history = append(history, nl)
	}

	data := map[string]interface{}{
		"Title":             "Newsletter",
		"PreviewHTML":       previewHTML,
		"NoContent":         noContent,
		"CurrentNewsletter": currentNL,
		"Candidates":        candidates,
		"EndedNote":         endedNote,
		"CurrentCard":       currentCard,
		"AllCards":          allCards,
		"History":           history,
		"Success":           successMsg,
		"Error":             errorMsg,
		"CSRFToken":         getCSRFToken(w, r),
	}
	app.renderTemplate(w, "newsletter.html", data)
}

func (app *App) handleNewsletterFeature(w http.ResponseWriter, r *http.Request) {
	newsletterID, err := strconv.ParseInt(r.FormValue("newsletter_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/newsletter?error=Invalid+newsletter+ID", http.StatusSeeOther)
		return
	}
	articleID, err := strconv.ParseInt(r.FormValue("article_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/newsletter?error=Invalid+article+ID", http.StatusSeeOther)
		return
	}

	if err := app.db.SetFeaturedCandidate(newsletterID, articleID); err != nil {
		slog.Error("failed to set featured candidate", "error", err)
		http.Redirect(w, r, "/admin/newsletter?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	if err := app.db.UpdateNewsletterScoutsPick(newsletterID, articleID); err != nil {
		slog.Error("failed to update scout's pick", "error", err)
		http.Redirect(w, r, "/admin/newsletter?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/newsletter?success=Scout's+Pick+updated", http.StatusSeeOther)
}

func (app *App) handleNewsletterCardOverride(w http.ResponseWriter, r *http.Request) {
	newsletterID, err := strconv.ParseInt(r.FormValue("newsletter_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/newsletter?error=Invalid+newsletter+ID", http.StatusSeeOther)
		return
	}
	cardID, err := strconv.ParseInt(r.FormValue("card_id"), 10, 64)
	if err != nil || cardID == 0 {
		http.Redirect(w, r, "/admin/newsletter?error=Please+select+a+card", http.StatusSeeOther)
		return
	}

	if err := app.db.UpdateNewsletterCardOfWeek(newsletterID, cardID); err != nil {
		slog.Error("failed to override card of week", "error", err)
		http.Redirect(w, r, "/admin/newsletter?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/newsletter?success=Card+of+the+Week+updated", http.StatusSeeOther)
}

func (app *App) handleNewsletterStatus(w http.ResponseWriter, r *http.Request) {
	newsletterID, err := strconv.ParseInt(r.FormValue("newsletter_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/admin/newsletter?error=Invalid+newsletter+ID", http.StatusSeeOther)
		return
	}
	status := r.FormValue("status")
	validStatuses := map[string]bool{"approved": true, "hold": true, "skipped": true}
	if !validStatuses[status] {
		http.Redirect(w, r, "/admin/newsletter?error=Invalid+status", http.StatusSeeOther)
		return
	}

	if err := app.db.UpdateNewsletterStatus(newsletterID, status); err != nil {
		slog.Error("failed to update newsletter status", "error", err)
		http.Redirect(w, r, "/admin/newsletter?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/newsletter?success=Newsletter+status+set+to+"+status, http.StatusSeeOther)
}

func (app *App) handleNewsletterSend(w http.ResponseWriter, r *http.Request) {
	if app.newsletterSvc == nil {
		http.Redirect(w, r, "/admin/newsletter?error=Newsletter+service+not+configured", http.StatusSeeOther)
		return
	}

	if err := app.newsletterSvc.SendDaily(); err != nil {
		http.Redirect(w, r, "/admin/newsletter?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/newsletter?success=Newsletter+sent+successfully", http.StatusSeeOther)
}

func (app *App) handleNewsletterHistory(w http.ResponseWriter, r *http.Request) {
	newsletters, err := app.db.ListNewsletters()
	if err != nil {
		http.Error(w, "Failed to load newsletters", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":     "Newsletter History",
		"History":   newsletters,
		"CSRFToken": getCSRFToken(w, r),
	}
	app.renderTemplate(w, "newsletter.html", data)
}

// dbSource is a type alias to avoid import cycle in this file.
// We use db.Source directly since it's already imported through App.
type dbSource = db.Source

// --- Admin Reference Card Handlers ---

// adminCardRow holds a card for admin list display.
type adminCardRow struct {
	ID           int64
	Title        string
	Slug         string
	Category     string
	Issuer       string
	Status       string
	ImageURL     string
	SectionCount int
	PendingCount int
	FlaggedCount int
}

// adminIssuerGroup groups cards by issuer for admin display.
type adminIssuerGroup struct {
	Name  string
	Cards []adminCardRow
}

func (app *App) handleAdminCards(w http.ResponseWriter, r *http.Request) {
	pages, err := app.db.ListReferencePages("")
	if err != nil {
		http.Error(w, "Failed to load cards", http.StatusInternalServerError)
		return
	}

	// Group by issuer.
	issuerMap := make(map[string][]adminCardRow)
	var issuerOrder []string
	for _, p := range pages {
		var sections []db.ReferenceSection
		if p.SectionsJSON != "" && p.SectionsJSON != "[]" {
			json.Unmarshal([]byte(p.SectionsJSON), &sections)
		}
		var pending []db.PendingUpdate
		if p.PendingUpdatesJSON != "" && p.PendingUpdatesJSON != "[]" {
			json.Unmarshal([]byte(p.PendingUpdatesJSON), &pending)
		}
		var flagged []db.FlaggedField
		if p.FlaggedFieldsJSON != "" && p.FlaggedFieldsJSON != "[]" {
			json.Unmarshal([]byte(p.FlaggedFieldsJSON), &flagged)
		}
		row := adminCardRow{
			ID:           p.ID,
			Title:        p.Title,
			Slug:         p.Slug,
			Category:     p.Category,
			Issuer:       p.Issuer,
			Status:       p.Status,
			ImageURL:     p.ImageURL,
			SectionCount: len(sections),
			PendingCount: len(pending),
			FlaggedCount: len(flagged),
		}
		if _, exists := issuerMap[p.Issuer]; !exists {
			issuerOrder = append(issuerOrder, p.Issuer)
		}
		issuerMap[p.Issuer] = append(issuerMap[p.Issuer], row)
	}

	var issuers []adminIssuerGroup
	for _, name := range issuerOrder {
		issuers = append(issuers, adminIssuerGroup{Name: name, Cards: issuerMap[name]})
	}

	data := map[string]interface{}{
		"Title":     "Cards",
		"Cards":     pages,
		"Issuers":   issuers,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   r.URL.Query().Get("msg"),
	}
	app.renderTemplate(w, "admin-cards.html", data)
}

func (app *App) handleAdminCardNew(w http.ResponseWriter, r *http.Request) {
	personaName := app.cfg.Site.PersonaName

	data := map[string]interface{}{
		"Title":       "New Card",
		"IsNew":       true,
		"Card":        &db.ReferencePage{Status: "draft"},
		"Sections":    []db.ReferenceSection{},
		"Keywords":    "",
		"FormAction":  "/admin/cards",
		"PersonaName": personaName,
		"CSRFToken":   getCSRFToken(w, r),
	}
	app.renderTemplate(w, "card-edit.html", data)
}

func (app *App) handleAdminCardCreate(w http.ResponseWriter, r *http.Request) {
	page := &db.ReferencePage{
		Title:         strings.TrimSpace(r.FormValue("title")),
		Slug:          strings.TrimSpace(r.FormValue("slug")),
		Category:      strings.TrimSpace(r.FormValue("category")),
		Issuer:        strings.TrimSpace(r.FormValue("issuer")),
		ImageURL:      strings.TrimSpace(r.FormValue("image_url")),
		ApplyURL:      strings.TrimSpace(r.FormValue("apply_url")),
		Status:        r.FormValue("status"),
		ScoutsVerdict: r.FormValue("scouts_verdict"),
	}

	if page.Title == "" || page.Slug == "" {
		http.Error(w, "Title and slug are required", http.StatusBadRequest)
		return
	}
	if page.Category == "" {
		page.Category = "credit-card"
	}
	if page.Status == "" {
		page.Status = "draft"
	}

	// Parse keywords.
	keywords := parseKeywords(r.FormValue("keywords"))
	keywordsJSON, _ := json.Marshal(keywords)
	page.KeywordsJSON = string(keywordsJSON)

	// Parse sections.
	sections := parseSectionsFromForm(r)
	sectionsJSON, _ := json.Marshal(sections)
	page.SectionsJSON = string(sectionsJSON)

	// Parse structured data fields.
	if feeStr := strings.TrimSpace(r.FormValue("annual_fee_amount")); feeStr != "" {
		fmt.Sscanf(feeStr, "%d", &page.AnnualFeeAmount)
	}
	page.AnnualFeeNote = strings.TrimSpace(r.FormValue("annual_fee_note"))
	page.WelcomeBonusText = strings.TrimSpace(r.FormValue("welcome_bonus_text"))
	earningRatesJSON := strings.TrimSpace(r.FormValue("earning_rates_json"))
	creditsJSON := strings.TrimSpace(r.FormValue("credits_json"))
	benefitsJSON := strings.TrimSpace(r.FormValue("benefits_json"))
	protectionsJSON := strings.TrimSpace(r.FormValue("protections_json"))

	for _, field := range []struct{ name, value string }{
		{"earning_rates_json", earningRatesJSON},
		{"credits_json", creditsJSON},
		{"benefits_json", benefitsJSON},
		{"protections_json", protectionsJSON},
	} {
		if field.value != "" && !json.Valid([]byte(field.value)) {
			http.Error(w, fmt.Sprintf("Invalid JSON in %s", field.name), http.StatusBadRequest)
			return
		}
	}

	page.EarningRatesJSON = earningRatesJSON
	page.CreditsJSON = creditsJSON
	page.BenefitsJSON = benefitsJSON
	page.ProtectionsJSON = protectionsJSON

	// Initialize empty JSON arrays.
	page.RecentChangesJSON = "[]"
	page.PendingUpdatesJSON = "[]"
	if page.EarningRatesJSON == "" {
		page.EarningRatesJSON = "{}"
	}
	if page.CreditsJSON == "" {
		page.CreditsJSON = "{}"
	}
	if page.BenefitsJSON == "" {
		page.BenefitsJSON = "{}"
	}
	if page.ProtectionsJSON == "" {
		page.ProtectionsJSON = "{}"
	}
	page.FlaggedFieldsJSON = "[]"

	if _, err := app.db.CreateReferencePage(page); err != nil {
		http.Error(w, fmt.Sprintf("Failed to create card: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cards?msg=Card+created", http.StatusSeeOther)
}

func (app *App) handleAdminCardEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	page, err := app.db.GetReferencePage(id)
	if err != nil {
		http.Error(w, "Card not found", http.StatusNotFound)
		return
	}

	var sections []db.ReferenceSection
	if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
		json.Unmarshal([]byte(page.SectionsJSON), &sections)
	}

	var keywords []string
	if page.KeywordsJSON != "" && page.KeywordsJSON != "[]" {
		json.Unmarshal([]byte(page.KeywordsJSON), &keywords)
	}

	var flaggedFields []db.FlaggedField
	if page.FlaggedFieldsJSON != "" && page.FlaggedFieldsJSON != "[]" {
		json.Unmarshal([]byte(page.FlaggedFieldsJSON), &flaggedFields)
	}
	if flaggedFields == nil {
		flaggedFields = []db.FlaggedField{}
	}

	personaName := app.cfg.Site.PersonaName

	data := map[string]interface{}{
		"Title":         "Edit: " + page.Title,
		"IsNew":         false,
		"Card":          page,
		"Sections":      sections,
		"Keywords":      strings.Join(keywords, ", "),
		"FormAction":    fmt.Sprintf("/admin/cards/%d/edit", page.ID),
		"PersonaName":   personaName,
		"CSRFToken":     getCSRFToken(w, r),
		"FlaggedFields": flaggedFields,
	}
	app.renderTemplate(w, "card-edit.html", data)
}

func (app *App) handleAdminCardUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	page, err := app.db.GetReferencePage(id)
	if err != nil {
		http.Error(w, "Card not found", http.StatusNotFound)
		return
	}

	page.Title = strings.TrimSpace(r.FormValue("title"))
	page.Slug = strings.TrimSpace(r.FormValue("slug"))
	page.Category = strings.TrimSpace(r.FormValue("category"))
	page.Issuer = strings.TrimSpace(r.FormValue("issuer"))
	page.ImageURL = strings.TrimSpace(r.FormValue("image_url"))
	page.ApplyURL = strings.TrimSpace(r.FormValue("apply_url"))
	page.Status = r.FormValue("status")
	page.ScoutsVerdict = r.FormValue("scouts_verdict")

	// Parse structured data fields.
	if feeStr := strings.TrimSpace(r.FormValue("annual_fee_amount")); feeStr != "" {
		fmt.Sscanf(feeStr, "%d", &page.AnnualFeeAmount)
	} else {
		page.AnnualFeeAmount = 0
	}
	page.AnnualFeeNote = strings.TrimSpace(r.FormValue("annual_fee_note"))
	page.WelcomeBonusText = strings.TrimSpace(r.FormValue("welcome_bonus_text"))
	updateEarningRatesJSON := strings.TrimSpace(r.FormValue("earning_rates_json"))
	updateCreditsJSON := strings.TrimSpace(r.FormValue("credits_json"))
	updateBenefitsJSON := strings.TrimSpace(r.FormValue("benefits_json"))
	updateProtectionsJSON := strings.TrimSpace(r.FormValue("protections_json"))

	for _, field := range []struct{ name, value string }{
		{"earning_rates_json", updateEarningRatesJSON},
		{"credits_json", updateCreditsJSON},
		{"benefits_json", updateBenefitsJSON},
		{"protections_json", updateProtectionsJSON},
	} {
		if field.value != "" && !json.Valid([]byte(field.value)) {
			http.Error(w, fmt.Sprintf("Invalid JSON in %s", field.name), http.StatusBadRequest)
			return
		}
	}

	if updateEarningRatesJSON != "" {
		page.EarningRatesJSON = updateEarningRatesJSON
	}
	if updateCreditsJSON != "" {
		page.CreditsJSON = updateCreditsJSON
	}
	if updateBenefitsJSON != "" {
		page.BenefitsJSON = updateBenefitsJSON
	}
	if updateProtectionsJSON != "" {
		page.ProtectionsJSON = updateProtectionsJSON
	}

	if page.Title == "" || page.Slug == "" {
		http.Error(w, "Title and slug are required", http.StatusBadRequest)
		return
	}
	if page.Category == "" {
		page.Category = "credit-card"
	}

	// Parse keywords.
	keywords := parseKeywords(r.FormValue("keywords"))
	keywordsJSON, _ := json.Marshal(keywords)
	page.KeywordsJSON = string(keywordsJSON)

	// Parse sections.
	sections := parseSectionsFromForm(r)
	sectionsJSON, _ := json.Marshal(sections)
	page.SectionsJSON = string(sectionsJSON)

	if err := app.db.UpdateReferencePage(page); err != nil {
		http.Error(w, fmt.Sprintf("Failed to update card: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cards?msg=Card+updated", http.StatusSeeOther)
}

func (app *App) handleAdminCardDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	if err := app.db.DeleteReferencePage(id); err != nil {
		http.Error(w, "Failed to delete card", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cards?msg=Card+deleted", http.StatusSeeOther)
}

func (app *App) handleFlagField(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	field := strings.TrimSpace(r.FormValue("field"))
	note := strings.TrimSpace(r.FormValue("note"))
	if field == "" {
		http.Error(w, "Field name is required", http.StatusBadRequest)
		return
	}

	if err := app.db.AppendFlaggedField(id, field, note); err != nil {
		http.Error(w, fmt.Sprintf("Failed to flag field: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit", id), http.StatusSeeOther)
}

func (app *App) handleResolveFlag(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	field := strings.TrimSpace(r.FormValue("field"))
	if field == "" {
		http.Error(w, "Field name is required", http.StatusBadRequest)
		return
	}

	if err := app.db.ResolveFlaggedField(id, field); err != nil {
		http.Error(w, fmt.Sprintf("Failed to resolve flag: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit", id), http.StatusSeeOther)
}

func (app *App) handleAdminCardUpdates(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}

	page, err := app.db.GetReferencePage(id)
	if err != nil {
		http.Error(w, "Card not found", http.StatusNotFound)
		return
	}

	var updates []db.PendingUpdate
	if page.PendingUpdatesJSON != "" && page.PendingUpdatesJSON != "[]" {
		json.Unmarshal([]byte(page.PendingUpdatesJSON), &updates)
	}

	data := map[string]interface{}{
		"Title":     "Pending Updates: " + page.Title,
		"Card":      page,
		"Updates":   updates,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   r.URL.Query().Get("msg"),
	}
	app.renderTemplate(w, "card-updates.html", data)
}

func (app *App) handleApproveCardUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}
	uid, err := strconv.Atoi(r.PathValue("uid"))
	if err != nil {
		http.Error(w, "Invalid update ID", http.StatusBadRequest)
		return
	}

	page, err := app.db.GetReferencePage(id)
	if err != nil {
		http.Error(w, "Card not found", http.StatusNotFound)
		return
	}

	var updates []db.PendingUpdate
	if page.PendingUpdatesJSON != "" && page.PendingUpdatesJSON != "[]" {
		json.Unmarshal([]byte(page.PendingUpdatesJSON), &updates)
	}

	// Find the update to approve and apply it.
	var approved *db.PendingUpdate
	var remaining []db.PendingUpdate
	for _, u := range updates {
		if u.ID == uid {
			approved = &u
		} else {
			remaining = append(remaining, u)
		}
	}

	if approved == nil {
		http.Error(w, "Update not found", http.StatusNotFound)
		return
	}

	// Apply the proposed content to the matching section.
	var sections []db.ReferenceSection
	if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
		json.Unmarshal([]byte(page.SectionsJSON), &sections)
	}
	for i, s := range sections {
		if s.Key == approved.SectionKey {
			sections[i].Content = approved.ProposedContent
			break
		}
	}

	sectionsJSON, _ := json.Marshal(sections)
	page.SectionsJSON = string(sectionsJSON)

	if remaining == nil {
		remaining = []db.PendingUpdate{}
	}
	remainingJSON, _ := json.Marshal(remaining)
	page.PendingUpdatesJSON = string(remainingJSON)

	if err := app.db.UpdateReferencePage(page); err != nil {
		http.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/updates?msg=Update+approved", id), http.StatusSeeOther)
}

func (app *App) handleRejectCardUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid card ID", http.StatusBadRequest)
		return
	}
	uid, err := strconv.Atoi(r.PathValue("uid"))
	if err != nil {
		http.Error(w, "Invalid update ID", http.StatusBadRequest)
		return
	}

	page, err := app.db.GetReferencePage(id)
	if err != nil {
		http.Error(w, "Card not found", http.StatusNotFound)
		return
	}

	var updates []db.PendingUpdate
	if page.PendingUpdatesJSON != "" && page.PendingUpdatesJSON != "[]" {
		json.Unmarshal([]byte(page.PendingUpdatesJSON), &updates)
	}

	// Remove the rejected update.
	var remaining []db.PendingUpdate
	for _, u := range updates {
		if u.ID != uid {
			remaining = append(remaining, u)
		}
	}

	if remaining == nil {
		remaining = []db.PendingUpdate{}
	}
	remainingJSON, _ := json.Marshal(remaining)
	page.PendingUpdatesJSON = string(remainingJSON)

	if err := app.db.UpdateReferencePage(page); err != nil {
		http.Error(w, "Failed to save", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/updates?msg=Update+rejected", id), http.StatusSeeOther)
}

// adminLinkedCard is the per-row data for the curation page (combines reference
// page metadata with optional mention count).
type adminLinkedCard struct {
	Slug     string
	Title    string
	Issuer   string
	Category string
	ImageURL string
	Count    int // 0 if added manually; >0 if also auto-detected
}

func (app *App) handleAdminArticleCardsGET(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	article, err := app.db.GetGeneratedArticle(id)
	if err != nil {
		http.Error(w, "Article not found", http.StatusNotFound)
		return
	}

	// Build "linked" rows from related_cards_json.
	linkedSlugs := map[string]bool{}
	var linkedSlugList []string
	if article.RelatedCardsJSON != "" && article.RelatedCardsJSON != "[]" {
		var rels []struct{ Slug, Title string }
		_ = json.Unmarshal([]byte(article.RelatedCardsJSON), &rels)
		for _, rel := range rels {
			linkedSlugs[rel.Slug] = true
			linkedSlugList = append(linkedSlugList, rel.Slug)
		}
	}

	// Detect mentions to compute counts (used for both linked and suggested rows).
	refPages, _ := app.db.ListPublishedReferencePages("")
	detected := linker.DetectMentions(article.Content, refPages)
	countBySlug := map[string]int{}
	for _, dc := range detected {
		countBySlug[dc.Slug] = dc.Count
	}

	// Build linked rows in the order they appear in related_cards_json.
	linked := make([]adminLinkedCard, 0, len(linkedSlugList))
	for _, slug := range linkedSlugList {
		page, err := app.db.GetReferencePageBySlug(slug)
		if err != nil || page == nil {
			continue
		}
		linked = append(linked, adminLinkedCard{
			Slug: page.Slug, Title: page.Title, Issuer: page.Issuer,
			Category: page.Category, ImageURL: page.ImageURL,
			Count: countBySlug[slug],
		})
	}

	// Build suggested rows: detected cards NOT in linkedSlugs.
	suggested := make([]adminLinkedCard, 0)
	for _, dc := range detected {
		if linkedSlugs[dc.Slug] {
			continue
		}
		page, err := app.db.GetReferencePageBySlug(dc.Slug)
		if err != nil || page == nil {
			continue
		}
		suggested = append(suggested, adminLinkedCard{
			Slug: page.Slug, Title: page.Title, Issuer: page.Issuer,
			Category: page.Category, ImageURL: page.ImageURL,
			Count: dc.Count,
		})
	}

	data := map[string]any{
		"Article":   article,
		"Linked":    linked,
		"Suggested": suggested,
		"CSRFToken": getCSRFToken(w, r),
	}
	app.renderTemplate(w, "article-cards.html", data)
}

func (app *App) handleAdminArticleCardsPOST(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form", http.StatusBadRequest)
		return
	}

	// Compose the new slug list. Start with whatever was submitted in
	// linked_slugs[] (current rows), then apply add_slug / remove_slug.
	slugs := r.Form["linked_slugs[]"]
	if add := r.FormValue("add_slug"); add != "" {
		slugs = appendUnique(slugs, add)
	}
	if remove := r.FormValue("remove_slug"); remove != "" {
		slugs = removeFromSlice(slugs, remove)
	}

	// Resolve titles via reference_pages.
	type relatedRef struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	}
	out := make([]relatedRef, 0, len(slugs))
	for _, s := range slugs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		page, err := app.db.GetReferencePageBySlug(s)
		if err != nil || page == nil {
			continue
		}
		out = append(out, relatedRef{Slug: page.Slug, Title: page.Title})
	}
	relatedJSON, _ := json.Marshal(out)

	heading := strings.TrimSpace(r.FormValue("heading_override"))
	const maxHeadingLen = 256
	if len(heading) > maxHeadingLen {
		http.Error(w, fmt.Sprintf("heading_override exceeds %d characters", maxHeadingLen), http.StatusBadRequest)
		return
	}

	if err := app.db.UpdateArticleLinkedCards(id, string(relatedJSON), heading); err != nil {
		slog.Error("update linked cards", "id", id, "err", err)
		http.Error(w, "Failed to update", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/articles/%d/cards?msg=saved", id), http.StatusSeeOther)
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// removeFromSlice returns slice with all occurrences of s removed.
func removeFromSlice(slice []string, s string) []string {
	out := slice[:0]
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// parseKeywords splits a comma-separated keyword string into a clean slice.
func parseKeywords(s string) []string {
	var result []string
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			result = append(result, k)
		}
	}
	return result
}

// parseSectionsFromForm extracts sections from form arrays.
func parseSectionsFromForm(r *http.Request) []db.ReferenceSection {
	r.ParseForm()
	keys := r.Form["section_key[]"]
	titles := r.Form["section_title[]"]
	contents := r.Form["section_content[]"]

	var sections []db.ReferenceSection
	for i := 0; i < len(keys); i++ {
		key := strings.TrimSpace(keys[i])
		if key == "" {
			continue
		}
		title := ""
		if i < len(titles) {
			title = strings.TrimSpace(titles[i])
		}
		content := ""
		if i < len(contents) {
			content = contents[i]
		}
		sections = append(sections, db.ReferenceSection{
			Key:     key,
			Title:   title,
			Content: content,
		})
	}
	return sections
}

// weekHasLiveArticle reports whether the weekly issue dated sendDate would go
// out: SendWeekly sends the approved articles of the seven days before
// sendDate whose offers have not ended, and nothing when none are left. On
// an unreadable date or a database error it assumes the issue goes out,
// which keeps the preview's usual note.
func (app *App) weekHasLiveArticle(sendDate string) bool {
	day, err := time.Parse("2006-01-02", sendDate)
	if err != nil {
		return true
	}
	articles, err := app.db.ListApprovedArticlesSince(day.AddDate(0, 0, -7))
	if err != nil {
		return true
	}
	today := app.offerToday()
	for _, a := range articles {
		if !offer.Ended(a.ExpiresOn, today) {
			return true
		}
	}
	return false
}
