package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// The review queue's groups, in the order the dashboard shows them.
const (
	groupDecide    = "decide"    // stuck until the admin acts
	groupReady     = "ready"     // pending and drafts, ready to review
	groupRewriting = "rewriting" // a rewrite is due, running or retrying
)

// readyLimit caps the "Ready to review" group, which carries a full preview
// per article; the page says how many more there are and links to all.
const readyLimit = 25

// undoWindow is how long after approving the dashboard's Undo still works.
// The toast offering it hides after 15 seconds; the server allows a little
// more for a slow click.
const undoWindow = 30 * time.Second

// dashboardGroup is one section of the review queue.
type dashboardGroup struct {
	Key, Title, Note string
	Rows             []adminArticleRow
	// Total is how many articles are in the group; Rows may hold fewer
	// (see readyLimit), and then the page says so.
	Total int
}

// Capped reports whether the group shows fewer rows than it has.
func (g dashboardGroup) Capped() bool { return len(g.Rows) < g.Total }

// queueExtras is what the queue rows need beyond the article itself, loaded
// for all rows at once: rewrite retry state, latest critic result and source
// posts, by article id.
type queueExtras struct {
	pending       map[int64]db.PendingRevision
	verifications map[int64]db.VerificationResult
	sources       map[int64][]db.LinkedSource
}

// buildArticleRow turns an article into a queue row with everything the row
// and its preview show.
func (app *App) buildArticleRow(a db.GeneratedArticle, x queueExtras, now time.Time) adminArticleRow {
	pending := x.pending
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
		Age:            ago(a.CreatedAt, now),
		createdAtRaw:   a.CreatedAt,
		updatedAtRaw:   a.UpdatedAt,
	}
	row.Enriched = a.ImageURL != "" && a.ScoutsTake != ""

	if a.Status == db.StatusRevisionRequested {
		p := pending[a.ID]
		row.RevisionFailures = p.Failures
		row.RevisionLastError = p.LastError
		row.RevisionGaveUp = p.Failures > 0 && app.articleManager != nil && app.articleManager.RevisionGaveUp(p.Failures)
		switch {
		case row.RevisionGaveUp:
		case p.Failures > 0:
			if wait := p.RetryAfter.Sub(now); wait > time.Minute {
				row.RetryNote = fmt.Sprintf("Failed %d×, retrying in %d min", p.Failures, int(wait.Minutes()+0.5))
			} else {
				row.RetryNote = fmt.Sprintf("Failed %d×, retrying now", p.Failures)
			}
		default:
			row.RetryNote = "Rewriting, requested " + ago(a.UpdatedAt, now)
		}
	}

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

	if v, ok := x.verifications[a.ID]; ok {
		row.VerificationScore = v.Score
		row.HasScore = true
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
		for _, c := range []string{v.FactualCheck, v.AttributionCheck, v.CompletenessCheck, v.HallucinationCheck} {
			if c != "pass" {
				row.FailedChecks++
			}
		}
	}
	row.ScoreClass = app.scoreClass(row.VerificationScore, row.HasScore)

	for _, ls := range x.sources[a.ID] {
		row.Sources = append(row.Sources, adminSourceInfo{Name: ls.SourceName, Title: ls.Title, URL: ls.URL})
	}
	row.SourceCount = len(row.Sources)
	return row
}

// scoreClass colours a critic score against the publishing threshold: "ok"
// at or above it, "warn" within 10 points below, "bad" further below.
func (app *App) scoreClass(score int, has bool) string {
	if !has {
		return "none"
	}
	threshold := app.cfg.AI.CriticThreshold
	if threshold <= 0 {
		threshold = 70
	}
	switch {
	case score >= threshold:
		return "ok"
	case score >= threshold-10:
		return "warn"
	default:
		return "bad"
	}
}

// reviewQueue loads every article waiting on the admin and sorts it into the
// dashboard's groups. With all false the ready group is capped at
// readyLimit.
func (app *App) reviewQueue(all bool) []dashboardGroup {
	x := queueExtras{pending: map[int64]db.PendingRevision{}}
	if list, err := app.db.ListPendingRevisions(); err == nil {
		for _, p := range list {
			x.pending[p.ID] = p
		}
	} else {
		slog.Warn("dashboard: could not load revision retry state", "error", err)
	}

	now := time.Now()
	groups := map[string]*dashboardGroup{
		groupDecide: {Key: groupDecide, Title: "Needs a decision",
			Note: "Rewrites that gave up, and articles taken down. Nothing happens until you act."},
		groupReady: {Key: groupReady, Title: "Ready to review",
			Note: "Oldest first."},
		groupRewriting: {Key: groupRewriting, Title: "Being rewritten",
			Note: "Waiting on a rewrite; they move to Ready when it lands. You can still approve or reject what is shown."},
	}
	var ready, other []db.GeneratedArticle
	for _, status := range db.QueueStatuses {
		batch, err := app.db.ListArticlesByStatus(status)
		if err != nil {
			slog.Warn("dashboard: could not list articles", "status", status, "error", err)
			continue
		}
		for _, a := range batch {
			if a.Status == db.StatusPending || a.Status == db.StatusDraft {
				ready = append(ready, a)
			} else {
				other = append(other, a)
			}
		}
	}

	// Ready: oldest first, so nothing waits forever behind new arrivals.
	sort.Slice(ready, func(i, j int) bool { return ready[i].CreatedAt.Before(ready[j].CreatedAt) })
	groups[groupReady].Total = len(ready)
	if !all && len(ready) > readyLimit {
		ready = ready[:readyLimit]
	}

	// One query each for the critic results and sources of every row shown.
	ids := make([]int64, 0, len(ready)+len(other))
	for _, a := range append(append([]db.GeneratedArticle{}, other...), ready...) {
		ids = append(ids, a.ID)
	}
	var err error
	if x.verifications, err = app.db.LatestVerifications(ids); err != nil {
		slog.Warn("dashboard: could not load critic results", "error", err)
	}
	if x.sources, err = app.db.LinkedSourcesForArticles(ids); err != nil {
		slog.Warn("dashboard: could not load sources", "error", err)
	}

	for _, a := range other {
		row := app.buildArticleRow(a, x, now)
		if a.Status == db.StatusUnpublished || row.RevisionGaveUp {
			row.Group = groupDecide
		} else {
			row.Group = groupRewriting
		}
		groups[row.Group].Rows = append(groups[row.Group].Rows, row)
	}
	for _, a := range ready {
		row := app.buildArticleRow(a, x, now)
		row.Group = groupReady
		groups[groupReady].Rows = append(groups[groupReady].Rows, row)
	}

	byUpdated := func(rows []adminArticleRow) {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].updatedAtRaw.Before(rows[j].updatedAtRaw) })
	}
	byUpdated(groups[groupDecide].Rows)
	byUpdated(groups[groupRewriting].Rows)
	groups[groupDecide].Total = len(groups[groupDecide].Rows)
	groups[groupRewriting].Total = len(groups[groupRewriting].Rows)

	out := []dashboardGroup{*groups[groupDecide], *groups[groupReady], *groups[groupRewriting]}

	// Prev and next follow the order on the page, across groups, so the
	// preview can step through the whole queue.
	var order []*adminArticleRow
	for g := range out {
		app.attachRevisionHistory(out[g].Rows)
		for i := range out[g].Rows {
			order = append(order, &out[g].Rows[i])
		}
	}
	for i, row := range order {
		row.Position = i + 1
		row.QueueLen = len(order)
		if i > 0 {
			row.PrevID = order[i-1].ID
		}
		if i+1 < len(order) {
			row.NextID = order[i+1].ID
		}
	}
	return out
}

// dashboardMessages are the banners for ?msg= after an action. "approved"
// is shown as the undo toast instead.
var dashboardMessages = map[string]string{
	"rejected":           "Article rejected.",
	"revision_submitted": "Rewrite requested. It moves to Ready to review when the new version lands, usually within a minute.",
	"undone":             "Approval undone: the article is back in the queue and no longer on the site.",
	"undo_expired":       "Too late to undo that approval. Use Unpublish on the Published page to take it down.",
}

func (app *App) handleAdminDashboard(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	groups := app.reviewQueue(all)
	total := 0
	for _, g := range groups {
		total += g.Total
	}
	version, err := app.db.QueueFingerprint()
	if err != nil {
		slog.Warn("dashboard: queue fingerprint", "error", err)
	}

	msg := r.URL.Query().Get("msg")
	data := map[string]interface{}{
		"Title":        "Review",
		"Groups":       groups,
		"Total":        total,
		"StateVersion": version,
		"CSRFToken":    getCSRFToken(w, r),
		"Message":      msg,
		"MessageText":  dashboardMessages[msg],
		"MessageID":    queryInt(r, "id"),
		"MessageRev":   queryInt(r, "rev"),
		"OpenID":       queryInt(r, "open"),
		"UndoPrev":     undoablePrev(r.URL.Query().Get("prev")),
		"PersonaName":  app.cfg.Site.PersonaName,
	}
	if msg == "approved" {
		if id := queryInt(r, "id"); id > 0 {
			if a, err := app.db.GetGeneratedArticle(id); err == nil {
				data["ApprovedTitle"] = a.Title
				data["ApprovedSlug"] = a.Slug
			}
		}
	}
	app.renderTemplate(w, "dashboard.html", data)
}

// undoablePrev is prev if an approval can be undone back to it, else "".
func undoablePrev(prev string) string {
	switch prev {
	case db.StatusPending, db.StatusDraft, db.StatusUnpublished, db.StatusRevisionRequested:
		return prev
	}
	return ""
}

// handleDashboardState answers the dashboard's poll with the queue's
// fingerprint; the page compares it with the one it was rendered with.
func (app *App) handleDashboardState(w http.ResponseWriter, r *http.Request) {
	version, err := app.db.QueueFingerprint()
	if err != nil {
		http.Error(w, "could not read the queue", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"version": version})
}

// handleUndoApprove is the toast's Undo: it puts an article approved moments
// ago back to the status it was approved from.
func (app *App) handleUndoApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid article ID", http.StatusBadRequest)
		return
	}
	prev := undoablePrev(r.FormValue("prev"))
	if prev == "" {
		http.Error(w, "Nothing to undo", http.StatusBadRequest)
		return
	}
	if err := app.articleManager.UndoApprove(id, prev, undoWindow); err != nil {
		if errors.Is(err, db.ErrUndoExpired) {
			http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=undo_expired&id=%d", id), http.StatusSeeOther)
			return
		}
		http.Error(w, fmt.Sprintf("Failed to undo: %v", err), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/dashboard?msg=undone&id=%d&open=%d", id, id), http.StatusSeeOther)
}

// nextParam is the "next" form value as an article id for ?open=, or 0.
func nextParam(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.FormValue("next"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// queryInt is the query parameter name as a positive integer, or 0. Banner
// text built from the URL only ever shows numbers.
func queryInt(r *http.Request, name string) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
