package db

import (
	"fmt"
	"testing"
	"time"
)

func TestModelHealth_RoundTrip(t *testing.T) {
	d := openTestDB(t)
	got, found, err := d.GetModelHealth("critic", "m1")
	if err != nil || found || got.Step != "critic" || got.Model != "m1" {
		t.Fatalf("missing row: %+v found=%v err=%v", got, found, err)
	}
	at := time.Date(2026, 10, 20, 1, 8, 49, 0, time.UTC)
	want := ModelHealth{Step: "critic", Model: "m1", LastFail: at, FailingSince: at,
		CheckFailStreak: 2, LastOutcome: "retired", LastError: "404 Not Found", LastCheck: at}
	if err := d.UpsertModelHealth(want); err != nil {
		t.Fatal(err)
	}
	want.CallFailStreak = 1 // an update, not a second row
	if err := d.UpsertModelHealth(want); err != nil {
		t.Fatal(err)
	}
	got, found, err = d.GetModelHealth("critic", "m1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got.CheckFailStreak != 2 || got.CallFailStreak != 1 || got.LastOutcome != "retired" ||
		got.LastError != "404 Not Found" || !got.LastFail.Equal(at) || !got.FailingSince.Equal(at) ||
		!got.LastCheck.Equal(at) || !got.LastOK.IsZero() || !got.LastAnswered.IsZero() {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	all, err := d.ListModelHealth()
	if err != nil || len(all) != 1 {
		t.Errorf("list: %d rows, %v", len(all), err)
	}
}

func TestModelAlerts_SaveAndList(t *testing.T) {
	d := openTestDB(t)
	at := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	if err := d.SaveModelAlerts([]ModelAlert{{Key: "step:critic", State: "down", Since: at, NotifiedAt: at}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveModelAlerts([]ModelAlert{{Key: "step:critic", State: "ok", Since: at.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListModelAlerts()
	if err != nil {
		t.Fatal(err)
	}
	if a := got["step:critic"]; a.State != "ok" || !a.Since.Equal(at.Add(time.Hour)) || !a.NotifiedAt.IsZero() {
		t.Errorf("saved %+v", a)
	}
}

func TestModelAlertSend_KeepsTheLastAttempt(t *testing.T) {
	d := openTestDB(t)
	if at, msg, err := d.LastModelAlertSend(); err != nil || !at.IsZero() || msg != "" {
		t.Fatalf("empty: %v %q %v", at, msg, err)
	}
	at := time.Date(2026, 10, 20, 2, 0, 0, 0, time.UTC)
	d.RecordModelAlertSend(at, "550 mailbox unavailable")
	d.RecordModelAlertSend(at.Add(time.Hour), "")
	got, msg, err := d.LastModelAlertSend()
	if err != nil || !got.Equal(at.Add(time.Hour)) || msg != "" {
		t.Errorf("last = %v %q %v", got, msg, err)
	}
}

// A source switched off since it added stories is not scanned, so it does not
// count towards the drought, as the scan's own clustering ignores it.
func TestCountSourcesWithRawSince_OnlyActiveSources(t *testing.T) {
	d := openTestDB(t)
	since := time.Now().Add(-time.Hour)
	var ids []int64
	for i, name := range []string{"A", "B"} {
		id, err := d.CreateSource(&Source{Name: name, URL: "https://" + name + ".example", Active: true})
		if err != nil {
			t.Fatal(err)
		}
		for j := range 2 {
			if _, err := d.CreateRawArticle(&RawArticle{SourceID: id, Title: "T",
				URL: fmt.Sprintf("https://%s.example/%d-%d", name, i, j), Content: "c",
				PublishedAt: time.Now(), Hash: fmt.Sprintf("h%s%d", name, j)}); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, id)
	}
	if err := d.ToggleSourceActive(ids[1]); err != nil {
		t.Fatal(err)
	}
	if n, err := d.CountSourcesWithRawSince(since); err != nil || n != 1 {
		t.Errorf("sources with raw = %d, %v; want 1 (the inactive source is not counted)", n, err)
	}
}

func TestDroughtCounts(t *testing.T) {
	d := openTestDB(t)
	since := time.Now().Add(-time.Hour)
	for i, name := range []string{"A", "B"} {
		id, err := d.CreateSource(&Source{Name: name, URL: "https://" + name + ".example", Active: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.CreateRawArticle(&RawArticle{SourceID: id, Title: "T", URL: "https://" + name + ".example/" + string(rune('a'+i)),
			Content: "c", PublishedAt: time.Now(), Hash: "h" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := d.CountSourcesWithRawSince(since); err != nil || n != 2 {
		t.Errorf("sources with raw = %d, %v; want 2", n, err)
	}
	if n, err := d.CountArticlesCreatedSince(since); err != nil || n != 0 {
		t.Errorf("articles = %d, %v; want 0", n, err)
	}
	if latest, err := d.LatestArticleCreatedAt(); err != nil || !latest.IsZero() {
		t.Errorf("latest with none = %v, %v", latest, err)
	}
	if _, err := d.CreateGeneratedArticle(&GeneratedArticle{Title: "A", Slug: "a", Content: "c", Type: "deal",
		Status: StatusPending, SourcesJSON: "[]", RevisionNumber: 1}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.CountArticlesCreatedSince(since); err != nil || n != 1 {
		t.Errorf("articles = %d, %v; want 1", n, err)
	}
	if latest, err := d.LatestArticleCreatedAt(); err != nil || latest.IsZero() {
		t.Errorf("latest = %v, %v", latest, err)
	}
}
