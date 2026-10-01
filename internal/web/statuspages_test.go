package web

import (
	"bytes"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

// do sends a request through the app and returns the recorder.
func do(t *testing.T, app *App, method, target, form string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if form != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	return rec
}

const noindexMeta = `<meta name="robots" content="noindex, nofollow">`

// --- 404, 410 and 500 ---

func TestNotFound_BrandedPage(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	rec := do(t, app, http.MethodGet, "/no-such-page", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<nav class="site-nav"`, "find that page", `action="/search"`, noindexMeta, "<title>Page not found - MilesAhead</title>"} {
		if !strings.Contains(body, want) {
			t.Errorf("404 page should contain %s", want)
		}
	}
	if strings.Contains(body, "404 page not found") {
		t.Error("the bare Go 404 text is still being served")
	}
}

func TestNotFound_ArticleSlugSuggests(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	_, slug := createApprovedArticle(t, app.db, "Sapphire Reserve Points Boost Explained", "summary", false)
	createPublishedCard(t, app, "chase-sapphire-reserve", "Chase Sapphire Reserve")

	rec := do(t, app, http.MethodGet, "/article/chase-sapphire-reserve-old-offer-x1y2z3", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/article/`+slug+`"`) {
		t.Error("expected the matching story as a suggestion")
	}
	if !strings.Contains(body, `href="/cards/chase-sapphire-reserve"`) {
		t.Error("expected the matching card as a suggestion")
	}
	if !strings.Contains(body, `value="chase sapphire reserve old offer"`) {
		t.Error("the search box should be filled with the slug's words, without the random suffix")
	}
}

func TestNotFound_CardSlugSuggests(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, "american-express-gold-card", "American Express Gold Card")
	rec := do(t, app, http.MethodGet, "/cards/amex-gold", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `href="/cards/american-express-gold-card"`) {
		t.Error("expected the Gold card as a suggestion for /cards/amex-gold")
	}
}

// A draft is 404 and a taken-down story 410, and neither page carries its
// headline anywhere, <title> and og:title included.
func TestArticle_NotPublicHasNoHeadline(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	pendingID, err := app.db.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Secret Draft Headline", Slug: "secret-draft-headline-abc123", Type: "deal",
		Content: "<p>Draft body.</p>", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil || pendingID == 0 {
		t.Fatal(err)
	}
	id, slug := createApprovedArticle(t, app.db, "Pulled Story Headline", "deal", false)
	if err := app.db.UpdateArticleStatus(id, db.StatusUnpublished); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		path, headline string
		status         int
	}{
		{"/article/secret-draft-headline-abc123", "Secret Draft Headline", http.StatusNotFound},
		{"/article/" + slug, "Pulled Story Headline", http.StatusGone},
	} {
		rec := do(t, app, http.MethodGet, c.path, "")
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
		}
		body := rec.Body.String()
		if strings.Contains(body, c.headline) {
			t.Errorf("%s: the page shows the headline %q", c.path, c.headline)
		}
		if !strings.Contains(body, noindexMeta) {
			t.Errorf("%s: missing noindex", c.path)
		}
	}
}

func TestServerError_HidesTheError(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	app.templates["broken.html"] = template.Must(template.New("broken.html").Parse(`{{define "broken.html"}}half a page{{index .Items 5}}{{end}}`))
	rec := httptest.NewRecorder()
	app.renderTemplate(rec, "broken.html", map[string]interface{}{"Items": []int{}})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Template error") || strings.Contains(body, "out of range") || strings.Contains(body, "half a page") {
		t.Errorf("the reader should not see the Go error:\n%s", body)
	}
	if !strings.Contains(body, "Something went wrong") || !strings.Contains(body, `<nav class="site-nav"`) {
		t.Error("expected the branded 500 page")
	}
}

func TestServerError_DatabaseFailure(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	app.db.Close()
	rec := do(t, app, http.MethodGet, "/", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sql:") || strings.Contains(body, "Failed to load") {
		t.Errorf("the reader should not see the database error:\n%s", body)
	}
}

// --- Newsletter flow ---

// fakeMailer records confirmation emails. With fail set, every send
// fails with it (and is not recorded).
type fakeMailer struct {
	mu      sync.Mutex
	sent    []string
	domains []string
	fail    error
}

func (f *fakeMailer) SendConfirmation(to, token, domain string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, to+" "+token)
	f.domains = append(f.domains, domain)
	return nil
}

func (f *fakeMailer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// newsletterApp is the real templates with a fake mailer and a settable clock.
func newsletterApp(t *testing.T) (*App, *fakeMailer, *time.Time) {
	t.Helper()
	app := setupRealTemplateCardApp(t)
	m := &fakeMailer{}
	app.SetConfirmationMailer(m)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	app.now = func() time.Time { return now }
	return app, m, &now
}

func subscribe(t *testing.T, app *App, email string) *httptest.ResponseRecorder {
	t.Helper()
	rec := do(t, app, http.MethodPost, "/subscribe", "email="+email)
	app.WaitBackground()
	return rec
}

func TestSubscribePage_Form(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	rec := do(t, app, http.MethodGet, "/subscribe", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /subscribe: status %d, want 200 (it used to be 405)", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`<label for="subscribe-email">`, `autocomplete="email"`, `type="email"`, `name="website"`, noindexMeta} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-up page should contain %s", want)
		}
	}
}

// New, pending and already-active addresses all get the same redirect, so the
// form does not reveal who is subscribed.
func TestSubscribe_SameAnswerForEveryAddress(t *testing.T) {
	app, _, _ := newsletterApp(t)
	if _, err := app.db.CreateSubscriber(&db.Subscriber{Email: "active@example.com", Token: "tok-active"}); err != nil {
		t.Fatal(err)
	}
	if err := app.db.ConfirmSubscriber("tok-active"); err != nil {
		t.Fatal(err)
	}

	answer := func(rec *httptest.ResponseRecorder) string {
		return strconv.Itoa(rec.Code) + " " + rec.Header().Get("Location") + " " + rec.Body.String()
	}
	newAddr := answer(subscribe(t, app, "new@example.com"))
	pending := answer(subscribe(t, app, "new@example.com"))
	active := answer(subscribe(t, app, "active@example.com"))
	if newAddr != pending || newAddr != active {
		t.Errorf("answers differ:\nnew:     %q\npending: %q\nactive:  %q", newAddr, pending, active)
	}
	if !strings.HasPrefix(newAddr, "303 /subscribe/check-email") {
		t.Errorf("want a 303 to /subscribe/check-email, got %q", newAddr)
	}

	page := do(t, app, http.MethodGet, "/subscribe/check-email", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Check your inbox") || !strings.Contains(page.Body.String(), noindexMeta) {
		t.Error("the check-email page should load, say \"Check your inbox\" and be noindex")
	}
}

// A pending address gets one confirmation email per cooldown; asking again
// later resends it. An active address gets none.
func TestSubscribe_ResendsConfirmationAfterCooldown(t *testing.T) {
	app, mailer, now := newsletterApp(t)

	subscribe(t, app, "Reader@Example.com")
	if mailer.count() != 1 {
		t.Fatalf("want 1 confirmation email, got %d", mailer.count())
	}
	if !strings.HasPrefix(mailer.sent[0], "reader@example.com ") {
		t.Errorf("the address should be stored lower-cased, got %q", mailer.sent[0])
	}
	subscribe(t, app, "reader@example.com")
	if mailer.count() != 1 {
		t.Errorf("a second request inside the cooldown should not send again; got %d emails", mailer.count())
	}
	*now = now.Add(confirmationCooldown + time.Minute)
	subscribe(t, app, "reader@example.com")
	if mailer.count() != 2 {
		t.Errorf("after the cooldown the confirmation should be resent; got %d emails", mailer.count())
	}

	subs, _ := app.db.ListAllSubscribers()
	if len(subs) != 1 {
		t.Fatalf("want one subscriber row, got %d", len(subs))
	}
	if err := app.db.ConfirmSubscriber(subs[0].Token); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	subscribe(t, app, "reader@example.com")
	if mailer.count() != 2 {
		t.Errorf("an active subscriber should not be sent a confirmation; got %d emails", mailer.count())
	}
}

// confirmationCount reads milesahead_emails_sent_total{kind="confirmation",outcome}.
func confirmationCount(t *testing.T, outcome string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.EmailsSentTotal.WithLabelValues(metrics.EmailConfirmation, outcome).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// lockedBuffer is a log destination the background send can write to while
// the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A sent confirmation is counted. A failed one is logged and counted, and
// gives back its claim, so the reader can ask again at once rather than wait
// out the cooldown for an email that never came.
func TestSubscribe_ConfirmationOutcomesCounted(t *testing.T) {
	app, mailer, _ := newsletterApp(t)
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	sent0, failed0 := confirmationCount(t, metrics.EmailSent), confirmationCount(t, metrics.EmailFailed)
	subscribe(t, app, "ok@example.com")
	if mailer.count() != 1 || confirmationCount(t, metrics.EmailSent)-sent0 != 1 {
		t.Fatalf("a sent confirmation should count once as sent (sent %d)", mailer.count())
	}
	if mailer.domains[0] != app.siteDomain() {
		t.Errorf("confirmation built on domain %q, want the site's %q", mailer.domains[0], app.siteDomain())
	}

	mailer.mu.Lock()
	mailer.fail = errors.New("dialing SMTP: connection refused")
	mailer.mu.Unlock()
	subscribe(t, app, "fails@example.com")
	if d := confirmationCount(t, metrics.EmailFailed) - failed0; d != 1 {
		t.Errorf("failed confirmations counted %v, want 1", d)
	}
	if !strings.Contains(logs.String(), "sending the confirmation email failed") || !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("the failure should be logged with its cause:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "fails@example.com") {
		t.Errorf("the log should identify the subscriber by id, not address:\n%s", logs.String())
	}

	// The claim was released: the next try sends straight away.
	mailer.mu.Lock()
	mailer.fail = nil
	mailer.mu.Unlock()
	subscribe(t, app, "fails@example.com")
	if mailer.count() != 2 {
		t.Errorf("after a failed send the reader's retry should send at once; %d sent", mailer.count())
	}
}

func TestSubscribe_InvalidAddressIs400(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	rec := do(t, app, http.MethodPost, "/subscribe", "email=not-an-address")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "valid email address") || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, `value="not-an-address"`) {
		t.Error("the form should come back with the error, the field marked invalid and the input kept")
	}
}

func TestSubscribe_HoneypotIsIgnored(t *testing.T) {
	app, mailer, _ := newsletterApp(t)
	rec := do(t, app, http.MethodPost, "/subscribe", "email=bot@example.com&website=http://spam.example")
	app.WaitBackground()
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d, want the usual 303", rec.Code)
	}
	if subs, _ := app.db.ListAllSubscribers(); len(subs) != 0 || mailer.count() != 0 {
		t.Error("a filled-in honeypot should store and send nothing")
	}
}

func TestSubscribe_RateLimited(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	var last int
	for i := 0; i <= subscribeMaxAttempts; i++ {
		last = do(t, app, http.MethodPost, "/subscribe", "email=a"+strconv.Itoa(i)+"@example.com").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("attempt %d: status %d, want 429", subscribeMaxAttempts+1, last)
	}
}

// Confirming twice (or a mail scanner opening the link first) is fine.
func TestConfirm_Idempotent(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	if _, err := app.db.CreateSubscriber(&db.Subscriber{Email: "c@example.com", Token: "tok-c"}); err != nil {
		t.Fatal(err)
	}
	first := do(t, app, http.MethodGet, "/subscribe/confirm?token=tok-c", "")
	second := do(t, app, http.MethodGet, "/subscribe/confirm?token=tok-c", "")
	if first.Code != 200 || !strings.Contains(first.Body.String(), "Your subscription is confirmed") {
		t.Errorf("first click: %d, want the confirmed page", first.Code)
	}
	if second.Code != 200 || !strings.Contains(second.Body.String(), "already subscribed") {
		t.Errorf("second click: %d, want the already-subscribed page", second.Code)
	}
	if strings.Contains(second.Body.String(), "expired") {
		t.Error("the second click should not mention an expired link")
	}
	if rec := do(t, app, http.MethodGet, "/subscribe/confirm?token=nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: status %d, want 404", rec.Code)
	}
	if rec := do(t, app, http.MethodGet, "/subscribe/confirm", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("no token: status %d, want 400", rec.Code)
	}
	for _, rec := range []*httptest.ResponseRecorder{first, second} {
		if !strings.Contains(rec.Body.String(), noindexMeta) {
			t.Error("confirmation pages should be noindex")
		}
	}
}

// Opening the Unsubscribe link asks; only the POST unsubscribes. Afterwards
// the same link shows "You're unsubscribed" with Resubscribe, which works,
// and an old confirmation link does not quietly resubscribe anyone.
func TestUnsubscribe_ConfirmThenResubscribe(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	if _, err := app.db.CreateSubscriber(&db.Subscriber{Email: "reader@example.com", Token: "tok-u"}); err != nil {
		t.Fatal(err)
	}
	if err := app.db.ConfirmSubscriber("tok-u"); err != nil {
		t.Fatal(err)
	}

	ask := do(t, app, http.MethodGet, "/unsubscribe?token=tok-u", "")
	if ask.Code != 200 {
		t.Fatalf("GET: status %d", ask.Code)
	}
	body := ask.Body.String()
	if !strings.Contains(body, `<form method="POST" action="/unsubscribe"`) || !strings.Contains(body, `name="token" value="tok-u"`) {
		t.Error("the page should have a button that POSTs the token")
	}
	if !strings.Contains(body, "r•••@example.com") {
		t.Error("the page should show the masked address")
	}
	if subs, _ := app.db.ListConfirmedSubscribers(); len(subs) != 1 {
		t.Fatal("GET must not unsubscribe")
	}

	post := do(t, app, http.MethodPost, "/unsubscribe", "token=tok-u")
	if post.Code != http.StatusSeeOther || post.Header().Get("Location") != "/unsubscribe?token=tok-u" {
		t.Fatalf("POST: %d %q, want 303 to the link's page", post.Code, post.Header().Get("Location"))
	}
	if subs, _ := app.db.ListConfirmedSubscribers(); len(subs) != 0 {
		t.Fatal("POST should unsubscribe")
	}
	done := do(t, app, http.MethodGet, "/unsubscribe?token=tok-u", "").Body.String()
	if !strings.Contains(done, "unsubscribed") || !strings.Contains(done, `action="/resubscribe"`) {
		t.Error("after unsubscribing, the link should show the unsubscribed page with Resubscribe")
	}
	if strings.Contains(done, `action="/subscribe"`) {
		t.Error("the unsubscribed page should not pitch the sign-up form")
	}

	old := do(t, app, http.MethodGet, "/subscribe/confirm?token=tok-u", "")
	if old.Code != 200 || !strings.Contains(old.Body.String(), `action="/resubscribe"`) {
		t.Error("an old confirmation link should offer Resubscribe, not resubscribe silently")
	}
	if subs, _ := app.db.ListConfirmedSubscribers(); len(subs) != 0 {
		t.Fatal("an old confirmation link must not undo an unsubscribe")
	}

	re := do(t, app, http.MethodPost, "/resubscribe", "token=tok-u")
	if re.Code != http.StatusSeeOther {
		t.Fatalf("resubscribe: status %d, want 303", re.Code)
	}
	if subs, _ := app.db.ListConfirmedSubscribers(); len(subs) != 1 {
		t.Fatal("resubscribe should make the subscription active again")
	}
	welcome := do(t, app, http.MethodGet, re.Header().Get("Location"), "")
	if welcome.Code != 200 || !strings.Contains(welcome.Body.String(), "subscribed again") {
		t.Errorf("after resubscribing: %d, want the welcome-back page", welcome.Code)
	}
}

// A mail provider's one-click unsubscribe (RFC 8058) is a POST to the
// List-Unsubscribe URL with List-Unsubscribe=One-Click in the body.
func TestUnsubscribe_OneClickPost(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	if _, err := app.db.CreateSubscriber(&db.Subscriber{Email: "o@example.com", Token: "tok-o"}); err != nil {
		t.Fatal(err)
	}
	if err := app.db.ConfirmSubscriber("tok-o"); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodPost, "/unsubscribe?token=tok-o", "List-Unsubscribe=One-Click")
	if rec.Code != http.StatusOK {
		t.Errorf("one-click: status %d, want 200", rec.Code)
	}
	if subs, _ := app.db.ListConfirmedSubscribers(); len(subs) != 0 {
		t.Error("one-click POST should unsubscribe")
	}
	if rec := do(t, app, http.MethodPost, "/unsubscribe?token=nope", "List-Unsubscribe=One-Click"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: status %d, want 404", rec.Code)
	}
}

// Newsletters already sent link to /unsubscribe with no token. That page
// explains and gives a way out instead of a subscribe form.
func TestUnsubscribe_NoTokenExplains(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	app.cfg.Mail.SMTP.From = "MilesAhead <news@example.com>"
	rec := do(t, app, http.MethodGet, "/unsubscribe", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="mailto:news@example.com?subject=Unsubscribe"`) {
		t.Error("the page should offer an email to unsubscribe by hand")
	}
	if strings.Contains(body, `action="/subscribe"`) || strings.Contains(body, "Subscribe to MilesAhead") {
		t.Error("an unsubscribe page should not offer a subscribe form")
	}
	if rec := do(t, app, http.MethodGet, "/unsubscribe?token=nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: status %d, want 404", rec.Code)
	}
}

// Someone who unsubscribed and signs up again through the form becomes
// pending and is sent a confirmation, like a new address.
func TestSubscribe_AfterUnsubscribeNeedsConfirmation(t *testing.T) {
	app, mailer, _ := newsletterApp(t)
	if _, err := app.db.CreateSubscriber(&db.Subscriber{Email: "back@example.com", Token: "tok-b"}); err != nil {
		t.Fatal(err)
	}
	if err := app.db.Unsubscribe("tok-b"); err != nil {
		t.Fatal(err)
	}
	subscribe(t, app, "back@example.com")
	s, err := app.db.GetSubscriberByEmail("back@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if s.State() != db.SubscriberPending || mailer.count() != 1 {
		t.Errorf("state %q, %d emails; want pending and one confirmation", s.State(), mailer.count())
	}
}

// A failing database is a 500, not a "not found" page: only a missing row
// means the card or story does not exist.
func TestServerError_LookupFailureIsNot404(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	createPublishedCard(t, app, "amex-gold", "American Express Gold Card")
	_, slug := createApprovedArticle(t, app.db, "A Story That Exists", "deal", false)
	app.db.Close()
	for _, path := range []string{"/cards/amex-gold", "/article/" + slug} {
		rec := do(t, app, http.MethodGet, path, "")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("%s with the database down: status %d, want 500", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Something went wrong") || !strings.Contains(body, `<nav class="site-nav"`) {
			t.Errorf("%s: expected the branded 500 page", path)
		}
		if strings.Contains(body, "sql:") || strings.Contains(body, "not found") {
			t.Errorf("%s: the page should neither show the error nor claim the page is missing", path)
		}
	}
}
