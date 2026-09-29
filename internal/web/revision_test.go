package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
)

// passingRevisionClient is a fake AI client whose rewrites pass the critic.
type passingRevisionClient struct{}

func (passingRevisionClient) Generate(model, prompt string, maxTokens int) (string, error) {
	if model == "critic-model" {
		return `{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`, nil
	}
	return "Revised Title\nRevised content with better details [Source]", nil
}

// useRevisingManager swaps in a manager whose pipeline actually rewrites, so a
// test can see whether a handler started the rewrite.
func useRevisingManager(app *App, database *db.Database) {
	client := passingRevisionClient{}
	pipeline := ai.NewPipeline(
		ai.NewGenerator(client, "gen-model", "", nil),
		ai.NewCritic(client, "critic-model"), 80)
	app.articleManager = article.NewManager(database, pipeline)
}

// TestRevisionRoutes_StartRewrite covers every admin route that asks for a
// revision. Only the Revise button used to start the rewrite; the two unpublish
// routes set revision_requested and stopped, leaving the article "Revising"
// indefinitely.
func TestRevisionRoutes_StartRewrite(t *testing.T) {
	tests := []struct {
		name   string
		status string
		path   string
	}{
		{"revise button", db.StatusPending, "/admin/articles/%d/revise"},
		{"unpublish with feedback", db.StatusApproved, "/admin/articles/%d/unpublish"},
		{"unpublish and revise", db.StatusApproved, "/admin/articles/%d/unpublish-revise"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, database := setupAdminTestApp(t)
			useRevisingManager(app, database)

			id := createPendingArticle(t, database, "Needs Work")
			if tt.status == db.StatusApproved {
				if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
					t.Fatalf("approve: %v", err)
				}
			}

			session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
			form := url.Values{}
			form.Set("feedback", "Add the annual fee")
			rec := doAdminPOST(t, app, fmt.Sprintf(tt.path, id), session, csrf, form)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}

			app.WaitBackground()
			a, err := database.GetGeneratedArticle(id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if a.Status != db.StatusPending || a.RevisionNumber != 2 {
				t.Errorf("after %s: status %q rev %d, want the rewrite applied (pending, rev 2)",
					tt.name, a.Status, a.RevisionNumber)
			}
		})
	}
}

// TestAdminDashboard_ShowsRevisionFailures renders the real dashboard template:
// "Revising..." must not look the same for a rewrite that is running, one that
// is retrying after failures, and one the reconciler has given up on.
func TestAdminDashboard_ShowsRevisionFailures(t *testing.T) {
	base, database := setupAdminTestApp(t)
	mgr := article.NewManager(database, nil)
	mgr.RevisionMaxFailures = 3
	app, err := NewAppWithTemplateDir(base.cfg, database, mgr, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)

	request := func(title string, failures int) {
		id := createPendingArticle(t, database, title)
		if err := mgr.RequestRevision(id, "fix", "admin"); err != nil {
			t.Fatalf("RequestRevision: %v", err)
		}
		for i := 0; i < failures; i++ {
			if _, err := database.RecordRevisionFailure(id, "provider returned 503",
				func(int) time.Time { return time.Now().Add(time.Hour) }); err != nil {
				t.Fatalf("record failure: %v", err)
			}
		}
	}
	request("Fresh Request", 0)
	request("Retrying Request", 1)
	request("Abandoned Request", 3)

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"failed 1&times;, retrying",
		"Revision failed 3 times",
		`title="provider returned 503"`,
	} {
		if !strings.Contains(body, want) && !strings.Contains(body, strings.ReplaceAll(want, "&times;", "×")) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if n := strings.Count(body, `class="status-badge revision-failed"`); n != 1 {
		t.Errorf("given-up badges = %d, want 1", n)
	}
}
