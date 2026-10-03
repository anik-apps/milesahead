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

// Service handles vote casting, deduplication, and threshold alerting.
type Service struct {
	db             *db.Database
	alertThreshold int
	alertRatio     float64
	// alertWindow is how far back CheckThreshold counts votes; 0 counts
	// all of an article's votes.
	alertWindow time.Duration
	OnAlert     func(articleID int64)
	// now is the clock; tests replace it.
	now func() time.Time
}

// NewService creates a new vote Service.
func NewService(database *db.Database, cfg config.VotesConfig, onAlert func(int64)) *Service {
	return &Service{
		db:             database,
		alertThreshold: cfg.AlertThreshold,
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

	// Check threshold for downvote alerts.
	if voteVal == -1 {
		if triggered, _ := s.CheckThreshold(articleID); triggered {
			slog.Warn("vote threshold exceeded", "article_id", articleID)
			if s.OnAlert != nil {
				s.OnAlert(articleID)
			}
		}
	}

	return nil
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
// of them when alert_window is 0). Windowed, an article that drew a few
// complaints long ago and has been read quietly since does not alert on the
// next single downvote; what alerts is a burst of them.
func (s *Service) CheckThreshold(articleID int64) (bool, error) {
	vc, err := s.db.GetVoteCountsSince(articleID, s.windowStart())
	if err != nil {
		return false, err
	}
	return s.exceeds(vc.Upvotes, vc.Downvotes), nil
}

// windowStart is the earliest vote CheckThreshold counts; zero for all.
func (s *Service) windowStart() time.Time {
	if s.alertWindow <= 0 {
		return time.Time{}
	}
	return s.now().Add(-s.alertWindow)
}

// exceeds applies alert_threshold and alert_ratio to a vote count.
func (s *Service) exceeds(up, down int) bool {
	if down < s.alertThreshold || up+down == 0 {
		return false
	}
	return float64(down)/float64(up+down) >= s.alertRatio
}
