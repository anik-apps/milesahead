package mail

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// mockSendCloser captures sent messages for verification.
type mockSendCloser struct {
	sent []mockMessage
}

type mockMessage struct {
	from string
	to   []string
}

func (m *mockSendCloser) Send(from string, to []string, msg io.WriterTo) error {
	m.sent = append(m.sent, mockMessage{from: from, to: to})
	return nil
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
	html, err := RenderConfirmation("abc123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}

	expected := "https://milesahead.com/subscribe/confirm?token=abc123"
	if !strings.Contains(html, expected) {
		t.Error("confirmation HTML should contain confirm link")
	}
}

func TestRenderConfirmation_ContainsUnsubscribeLink(t *testing.T) {
	html, err := RenderConfirmation("abc123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}

	expected := "https://milesahead.com/unsubscribe?token=abc123"
	if !strings.Contains(html, expected) {
		t.Error("confirmation HTML should contain unsubscribe link")
	}
}

func TestRenderConfirmation_ContainsSubscriptionTitle(t *testing.T) {
	html, err := RenderConfirmation("abc123", "milesahead.com")
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

func TestSendNewsletter(t *testing.T) {
	sender, mock := newTestSender()
	recipients := []string{"a@test.com", "b@test.com", "c@test.com"}

	err := sender.SendNewsletter("Weekly Digest", "<h1>News</h1>", recipients)
	if err != nil {
		t.Fatalf("SendNewsletter failed: %v", err)
	}

	if len(mock.sent) != 3 {
		t.Fatalf("expected 3 sent messages, got %d", len(mock.sent))
	}
	for i, r := range recipients {
		if mock.sent[i].to[0] != r {
			t.Errorf("message %d: expected to=%s, got %s", i, r, mock.sent[i].to[0])
		}
	}
}

func TestSendNewsletter_EmptyRecipients(t *testing.T) {
	sender, _ := newTestSender()

	err := sender.SendNewsletter("Subject", "<p>body</p>", nil)
	if err != nil {
		t.Fatalf("SendNewsletter with empty recipients should not error, got: %v", err)
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
	html, err := RenderConfirmation("abc123", "milesahead.com")
	if err != nil {
		t.Fatalf("RenderConfirmation failed: %v", err)
	}
	for _, want := range []string{
		`src="https://milesahead.com/static/img/brand/email-lockup.png"`,
		`src="https://milesahead.com/static/img/brand/email-band.png"`,
		"Thank you for subscribing to MilesAhead!",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("confirmation email should contain %s", want)
		}
	}
	if strings.Contains(html, "Miles Ahead") || strings.Contains(html, ".svg") {
		t.Error("confirmation email should use the one-word name and raster images only")
	}
}
