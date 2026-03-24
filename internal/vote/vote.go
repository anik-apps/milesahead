package vote

import (
	"errors"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
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
	alertWindow    time.Duration
	OnAlert        func(articleID int64)
}

// NewService creates a new vote Service.
func NewService(database *db.Database, cfg config.VotesConfig, onAlert func(int64)) *Service {
	return &Service{
		db:             database,
		alertThreshold: cfg.AlertThreshold,
		alertRatio:     cfg.AlertRatio,
		alertWindow:    time.Duration(cfg.AlertWindow) * time.Hour,
		OnAlert:        onAlert,
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

	// Check threshold for downvote alerts.
	if voteVal == -1 {
		if triggered, _ := s.CheckThreshold(articleID); triggered {
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

// CheckThreshold checks whether the downvote count within the alert window
// exceeds the configured threshold and ratio. Returns true if an alert should fire.
func (s *Service) CheckThreshold(articleID int64) (bool, error) {
	vc, err := s.db.GetVoteCounts(articleID)
	if err != nil {
		return false, err
	}

	if vc.Downvotes < s.alertThreshold {
		return false, nil
	}

	total := vc.Upvotes + vc.Downvotes
	if total == 0 {
		return false, nil
	}

	ratio := float64(vc.Downvotes) / float64(total)
	return ratio >= s.alertRatio, nil
}
