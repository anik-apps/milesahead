package newsletter

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/metrics"
	dto "github.com/prometheus/client_model/go"
)

// captureSMTP stands in for the SMTP connection and keeps every message as
// it would go over the wire.
type captureSMTP struct{ raw []string }

func (c *captureSMTP) Send(from string, to []string, msg io.WriterTo) error {
	var buf bytes.Buffer
	if _, err := msg.WriteTo(&buf); err != nil {
		return err
	}
	c.raw = append(c.raw, buf.String())
	return nil
}

func (c *captureSMTP) Close() error { return nil }

// subscribeTwo adds two confirmed readers with tokens tok-a and tok-b.
func subscribeTwo(t *testing.T, database *db.Database) {
	t.Helper()
	for _, s := range []struct{ email, token string }{{"a@example.com", "tok-a"}, {"b@example.com", "tok-b"}} {
		if _, err := database.CreateSubscriber(&db.Subscriber{Email: s.email, Token: s.token}); err != nil {
			t.Fatal(err)
		}
		if err := database.ConfirmSubscriber(s.token); err != nil {
			t.Fatal(err)
		}
	}
}

// readMessage parses a captured message and decodes its HTML body.
func readMessage(t *testing.T, raw string) (netmail.Header, string) {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if mt, _, _ := mime.ParseMediaType(msg.Header.Get("Content-Type")); mt != "text/html" {
		t.Fatalf("content type %q, want text/html", mt)
	}
	var body io.Reader = msg.Body
	if strings.EqualFold(msg.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
		body = quotedprintable.NewReader(body)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header, string(b)
}

// The newsletter as it goes out, through the real template and SMTP sender:
// each reader's copy links to their own /unsubscribe?token=, in the body and
// in List-Unsubscribe, and none has the bare /unsubscribe that could not tell
// readers apart.
func TestSendDaily_EachReaderGetsTheirOwnUnsubscribeLink(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	seed(t, database)
	subscribeTwo(t, database)

	smtp := &captureSMTP{}
	sender := mail.NewSender(config.SMTPConfig{From: "MilesAhead <noreply@milesahead.example>"})
	sender.SetDialFunc(func() (mail.SendCloser, error) { return smtp, nil })
	svc := NewServiceWithAI(database, sender, config.NewsletterConfig{Timezone: "America/New_York"}, nil, nil, "milesahead.example")

	if err := svc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	if len(smtp.raw) != 2 {
		t.Fatalf("sent %d messages, want 2", len(smtp.raw))
	}
	links := map[string]bool{}
	for _, raw := range smtp.raw {
		h, body := readMessage(t, raw)
		to := h.Get("To")
		token := map[string]string{"a@example.com": "tok-a", "b@example.com": "tok-b"}[to]
		own := "https://milesahead.example/unsubscribe?token=" + token
		if token == "" {
			t.Fatalf("message to unexpected %q", to)
		}
		if !strings.Contains(body, `href="`+own+`"`) {
			t.Errorf("%s: the body's Unsubscribe link is not their own %s", to, own)
		}
		if strings.Contains(body, `/unsubscribe"`) || strings.Contains(body, mail.UnsubscribeURLPlaceholder) {
			t.Errorf("%s: the body has a bare /unsubscribe or the placeholder", to)
		}
		if got, want := h.Get("List-Unsubscribe"), "<"+own+">"; got != want {
			t.Errorf("%s: List-Unsubscribe = %q, want %q", to, got, want)
		}
		if got := h.Get("List-Unsubscribe-Post"); got != "List-Unsubscribe=One-Click" {
			t.Errorf("%s: List-Unsubscribe-Post = %q", to, got)
		}
		links[own] = true
	}
	if len(links) != 2 {
		t.Errorf("the two readers got %d distinct links, want 2", len(links))
	}

	// The copy kept for the admin has no reader's token and no placeholder.
	nl, err := database.GetNewsletterByDate(svc.todayDate())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(nl.HTMLContent, "tok-") || strings.Contains(nl.HTMLContent, mail.UnsubscribeURLPlaceholder) {
		t.Error("the stored copy should have the plain /unsubscribe link, not a reader's or the placeholder")
	}
}

// The weekly hands the sender the issue with the placeholder and each
// reader's own link.
func TestSendWeekly_PassesEachReadersUnsubscribeLink(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	_, live, _ := seed(t, database)
	subscribeTwo(t, database)

	sender := &mockSender{}
	svc := NewServiceWithAI(database, sender, config.NewsletterConfig{Timezone: "America/New_York"}, nil, nil, "milesahead.example")
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved", ArticleCount: 1, ScoutsPickArticleID: &live,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SendWeekly(); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 {
		t.Fatalf("want one send, got %d", len(sender.calls))
	}
	call := sender.calls[0]
	if !strings.Contains(call.html, mail.UnsubscribeURLPlaceholder) {
		t.Error("the issue should hold the placeholder for each reader's link")
	}
	got := map[string]string{}
	for _, r := range call.recipients {
		got[r.Email] = r.UnsubscribeURL
	}
	want := map[string]string{
		"a@example.com": "https://milesahead.example/unsubscribe?token=tok-a",
		"b@example.com": "https://milesahead.example/unsubscribe?token=tok-b",
	}
	if len(got) != 2 || got["a@example.com"] != want["a@example.com"] || got["b@example.com"] != want["b@example.com"] {
		t.Errorf("recipients = %v, want %v", got, want)
	}
}

// failingSender reaches reach recipients, then fails.
type failingSender struct{ reach int }

func (f failingSender) SendNewsletter(_, _ string, _ []mail.Recipient) (int, error) {
	return f.reach, errors.New("421 try later")
}

// Every send outcome is counted: one "sent" per reader reached, one "failed"
// per send that stops.
func TestSendOnce_CountsOutcomes(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	seed(t, database)
	subscribeTwo(t, database)

	sent0, failed0 := emailCount(t, metrics.EmailSent), emailCount(t, metrics.EmailFailed)

	svc := NewService(database, failingSender{reach: 1}, config.NewsletterConfig{Timezone: "UTC"})
	var partial *PartialSendError
	if err := svc.SendDaily(); !errors.As(err, &partial) {
		t.Fatalf("SendDaily() = %v, want a partial send", err)
	}
	if d := emailCount(t, metrics.EmailSent) - sent0; d != 1 {
		t.Errorf("sent counted %v, want 1", d)
	}
	if d := emailCount(t, metrics.EmailFailed) - failed0; d != 1 {
		t.Errorf("failed counted %v, want 1", d)
	}
}

// emailCount reads milesahead_emails_sent_total{kind="newsletter",outcome}.
func emailCount(t *testing.T, outcome string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.EmailsSentTotal.WithLabelValues(metrics.EmailNewsletter, outcome).Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

// With SMTP not configured, the scheduled weekly is skipped with a warning:
// nothing is dialled, nothing counted as failed, and the issue stays unsent
// for when mail works. The admin's daily Send says why instead.
func TestSend_MailUnavailable(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	_, live, _ := seed(t, database)
	subscribeTwo(t, database)

	sender := &mockSender{}
	svc := NewServiceWithAI(database, sender, config.NewsletterConfig{Timezone: "America/New_York"}, nil, nil, "milesahead.example")
	svc.SetMailUnavailable(true)
	if _, err := database.CreateNewsletter(&db.Newsletter{
		Date: svc.thisSendDate(), Subject: "MilesAhead Weekly", Status: "auto_approved", ArticleCount: 1, ScoutsPickArticleID: &live,
	}); err != nil {
		t.Fatal(err)
	}
	failed0 := emailCount(t, metrics.EmailFailed)
	if err := svc.SendWeekly(); err != nil {
		t.Errorf("SendWeekly() = %v, want a skipped send (nil)", err)
	}
	if len(sender.calls) != 0 {
		t.Error("nothing should be sent without SMTP")
	}
	if d := emailCount(t, metrics.EmailFailed) - failed0; d != 0 {
		t.Errorf("a skipped send counted %v failures, want 0", d)
	}
	if nl, _ := database.GetNewsletterByDate(svc.thisSendDate()); nl == nil || nl.Status != "auto_approved" {
		t.Errorf("the weekly should stay unsent, got %+v", nl)
	}

	daily := NewServiceWithAI(database, sender, config.NewsletterConfig{Timezone: "UTC"}, nil, nil, "milesahead.example")
	daily.SetMailUnavailable(true)
	if err := daily.SendDaily(); !errors.Is(err, ErrMailUnavailable) {
		t.Errorf("SendDaily() = %v, want ErrMailUnavailable", err)
	}
	if len(sender.calls) != 0 {
		t.Error("nothing should be sent without SMTP")
	}
}

// An issue is never recorded as sent to nobody: with no subscriber left to
// send to (none has an unsubscribe token), the send is refused and the
// issue keeps its status.
func TestSendOnce_RefusesEmptyRecipientList(t *testing.T) {
	setTemplateDir(t)
	database, cleanup := setupTestDB(t)
	defer cleanup()
	seed(t, database)
	sender := &mockSender{}
	svc := NewService(database, sender, config.NewsletterConfig{Timezone: "UTC"})
	id := storeDaily(t, database, svc, "approved", "<p>x</p>")
	nl := &db.Newsletter{ID: id, Date: svc.todayDate(), Status: "approved", Subject: "s"}
	if err := svc.sendOnce(nl, "<p>x</p>", nil); !errors.Is(err, ErrNoRecipients) {
		t.Errorf("sendOnce with no recipients = %v, want ErrNoRecipients", err)
	}
	if got, _ := database.GetNewsletterByDate(svc.todayDate()); got == nil || got.Status != "approved" {
		t.Errorf("the issue should keep its status, got %+v", got)
	}
	if len(sender.calls) != 0 {
		t.Error("nothing should be sent")
	}
}
