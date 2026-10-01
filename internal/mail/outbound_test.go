package mail

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// mockSendCloser captures sent messages for verification. With failTo
// set, sending to that address fails.
type mockSendCloser struct {
	sent   []mockMessage
	failTo string
}

type mockMessage struct {
	from string
	to   []string
	raw  string // the message as it would go over SMTP
}

func (m *mockSendCloser) Send(from string, to []string, msg io.WriterTo) error {
	if m.failTo != "" && len(to) > 0 && to[0] == m.failTo {
		return fmt.Errorf("550 mailbox unavailable")
	}
	var buf bytes.Buffer
	if _, err := msg.WriteTo(&buf); err != nil {
		return err
	}
	m.sent = append(m.sent, mockMessage{from: from, to: to, raw: buf.String()})
	return nil
}

// parsed is a captured message read back the way a mail client would:
// headers unfolded, and the body of the part with the given content type
// decoded.
func (m mockMessage) parsed(t *testing.T) *netmail.Message {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(m.raw))
	if err != nil {
		t.Fatalf("reading captured message: %v", err)
	}
	return msg
}

// part returns the decoded body of the message's part of contentType
// ("text/plain" or "text/html"), whether the message is multipart or not.
func (m mockMessage) part(t *testing.T, contentType string) string {
	t.Helper()
	msg := m.parsed(t)
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	if !strings.HasPrefix(mt, "multipart/") {
		if mt != contentType {
			return ""
		}
		return decodeBody(t, msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return ""
		}
		if err != nil {
			t.Fatalf("reading part: %v", err)
		}
		if pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); pt == contentType {
			return decodeBody(t, p.Header.Get("Content-Transfer-Encoding"), p)
		}
	}
}

func decodeBody(t *testing.T, encoding string, r io.Reader) string {
	t.Helper()
	if strings.EqualFold(encoding, "quoted-printable") {
		r = quotedprintable.NewReader(r)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

func (m *mockSendCloser) Close() error { return nil }

func newTestSender() (*Sender, *mockSendCloser) {
	mock := &mockSendCloser{}
	s := &Sender{
		from:     "test@milesahead.com",
		smtpUser: "testuser",
		dialFn: func() (SendCloser, error) {
			return mock, nil
		},
	}
	return s, mock
}

func sampleArticle() *db.GeneratedArticle {
	return &db.GeneratedArticle{
		ID:          42,
		Title:       "Self-Driving Trucks Hit the Highway",
		Slug:        "self-driving-trucks",
		Content:     "Autonomous trucks are now operating on major US highways, marking a milestone in freight logistics.",
		Type:        "analysis",
		Status:      db.StatusPending,
		SourcesJSON: `[{"url":"https://example.com/trucks","title":"Truck News"}]`,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

func sampleVerification() *db.VerificationResult {
	return &db.VerificationResult{
		ID:                 1,
		GeneratedArticleID: 42,
		RevisionNumber:     1,
		Score:              85,
		IssuesJSON:         "[]",
		FactualCheck:       "pass",
		AttributionCheck:   "pass",
		CompletenessCheck:  "minor gaps",
		HallucinationCheck: "none detected",
		VerifiedAt:         time.Now(),
	}
}

func init() {
	// Point templates to the project root templates directory.
	templateDir = "../../templates/email"
}

// --- Template Rendering Tests ---

func TestRenderNotification_ContainsArticleTitle(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	if !strings.Contains(html, article.Title) {
		t.Error("notification HTML should contain article title")
	}
}

func TestRenderNotification_ContainsContent(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	if !strings.Contains(html, "Autonomous trucks") {
		t.Error("notification HTML should contain article content")
	}
}

// The admin's review email shows the article's editor notes, which the
// public never sees; an article without notes gets no notes block.
func TestRenderNotification_ShowsEditorNotes(t *testing.T) {
	article := sampleArticle()
	html, err := RenderNotification(article, sampleVerification(), "tok123", "milesahead.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "Editor notes") {
		t.Error("notes block shown for an article without notes")
	}

	article.EditorNotes = "Could not find a link for: the terms page"
	html, err = RenderNotification(article, sampleVerification(), "tok123", "milesahead.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "Editor notes (not published):") || !strings.Contains(html, "Could not find a link for: the terms page") {
		t.Error("notification should show the editor notes")
	}
}

func TestRenderNotification_ContainsVerificationScore(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	if !strings.Contains(html, "85/100") {
		t.Error("notification HTML should contain verification score")
	}
}

func TestRenderNotification_ContainsSourceLinks(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	if !strings.Contains(html, "https://example.com/trucks") {
		t.Error("notification HTML should contain source links")
	}
}

func TestRenderNotification_ContainsReplyInstructions(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	for _, keyword := range []string{"approve", "reject", "unpublish", "feedback"} {
		if !strings.Contains(html, keyword) {
			t.Errorf("notification HTML should contain reply action: %s", keyword)
		}
	}
}

func TestRenderNotification_ContainsTypeBadge(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	if !strings.Contains(html, "analysis") {
		t.Error("notification HTML should contain article type badge")
	}
}

func TestRenderNotification_ContainsVerificationChecks(t *testing.T) {
	article := sampleArticle()
	ver := sampleVerification()

	html, err := RenderNotification(article, ver, "tok123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderNotification failed: %v", err)
	}

	for _, check := range []string{"pass", "minor gaps", "none detected"} {
		if !strings.Contains(html, check) {
			t.Errorf("notification HTML should contain verification check: %s", check)
		}
	}
}

func TestRenderConfirmation_ContainsConfirmLink(t *testing.T) {
	html, _, err := RenderConfirmation("abc123", "milesahead.com", "")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}

	expected := "https://milesahead.com/subscribe/confirm?token=abc123"
	if !strings.Contains(html, expected) {
		t.Error("confirmation HTML should contain confirm link")
	}
}

func TestRenderConfirmation_ContainsUnsubscribeLink(t *testing.T) {
	html, _, err := RenderConfirmation("abc123", "milesahead.com", "")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}

	expected := "https://milesahead.com/unsubscribe?token=abc123"
	if !strings.Contains(html, expected) {
		t.Error("confirmation HTML should contain unsubscribe link")
	}
}

func TestRenderConfirmation_ContainsSubscriptionTitle(t *testing.T) {
	html, _, err := RenderConfirmation("abc123", "milesahead.com", "")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}

	if !strings.Contains(html, "Confirm your subscription") {
		t.Error("confirmation HTML should contain subscription title")
	}
}

// --- Sender Tests ---

func TestNewSender(t *testing.T) {
	cfg := config.SMTPConfig{
		Host:     "smtp.example.com",
		Port:     587,
		User:     "user@example.com",
		Password: "secret",
		From:     "noreply@example.com",
	}

	s := NewSender(cfg)
	if s.from != "noreply@example.com" {
		t.Errorf("expected from=%q, got %q", "noreply@example.com", s.from)
	}
	if s.smtpUser != "user@example.com" {
		t.Errorf("expected smtpUser=%q, got %q", "user@example.com", s.smtpUser)
	}
}

func TestSendAdminNotification(t *testing.T) {
	sender, mock := newTestSender()
	article := sampleArticle()
	ver := sampleVerification()

	err := sender.SendAdminNotification("admin@test.com", article, ver, "review-token-1")
	if err != nil {
		t.Fatalf("SendAdminNotification failed: %v", err)
	}

	if len(mock.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(mock.sent))
	}
	if mock.sent[0].to[0] != "admin@test.com" {
		t.Errorf("expected to=admin@test.com, got %s", mock.sent[0].to[0])
	}
}

func TestSendConfirmation(t *testing.T) {
	sender, mock := newTestSender()

	err := sender.SendConfirmation("subscriber@test.com", "conf-token", "milesahead.com")
	if err != nil {
		t.Fatalf("SendConfirmation failed: %v", err)
	}

	if len(mock.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(mock.sent))
	}
	if mock.sent[0].to[0] != "subscriber@test.com" {
		t.Errorf("expected to=subscriber@test.com, got %s", mock.sent[0].to[0])
	}
}

// The confirmation email goes as plain text and HTML, says what the reader
// signed up for and how often it comes, and links back absolutely: an email
// client has no base URL.
func TestSendConfirmation_Content(t *testing.T) {
	sender, mock := newTestSender()
	sender.SetNewsletterSchedule("every Saturday at 8:00 AM ET")

	if err := sender.SendConfirmation("subscriber@test.com", "conf-token", "milesahead.example"); err != nil {
		t.Fatal(err)
	}
	msg := mock.sent[0]
	if got := msg.parsed(t).Header.Get("Subject"); got != "Confirm your subscription to MilesAhead" {
		t.Errorf("subject = %q", got)
	}
	html, text := msg.part(t, "text/html"), msg.part(t, "text/plain")
	if html == "" || text == "" {
		t.Fatalf("want both an HTML and a plain-text part; html %d bytes, text %d bytes", len(html), len(text))
	}
	confirm := "https://milesahead.example/subscribe/confirm?token=conf-token"
	unsub := "https://milesahead.example/unsubscribe?token=conf-token"
	for name, body := range map[string]string{"html": html, "text": text} {
		for _, want := range []string{
			confirm, unsub,
			"One email, every Saturday at 8:00 AM ET.",
			"the ones about to end",
			"We only use your address to send the newsletter.",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s part should contain %q:\n%s", name, want, body)
			}
		}
	}
	if !strings.Contains(html, `href="`+confirm+`"`) {
		t.Errorf("the HTML button should link to %s", confirm)
	}
	if strings.Contains(html, `href="/`) {
		t.Error("the HTML has a relative link, which does not work in an email")
	}
}

// Without a schedule set, the email still says how often.
func TestRenderConfirmation_DefaultFrequency(t *testing.T) {
	html, text, err := RenderConfirmation("t", "milesahead.example", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{html, text} {
		if !strings.Contains(body, "One email, once a week.") {
			t.Errorf("want the default frequency in:\n%s", body)
		}
	}
}

// recipientsFor gives each address its own unsubscribe link.
func recipientsFor(emails ...string) []Recipient {
	out := make([]Recipient, len(emails))
	for i, e := range emails {
		out[i] = Recipient{Email: e, UnsubscribeURL: "https://milesahead.com/unsubscribe?token=tok-" + strings.Split(e, "@")[0]}
	}
	return out
}

// issueHTML is a rendered issue: the reader's link goes where the
// placeholder is.
const issueHTML = `<h1>News</h1><p><a href="` + UnsubscribeURLPlaceholder + `">Unsubscribe</a></p>`

func TestSendNewsletter(t *testing.T) {
	sender, mock := newTestSender()
	recipients := recipientsFor("a@test.com", "b@test.com", "c@test.com")

	sent, err := sender.SendNewsletter("Weekly Digest", issueHTML, recipients)
	if err != nil {
		t.Fatalf("SendNewsletter failed: %v", err)
	}
	if sent != 3 {
		t.Errorf("SendNewsletter reported %d sent, want 3", sent)
	}

	if len(mock.sent) != 3 {
		t.Fatalf("expected 3 sent messages, got %d", len(mock.sent))
	}
	for i, r := range recipients {
		if mock.sent[i].to[0] != r.Email {
			t.Errorf("message %d: expected to=%s, got %s", i, r.Email, mock.sent[i].to[0])
		}
	}
}

// Every copy carries its own reader's unsubscribe link, in the body and in
// the List-Unsubscribe headers a mail provider's Unsubscribe button uses
// (RFC 2369, RFC 8058), and never the shared placeholder or a bare
// /unsubscribe.
func TestSendNewsletter_PerRecipientUnsubscribe(t *testing.T) {
	sender, mock := newTestSender()
	sender.from = "MilesAhead <noreply@milesahead.com>"
	recipients := recipientsFor("a@test.com", "b@test.com")

	if _, err := sender.SendNewsletter("Weekly Digest", issueHTML, recipients); err != nil {
		t.Fatalf("SendNewsletter failed: %v", err)
	}
	if len(mock.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(mock.sent))
	}
	for i, r := range recipients {
		msg := mock.sent[i]
		body := msg.part(t, "text/html")
		if !strings.Contains(body, `href="`+r.UnsubscribeURL+`"`) {
			t.Errorf("%s: body should link to its own %s:\n%s", r.Email, r.UnsubscribeURL, body)
		}
		other := recipients[1-i].UnsubscribeURL
		if strings.Contains(msg.raw, other) {
			t.Errorf("%s: message carries the other reader's link %s", r.Email, other)
		}
		if strings.Contains(body, UnsubscribeURLPlaceholder) || strings.Contains(body, `/unsubscribe"`) {
			t.Errorf("%s: body still has the placeholder or a bare /unsubscribe:\n%s", r.Email, body)
		}
		h := msg.parsed(t).Header
		wantLU := "<" + r.UnsubscribeURL + ">, <mailto:noreply@milesahead.com?subject=unsubscribe>"
		if got := h.Get("List-Unsubscribe"); got != wantLU {
			t.Errorf("%s: List-Unsubscribe = %q, want %q", r.Email, got, wantLU)
		}
		if got := h.Get("List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
			t.Errorf("%s: List-Unsubscribe-Post = %q, want List-Unsubscribe=One-Click", r.Email, got)
		}
	}
}

// A copy that could not carry its reader's link is never sent: not when the
// issue has no place for it, nor when a recipient's link is missing or not
// https (a provider's one-click POST needs https). Nothing is dialled.
func TestSendNewsletter_RefusesWithoutUnsubscribeLink(t *testing.T) {
	cases := map[string]struct {
		html       string
		recipients []Recipient
	}{
		"no placeholder in the issue": {"<h1>News</h1>", recipientsFor("a@test.com")},
		"recipient without a link":    {issueHTML, []Recipient{{Email: "a@test.com"}}},
		"relative link":               {issueHTML, []Recipient{{Email: "a@test.com", UnsubscribeURL: "/unsubscribe?token=x"}}},
		"one bad among good":          {issueHTML, append(recipientsFor("a@test.com"), Recipient{Email: "b@test.com", UnsubscribeURL: "http://x/unsubscribe?token=y"})},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sender, mock := newTestSender()
			dialled := false
			sender.dialFn = func() (SendCloser, error) { dialled = true; return mock, nil }
			sent, err := sender.SendNewsletter("Weekly Digest", tc.html, tc.recipients)
			if err == nil || sent != 0 || dialled || len(mock.sent) != 0 {
				t.Errorf("SendNewsletter = %d, %v (dialled %v, %d sent); want 0, an error, nothing dialled", sent, err, dialled, len(mock.sent))
			}
		})
	}
}

func TestSendNewsletter_EmptyRecipients(t *testing.T) {
	sender, _ := newTestSender()

	sent, err := sender.SendNewsletter("Subject", issueHTML, nil)
	if err != nil || sent != 0 {
		t.Fatalf("SendNewsletter with empty recipients = %d, %v; want 0, nil", sent, err)
	}
}

// A send that fails partway says how many recipients it reached, so the
// caller can tell who already has the issue.
func TestSendNewsletter_FailsPartway(t *testing.T) {
	sender, mock := newTestSender()
	mock.failTo = "b@test.com"
	recipients := recipientsFor("a@test.com", "b@test.com", "c@test.com")

	sent, err := sender.SendNewsletter("Weekly Digest", issueHTML, recipients)
	if err == nil || !strings.Contains(err.Error(), "b@test.com") {
		t.Errorf("SendNewsletter error = %v, want one naming b@test.com", err)
	}
	if sent != 1 {
		t.Errorf("SendNewsletter reported %d sent, want 1", sent)
	}
	if len(mock.sent) != 1 || mock.sent[0].to[0] != "a@test.com" {
		t.Errorf("sent %v, want only a@test.com", mock.sent)
	}
}

// A dial failure reaches no one.
func TestSendNewsletter_DialFails(t *testing.T) {
	sender, _ := newTestSender()
	sender.dialFn = func() (SendCloser, error) { return nil, fmt.Errorf("dialing SMTP: refused") }
	sent, err := sender.SendNewsletter("Subject", issueHTML, recipientsFor("a@test.com"))
	if err == nil || sent != 0 {
		t.Errorf("SendNewsletter = %d, %v; want 0 and an error", sent, err)
	}
}

func TestSendVoteAlert(t *testing.T) {
	sender, mock := newTestSender()

	err := sender.SendVoteAlert("admin@test.com", 42, "Controversial Article")
	if err != nil {
		t.Fatalf("SendVoteAlert failed: %v", err)
	}

	if len(mock.sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(mock.sent))
	}
	if mock.sent[0].to[0] != "admin@test.com" {
		t.Errorf("expected to=admin@test.com, got %s", mock.sent[0].to[0])
	}
}

func TestSendAdminNotification_DialError(t *testing.T) {
	sender := &Sender{
		from: "test@milesahead.com",
		dialFn: func() (SendCloser, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	err := sender.SendAdminNotification("admin@test.com", sampleArticle(), sampleVerification(), "tok")
	if err == nil {
		t.Fatal("expected error on dial failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' in error, got: %v", err)
	}
}

func TestSetDialFunc(t *testing.T) {
	cfg := config.SMTPConfig{
		Host: "smtp.example.com",
		Port: 587,
		From: "test@example.com",
	}
	s := NewSender(cfg)

	mock := &mockSendCloser{}
	s.SetDialFunc(func() (SendCloser, error) {
		return mock, nil
	})

	err := s.SendVoteAlert("admin@test.com", 1, "Test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.sent) != 1 {
		t.Error("expected 1 message sent via injected dial function")
	}
}

func TestRenderConfirmation_IsBranded(t *testing.T) {
	html, _, err := RenderConfirmation("abc123", "milesahead.com", "")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}
	for _, want := range []string{
		`src="https://milesahead.com/static/img/brand/email-lockup.png"`,
		`src="https://milesahead.com/static/img/brand/email-band.png"`,
		"Thanks for signing up for the MilesAhead weekly email.",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("confirmation email should contain %s", want)
		}
	}
	if strings.Contains(html, "Miles Ahead") || strings.Contains(html, ".svg") {
		t.Error("confirmation email should use the one-word name and raster images only")
	}
}
