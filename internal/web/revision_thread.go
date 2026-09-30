package web

import (
	"log/slog"
	"strings"
	"unicode"

	"github.com/kumaranik/milesahead/internal/db"
)

// revisionRequest is one piece of admin feedback in the preview's revision
// history, with the rewrite(s) that answered it.
type revisionRequest struct {
	Source    string // "Admin", "Email", ...
	CreatedAt string
	Text      string // shown escaped: it is user input
	Responses []revisionResponse
	// Pending says what happened to a request no rewrite answered: one of
	// the revisionPending* values. Failures and LastError go with retrying
	// and gave_up.
	Pending   string
	Failures  int
	LastError string
}

// Why a request has no rewrite.
const (
	revisionPendingRewriting  = "rewriting"  // the latest request; a rewrite is due or running
	revisionPendingRetrying   = "retrying"   // rewrites have failed, still retrying
	revisionPendingGaveUp     = "gave_up"    // the retry budget ran out
	revisionPendingSuperseded = "superseded" // a later request replaced it
	revisionPendingNone       = "none"       // the article left revision_requested unrevised
)

// revisionResponse is one stored rewrite.
type revisionResponse struct {
	Number      int
	CreatedAt   string
	HasScore    bool
	Score       int
	FailedCheck []string // critic checks that did not pass
	WordsBefore int
	WordsAfter  int
	NewContent  string // generated HTML, rendered as the preview renders content
	OldContent  string
}

// attachRevisionHistory fills RevisionThread for the dashboard rows, with one
// query each for feedback, revisions and verification results across all rows.
// A load failure leaves the history out; the rest of the dashboard still works.
func (app *App) attachRevisionHistory(rows []adminArticleRow) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	feedback, err := app.db.ListFeedbackForArticles(ids)
	if err != nil {
		slog.Warn("dashboard: could not load revision feedback", "error", err)
		return
	}
	revisions, err := app.db.ListRevisionsForArticles(ids)
	if err != nil {
		slog.Warn("dashboard: could not load revisions", "error", err)
		return
	}
	verifications, err := app.db.ListVerificationsForArticles(ids)
	if err != nil {
		slog.Warn("dashboard: could not load revision scores", "error", err)
		verifications = nil // show the history without scores
	}
	for i := range rows {
		rows[i].RevisionThread = buildRevisionThread(&rows[i], feedback[rows[i].ID], revisions[rows[i].ID], verifications[rows[i].ID])
	}
}

// buildRevisionThread pairs each piece of feedback (oldest first) with the
// rewrites that answered it. Every stored rewrite names its feedback, so a
// rewrite whose feedback is not in the list cannot occur and is not shown.
func buildRevisionThread(row *adminArticleRow, feedback []db.AdminFeedback, revisions []db.ArticleRevision, scores map[int]db.VerificationResult) []revisionRequest {
	if len(feedback) == 0 {
		return nil
	}
	byFeedback := map[int64][]revisionResponse{}
	for _, r := range revisions {
		resp := revisionResponse{
			Number:      r.RevisionNumber,
			CreatedAt:   r.CreatedAt.Format(adminTimeFormat),
			WordsBefore: htmlWordCount(r.PreviousContent),
			WordsAfter:  htmlWordCount(r.NewContent),
			NewContent:  r.NewContent,
			OldContent:  r.PreviousContent,
		}
		if v, ok := scores[r.RevisionNumber]; ok {
			resp.HasScore = true
			resp.Score = v.Score
			for _, c := range []struct{ name, result string }{
				{"factual", v.FactualCheck},
				{"attribution", v.AttributionCheck},
				{"completeness", v.CompletenessCheck},
				{"hallucination", v.HallucinationCheck},
			} {
				if c.result != "" && c.result != "pass" {
					resp.FailedCheck = append(resp.FailedCheck, c.name)
				}
			}
		}
		byFeedback[r.FeedbackID] = append(byFeedback[r.FeedbackID], resp)
	}

	// The rewrite always answers the newest feedback by id; the list is in id
	// order, so that is the last one.
	latest := feedback[len(feedback)-1].ID
	thread := make([]revisionRequest, 0, len(feedback))
	for _, f := range feedback {
		req := revisionRequest{
			Source:    feedbackSourceLabel(f.Source),
			CreatedAt: f.CreatedAt.Format(adminTimeFormat),
			Text:      f.FeedbackText,
			Responses: byFeedback[f.ID],
		}
		if len(req.Responses) == 0 {
			switch {
			case f.ID != latest:
				req.Pending = revisionPendingSuperseded
			case row.Status != db.StatusRevisionRequested:
				req.Pending = revisionPendingNone
			case row.RevisionGaveUp:
				req.Pending = revisionPendingGaveUp
				req.Failures, req.LastError = row.RevisionFailures, row.RevisionLastError
			case row.RevisionFailures > 0:
				req.Pending = revisionPendingRetrying
				req.Failures, req.LastError = row.RevisionFailures, row.RevisionLastError
			default:
				req.Pending = revisionPendingRewriting
			}
		}
		thread = append(thread, req)
	}
	return thread
}

// feedbackSourceLabel turns admin_feedback.source ("admin", "email") into a
// label.
func feedbackSourceLabel(source string) string {
	if source == "" {
		return "Admin"
	}
	return strings.ToUpper(source[:1]) + source[1:]
}

// htmlWordCount counts the words in generated HTML. Tags count as spaces, so
// "</p><p>" does not glue two words together.
func htmlWordCount(s string) int {
	words, inTag, inWord := 0, false, false
	for _, r := range s {
		switch {
		case r == '<':
			inTag, inWord = true, false
		case r == '>':
			inTag = false
		case inTag:
		case unicode.IsSpace(r):
			inWord = false
		case !inWord:
			inWord = true
			words++
		}
	}
	return words
}
