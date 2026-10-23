package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/health"
)

type fakeModelHealth struct {
	snap    health.Snapshot
	testErr error
	tested  int
}

func (f *fakeModelHealth) Snapshot() health.Snapshot { return f.snap }
func (f *fakeModelHealth) SendTestAlert() error      { f.tested++; return f.testErr }

var oct20 = time.Date(2026, 10, 20, 1, 8, 0, 0, time.UTC)

func criticDown() health.Snapshot {
	return health.Snapshot{
		Steps: []health.StepView{{
			Step:  health.Step{Name: health.StepCritic, Label: "Critic", Uses: "Verifying every article", StopsPublishing: true},
			State: health.StepDown, Since: oct20,
			Models: []health.ModelView{
				{Model: "nvidia/nemotron-3-super-120b-a12b", State: health.ModelRetired, FailingSince: oct20, LastError: "404 Not Found"},
				{Model: "moonshotai/kimi-k3", State: health.ModelRetired, FailingSince: oct20, LastError: "<b>410</b> end of life"},
			},
		}},
		Publishing: health.Publishing{Stopped: true, Reason: "no critic model answers", Since: oct20, LastArticle: oct20},
		NotOK:      1, Stopped: true, CheckEvery: time.Hour, LastCheck: oct20,
		NotEmailed: "no address: set the ALERT_EMAIL repository secret",
	}
}

func TestModelsPage_ShowsWhatIsBrokenAndWhatToDo(t *testing.T) {
	app, _ := realTemplateApp(t)
	app.SetModelHealth(&fakeModelHealth{snap: criticDown()})
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))

	body := doAdminGET(t, app, "/admin/models", session).Body.String()

	for _, want := range []string{
		"Publishing has stopped", "no critic model answers",
		"nvidia/nemotron-3-super-120b-a12b", "retired",
		"Pick a replacement",
		"not emailed", "ALERT_EMAIL",
		`action="/admin/models/test-alert"`,
		`<span class="tab-n alert" title="1 model lists need a look">1</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "<b>410</b>") {
		t.Error("error text is not escaped")
	}
}

func TestModelsPage_WithoutHealth(t *testing.T) {
	app, _ := realTemplateApp(t)
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	rec := doAdminGET(t, app, "/admin/models", session)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Model health is not running") {
		t.Errorf("code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestSendTestAlert(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "/admin/models?msg=test_sent"},
		{errors.New("550"), "/admin/models?error=test_failed"},
		{health.ErrNotEmailed, "/admin/models?error=not_emailed"},
	} {
		app, _ := realTemplateApp(t)
		fake := &fakeModelHealth{testErr: tc.err}
		app.SetModelHealth(fake)
		session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
		rec := doAdminPOST(t, app, "/admin/models/test-alert", session, csrf, url.Values{})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != tc.want || fake.tested != 1 {
			t.Errorf("err %v: code %d location %q tested %d", tc.err, rec.Code, rec.Header().Get("Location"), fake.tested)
		}
	}
}

func TestSendTestAlert_NeedsCSRF(t *testing.T) {
	app, _ := realTemplateApp(t)
	app.SetModelHealth(&fakeModelHealth{})
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	req, _ := http.NewRequest("POST", "/admin/models/test-alert", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session", Value: session})
	rec := serve(app, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("code %d, want 403 without a CSRF token", rec.Code)
	}
}

func TestReview_BannerWhilePublishingIsStopped(t *testing.T) {
	app, _ := realTemplateApp(t)
	app.SetModelHealth(&fakeModelHealth{snap: criticDown()})
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	// The nav tab links to /admin/models on every page, so look for the
	// banner's own markup.
	const banner = `class="message error" role="alert">Publishing has stopped`
	at := strings.Index(body, banner)
	if at < 0 {
		t.Fatal("Review has no banner while publishing is stopped")
	}
	// Below the page header, not squeezed into its flex row.
	if tools := strings.Index(body, `id="kbd-help"`); tools < 0 || at < tools {
		t.Error("the banner sits inside the page header")
	}

	app.SetModelHealth(&fakeModelHealth{snap: health.Snapshot{CheckEvery: time.Hour, LastCheck: oct20}})
	body = doAdminGET(t, app, "/admin/dashboard", session).Body.String()
	if strings.Contains(body, `class="message error" role="alert">Publishing has stopped`) ||
		strings.Contains(body, "Model health alerts are not emailed") {
		t.Error("Review shows the banner while models are healthy")
	}
}

func serve(app *App, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}
