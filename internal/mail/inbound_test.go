package mail

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/kumaranik/milesahead/internal/config"
)

func TestParseReply_Approve(t *testing.T) {
	action, feedback := ParseReply("Approve\n\nSent from my iPhone")
	if action != ActionApprove {
		t.Errorf("action = %q, want %q", action, ActionApprove)
	}
	if feedback != "" {
		t.Errorf("feedback = %q, want empty", feedback)
	}
}

func TestParseReply_Reject(t *testing.T) {
	action, feedback := ParseReply("reject")
	if action != ActionReject {
		t.Errorf("action = %q, want %q", action, ActionReject)
	}
	if feedback != "" {
		t.Errorf("feedback = %q, want empty", feedback)
	}
}

func TestParseReply_Unpublish(t *testing.T) {
	action, feedback := ParseReply("Unpublish")
	if action != ActionUnpublish {
		t.Errorf("action = %q, want %q", action, ActionUnpublish)
	}
	if feedback != "" {
		t.Errorf("feedback = %q, want empty", feedback)
	}
}

func TestParseReply_Feedback(t *testing.T) {
	input := "Please fix the third paragraph, it has incorrect data."
	action, feedback := ParseReply(input)
	if action != ActionFeedback {
		t.Errorf("action = %q, want %q", action, ActionFeedback)
	}
	if feedback != input {
		t.Errorf("feedback = %q, want %q", feedback, input)
	}
}

func TestParseReply_FeedbackWithSignature(t *testing.T) {
	input := "This needs more detail on the methodology.\n\n-- \nJohn Doe\njohn@example.com"
	action, feedback := ParseReply(input)
	if action != ActionFeedback {
		t.Errorf("action = %q, want %q", action, ActionFeedback)
	}
	want := "This needs more detail on the methodology."
	if feedback != want {
		t.Errorf("feedback = %q, want %q", feedback, want)
	}
}

func TestParseReply_ApproveWithLeadingWhitespace(t *testing.T) {
	action, feedback := ParseReply("  \n  approve  \n\n")
	if action != ActionApprove {
		t.Errorf("action = %q, want %q", action, ActionApprove)
	}
	if feedback != "" {
		t.Errorf("feedback = %q, want empty", feedback)
	}
}

func TestParseReply_EmptyBody(t *testing.T) {
	action, feedback := ParseReply("")
	if action != ActionFeedback {
		t.Errorf("action = %q, want %q", action, ActionFeedback)
	}
	if feedback != "" {
		t.Errorf("feedback = %q, want empty", feedback)
	}
}

func TestExtractToken(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
	}{
		{
			name:    "valid plus address",
			address: "review+abc123def@gmail.com",
			want:    "abc123def",
		},
		{
			name:    "no plus address",
			address: "admin@gmail.com",
			want:    "",
		},
		{
			name:    "wrong prefix",
			address: "support+abc@gmail.com",
			want:    "",
		},
		{
			name:    "empty string",
			address: "",
			want:    "",
		},
		{
			name:    "complex token",
			address: "review+art-42-rev3@example.com",
			want:    "art-42-rev3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractToken(tt.address)
			if got != tt.want {
				t.Errorf("ExtractToken(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

// mockArticleManager records calls for verification.
type mockArticleManager struct {
	approveCalls         []int64
	rejectCalls          []int64
	unpublishCalls       []int64
	requestRevisionCalls []revisionCall
	unpublishReviseCalls []revisionCall
	err                  error
}

type revisionCall struct {
	articleID int64
	feedback  string
	source    string
}

func (m *mockArticleManager) Approve(articleID int64) error {
	m.approveCalls = append(m.approveCalls, articleID)
	return m.err
}

func (m *mockArticleManager) Reject(articleID int64) error {
	m.rejectCalls = append(m.rejectCalls, articleID)
	return m.err
}

func (m *mockArticleManager) Unpublish(articleID int64) error {
	m.unpublishCalls = append(m.unpublishCalls, articleID)
	return m.err
}

func (m *mockArticleManager) RequestRevision(articleID int64, feedback string, source string) error {
	m.requestRevisionCalls = append(m.requestRevisionCalls, revisionCall{articleID, feedback, source})
	return m.err
}

func (m *mockArticleManager) UnpublishAndRevise(articleID int64, feedback string, source string) error {
	m.unpublishReviseCalls = append(m.unpublishReviseCalls, revisionCall{articleID, feedback, source})
	return m.err
}

// mockTokenResolver returns predetermined results.
type mockTokenResolver struct {
	articles map[string]resolvedArticle
}

type resolvedArticle struct {
	id     int64
	status string
}

func (r *mockTokenResolver) Resolve(token string) (int64, string, error) {
	a, ok := r.articles[token]
	if !ok {
		return 0, "", fmt.Errorf("unknown token: %s", token)
	}
	return a.id, a.status, nil
}

func TestNewPoller(t *testing.T) {
	cfg := defaultIMAPConfig()
	mgr := &mockArticleManager{}
	res := &mockTokenResolver{}

	poller := NewPoller(cfg, mgr, res)
	if poller == nil {
		t.Fatal("NewPoller returned nil")
	}
	if poller.host != "imap.gmail.com" {
		t.Errorf("host = %q, want %q", poller.host, "imap.gmail.com")
	}
	if poller.pollInterval != 120*time.Second {
		t.Errorf("pollInterval = %v, want %v", poller.pollInterval, 120*time.Second)
	}
}

func TestNewPoller_DefaultInterval(t *testing.T) {
	cfg := defaultIMAPConfig()
	cfg.PollInterval = 0

	poller := NewPoller(cfg, &mockArticleManager{}, &mockTokenResolver{})
	if poller.pollInterval != 2*time.Minute {
		t.Errorf("pollInterval = %v, want %v", poller.pollInterval, 2*time.Minute)
	}
}

func TestStartRespectsContextCancellation(t *testing.T) {
	cfg := defaultIMAPConfig()
	cfg.PollInterval = 3600 // very long to avoid actual polls

	poller := &Poller{
		host:         cfg.Host,
		port:         cfg.Port,
		user:         cfg.User,
		password:     cfg.Password,
		pollInterval: time.Hour,
		manager:      &mockArticleManager{},
		resolver:     &mockTokenResolver{},
		dialer:       &failingDialer{},
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		poller.Start(ctx)
		close(done)
	}()

	// Cancel immediately.
	cancel()

	select {
	case <-done:
		// OK: Start returned after cancellation.
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// failingDialer always fails, which is fine for testing Start loop behavior.
type failingDialer struct{}

func (d *failingDialer) DialTLS(address string) (*imapclient.Client, error) {
	return nil, fmt.Errorf("dial disabled in test")
}

func TestStripSignature(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no signature",
			input: "Hello world",
			want:  "Hello world",
		},
		{
			name:  "dash dash space",
			input: "Hello\n-- \nJohn",
			want:  "Hello",
		},
		{
			name:  "sent from iphone",
			input: "Approve\n\nSent from my iPhone",
			want:  "Approve\n",
		},
		{
			name:  "double dash without space",
			input: "Hello\n--\nJohn",
			want:  "Hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripSignature(tt.input)
			if got != tt.want {
				t.Errorf("stripSignature(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func defaultIMAPConfig() config.IMAPConfig {
	return config.IMAPConfig{
		Host:         "imap.gmail.com",
		Port:         993,
		User:         "test@gmail.com",
		Password:     "secret",
		PollInterval: 120,
	}
}
