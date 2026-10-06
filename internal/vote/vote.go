package vote

import (
	"errors"
	"log/slog"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// ErrAlreadyVoted is returned when the same IP tries to vote on the same article twice.
var ErrAlreadyVoted = errors.New("already voted")

// VoteCounts holds the up and down vote totals for an article.
type VoteCounts struct {
	Up   int `json:"up"`
	Down int `json:"down"`
}

// Alert is a vote alert just opened: an article's votes in the alert window
// crossed alert_threshold and alert_ratio. It stays open, listed on the
// admin Published page, until the admin acknowledges it.
type Alert struct {
	ArticleID          int64
	Upvotes, Downvotes int
	// Window is how far back the counts go; 0 means all of the article's
	// votes (less any before the last acknowledged alert).
	Window time.Duration
}

// DefaultAlertThreshold is votes.alert_threshold when it is unset.
const DefaultAlertThreshold = 10

// Service handles vote casting, deduplication, and threshold alerting.
type Service struct {
	db             *db.Database
	alertThreshold int
	alertRatio     float64
	// alertWindow is how far back CheckThreshold counts votes; 0 counts
	// all of an article's votes.
	alertWindow time.Duration
	// OnAlert is called when an alert opens (not again while it is open).
	OnAlert func(Alert)
	// now is the clock; tests replace it.
	now func() time.Time
}

// NewService creates a new vote Service. onAlert, if not nil, is called
// when an alert opens, on the voter's request: it should not block.
func NewService(database *db.Database, cfg config.VotesConfig, onAlert func(Alert)) *Service {
	threshold := cfg.AlertThreshold
	if threshold <= 0 {
		threshold = DefaultAlertThreshold
	}
	return &Service{
		db:             database,
		alertThreshold: threshold,
		alertRatio:     cfg.AlertRatio,
		alertWindow:    time.Duration(cfg.AlertWindow) * time.Hour,
		OnAlert:        onAlert,
		now:            time.Now,
	}
}

// CastVote records a vote ("up" or "down") for an article from the given IP hash.
// It returns ErrAlreadyVoted if the IP has already voted on this article.
func (s *Service) CastVote(articleID int64, vote string, ipHash string) error {
	// Check for duplicate.
	voted, err := s.db.HasVoted(articleID, ipHash)
	if err != nil {
		return err
	}
	if voted {
		return ErrAlreadyVoted
	}

	// Map vote string to integer.
	voteVal := 1
	if vote == "down" {
		voteVal = -1
	}

	_, err = s.db.CreateVote(&db.ArticleVote{
		ArticleID: articleID,
		Vote:      voteVal,
		IPHash:    ipHash,
	})
	if err != nil {
		return err
	}

	slog.Info("vote cast", "article_id", articleID, "vote", vote)

	if vote == "up" {
		metrics.VotesTotal.WithLabelValues("up").Inc()
	} else {
		metrics.VotesTotal.WithLabelValues("down").Inc()
	}

	// A downvote can open an alert.
	if voteVal == -1 {
		s.raiseIfDue(articleID)
	}

	return nil
}

// raiseIfDue opens a vote alert for the article if its recent votes call for
// one and none is open, and then calls OnAlert. With one open, it only
// updates the counts the admin sees. Failures are logged: the vote itself
// has been recorded.
func (s *Service) raiseIfDue(articleID int64) {
	vc, err := s.alertCounts(articleID)
	if err != nil {
		slog.Error("vote alert check failed", "article_id", articleID, "error", err)
		return
	}
	if !s.Exceeds(vc.Upvotes, vc.Downvotes) {
		return
	}
	opened, err := s.db.RecordVoteAlert(articleID, vc.Upvotes, vc.Downvotes)
	if err != nil {
		slog.Error("recording vote alert failed", "article_id", articleID, "error", err)
		return
	}
	if !opened {
		return
	}
	slog.Warn("vote threshold exceeded", "article_id", articleID,
		"upvotes", vc.Upvotes, "downvotes", vc.Downvotes, "window", s.alertWindow)
	if s.OnAlert != nil {
		s.OnAlert(Alert{ArticleID: articleID, Upvotes: vc.Upvotes, Downvotes: vc.Downvotes, Window: s.alertWindow})
	}
}

// GetCounts returns the up and down vote counts for an article.
func (s *Service) GetCounts(articleID int64) (*VoteCounts, error) {
	vc, err := s.db.GetVoteCounts(articleID)
	if err != nil {
		return nil, err
	}
	return &VoteCounts{
		Up:   vc.Upvotes,
		Down: vc.Downvotes,
	}, nil
}

// CheckThreshold reports whether an article's recent votes call for an
// alert: at least alert_threshold downvotes, making up at least alert_ratio
// of its votes, counting only votes cast in the last alert_window hours (all
// of them when alert_window is 0) and after its last acknowledged alert.
// Windowed, an article that drew a few complaints long ago and has been read
// quietly since does not alert on the next single downvote; what alerts is a
// burst of them. And votes the admin has already seen do not count again.
func (s *Service) CheckThreshold(articleID int64) (bool, error) {
	vc, err := s.alertCounts(articleID)
	if err != nil {
		return false, err
	}
	return s.Exceeds(vc.Upvotes, vc.Downvotes), nil
}

// alertCounts counts the votes CheckThreshold weighs.
func (s *Service) alertCounts(articleID int64) (*db.VoteCounts, error) {
	since := s.windowStart()
	acked, err := s.db.LastVoteAlertAcknowledged(articleID)
	if err != nil {
		return nil, err
	}
	// created_at and acknowledged_at are whole seconds: start the second
	// after the acknowledgement, so votes cast in its second (seen by the
	// admin) do not count again.
	if !acked.IsZero() && !acked.Add(time.Second).Before(since) {
		since = acked.Add(time.Second)
	}
	return s.db.GetVoteCountsSince(articleID, since)
}

// windowStart is the earliest vote CheckThreshold counts; zero for all.
func (s *Service) windowStart() time.Time {
	if s.alertWindow <= 0 {
		return time.Time{}
	}
	return s.now().Add(-s.alertWindow)
}

// Exceeds applies alert_threshold and alert_ratio to a vote count.
func (s *Service) Exceeds(up, down int) bool {
	return Exceeds(config.VotesConfig{AlertThreshold: s.alertThreshold, AlertRatio: s.alertRatio}, up, down)
}

// Exceeds reports whether up and down votes cross cfg's alert_threshold
// (DefaultAlertThreshold when unset) and alert_ratio. The admin Published
// page's "Many downvotes" chip uses it on an article's all-time votes.
func Exceeds(cfg config.VotesConfig, up, down int) bool {
	threshold := cfg.AlertThreshold
	if threshold <= 0 {
		threshold = DefaultAlertThreshold
	}
	if down < threshold || up+down == 0 {
		return false
	}
	return float64(down)/float64(up+down) >= cfg.AlertRatio
}
