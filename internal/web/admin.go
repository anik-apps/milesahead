package web

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/linker"
	"github.com/kumaranik/milesahead/internal/newsletter"
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

	// Queue row extras (dashboard.go).
	Group        string // groupDecide, groupReady or groupRewriting
	Age          string // "3 h ago"
	Enriched     bool   // has its image and Scout's Take
	RetryNote    string // a rewrite's state: "Failed 2×, retrying in 8 min"
	HasScore     bool
	ScoreClass   string // "ok", "warn", "bad" against the critic threshold
	FailedChecks int    // critic checks that did not pass
	// Position is the row's place in the queue (1-based) of QueueLen;
	// PrevID and NextID are its neighbours, 0 at the ends.
	Position, QueueLen int
	PrevID, NextID     int64

	createdAtRaw time.Time // for sorting only, not exposed to template
	updatedAtRaw time.Time
}

// adminTimeFormat is how admin views show timestamps.
const adminTimeFormat = "Jan 2, 2006 3:04 PM"

// publishedLimit is how many published articles the Published page lists,
// most recent first; it says how many there are in all.
const publishedLimit = 100

func (app *App) handleAdminPublished(w http.ResponseWriter, r *http.Request) {
	articles, err := app.db.ListPublishedArticles(publishedLimit, 0)
	if err != nil {
		http.Error(w, "Failed to load articles", http.StatusInternalServerError)
		return
	}
	total := len(articles)
	if counts, err := app.db.CountArticlesByStatus(); err == nil {
		total = counts[db.StatusApproved]
	}

	threshold := app.cfg.Votes.AlertThreshold
	if threshold == 0 {
		threshold = 5
	}

	now := time.Now()
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
			row.PublishedAt = a.PublishedAt.Format(adminTimeFormat)
			row.Age = ago(*a.PublishedAt, now)
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
		"Total":     total,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   r.URL.Query().Get("msg"),
		"MessageID": queryInt(r, "id"),
	}
	// After a plain unpublish, the toast offers to put it back: approving
	// the revision that was live, which needs that revision number.
	if data["Message"] == "unpublished" && r.URL.Query().Get("undo") == "1" {
		if a, err := app.db.GetGeneratedArticle(queryInt(r, "id")); err == nil && a.Status == db.StatusUnpublished {
			data["UndoRevision"] = a.RevisionNumber
			data["UndoTitle"] = a.Title
		}
	}
	app.renderTemplate(w, "published.html", data)
}

func (app *App) handleApproveArticle(w http.ResponseWriter, r *http.Request) {
	id, revision, ok := articleDecisionForm(w, r)
	if !ok {
		return
	}
	// The status it is approved from, for the Undo that follows.
	var prev string
	if a, err := app.db.GetGeneratedArticle(id); err == nil {
		prev = a.Status
	}

	if err := app.articleManager.ApproveRevision(id, revision); err != nil {
		app.decisionFailed(w, r, id, "approve", err)
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
			// Wait out the undo window, then only draft card updates if
			// the article is still published. (A shutdown during the wait
			// skips detection for this article.)
			if d := app.cardDetectDelay; d > 0 {
				select {
				case <-time.After(d):
				case <-app.shutdownCtx.Done():
					return
				}
			}
			if a, err := app.db.GetGeneratedArticle(id); err != nil || a.Status != db.StatusApproved {
				slog.Info("card update detection skipped: article no longer published", "article_id", id)
				return
			}
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := app.cardDetector.DetectAndDraft(app.shutdownCtx, id); err != nil {
				slog.Error("card update detection failed", "article_id", id, "error", err)
			}
		}()
	}

	if r.FormValue("return") == "published" {
		http.Redirect(w, r, fmt.Sprintf("/admin/published?msg=republished&id=%d", id), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=approved&id=%d&prev=%s&open=%d", id, url.QueryEscape(prev), nextParam(r)), http.StatusSeeOther)
}

func (app *App) handleRejectArticle(w http.ResponseWriter, r *http.Request) {
	id, revision, ok := articleDecisionForm(w, r)
	if !ok {
		return
	}

	if err := app.articleManager.RejectRevision(id, revision); err != nil {
		app.decisionFailed(w, r, id, "reject", err)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=rejected&id=%d&open=%d", id, nextParam(r)), http.StatusSeeOther)
}

// articleDecisionForm reads an approve or reject request: the article id from
// the path and the revision the admin was looking at from the form. The
// revision is required, so a decision always names the text it is about.
func articleDecisionForm(w http.ResponseWriter, r *http.Request) (id int64, revision int, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return 0, 0, false
	}
	revision, err = strconv.Atoi(r.FormValue("revision"))
	if err != nil || revision <= 0 {
		http.Error(w, "Missing the revision you reviewed; reload the dashboard and try again", http.StatusBadRequest)
		return 0, 0, false
	}
	return id, revision, true
}

// decisionFailed answers an approve or reject that did not go through. When a
// rewrite landed after the admin loaded the page, it says so on the dashboard
// rather than as an error page, so the admin can review the new text.
func (app *App) decisionFailed(w http.ResponseWriter, r *http.Request, id int64, verb string, err error) {
	var changed *article.ErrArticleChanged
	if errors.As(err, &changed) {
		http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=changed&id=%d&rev=%d", id, changed.Revision), http.StatusSeeOther)
		return
	}
	if errors.Is(err, db.ErrStatusChanged) {
		http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=status_changed&id=%d", id), http.StatusSeeOther)
		return
	}
	http.Error(w, fmt.Sprintf("Failed to %s: %v", verb, err), http.StatusBadRequest)
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

	http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=revision_submitted&id=%d&sent=revise-%d&open=%d", id, id, nextParam(r)), http.StatusSeeOther)
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
		http.Redirect(w, r, fmt.Sprintf("/admin/published?msg=unpublished_revise&id=%d&sent=unpublish-%d", id, id), http.StatusSeeOther)
		return
	}
	if err := app.articleManager.Unpublish(id); err != nil {
		http.Error(w, fmt.Sprintf("Failed to unpublish: %v", err), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/published?msg=unpublished&undo=1&id=%d&sent=unpublish-%d", id, id), http.StatusSeeOther)
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

	http.Redirect(w, r, fmt.Sprintf("/admin/published?msg=unpublished&id=%d&sent=unpublish-%d", id, id), http.StatusSeeOther)
}

func (app *App) handleAdminSources(w http.ResponseWriter, r *http.Request) {
	sources, err := app.db.ListAllSources()
	if err != nil {
		http.Error(w, "Failed to load sources", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	health, err := app.db.ListSourceHealth(now.Add(-sourceRecentWindow))
	if err != nil {
		slog.Warn("sources: could not load health", "error", err)
	}
	rows := make([]adminSourceRow, 0, len(sources))
	for _, src := range sources {
		h := health[src.ID]
		rows = append(rows, adminSourceRow{
			Source:      src,
			LastArticle: ago(h.LastArticleAt, now),
			Recent:      h.Recent,
			Total:       h.Total,
			Stale:       h.Stale(src.Active, now, sourceStaleAfter),
		})
	}

	data := map[string]interface{}{
		"Title":     "Sources",
		"Sources":   rows,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   sourceMessages[r.URL.Query().Get("msg")],
		"Error":     sourceErrors[r.URL.Query().Get("error")],
	}
	app.renderTemplate(w, "sources.html", data)
}

// adminSourceRow is a source with what the scanner has collected from it.
type adminSourceRow struct {
	db.Source
	LastArticle string // "3 h ago", or "never"
	Recent      int    // articles stored in the last sourceRecentWindow
	Total       int
	// Stale: active, but nothing new for longer than sourceStaleAfter --
	// the feed has probably moved or broken.
	Stale bool
}

const (
	sourceRecentWindow = 7 * 24 * time.Hour
	sourceStaleAfter   = 3 * 24 * time.Hour
)

var sourceMessages = map[string]string{
	"added": "Source added.", "saved": "Source saved.", "toggled": "Source updated.",
}

var sourceErrors = map[string]string{
	"required": "Not saved: a source needs a name and a URL.",
	"interval": "Not saved: the scan interval must be a whole number of minutes, 1 or more. Leave it empty for the default.",
	"store":    "Not saved: the source could not be stored.",
}

// sourceForm reads the add and edit forms. An empty RSS URL or interval is
// stored as unset, so clearing a field clears it; errCode is a key of
// sourceErrors, or "" when the form is valid.
func sourceForm(r *http.Request) (name, siteURL string, rssURL *string, interval *int, errCode string) {
	name = strings.TrimSpace(r.FormValue("name"))
	siteURL = strings.TrimSpace(r.FormValue("url"))
	if name == "" || siteURL == "" {
		return "", "", nil, nil, "required"
	}
	if v := strings.TrimSpace(r.FormValue("rss_url")); v != "" {
		rssURL = &v
	}
	if v := strings.TrimSpace(r.FormValue("scan_interval")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return "", "", nil, nil, "interval"
		}
		interval = &n
	}
	return name, siteURL, rssURL, interval, ""
}

func (app *App) handleAddSource(w http.ResponseWriter, r *http.Request) {
	name, siteURL, rssURL, interval, errCode := sourceForm(r)
	if errCode != "" {
		http.Redirect(w, r, "/admin/sources?error="+errCode, http.StatusSeeOther)
		return
	}
	source := &dbSource{Name: name, URL: siteURL, RSSURL: rssURL, ScanInterval: interval, Active: true}
	if _, err := app.db.CreateSource(source); err != nil {
		slog.Error("add source", "error", err)
		http.Redirect(w, r, "/admin/sources?error=store", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/sources?msg=added", http.StatusSeeOther)
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

	name, siteURL, rssURL, interval, errCode := sourceForm(r)
	if errCode != "" {
		http.Redirect(w, r, "/admin/sources?error="+errCode, http.StatusSeeOther)
		return
	}
	source.Name, source.URL, source.RSSURL, source.ScanInterval = name, siteURL, rssURL, interval

	if err := app.db.UpdateSource(source); err != nil {
		slog.Error("edit source", "id", id, "error", err)
		http.Redirect(w, r, "/admin/sources?error=store", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/sources?msg=saved", http.StatusSeeOther)
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

	http.Redirect(w, r, "/admin/sources?msg=toggled", http.StatusSeeOther)
}

func (app *App) handleAdminSubscribers(w http.ResponseWriter, r *http.Request) {
	subscribers, err := app.db.ListAllSubscribers()
	if err != nil {
		http.Error(w, "Failed to load subscribers", http.StatusInternalServerError)
		return
	}

	confirmed := 0
	for _, s := range subscribers {
		if s.Confirmed {
			confirmed++
		}
	}
	data := map[string]interface{}{
		"Title":       "Subscribers",
		"Subscribers": subscribers,
		"Confirmed":   confirmed,
		"Unconfirmed": len(subscribers) - confirmed,
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
		// Flat, so each value is its own row: nested maps printed as
		// "map[Host:... Password:...]".
		"Mail": map[string]interface{}{
			"SMTP host":          app.cfg.Mail.SMTP.Host,
			"SMTP port":          app.cfg.Mail.SMTP.Port,
			"SMTP user":          app.cfg.Mail.SMTP.User,
			"SMTP password":      maskString(app.cfg.Mail.SMTP.Password),
			"SMTP from":          app.cfg.Mail.SMTP.From,
			"IMAP host":          app.cfg.Mail.IMAP.Host,
			"IMAP port":          app.cfg.Mail.IMAP.Port,
			"IMAP user":          app.cfg.Mail.IMAP.User,
			"IMAP password":      maskString(app.cfg.Mail.IMAP.Password),
			"IMAP poll interval": app.cfg.Mail.IMAP.PollInterval,
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

	confirmed := 0
	if subs, err := app.db.ListConfirmedSubscribers(); err == nil {
		confirmed = len(subs)
	}

	data := map[string]interface{}{
		"Title":             "Newsletter",
		"PersonaName":       app.cfg.Site.PersonaName,
		"StatusNote":        newsletterStatusNote(currentNL, confirmed, app.newsletterToday()),
		"IssuePassed":       currentNL != nil && currentNL.Status != "sent" && currentNL.Date < app.newsletterToday(),
		"Confirmed":         confirmed,
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

// handleNewsletterHistory keeps the old URL working: the history is the
// "Past newsletters" section of the newsletter page.
func (app *App) handleNewsletterHistory(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/newsletter#history", http.StatusSeeOther)
}

// newsletterStatusNote says what will happen to the issue: whether and when
// it goes out, and to how many people. SendWeekly sends only an approved or
// auto-approved issue, on its Saturday.
//
// today ("2006-01-02", the newsletter's zone) marks an unsent issue whose
// date has passed: SendWeekly only sends its own Saturday's issue, so it
// will never go out, whatever its status.
func newsletterStatusNote(nl *db.Newsletter, confirmed int, today string) string {
	if nl == nil {
		return ""
	}
	if nl.Status != "sent" && nl.Date < today {
		when := nl.Date
		if d, err := time.Parse("2006-01-02", nl.Date); err == nil {
			when = d.Format("Mon Jan 2")
		}
		return fmt.Sprintf("Not sent: this issue was for %s, which has passed, so it will not go out. The next issue is built on Friday evening.", when)
	}
	when := nl.Date
	if d, err := time.Parse("2006-01-02", nl.Date); err == nil {
		when = d.Format("Mon Jan 2")
	}
	people := fmt.Sprintf("%d confirmed subscribers", confirmed)
	if confirmed == 1 {
		people = "1 confirmed subscriber"
	}
	switch nl.Status {
	case "approved", "auto_approved":
		return fmt.Sprintf("Goes out %s, %s, to %s.", when, newsletter.WeeklySendLabel, people)
	case "draft":
		return fmt.Sprintf("Draft: it will not go out on %s unless you approve it. Approving sends it at %s to %s.", when, newsletter.WeeklySendLabel, people)
	case "hold":
		return fmt.Sprintf("On hold: it will not go out on %s unless you approve it.", when)
	case "skipped":
		return fmt.Sprintf("Skipped: the %s issue will not go out.", when)
	case "sent":
		if nl.SentAt != nil {
			return "Sent " + nl.SentAt.Format(adminTimeFormat) + "."
		}
		return "Sent."
	}
	return ""
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
	var attention []adminCardRow
	for _, name := range issuerOrder {
		issuers = append(issuers, adminIssuerGroup{Name: name, Cards: issuerMap[name]})
		for _, c := range issuerMap[name] {
			if c.PendingCount > 0 || c.FlaggedCount > 0 {
				attention = append(attention, c)
			}
		}
	}

	data := map[string]interface{}{
		"Title":     "Cards",
		"Cards":     pages,
		"Issuers":   issuers,
		"Attention": attention,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   r.URL.Query().Get("msg"),
	}
	app.renderTemplate(w, "admin-cards.html", data)
}

func (app *App) handleAdminCardNew(w http.ResponseWriter, r *http.Request) {
	app.renderCardForm(w, r, http.StatusOK, cardForm{
		Card:     &db.ReferencePage{Status: "draft"},
		Sections: []db.ReferenceSection{},
		IsNew:    true,
	})
}

// cardForm is what the card editor renders: the card as stored, or as the
// admin submitted it when saving failed, with the reason.
type cardForm struct {
	Card     *db.ReferencePage
	Sections []db.ReferenceSection
	Keywords string
	IsNew    bool
	// FormError says why the save failed; FieldErrors holds the message for
	// each failing form field, keyed by the field's name.
	FormError   string
	FieldErrors map[string]string
	Message     string
}

// renderCardForm renders the card editor with status.
func (app *App) renderCardForm(w http.ResponseWriter, r *http.Request, status int, f cardForm) {
	formAction := "/admin/cards"
	title := "New Card"
	if !f.IsNew {
		formAction = fmt.Sprintf("/admin/cards/%d/edit", f.Card.ID)
		title = "Edit: " + f.Card.Title
	}
	var flagged []db.FlaggedField
	if f.Card.FlaggedFieldsJSON != "" && f.Card.FlaggedFieldsJSON != "[]" {
		json.Unmarshal([]byte(f.Card.FlaggedFieldsJSON), &flagged)
	}
	if flagged == nil {
		flagged = []db.FlaggedField{}
	}
	var pending []db.PendingUpdate
	if f.Card.PendingUpdatesJSON != "" && f.Card.PendingUpdatesJSON != "[]" {
		json.Unmarshal([]byte(f.Card.PendingUpdatesJSON), &pending)
	}
	// Flags are shown on the field they are about; flags on a name the
	// editor has no field for are listed together above the form.
	flagMap := map[string][]db.FlaggedField{}
	var otherFlags []db.FlaggedField
	known := map[string]bool{}
	for _, cf := range cardFlagFields {
		known[cf.Name] = true
	}
	for _, fl := range flagged {
		if known[fl.Field] {
			flagMap[fl.Field] = append(flagMap[fl.Field], fl)
		} else {
			otherFlags = append(otherFlags, fl)
		}
	}
	sectionUpdates := map[string]int{}
	for _, u := range pending {
		sectionUpdates[u.SectionKey]++
	}
	data := map[string]interface{}{
		"Title":          title,
		"IsNew":          f.IsNew,
		"Card":           f.Card,
		"Sections":       f.Sections,
		"Keywords":       f.Keywords,
		"FormAction":     formAction,
		"PersonaName":    app.cfg.Site.PersonaName,
		"CSRFToken":      getCSRFToken(w, r),
		"FlaggedFields":  flagged,
		"FlagMap":        flagMap,
		"OtherFlags":     otherFlags,
		"FlagFields":     cardFlagFields,
		"JSONFields":     cardJSONFieldValues(f.Card),
		"SectionUpdates": sectionUpdates,
		"PendingCount":   len(pending),
		"FormError":      f.FormError,
		"FieldErrors":    f.FieldErrors,
		"Message":        f.Message,
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	app.renderTemplate(w, "card-edit.html", data)
}

// cardFlagFields are the editor's fields an admin can flag, in form order.
var cardFlagFields = []struct{ Name, Label string }{
	{"title", "Title"}, {"slug", "Slug"}, {"category", "Category"}, {"issuer", "Issuer"},
	{"keywords", "Keywords"}, {"image_url", "Image URL"}, {"apply_url", "Card details URL"},
	{"annual_fee_amount", "Annual fee"}, {"annual_fee_note", "Annual fee note"},
	{"welcome_bonus_text", "Welcome bonus"}, {"earning_rates_json", "Earning rates"},
	{"credits_json", "Credits"}, {"benefits_json", "Benefits"}, {"protections_json", "Protections"},
	{"sections", "Sections"}, {"scouts_verdict", "Verdict"},
}

// cardJSONFields are the card's structured-data fields, edited as JSON.
var cardJSONFields = []struct{ name, label string }{
	{"earning_rates_json", "Earning rates"},
	{"credits_json", "Credits"},
	{"benefits_json", "Benefits"},
	{"protections_json", "Protections"},
}

// applyCardForm copies the submitted form onto page and reports what is
// wrong with it, keyed by form field. page always ends up holding exactly
// what was submitted, valid or not, so a failed save can show it again.
// An emptied JSON field is stored as {} -- it clears the data rather than
// keeping the old value.
func applyCardForm(r *http.Request, page *db.ReferencePage) (keywords string, sections []db.ReferenceSection, errs map[string]string) {
	errs = map[string]string{}
	page.Title = strings.TrimSpace(r.FormValue("title"))
	page.Slug = strings.TrimSpace(r.FormValue("slug"))
	page.Category = strings.TrimSpace(r.FormValue("category"))
	page.Issuer = strings.TrimSpace(r.FormValue("issuer"))
	page.ImageURL = strings.TrimSpace(r.FormValue("image_url"))
	page.ApplyURL = strings.TrimSpace(r.FormValue("apply_url"))
	page.Status = r.FormValue("status")
	page.ScoutsVerdict = r.FormValue("scouts_verdict")
	page.AnnualFeeNote = strings.TrimSpace(r.FormValue("annual_fee_note"))
	page.WelcomeBonusText = strings.TrimSpace(r.FormValue("welcome_bonus_text"))

	if page.Title == "" {
		errs["title"] = "Title is required."
	}
	if page.Slug == "" {
		errs["slug"] = "Slug is required."
	}
	if page.Category == "" {
		page.Category = "credit-card"
	}
	if page.Status != "published" {
		page.Status = "draft"
	}

	page.AnnualFeeAmount = 0
	if fee := strings.TrimSpace(r.FormValue("annual_fee_amount")); fee != "" {
		n, err := strconv.Atoi(fee)
		if err != nil || n < 0 {
			errs["annual_fee_amount"] = "Annual fee must be a whole number of dollars, like 895."
		} else {
			page.AnnualFeeAmount = n
		}
	}

	values := map[string]string{}
	for _, f := range cardJSONFields {
		v := strings.TrimSpace(r.FormValue(f.name))
		if v == "" {
			v = "{}"
		} else if err := checkJSON(v); err != "" {
			errs[f.name] = f.label + " is not valid JSON: " + err
		}
		values[f.name] = v
	}
	page.EarningRatesJSON = values["earning_rates_json"]
	page.CreditsJSON = values["credits_json"]
	page.BenefitsJSON = values["benefits_json"]
	page.ProtectionsJSON = values["protections_json"]

	keywords = r.FormValue("keywords")
	kwJSON, _ := json.Marshal(parseKeywords(keywords))
	page.KeywordsJSON = string(kwJSON)

	sections = parseSectionsFromForm(r)
	secJSON, _ := json.Marshal(sections)
	page.SectionsJSON = string(secJSON)
	if sections == nil {
		sections = []db.ReferenceSection{}
	}
	return keywords, sections, errs
}

// cardJSONField is one JSON box in the card editor.
type cardJSONField struct {
	Name, Label, Placeholder, Value string
}

// cardJSONFieldValues are the editor's JSON boxes, holding card's values. A
// stored "{}" shows as an empty box.
func cardJSONFieldValues(card *db.ReferencePage) []cardJSONField {
	placeholders := map[string]string{
		"earning_rates_json": `{"dining": {"rate": "3x", "note": ""}}`,
		"credits_json":       `{"airline": {"amount": "$200/yr", "note": ""}}`,
		"benefits_json":      `{"lounge_access": {"value": "Priority Pass", "note": ""}}`,
		"protections_json":   `{"purchase_protection": {"amount": "$10,000", "note": "120 days"}}`,
	}
	values := map[string]string{
		"earning_rates_json": card.EarningRatesJSON,
		"credits_json":       card.CreditsJSON,
		"benefits_json":      card.BenefitsJSON,
		"protections_json":   card.ProtectionsJSON,
	}
	out := make([]cardJSONField, 0, len(cardJSONFields))
	for _, f := range cardJSONFields {
		v := values[f.name]
		if v == "{}" {
			v = ""
		}
		out = append(out, cardJSONField{Name: f.name, Label: f.label, Placeholder: placeholders[f.name], Value: v})
	}
	return out
}

// checkJSON returns "" for valid JSON, or where and why it is not.
func checkJSON(s string) string {
	var v interface{}
	err := json.Unmarshal([]byte(s), &v)
	if err == nil {
		return ""
	}
	var syn *json.SyntaxError
	if errors.As(err, &syn) {
		line := 1 + strings.Count(s[:min(int(syn.Offset), len(s))], "\n")
		return fmt.Sprintf("%s (line %d)", syn.Error(), line)
	}
	return err.Error()
}

// cardFormSummary is the banner above a card form that failed to save.
func cardFormSummary(errs map[string]string) string {
	if len(errs) == 1 {
		for _, e := range errs {
			return "Not saved: " + e + " Your other changes are kept below."
		}
	}
	return fmt.Sprintf("Not saved: %d fields need fixing. Your changes are kept below.", len(errs))
}

func (app *App) handleAdminCardCreate(w http.ResponseWriter, r *http.Request) {
	page := &db.ReferencePage{RecentChangesJSON: "[]", PendingUpdatesJSON: "[]", FlaggedFieldsJSON: "[]"}
	keywords, sections, errs := applyCardForm(r, page)
	form := cardForm{Card: page, Sections: sections, Keywords: keywords, IsNew: true, FieldErrors: errs}
	if len(errs) > 0 {
		form.FormError = cardFormSummary(errs)
		app.renderCardForm(w, r, http.StatusBadRequest, form)
		return
	}

	id, err := app.db.CreateReferencePage(page)
	if err != nil {
		slog.Error("create card", "slug", page.Slug, "error", err)
		form.FormError = "Not saved: the card could not be stored (" + err.Error() + "). Is the slug already used by another card?"
		app.renderCardForm(w, r, http.StatusBadRequest, form)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit?msg=created", id), http.StatusSeeOther)
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

	msg := ""
	switch r.URL.Query().Get("msg") {
	case "saved":
		msg = "Saved."
	case "created":
		msg = "Card created."
	case "flagged":
		msg = "Flag added."
	case "resolved":
		msg = "Flag resolved."
	}
	app.renderCardForm(w, r, http.StatusOK, cardForm{
		Card: page, Sections: sections, Keywords: strings.Join(keywords, ", "), Message: msg,
	})
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

	keywords, sections, errs := applyCardForm(r, page)
	form := cardForm{Card: page, Sections: sections, Keywords: keywords, FieldErrors: errs}
	if len(errs) > 0 {
		form.FormError = cardFormSummary(errs)
		app.renderCardForm(w, r, http.StatusBadRequest, form)
		return
	}

	if err := app.db.UpdateReferencePage(page); err != nil {
		slog.Error("update card", "id", id, "error", err)
		form.FormError = "Not saved: the card could not be stored (" + err.Error() + "). Is the slug already used by another card?"
		app.renderCardForm(w, r, http.StatusBadRequest, form)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit?msg=saved", id), http.StatusSeeOther)
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

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit?msg=flagged", id), http.StatusSeeOther)
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

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/edit?msg=resolved", id), http.StatusSeeOther)
}

// cardUpdateView is a pending card update as the updates page shows it.
type cardUpdateView struct {
	db.PendingUpdate
	Diff          wordDiff
	SectionTitle  string
	SectionExists bool   // the card still has a section with the update's key
	ArticleSlug   string // the source article's slug, if it is published
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

	msg := ""
	switch r.URL.Query().Get("msg") {
	case "approved":
		msg = "Update approved: the section now has the proposed text."
	case "rejected":
		msg = "Update rejected."
	}
	// MissingUID is the update whose section no longer exists, after an
	// approve that could not apply it.
	var missingUID int64
	if r.URL.Query().Get("error") == "missing_section" {
		missingUID = queryInt(r, "uid")
	}
	var sections []db.ReferenceSection
	if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
		json.Unmarshal([]byte(page.SectionsJSON), &sections)
	}
	titles := map[string]string{}
	for _, sec := range sections {
		titles[sec.Key] = sec.Title
	}
	views := make([]cardUpdateView, 0, len(updates))
	for _, u := range updates {
		v := cardUpdateView{PendingUpdate: u, Diff: diffWords(u.CurrentContent, u.ProposedContent)}
		v.SectionTitle, v.SectionExists = titles[u.SectionKey]
		if a, err := app.db.GetGeneratedArticle(u.ArticleID); err == nil && a.Status == db.StatusApproved {
			v.ArticleSlug = a.Slug
		}
		views = append(views, v)
	}

	data := map[string]interface{}{
		"Title":      "Pending Updates: " + page.Title,
		"Card":       page,
		"Updates":    views,
		"CSRFToken":  getCSRFToken(w, r),
		"Message":    msg,
		"MissingUID": int(missingUID),
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

	// Apply the proposed content to the matching section. If the card no
	// longer has that section (renamed or removed since the update was
	// drafted), approving would throw the text away: keep the update and say
	// so instead.
	var sections []db.ReferenceSection
	if page.SectionsJSON != "" && page.SectionsJSON != "[]" {
		json.Unmarshal([]byte(page.SectionsJSON), &sections)
	}
	applied := false
	for i, s := range sections {
		if s.Key == approved.SectionKey {
			sections[i].Content = approved.ProposedContent
			applied = true
			break
		}
	}
	if !applied {
		http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/updates?error=missing_section&uid=%d", id, uid), http.StatusSeeOther)
		return
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

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/updates?msg=approved", id), http.StatusSeeOther)
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

	http.Redirect(w, r, fmt.Sprintf("/admin/cards/%d/updates?msg=rejected", id), http.StatusSeeOther)
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

	// Back goes to the list the article is on.
	backURL, backLabel, navTab := "/admin/dashboard", "Review", "review"
	if article.Status == db.StatusApproved {
		backURL, backLabel, navTab = "/admin/published", "Published", "published"
	}
	data := map[string]any{
		"Title":     "Linked cards",
		"Article":   article,
		"Linked":    linked,
		"Suggested": suggested,
		"CSRFToken": getCSRFToken(w, r),
		"Saved":     r.URL.Query().Get("msg") == "saved",
		"BackURL":   backURL,
		"BackLabel": backLabel,
		"NavTab":    navTab,
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
