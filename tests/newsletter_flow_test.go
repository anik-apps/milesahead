package tests

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/vote"
	"github.com/kumaranik/milesahead/internal/web"
)

// smtpCapture stands in for the SMTP server: it keeps every message as it
// would go over the wire. Nothing is sent anywhere.
type smtpCapture struct {
	mu  sync.Mutex
	raw []string
}

func (c *smtpCapture) Send(from string, to []string, msg io.WriterTo) error {
	var buf bytes.Buffer
	if _, err := msg.WriteTo(&buf); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.raw = append(c.raw, buf.String())
	return nil
}

func (c *smtpCapture) Close() error { return nil }

func (c *smtpCapture) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.raw
	c.raw = nil
	return out
}

// emailPart returns the message's headers and the decoded body of its part
// of contentType.
func emailPart(t *testing.T, raw, contentType string) (netmail.Header, string) {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decode := func(enc string, r io.Reader) string {
		if strings.EqualFold(enc, "quoted-printable") {
			r = quotedprintable.NewReader(r)
		}
		b, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mt, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if mt == contentType {
		return msg.Header, decode(msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			if pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type")); pt == contentType {
				return msg.Header, decode(p.Header.Get("Content-Transfer-Encoding"), p)
			}
		}
	}
	t.Fatalf("no %s part in:\n%s", contentType, raw)
	return nil, ""
}

var (
	confirmLinkRE = regexp.MustCompile(`https://[^\s"<>]+/subscribe/confirm\?token=[0-9a-f]+`)
	listUnsubRE   = regexp.MustCompile(`<(https://[^>]+)>`)
)

// The newsletter from sign-up to one-click unsubscribe, through the real
// handlers, templates and SMTP sender: two readers sign up and confirm from
// their emails, both get the issue with their own unsubscribe link, and a
// mail provider's one-click POST to one reader's List-Unsubscribe URL takes
// that reader, and only that reader, off the list.
func TestNewsletterFlow_SignUpToOneClickUnsubscribe(t *testing.T) {
	t.Chdir("..") // the mail and newsletter templates load from templates/email
	database := setupTestDB(t)
	cfg := testConfig()
	cfg.Server.Domain = "milesahead.example"
	cfg.Mail.SMTP.From = "MilesAhead <noreply@milesahead.example>"

	smtp := &smtpCapture{}
	sender := mail.NewSender(cfg.Mail.SMTP)
	sender.SetDialFunc(func() (mail.SendCloser, error) { return smtp, nil })
	sender.SetNewsletterSchedule(newsletter.WeeklySendWhen)

	nlSvc := newsletter.NewServiceWithAI(database, sender, config.NewsletterConfig{Timezone: "America/New_York"}, nil, nil, cfg.Server.Domain)
	app, err := web.NewAppWithTemplateDir(cfg, database, article.NewManager(database, ai.NewPipeline(nil, nil, 70)),
		vote.NewService(database, cfg.Votes, nil), nlSvc, nil, context.Background(), "templates")
	if err != nil {
		t.Fatal(err)
	}
	app.SetConfirmationMailer(sender)

	serve := func(method, target, form string) *httptest.ResponseRecorder {
		var body io.Reader
		if form != "" {
			body = strings.NewReader(form)
		}
		req := httptest.NewRequest(method, target, body)
		if form != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		app.WaitBackground()
		return rec
	}

	// Sign up and confirm from the email.
	for _, email := range []string{"ann@example.com", "bob@example.com"} {
		if rec := serve(http.MethodPost, "/subscribe", "email="+url.QueryEscape(email)); rec.Code != http.StatusSeeOther {
			t.Fatalf("sign-up %s: status %d", email, rec.Code)
		}
		sent := smtp.take()
		if len(sent) != 1 {
			t.Fatalf("sign-up %s: %d confirmation emails, want 1", email, len(sent))
		}
		_, text := emailPart(t, sent[0], "text/plain")
		link := confirmLinkRE.FindString(text)
		if !strings.HasPrefix(link, "https://milesahead.example/subscribe/confirm?token=") {
			t.Fatalf("confirmation email has no absolute confirm link:\n%s", text)
		}
		if !strings.Contains(text, "every Saturday at 8:00 AM ET") {
			t.Errorf("confirmation email should say when the newsletter comes:\n%s", text)
		}
		u, _ := url.Parse(link)
		if rec := serve(http.MethodGet, u.RequestURI(), ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "subscribed") {
			t.Fatalf("confirming %s: status %d", email, rec.Code)
		}
	}

	// The issue goes out.
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Amex 30% to Delta", Slug: "amex-delta", Content: "Body.", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", TLDR: "Summary.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateArticleStatus(id, db.StatusApproved); err != nil {
		t.Fatal(err)
	}
	if err := nlSvc.SendDaily(); err != nil {
		t.Fatal(err)
	}
	issues := smtp.take()
	if len(issues) != 2 {
		t.Fatalf("%d newsletters sent, want 2", len(issues))
	}
	links := map[string]string{}
	for _, raw := range issues {
		h, body := emailPart(t, raw, "text/html")
		m := listUnsubRE.FindStringSubmatch(h.Get("List-Unsubscribe"))
		if m == nil || h.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
			t.Fatalf("newsletter to %s lacks one-click List-Unsubscribe headers: %q / %q", h.Get("To"), h.Get("List-Unsubscribe"), h.Get("List-Unsubscribe-Post"))
		}
		if !strings.Contains(body, `href="`+m[1]+`"`) {
			t.Errorf("newsletter to %s: the body's link is not the header's %s", h.Get("To"), m[1])
		}
		links[h.Get("To")] = m[1]
	}
	if links["ann@example.com"] == links["bob@example.com"] {
		t.Fatalf("both readers got the same unsubscribe link %s", links["ann@example.com"])
	}

	// Ann's mail provider sends the RFC 8058 one-click POST.
	u, _ := url.Parse(links["ann@example.com"])
	if rec := serve(http.MethodPost, u.RequestURI(), "List-Unsubscribe=One-Click"); rec.Code != http.StatusOK {
		t.Fatalf("one-click unsubscribe: status %d", rec.Code)
	}
	subs, err := database.ListConfirmedSubscribers()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Email != "bob@example.com" {
		t.Errorf("after Ann's one-click unsubscribe, subscribers = %v; want only Bob", subs)
	}
}
