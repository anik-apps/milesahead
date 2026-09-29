package mail

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/kumaranik/milesahead/internal/config"
)

// Action constants for reply parsing.
const (
	ActionApprove   = "approve"
	ActionReject    = "reject"
	ActionUnpublish = "unpublish"
	ActionFeedback  = "feedback"
)

// plusAddressRe matches plus-addressed email: review+{token}@domain.
var plusAddressRe = regexp.MustCompile(`^review\+([^@]+)@`)

// ParseReply parses the body of an email reply and returns the action and
// optional feedback text. It strips email signatures (lines after "-- " or
// "Sent from") before interpreting the first word.
func ParseReply(body string) (action string, feedback string) {
	cleaned := stripSignature(strings.TrimSpace(body))
	cleaned = strings.TrimSpace(cleaned)

	if cleaned == "" {
		return ActionFeedback, ""
	}

	firstWord := strings.Fields(cleaned)[0]
	switch strings.ToLower(firstWord) {
	case "approve":
		return ActionApprove, ""
	case "reject":
		return ActionReject, ""
	case "unpublish":
		return ActionUnpublish, ""
	default:
		return ActionFeedback, cleaned
	}
}

// stripSignature removes common email signatures from the body.
// It truncates at lines starting with "-- " or "Sent from".
func stripSignature(body string) string {
	lines := strings.Split(body, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "--" || strings.HasPrefix(trimmed, "-- ") || strings.HasPrefix(trimmed, "Sent from") {
			break
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// ExtractToken extracts the review token from a plus-addressed email.
// For example, "review+abc123def@gmail.com" returns "abc123def".
func ExtractToken(address string) string {
	matches := plusAddressRe.FindStringSubmatch(address)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// ArticleManager defines the interface the Poller needs from the article layer.
// This avoids importing the article package directly, enabling easier testing.
type ArticleManager interface {
	Approve(articleID int64) error
	Reject(articleID int64) error
	Unpublish(articleID int64) error
	RequestRevision(articleID int64, feedback string, source string) error
	UnpublishAndRevise(articleID int64, feedback string, source string) error
	// StartRevision begins the AI rewrite in the background. Email feedback
	// used to set revision_requested and stop there, so the rewrite never ran.
	StartRevision(articleID int64)
}

// TokenResolver resolves a review token to an article ID and its current status.
type TokenResolver interface {
	Resolve(token string) (articleID int64, status string, err error)
}

// IMAPDialer abstracts IMAP connection creation for testability.
type IMAPDialer interface {
	DialTLS(address string) (*imapclient.Client, error)
}

// defaultDialer is the production IMAP dialer.
type defaultDialer struct{}

func (d *defaultDialer) DialTLS(address string) (*imapclient.Client, error) {
	return imapclient.DialTLS(address, nil)
}

// Poller polls a Gmail IMAP inbox for admin reply emails and processes them.
type Poller struct {
	host         string
	port         int
	user         string
	password     string
	pollInterval time.Duration
	manager      ArticleManager
	resolver     TokenResolver
	dialer       IMAPDialer
}

// NewPoller creates a new IMAP Poller.
func NewPoller(cfg config.IMAPConfig, manager ArticleManager, resolver TokenResolver) *Poller {
	interval := time.Duration(cfg.PollInterval) * time.Second
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	return &Poller{
		host:         cfg.Host,
		port:         cfg.Port,
		user:         cfg.User,
		password:     cfg.Password,
		pollInterval: interval,
		manager:      manager,
		resolver:     resolver,
		dialer:       &defaultDialer{},
	}
}

// SetDialer overrides the IMAP dialer for testing.
func (p *Poller) SetDialer(d IMAPDialer) {
	p.dialer = d
}

// Poll connects to the IMAP server, fetches unread messages, processes each
// one by extracting the review token and reply action, and marks them as read.
func (p *Poller) Poll() error {
	address := fmt.Sprintf("%s:%d", p.host, p.port)
	c, err := p.dialer.DialTLS(address)
	if err != nil {
		return fmt.Errorf("connecting to IMAP: %w", err)
	}
	defer c.Close()

	if err := c.Login(p.user, p.password).Wait(); err != nil {
		return fmt.Errorf("IMAP login: %w", err)
	}

	if _, err := c.Select("INBOX", nil).Wait(); err != nil {
		return fmt.Errorf("selecting INBOX: %w", err)
	}

	// Search for unseen messages.
	criteria := &imap.SearchCriteria{
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	searchData, err := c.Search(criteria, nil).Wait()
	if err != nil {
		return fmt.Errorf("searching UNSEEN: %w", err)
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		_ = c.Logout().Wait()
		return nil
	}

	// Fetch envelope and body for each unseen message.
	seqSet := imap.SeqSetNum(seqNums...)
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierText},
		},
	}

	messages, err := c.Fetch(seqSet, fetchOptions).Collect()
	if err != nil {
		return fmt.Errorf("fetching messages: %w", err)
	}

	for _, msg := range messages {
		if err := p.processMessage(msg); err != nil {
			slog.Error("error processing message", "seq", msg.SeqNum, "error", err)
			continue
		}
	}

	slog.Info("mail poll completed", "messages_processed", len(messages))

	// Mark all fetched messages as read.
	storeFlags := &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagSeen},
	}
	if err := c.Store(seqSet, storeFlags, nil).Close(); err != nil {
		return fmt.Errorf("marking messages as read: %w", err)
	}

	_ = c.Logout().Wait()
	return nil
}

// processMessage handles a single fetched IMAP message.
func (p *Poller) processMessage(msg *imapclient.FetchMessageBuffer) error {
	if msg.Envelope == nil {
		return fmt.Errorf("message has no envelope")
	}

	// Extract token from the To addresses (the plus-addressed reply-to).
	token := ""
	for _, addr := range msg.Envelope.To {
		t := ExtractToken(addr.Addr())
		if t != "" {
			token = t
			break
		}
	}
	if token == "" {
		return fmt.Errorf("no review token found in To addresses")
	}

	// Resolve token to article ID and status.
	articleID, status, err := p.resolver.Resolve(token)
	if err != nil {
		return fmt.Errorf("resolving token %q: %w", token, err)
	}

	// Extract body text from the fetched body sections.
	bodyText := ""
	for _, section := range msg.BodySection {
		bodyText = string(section.Bytes)
		break
	}

	action, feedback := ParseReply(bodyText)

	slog.Info("mail action", "action", action, "article_id", articleID)

	switch action {
	case ActionApprove:
		return p.manager.Approve(articleID)
	case ActionReject:
		return p.manager.Reject(articleID)
	case ActionUnpublish:
		return p.manager.Unpublish(articleID)
	case ActionFeedback:
		var err error
		if status == "approved" {
			err = p.manager.UnpublishAndRevise(articleID, feedback, "email")
		} else {
			err = p.manager.RequestRevision(articleID, feedback, "email")
		}
		if err != nil {
			return err
		}
		// Start the rewrite now rather than waiting for the revision
		// reconciler's next pass, which remains the backstop if this fails.
		p.manager.StartRevision(articleID)
		return nil
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// Start runs Poll in a loop at the configured interval, respecting context
// cancellation for graceful shutdown.
func (p *Poller) Start(ctx context.Context) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	// Run once immediately.
	if err := p.Poll(); err != nil {
		slog.Error("IMAP poll error", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("IMAP poller stopped")
			return
		case <-ticker.C:
			if err := p.Poll(); err != nil {
				slog.Error("IMAP poll error", "error", err)
			}
		}
	}
}
