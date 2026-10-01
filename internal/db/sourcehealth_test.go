package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestListSourceHealth(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	busy, _ := d.CreateSource(&Source{Name: "Busy", URL: "https://a.example", Active: true})
	quiet, _ := d.CreateSource(&Source{Name: "Quiet", URL: "https://b.example", Active: true})
	now := time.Now().UTC()
	for i, age := range []time.Duration{2 * time.Hour, 30 * time.Hour, 10 * 24 * time.Hour} {
		id, err := d.CreateRawArticle(&RawArticle{SourceID: busy, Title: "t", URL: "u", Content: "c", PublishedAt: now, Hash: string(rune('a' + i))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.db.Exec(`UPDATE raw_articles SET created_at = ? WHERE id = ?`, now.Add(-age).Format(sqliteTime), id); err != nil {
			t.Fatal(err)
		}
	}

	h, err := d.ListSourceHealth(now.Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	b := h[busy]
	if b.Total != 3 || b.Recent != 2 {
		t.Errorf("busy: total %d recent %d, want 3 and 2", b.Total, b.Recent)
	}
	if got := now.Sub(b.LastArticleAt); got < 2*time.Hour-time.Minute || got > 2*time.Hour+time.Minute {
		t.Errorf("busy: last article %v ago, want about 2h", got)
	}
	if q := h[quiet]; !q.LastArticleAt.IsZero() || q.Total != 0 {
		t.Errorf("quiet: %+v, want no articles", q)
	}
}
