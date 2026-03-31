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

	"github.com/kumaranik/milesahead/internal/db"
)

// adminSourceInfo holds source article info for admin display.
type adminSourceInfo struct {
	Name  string
	Title string
	URL   string
}

// adminCriticReport holds the critic's verification details.
type adminCriticReport struct {
	Score            int
	Issues           []string
	FactualCheck     string
	AttributionCheck string
	CompletenessCheck string
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
	createdAtRaw      time.Time // for sorting only, not exposed to template
}

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
			CreatedAt:      a.CreatedAt.Format("Jan 2, 2006 3:04 PM"),
			UpdatedAt:      a.UpdatedAt.Format("Jan 2, 2006 3:04 PM"),
			createdAtRaw:   a.CreatedAt,
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

	data := map[string]interface{}{
		"Title":     "Dashboard",
		"Articles":  rows,
		"CSRFToken": getCSRFToken(w, r),
		"Message":   r.URL.Query().Get("msg"),
	}
	app.renderTemplate(w,"dashboard.html", data)
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
	app.renderTemplate(w,"published.html", data)
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

	// Trigger AI regeneration immediately in the background
	go func() {
		if err := app.articleManager.ProcessRevision(id); err != nil {
			slog.Error("revision processing failed", "article_id", id, "error", err)
		}
	}()

	http.Redirect(w, r, "/admin/dashboard?msg=revision_submitted", http.StatusSeeOther)
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
	app.renderTemplate(w,"sources.html", data)
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
	app.renderTemplate(w,"subscribers.html", data)
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
			"GeneratorModel":  app.cfg.AI.GeneratorModel,
			"CriticModel":     app.cfg.AI.CriticModel,
			"HFAPIToken":      maskString(app.cfg.AI.HFAPIToken),
			"CriticThreshold": app.cfg.AI.CriticThreshold,
			"MaxRetries":      app.cfg.AI.MaxRetries,
			"CacheTTL":        app.cfg.AI.CacheTTL,
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
	app.renderTemplate(w,"settings.html", data)
}

// maskString fully masks a sensitive string, revealing no content.
func maskString(s string) string {
	if s == "" {
		return ""
	}
	return "********"
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

	if app.newsletterSvc != nil {
		html, err := app.newsletterSvc.PreviewNext()
		if err != nil {
			noContent = true
		} else {
			previewHTML = html
		}
	} else {
		noContent = true
	}

	// Fetch newsletter history.
	var history []db.Newsletter
	if newsletters, err := app.db.ListNewsletters(); err == nil {
		history = newsletters
	}

	data := map[string]interface{}{
		"Title":       "Newsletter",
		"PreviewHTML": previewHTML,
		"NoContent":   noContent,
		"History":     history,
		"Success":     successMsg,
		"Error":       errorMsg,
		"CSRFToken":   getCSRFToken(w, r),
	}
	app.renderTemplate(w, "newsletter.html", data)
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
		Title:    strings.TrimSpace(r.FormValue("title")),
		Slug:     strings.TrimSpace(r.FormValue("slug")),
		Category: strings.TrimSpace(r.FormValue("category")),
		Issuer:   strings.TrimSpace(r.FormValue("issuer")),
		ImageURL: strings.TrimSpace(r.FormValue("image_url")),
		Status:   r.FormValue("status"),
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

	// Initialize empty JSON arrays.
	page.RecentChangesJSON = "[]"
	page.PendingUpdatesJSON = "[]"

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

	personaName := app.cfg.Site.PersonaName

	data := map[string]interface{}{
		"Title":       "Edit: " + page.Title,
		"IsNew":       false,
		"Card":        page,
		"Sections":    sections,
		"Keywords":    strings.Join(keywords, ", "),
		"FormAction":  fmt.Sprintf("/admin/cards/%d/edit", page.ID),
		"PersonaName": personaName,
		"CSRFToken":   getCSRFToken(w, r),
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
	page.Status = r.FormValue("status")
	page.ScoutsVerdict = r.FormValue("scouts_verdict")

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
