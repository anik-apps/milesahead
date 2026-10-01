package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestSafeAdminNext(t *testing.T) {
	for raw, want := range map[string]string{
		"/admin/cards":              "/admin/cards",
		"/admin/cards/3/edit?x=1":   "/admin/cards/3/edit?x=1",
		"/admin/published#row-4":    "/admin/published",
		"":                          "",
		"//evil.example":            "",
		"https://evil.example/":     "",
		"/admin//evil.example":      "",
		"/\\evil.example":           "",
		"/admin/\\evil.example":     "",
		"/admin/../cards":           "",
		"/admin/%2e%2e/%2e%2e/x":    "",
		"/admin/login":              "",
		"/admin/login?next=/admin/": "",
		"/admin/logout":             "",
		"javascript:alert(1)":       "",
		"admin/cards":               "",
		"/administrator":            "",
		"/admin/cards\r\nX: y":      "",
		"/":                         "",
	} {
		if got := safeAdminNext(raw); got != want {
			t.Errorf("safeAdminNext(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestExpiredSession_GETReturnsToThePage(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/cards/3/edit?tab=flags", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "expired-or-unknown"})
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rec.Code)
	}
	want := "/admin/login?next=" + url.QueryEscape("/admin/cards/3/edit?tab=flags")
	if loc := rec.Header().Get("Location"); loc != want {
		t.Errorf("Location %q, want %q", loc, want)
	}
}

// A POST cannot be replayed after login; the admin goes back to the page the
// form was on (when it was this site's admin), told the session expired.
func TestExpiredSession_POSTReturnsToTheFormsPage(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	post := func(referer string) string {
		req := httptest.NewRequest(http.MethodPost, "/admin/articles/7/revise",
			strings.NewReader(url.Values{"feedback": {"lead with the fee"}, "csrf_token": {"tok"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: "tok"})
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status %d", rec.Code)
		}
		return rec.Header().Get("Location")
	}
	if loc := post("http://example.com/admin/published?page=2"); loc != "/admin/login?expired=1&next="+url.QueryEscape("/admin/published?page=2") {
		t.Errorf("same-site referer: Location %q", loc)
	}
	for _, ref := range []string{"https://evil.example/admin/published", "", "http://example.com/article/x"} {
		if loc := post(ref); loc != "/admin/login?expired=1" {
			t.Errorf("referer %q: Location %q, want no next", ref, loc)
		}
	}
}

func TestLogin_ReturnsToNextOnlyWithinAdmin(t *testing.T) {
	app, _ := setupAdminTestApp(t)
	login := func(next, password string) *httptest.ResponseRecorder {
		get := httptest.NewRecorder()
		app.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
		var csrf string
		for _, c := range get.Result().Cookies() {
			if c.Name == "csrf_token" {
				csrf = c.Value
			}
		}
		form := url.Values{"password": {password}, "csrf_token": {csrf}, "next": {next}}
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "csrf_token", Value: csrf})
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	if loc := login("/admin/cards/3/edit", "testpassword123").Header().Get("Location"); loc != "/admin/cards/3/edit" {
		t.Errorf("Location %q, want the page before the session expired", loc)
	}
	for _, evil := range []string{"//evil.example", "https://evil.example", "/\\evil.example", "/admin/../../x"} {
		if loc := login(evil, "testpassword123").Header().Get("Location"); loc != "/admin/dashboard" {
			t.Errorf("next %q: Location %q, want /admin/dashboard", evil, loc)
		}
	}
}

// Integration, real templates: the login page carries next through a wrong
// password, and says the session expired.
func TestLoginPage_KeepsNextAndExplainsExpiry(t *testing.T) {
	app, _ := realTemplateApp(t)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login?expired=1&next="+url.QueryEscape("/admin/published"), nil))
	body := rec.Body.String()
	if !strings.Contains(body, `name="next" value="/admin/published"`) {
		t.Error("login form does not carry next")
	}
	if !strings.Contains(body, "Your session expired") {
		t.Error("login page does not say the session expired")
	}
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/login?next="+url.QueryEscape("https://evil.example"), nil))
	if strings.Contains(rec.Body.String(), `name="next"`) {
		t.Error("login form carries an unsafe next")
	}
}

// Integration, real templates: the feedback boxes are kept in the browser
// (admin.js) so an expired session does not lose them.
func TestDashboard_FeedbackBoxesAreKeptAsDrafts(t *testing.T) {
	app, database := realTemplateApp(t)
	id := createPendingArticle(t, database, "Draft Me")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if !strings.Contains(body, `data-draft="revise-`) || !strings.Contains(body, "/static/js/admin.js") {
		t.Error("dashboard feedback is not kept as a draft")
	}
	rec := doAdminPOST(t, app, "/admin/articles/"+itoa(id)+"/revise", session, "", url.Values{"feedback": {"shorter"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "sent=revise-"+itoa(id)) {
		t.Errorf("after sending, Location %q should clear the draft", loc)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
