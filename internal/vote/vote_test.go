package vote

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// setupTestDB creates a temporary SQLite database and returns the Database handle.
func setupTestDB(t *testing.T) *db.Database {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// seedArticle inserts a minimal generated article and returns its ID.
func seedArticle(t *testing.T, d *db.Database) int64 {
	t.Helper()
	// Insert via raw SQL since we need a generated_articles row for the FK.
	res, err := d.DB().Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number)
		 VALUES ('Test Article', 'test-article', 'body', 'summary', 'approved', '[]', 1)`,
	)
	if err != nil {
		t.Fatalf("seeding article: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCastVote(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)

	cfg := config.VotesConfig{
		AlertThreshold: 10,
		AlertRatio:     0.5,
		AlertWindow:    24,
	}
	svc := NewService(d, cfg, nil)

	// Cast an upvote.
	err := svc.CastVote(artID, "up", "iphash1")
	if err != nil {
		t.Fatalf("CastVote up: %v", err)
	}

	// Cast a downvote from different IP.
	err = svc.CastVote(artID, "down", "iphash2")
	if err != nil {
		t.Fatalf("CastVote down: %v", err)
	}

	// Verify counts.
	counts, err := svc.GetCounts(artID)
	if err != nil {
		t.Fatalf("GetCounts: %v", err)
	}
	if counts.Up != 1 {
		t.Errorf("expected Up=1, got %d", counts.Up)
	}
	if counts.Down != 1 {
		t.Errorf("expected Down=1, got %d", counts.Down)
	}

	// Duplicate vote from same IP should be rejected.
	err = svc.CastVote(artID, "up", "iphash1")
	if err != ErrAlreadyVoted {
		t.Errorf("expected ErrAlreadyVoted, got %v", err)
	}
}

func TestVoteThresholdAlert(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)

	var mu sync.Mutex
	var alertedIDs []int64

	cfg := config.VotesConfig{
		AlertThreshold: 3,
		AlertRatio:     0.0, // any ratio triggers
		AlertWindow:    24,
	}
	svc := NewService(d, cfg, func(articleID int64) {
		mu.Lock()
		alertedIDs = append(alertedIDs, articleID)
		mu.Unlock()
	})

	// Cast 3 downvotes from different IPs.
	for i := 0; i < 3; i++ {
		ipHash := "downvoter" + string(rune('a'+i))
		err := svc.CastVote(artID, "down", ipHash)
		if err != nil {
			t.Fatalf("CastVote down #%d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(alertedIDs) == 0 {
		t.Fatal("expected alert callback to fire, but it did not")
	}
	if alertedIDs[len(alertedIDs)-1] != artID {
		t.Errorf("expected alert for article %d, got %d", artID, alertedIDs[len(alertedIDs)-1])
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
