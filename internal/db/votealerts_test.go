package db

import (
	"testing"
	"time"
)

func TestVoteAlerts_OneOpenPerArticleUntilAcknowledged(t *testing.T) {
	d := openTestDB(t)
	a := insertVoteAlertArticle(t, d, "first")
	b := insertVoteAlertArticle(t, d, "second")

	if opened, err := d.RecordVoteAlert(a, 1, 10); err != nil || !opened {
		t.Fatalf("first alert: opened %v, err %v", opened, err)
	}
	// Further downvotes update the open alert rather than opening another.
	if opened, err := d.RecordVoteAlert(a, 1, 12); err != nil || opened {
		t.Fatalf("second alert on the same article: opened %v, err %v", opened, err)
	}
	if opened, _ := d.RecordVoteAlert(b, 0, 10); !opened {
		t.Fatal("another article's alert should open")
	}
	alerts, err := d.ListOpenVoteAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 2 {
		t.Fatalf("open alerts = %d, want 2", len(alerts))
	}
	var first VoteAlert
	for _, al := range alerts {
		if al.ArticleID == a {
			first = al
		}
	}
	if first.Downvotes != 12 || first.Upvotes != 1 || first.Title != "Article first" || first.Slug != "first" || first.Status != "approved" {
		t.Errorf("alert for a = %+v", first)
	}
	if n, _ := d.CountOpenVoteAlerts(); n != 2 {
		t.Errorf("CountOpenVoteAlerts = %d", n)
	}
	if at, _ := d.LastVoteAlertAcknowledged(a); !at.IsZero() {
		t.Errorf("never acknowledged, got %v", at)
	}

	ack := time.Date(2026, 10, 2, 15, 4, 5, 0, time.UTC)
	if ok, err := d.AcknowledgeVoteAlert(first.ID, ack); err != nil || !ok {
		t.Fatalf("acknowledge: %v %v", ok, err)
	}
	if ok, _ := d.AcknowledgeVoteAlert(first.ID, ack); ok {
		t.Error("acknowledging twice should report no open alert")
	}
	if at, _ := d.LastVoteAlertAcknowledged(a); !at.Equal(ack) {
		t.Errorf("LastVoteAlertAcknowledged = %v, want %v", at, ack)
	}
	// Acknowledged, the article can alert again.
	if opened, _ := d.RecordVoteAlert(a, 0, 11); !opened {
		t.Error("after acknowledgement a new alert should open")
	}
	if n, err := d.AcknowledgeAllVoteAlerts(ack.Add(time.Hour)); err != nil || n != 2 {
		t.Errorf("AcknowledgeAllVoteAlerts = %d, %v", n, err)
	}
	if n, _ := d.CountOpenVoteAlerts(); n != 0 {
		t.Errorf("open after acknowledging all: %d", n)
	}
}

func insertVoteAlertArticle(t *testing.T, d *Database, slug string) int64 {
	t.Helper()
	res, err := d.DB().Exec(`INSERT INTO generated_articles (title, slug, content, type, status, sources_json, revision_number)
		VALUES (?, ?, 'body', 'summary', 'approved', '[]', 1)`, "Article "+slug, slug)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
