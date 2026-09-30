package web

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/db"
)

// previewModal returns the markup of one article's preview modal on the
// dashboard, so assertions cannot match another article's modal.
func previewModal(t *testing.T, body string, id int64) string {
	t.Helper()
	open := `<div id="preview-` + strconv.FormatInt(id, 10) + `" class="preview-overlay"`
	start := strings.Index(body, open)
	if start < 0 {
		t.Fatalf("no preview modal for article %d", id)
	}
	rest := body[start+len(open):]
	if end := strings.Index(rest, `class="preview-overlay"`); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

// assertInOrder fails unless each of wants appears in s after the previous one.
func assertInOrder(t *testing.T, label, s string, wants ...string) {
	t.Helper()
	pos := 0
	for _, want := range wants {
		i := strings.Index(s[pos:], want)
		if i < 0 {
			t.Errorf("%s: missing %q (in order)", label, want)
			return
		}
		pos += i + len(want)
	}
}

// Integration, real templates: the dashboard preview shows each article's
// revision history as a conversation -- the admin's requests, oldest first,
// each answered by the rewrite that followed it or by what became of it.
// Feedback text is escaped, an article without feedback gets no thread, and
// none of it reaches the public article page.
func TestAdminPreview_RevisionThread(t *testing.T) {
	base, database := setupAdminTestApp(t)
	mgr := article.NewManager(database, nil)
	mgr.RevisionMaxFailures = 3
	app, err := NewAppWithTemplateDir(base.cfg, database, mgr, nil, nil, nil,
		context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)

	feedback := func(id int64, text, source string) int64 {
		t.Helper()
		fid, err := database.CreateAdminFeedback(&db.AdminFeedback{GeneratedArticleID: id, FeedbackText: text, Source: source})
		if err != nil {
			t.Fatalf("CreateAdminFeedback: %v", err)
		}
		return fid
	}
	fail := func(id int64, times int) {
		t.Helper()
		for i := 0; i < times; i++ {
			if _, err := database.RecordRevisionFailure(id, "provider returned 503",
				func(int) time.Time { return time.Now().Add(time.Hour) }); err != nil {
				t.Fatalf("record failure: %v", err)
			}
		}
	}

	// Two requests: the first answered by revision 2 (scored by the critic),
	// the second still being rewritten.
	answered := createPendingArticle(t, database, "Answered Then Pending")
	first := feedback(answered, "Add the annual fee <script>alert(1)</script>", "admin")
	const newContent = "<p>one two three four five</p><p>six</p>"
	if _, err := database.CreateArticleRevision(&db.ArticleRevision{
		GeneratedArticleID: answered, RevisionNumber: 2,
		PreviousContent: "<p>one two three</p>", NewContent: newContent, FeedbackID: first,
	}); err != nil {
		t.Fatalf("CreateArticleRevision: %v", err)
	}
	if _, err := database.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: answered, RevisionNumber: 2, Score: 88, IssuesJSON: "[]",
		FactualCheck: "pass", AttributionCheck: "fail", CompletenessCheck: "pass", HallucinationCheck: "pass",
	}); err != nil {
		t.Fatalf("CreateVerificationResult: %v", err)
	}
	if err := database.UpdateArticleContent(answered, "Answered Then Pending", newContent, 2); err != nil {
		t.Fatalf("UpdateArticleContent: %v", err)
	}
	if err := mgr.RequestRevision(answered, "Mention the lounge access", "email"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}

	// A superseded request, then one whose rewrite keeps failing.
	retrying := createPendingArticle(t, database, "Retrying Article")
	feedback(retrying, "Shorter intro", "admin")
	if err := mgr.RequestRevision(retrying, "Actually, cut the intro", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	fail(retrying, 1)

	gaveUp := createPendingArticle(t, database, "Given Up Article")
	if err := mgr.RequestRevision(gaveUp, "Fix the dates", "admin"); err != nil {
		t.Fatalf("RequestRevision: %v", err)
	}
	fail(gaveUp, 3)

	untouched := createPendingArticle(t, database, "No Feedback Article")

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/dashboard", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Fatal("feedback text rendered unescaped")
	}

	modal := previewModal(t, body, answered)
	assertInOrder(t, "answered article", modal,
		// Collapsed by default: a <details> without the open attribute.
		`<details class="admin-revision-section">`,
		`<summary><h4>Revision history</h4><span class="revision-count">2 requests</span></summary>`,
		"The full prompt given to the model is not stored.",
		`revision-msg--request`, `>Admin</span>`,
		"Add the annual fee &lt;script&gt;alert(1)&lt;/script&gt;",
		`revision-msg--response`, `>Revision 2</span>`, "Critic 88/100",
		"3 &rarr; 6 words", "failed checks: attribution",
		`<summary>Revised article</summary>`, newContent,
		`<summary>Previous version</summary>`, "<p>one two three</p>",
		`revision-msg--request`, `>Email</span>`, "Mention the lounge access",
		"Rewriting&hellip;",
	)
	if strings.Contains(modal, "<details open") || strings.Contains(modal, `<details class="revision-content" open`) {
		t.Error("revision content should be collapsed by default")
	}

	assertInOrder(t, "retrying article", previewModal(t, body, retrying),
		"Shorter intro", "Superseded by a later request",
		"Actually, cut the intro", "Failed 1&times;: provider returned 503 &mdash; retrying",
	)
	assertInOrder(t, "given-up article", previewModal(t, body, gaveUp),
		"Fix the dates", "Gave up after 3 tries &mdash; request it again", "Last error: provider returned 503",
	)
	if m := previewModal(t, body, untouched); strings.Contains(m, "revision-thread") || strings.Contains(m, "Revision history") {
		t.Error("article with no feedback should render no revision thread")
	}

	// Published, the article with revisions shows none of the thread.
	if err := database.UpdateArticleStatus(answered, db.StatusApproved); err != nil {
		t.Fatalf("approve: %v", err)
	}
	a, err := database.GetGeneratedArticle(answered)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	page := getPage(t, app, "/article/"+a.Slug)
	if !strings.Contains(page, "one two three four five") {
		t.Fatal("public page did not render the article")
	}
	for _, leak := range []string{"revision-thread", "revision-msg", "Revision history", "Mention the lounge access", "Add the annual fee"} {
		if strings.Contains(page, leak) {
			t.Errorf("public article contains admin-only %q", leak)
		}
	}
}

func TestHTMLWordCount(t *testing.T) {
	for in, want := range map[string]int{
		"":                           0,
		"<p>one</p><p>two</p>":       2,
		"<p>one two</p>\n<h3>x</h3>": 3,
		`<a href="a b c">link</a>`:   1,
		"plain  text here":           3,
	} {
		if got := htmlWordCount(in); got != want {
			t.Errorf("htmlWordCount(%q) = %d, want %d", in, got, want)
		}
	}
}
