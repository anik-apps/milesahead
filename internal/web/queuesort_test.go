package web

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestParseSort(t *testing.T) {
	for in, want := range map[string]queueSort{
		"": sortNewest, "newest": sortNewest, "oldest": sortOldest,
		"score": sortScoreHigh, "score-low": sortScoreLow,
		"bogus": sortNewest, "NEWEST": sortNewest, "<script>": sortNewest,
	} {
		if got := parseSort(in, queueSorts); got != want {
			t.Errorf("parseSort(%q) = %q, want %q", in, got, want)
		}
	}
	// Published sorts by date only.
	if got := parseSort("score", publishedSorts); got != sortNewest {
		t.Errorf("published accepted sort=score: %q", got)
	}
	if got := parseSort("oldest", publishedSorts); got != sortOldest {
		t.Errorf("published sort=oldest = %q", got)
	}
}

func TestQueueSort_Less(t *testing.T) {
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	keys := []queueKey{
		{at: base, score: 80, hasScore: true, id: 1},
		{at: base.Add(time.Hour), score: 60, hasScore: true, id: 2},
		{at: base.Add(2 * time.Hour), id: 3}, // not scored yet
		{at: base.Add(2 * time.Hour), score: 80, hasScore: true, id: 4},
	}
	order := func(s queueSort) []int64 {
		ks := append([]queueKey{}, keys...)
		for i := 1; i < len(ks); i++ { // insertion sort: no reliance on sort.Slice
			for j := i; j > 0 && s.less(ks[j], ks[j-1]); j-- {
				ks[j], ks[j-1] = ks[j-1], ks[j]
			}
		}
		ids := make([]int64, len(ks))
		for i, k := range ks {
			ids[i] = k.id
		}
		return ids
	}
	for s, want := range map[queueSort][]int64{
		// Same time: the higher id (created later) first, or last.
		sortNewest: {4, 3, 2, 1},
		sortOldest: {1, 2, 3, 4},
		// Equal scores newest first; unscored last either way.
		sortScoreHigh: {4, 1, 2, 3},
		sortScoreLow:  {2, 4, 1, 3},
	} {
		if got := order(s); !equalIDs(got, want) {
			t.Errorf("%s: order %v, want %v", s, got, want)
		}
	}
}

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWithSort(t *testing.T) {
	for _, c := range []struct{ form, in, want string }{
		{"", "/admin/dashboard?msg=approved&id=1&open=0", "/admin/dashboard?msg=approved&id=1&open=0"},
		{"newest", "/admin/dashboard?msg=approved&id=1&open=0", "/admin/dashboard?msg=approved&id=1&open=0"},
		{"oldest", "/admin/dashboard?msg=approved&id=1&open=0", "/admin/dashboard?msg=approved&id=1&open=0&sort=oldest"},
		{"score-low", "/admin/dashboard", "/admin/dashboard?sort=score-low"},
		{"x&y=1", "/admin/dashboard", "/admin/dashboard"},
	} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"sort": {c.form}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if got := withSort(r, c.in, queueSorts); got != c.want {
			t.Errorf("withSort(sort=%q, %q) = %q, want %q", c.form, c.in, got, c.want)
		}
	}
}

// sortFixture is a queue with known times and scores: three ready articles
// created an hour apart (the middle one unscored) and two taken down at
// different times.
type sortFixture struct {
	oldReady, midReady, newReady int64 // created 3h, 2h, 1h ago
	downEarly, downLate          int64 // taken down 5h and 4h ago
}

func newSortFixture(t *testing.T, database *db.Database) sortFixture {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := database.DB().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	created := func(title string, hoursAgo int) int64 {
		id := createPendingArticle(t, database, title)
		exec(`UPDATE generated_articles SET created_at = datetime('now', ?), updated_at = datetime('now', ?) WHERE id = ?`,
			"-"+strconv.Itoa(hoursAgo)+" hours", "-"+strconv.Itoa(hoursAgo)+" hours", id)
		return id
	}
	score := func(id int64, s int) {
		if _, err := database.CreateVerificationResult(&db.VerificationResult{GeneratedArticleID: id, RevisionNumber: 1, Score: s,
			IssuesJSON: "[]", FactualCheck: "pass", AttributionCheck: "pass", CompletenessCheck: "pass", HallucinationCheck: "pass"}); err != nil {
			t.Fatal(err)
		}
	}
	f := sortFixture{
		oldReady: created("Sort Old Ready", 3),
		midReady: created("Sort Mid Ready", 2),
		newReady: created("Sort New Ready", 1),
	}
	score(f.oldReady, 90)
	score(f.newReady, 55)
	for _, d := range []struct {
		id    *int64
		title string
		hours int
	}{{&f.downEarly, "Sort Down Early", 5}, {&f.downLate, "Sort Down Late", 4}} {
		*d.id = createPendingArticle(t, database, d.title)
		database.UpdateArticleStatus(*d.id, db.StatusApproved)
		database.UpdateArticleStatus(*d.id, db.StatusUnpublished)
		exec(`UPDATE generated_articles SET updated_at = datetime('now', ?) WHERE id = ?`, "-"+strconv.Itoa(d.hours)+" hours", *d.id)
	}
	score(f.downEarly, 40)
	score(f.downLate, 75)
	return f
}

// rowOrder is the article ids of a section's rows, in page order.
func rowOrder(sec string) []int64 {
	var ids []int64
	for _, part := range strings.Split(sec, `class="q-row`)[1:] {
		i := strings.Index(part, `data-preview="preview-`)
		if i < 0 {
			continue
		}
		rest := part[i+len(`data-preview="preview-`):]
		n, _ := strconv.ParseInt(rest[:strings.IndexByte(rest, '"')], 10, 64)
		ids = append(ids, n)
	}
	return ids
}

// Integration, real templates: every sort orders the rows within each group
// (the groups stay where they are), and the previews' prev and next follow
// the page.
func TestDashboard_SortOrdersEachGroup(t *testing.T) {
	app, database := realTemplateApp(t)
	f := newSortFixture(t, database)
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))

	for _, c := range []struct {
		query         string
		ready, decide []int64
	}{
		{"", []int64{f.newReady, f.midReady, f.oldReady}, []int64{f.downLate, f.downEarly}},
		{"?sort=bogus", []int64{f.newReady, f.midReady, f.oldReady}, []int64{f.downLate, f.downEarly}},
		{"?sort=newest", []int64{f.newReady, f.midReady, f.oldReady}, []int64{f.downLate, f.downEarly}},
		{"?sort=oldest", []int64{f.oldReady, f.midReady, f.newReady}, []int64{f.downEarly, f.downLate}},
		{"?sort=score", []int64{f.oldReady, f.newReady, f.midReady}, []int64{f.downLate, f.downEarly}},
		{"?sort=score-low", []int64{f.newReady, f.oldReady, f.midReady}, []int64{f.downEarly, f.downLate}},
	} {
		body := doAdminGET(t, app, "/admin/dashboard"+c.query, session).Body.String()
		if got := rowOrder(section(body, "q-ready")); !equalIDs(got, c.ready) {
			t.Errorf("%q: ready order %v, want %v", c.query, got, c.ready)
		}
		if got := rowOrder(section(body, "q-decide")); !equalIDs(got, c.decide) {
			t.Errorf("%q: decide order %v, want %v", c.query, got, c.decide)
		}
		// Groups keep their places: decide, ready, rewriting.
		if d, r, w := strings.Index(body, `q-group q-decide`), strings.Index(body, `q-group q-ready`), strings.Index(body, `q-group q-rewriting`); !(d < r && r < w) {
			t.Errorf("%q: groups out of order", c.query)
		}
		// Prev and next step through the page as shown.
		page := append(append([]int64{}, c.decide...), c.ready...)
		for i, id := range page {
			next := ""
			if i+1 < len(page) {
				next = "preview-" + itoa(page[i+1])
			}
			prev := ""
			if i > 0 {
				prev = "preview-" + itoa(page[i-1])
			}
			want := `id="preview-` + itoa(id) + `" class="preview-overlay" data-id="` + itoa(id) + `" data-next="` + next + `" data-prev="` + prev + `"`
			if !strings.Contains(body, want) {
				t.Errorf("%q: preview of %d does not lead %q / %q", c.query, id, prev, next)
			}
		}
	}
}

// The Sort menu shows the current order (newest by default, also for an
// unknown value), the ready group says it, and every form posted from the
// page carries the sort so the list comes back the same way.
func TestDashboard_SortControlAndCopy(t *testing.T) {
	app, database := realTemplateApp(t)
	createPendingArticle(t, database, "Sorted Article")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))

	body := doAdminGET(t, app, "/admin/dashboard?sort=nonsense", session).Body.String()
	if !strings.Contains(body, `<select id="q-sort" name="sort" data-autosubmit>`) || !strings.Contains(body, `<option value="newest" selected>Newest first</option>`) {
		t.Error("no Sort menu with Newest first selected")
	}
	if strings.Contains(body, "Oldest first.") || !strings.Contains(body, "New articles and drafts, newest first.") {
		t.Error("the ready group's note still says oldest first")
	}

	body = doAdminGET(t, app, "/admin/dashboard?sort=score&all=1", session).Body.String()
	if !strings.Contains(body, `<option value="score" selected>Highest score</option>`) {
		t.Error("sort=score is not shown as selected")
	}
	if !strings.Contains(body, `<input type="hidden" name="all" value="1">`) {
		t.Error("changing the sort would drop Show all")
	}
	// Every action form on the page posts the sort: approve and reject in
	// the menu and the preview, the rewrite request.
	page := body[strings.Index(body, `<div class="admin-dashboard"`):]
	forms := strings.Count(page, `method="POST"`)
	if n := strings.Count(page, `<input type="hidden" name="sort" value="score">`); n != forms || forms < 4 {
		t.Errorf("%d of %d forms carry the sort", n, forms)
	}
}

func TestDashboard_ShowAllKeepsTheSort(t *testing.T) {
	app, database := realTemplateApp(t)
	for i := 0; i < readyLimit+1; i++ {
		createPendingArticle(t, database, "Capped "+strconv.Itoa(i))
	}
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard?sort=oldest", session).Body.String()
	if !strings.Contains(body, "Showing 25 of 26, oldest first.") || !strings.Contains(body, `href="/admin/dashboard?all=1&amp;sort=oldest"`) {
		t.Error("Show all loses the sort, or the note does not say the order")
	}
}

// After an action, the dashboard comes back in the order the admin chose.
func TestDecisions_RedirectKeepsTheSort(t *testing.T) {
	app, database := realTemplateApp(t)
	a := createPendingArticle(t, database, "Rejected In Order")
	b := createPendingArticle(t, database, "Stale In Order")
	if err := database.UpdateArticleContent(b, "Stale In Order", "<p>v2</p>", 2); err != nil {
		t.Fatal(err)
	}
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(a)+"/reject", session, csrf, url.Values{"revision": {"1"}, "sort": {"score-low"}})
	if want := "/admin/dashboard?msg=rejected&id=" + itoa(a) + "&open=0&sort=score-low"; rec.Header().Get("Location") != want {
		t.Errorf("reject: Location %q, want %q", rec.Header().Get("Location"), want)
	}
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(b)+"/approve", session, csrf, url.Values{"revision": {"1"}, "sort": {"oldest"}})
	if want := "/admin/dashboard?msg=changed&id=" + itoa(b) + "&rev=2&sort=oldest"; rec.Header().Get("Location") != want {
		t.Errorf("stale approve: Location %q, want %q", rec.Header().Get("Location"), want)
	}
	rec = doAdminPOST(t, app, "/admin/articles/"+itoa(b)+"/revise", session, csrf, url.Values{"feedback": {"tighter"}, "sort": {"made-up"}})
	if loc := rec.Header().Get("Location"); strings.Contains(loc, "sort=") {
		t.Errorf("an unknown sort leaked into the redirect: %q", loc)
	}
}
