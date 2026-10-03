package vote

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	svc := NewService(d, cfg, func(a Alert) {
		mu.Lock()
		alertedIDs = append(alertedIDs, a.ArticleID)
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

// castAt records a vote as cast at a given time, as CastVote would have then.
func castAt(t *testing.T, d *db.Database, articleID int64, vote int, ip string, at time.Time) {
	t.Helper()
	if _, err := d.DB().Exec(`INSERT INTO article_votes (article_id, vote, ip_hash, created_at) VALUES (?, ?, ?, ?)`,
		articleID, vote, ip, at.UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
}

// Only votes cast in the last alert_window hours count towards an alert,
// for the threshold and the ratio alike.
func TestCheckThreshold_CountsOnlyTheWindow(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc := NewService(d, config.VotesConfig{AlertThreshold: 3, AlertRatio: 0.5, AlertWindow: 24}, nil)
	svc.now = func() time.Time { return now }

	// Five downvotes two days ago, outside the window.
	for i := 0; i < 5; i++ {
		castAt(t, d, artID, -1, fmt.Sprintf("old%d", i), now.Add(-48*time.Hour))
	}
	if hit, err := svc.CheckThreshold(artID); err != nil || hit {
		t.Fatalf("old downvotes alone: alert %v, err %v", hit, err)
	}
	// Two recent ones: under the threshold of 3 in the window.
	castAt(t, d, artID, -1, "new0", now.Add(-time.Hour))
	castAt(t, d, artID, -1, "new1", now.Add(-23*time.Hour))
	if hit, _ := svc.CheckThreshold(artID); hit {
		t.Fatal("two downvotes in the window should not alert")
	}
	castAt(t, d, artID, -1, "new2", now.Add(-2*time.Hour))
	if hit, _ := svc.CheckThreshold(artID); !hit {
		t.Fatal("three downvotes in the window should alert")
	}
	// The ratio is the window's too: four recent upvotes make it 3 of 7.
	for i := 0; i < 4; i++ {
		castAt(t, d, artID, 1, fmt.Sprintf("up%d", i), now.Add(-time.Hour))
	}
	if hit, _ := svc.CheckThreshold(artID); hit {
		t.Error("3 downvotes of 7 recent votes is under the 0.5 ratio")
	}
	// Upvotes outside the window do not dilute it.
	art2 := seedArticleSlug(t, d, "second")
	for i := 0; i < 3; i++ {
		castAt(t, d, art2, -1, fmt.Sprintf("d%d", i), now.Add(-time.Hour))
	}
	for i := 0; i < 10; i++ {
		castAt(t, d, art2, 1, fmt.Sprintf("u%d", i), now.Add(-72*time.Hour))
	}
	if hit, _ := svc.CheckThreshold(art2); !hit {
		t.Error("old upvotes should not hold off an alert on a recent burst")
	}
}

// alert_window 0 counts every vote, however old.
func TestCheckThreshold_NoWindowCountsAll(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)
	now := time.Now()
	svc := NewService(d, config.VotesConfig{AlertThreshold: 2, AlertRatio: 0.5}, nil)
	castAt(t, d, artID, -1, "a", now.Add(-500*time.Hour))
	castAt(t, d, artID, -1, "b", now.Add(-time.Minute))
	if hit, _ := svc.CheckThreshold(artID); !hit {
		t.Error("with no window, old downvotes count")
	}
}

func seedArticleSlug(t *testing.T, d *db.Database, slug string) int64 {
	t.Helper()
	res, err := d.DB().Exec(
		`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number)
		 VALUES (?, ?, 'body', 'summary', 'approved', '[]', 1)`, "Article "+slug, slug)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// An alert opens once, is listed until acknowledged, and only votes cast
// after the acknowledgement can open the next one.
func TestCastVote_OpensOneAlertUntilAcknowledged(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)
	var alerts []Alert
	svc := NewService(d, config.VotesConfig{AlertThreshold: 3, AlertRatio: 0.5, AlertWindow: 24}, func(a Alert) {
		alerts = append(alerts, a)
	})
	for i := 0; i < 5; i++ {
		if err := svc.CastVote(artID, "down", fmt.Sprintf("d%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(alerts) != 1 {
		t.Fatalf("OnAlert called %d times for one crossing, want 1", len(alerts))
	}
	if a := alerts[0]; a.ArticleID != artID || a.Downvotes != 3 || a.Upvotes != 0 || a.Window != 24*time.Hour {
		t.Errorf("alert = %+v", a)
	}
	open, err := d.ListOpenVoteAlerts()
	if err != nil || len(open) != 1 || open[0].Downvotes != 5 {
		t.Fatalf("open alerts = %+v, %v (want one, updated to 5 downvotes)", open, err)
	}

	// Acknowledged a second from now, so the five votes above are behind it.
	if _, err := d.AcknowledgeVoteAlert(open[0].ID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if hit, _ := svc.CheckThreshold(artID); hit {
		t.Error("votes the admin has acknowledged should not count again")
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	for i := 0; i < 3; i++ {
		castAt(t, d, artID, -1, fmt.Sprintf("late%d", i), time.Now().Add(2*time.Second))
	}
	if hit, _ := svc.CheckThreshold(artID); !hit {
		t.Error("three new downvotes after the acknowledgement should alert again")
	}
}

// Unset, alert_threshold is 10, not 0 (which would alert on every downvote).
func TestNewService_DefaultThreshold(t *testing.T) {
	svc := NewService(nil, config.VotesConfig{AlertRatio: 0.4}, nil)
	if svc.Exceeds(0, 9) || !svc.Exceeds(0, 10) {
		t.Error("the default threshold should be 10 downvotes")
	}
}

// Votes cast in the same second as the acknowledgement were seen by the
// admin and do not count towards the next alert; the next second's do.
func TestCheckThreshold_StartsAfterTheAcknowledgedSecond(t *testing.T) {
	d := setupTestDB(t)
	artID := seedArticle(t, d)
	ack := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	svc := NewService(d, config.VotesConfig{AlertThreshold: 2, AlertRatio: 0.5, AlertWindow: 24}, nil)
	svc.now = func() time.Time { return ack.Add(time.Minute) }
	if _, err := d.RecordVoteAlert(artID, 0, 2); err != nil {
		t.Fatal(err)
	}
	alerts, _ := d.ListOpenVoteAlerts()
	if _, err := d.AcknowledgeVoteAlert(alerts[0].ID, ack); err != nil {
		t.Fatal(err)
	}
	castAt(t, d, artID, -1, "same0", ack)
	castAt(t, d, artID, -1, "same1", ack)
	if hit, _ := svc.CheckThreshold(artID); hit {
		t.Error("votes in the acknowledged second should not count again")
	}
	castAt(t, d, artID, -1, "next0", ack.Add(time.Second))
	if hit, _ := svc.CheckThreshold(artID); hit {
		t.Error("one new downvote is under the threshold of 2")
	}
	castAt(t, d, artID, -1, "next1", ack.Add(2*time.Second))
	if hit, _ := svc.CheckThreshold(artID); !hit {
		t.Error("two downvotes after the acknowledged second should alert")
	}
}
