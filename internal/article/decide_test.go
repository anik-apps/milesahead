package article

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// newRevisionRequested makes an article at revision 1 waiting on admin
// feedback, and returns it with that feedback's id.
func newRevisionRequested(t *testing.T, database *db.Database, slug string) (int64, int64) {
	t.Helper()
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "T " + slug, Slug: slug, Content: "<p>v1</p>", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(database, nil)
	if err := m.RequestRevision(id, "shorter", "admin"); err != nil {
		t.Fatal(err)
	}
	fbs, err := database.ListFeedbackForArticle(id)
	if err != nil || len(fbs) == 0 {
		t.Fatalf("feedback: %v %d", err, len(fbs))
	}
	return id, fbs[len(fbs)-1].ID
}

func applyRewrite(database *db.Database, id, feedbackID int64) (bool, error) {
	return database.ApplyRevision(db.AppliedRevision{
		ArticleID: id, BaseRevision: 1, FeedbackID: feedbackID, Title: "T",
		PreviousContent: "<p>v1</p>", NewContent: "<p>v2</p>", Status: db.StatusPending,
	})
}

// A rewrite that lands after the admin loaded revision 1 must not be
// approved by the admin's click on revision 1.
func TestApproveRevision_RefusesWhenARewriteLanded(t *testing.T) {
	database := setupFileDB(t)
	m := NewManager(database, nil)
	id, fb := newRevisionRequested(t, database, "landed")
	if ok, err := applyRewrite(database, id, fb); err != nil || !ok {
		t.Fatalf("apply rewrite: %v %v", ok, err)
	}

	err := m.ApproveRevision(id, 1)
	var changed *ErrArticleChanged
	if !errors.As(err, &changed) || changed.Revision != 2 {
		t.Fatalf("ApproveRevision(1) = %v, want ErrArticleChanged at revision 2", err)
	}
	if err := m.RejectRevision(id, 1); !errors.As(err, &changed) {
		t.Fatalf("RejectRevision(1) = %v, want ErrArticleChanged", err)
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.Status != db.StatusPending {
		t.Fatalf("status = %q, want pending (untouched)", a.Status)
	}
	if err := m.ApproveRevision(id, 2); err != nil {
		t.Fatalf("ApproveRevision(2): %v", err)
	}
}

// Approve and a landing rewrite race: whichever wins, the article is never
// approved at a revision the admin did not see.
func TestApproveRevision_RaceWithRewrite(t *testing.T) {
	database := setupFileDB(t)
	m := NewManager(database, nil)
	for i := 0; i < 25; i++ {
		id, fb := newRevisionRequested(t, database, "race-"+string(rune('a'+i)))
		var wg sync.WaitGroup
		var approveErr error
		wg.Add(2)
		go func() { defer wg.Done(); approveErr = m.ApproveRevision(id, 1) }()
		go func() { defer wg.Done(); _, _ = applyRewrite(database, id, fb) }()
		wg.Wait()

		a, err := database.GetGeneratedArticle(id)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case a.Status == db.StatusApproved && a.RevisionNumber == 1 && approveErr == nil:
		case a.Status == db.StatusPending && a.RevisionNumber == 2 && approveErr != nil:
		default:
			t.Fatalf("iteration %d: status %q revision %d approveErr %v", i, a.Status, a.RevisionNumber, approveErr)
		}
	}
}

func TestUndoApprove(t *testing.T) {
	database := setupFileDB(t)
	m := NewManager(database, nil)
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Undo me", Slug: "undo-me", Content: "<p>x</p>", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ApproveRevision(id, 1); err != nil {
		t.Fatal(err)
	}
	if err := m.UndoApprove(id, db.StatusApproved, time.Minute); err == nil {
		t.Error("undo back to approved should be refused")
	}
	if err := m.UndoApprove(id, db.StatusPending, time.Minute); err != nil {
		t.Fatalf("undo: %v", err)
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.Status != db.StatusPending || a.PublishedAt != nil {
		t.Fatalf("after undo: status %q published_at %v, want pending and unpublished", a.Status, a.PublishedAt)
	}
	// A second undo has nothing to undo.
	if err := m.UndoApprove(id, db.StatusPending, time.Minute); !errors.Is(err, db.ErrUndoExpired) {
		t.Errorf("second undo = %v, want ErrUndoExpired", err)
	}
}

func TestUndoApprove_TooLate(t *testing.T) {
	database := setupFileDB(t)
	m := NewManager(database, nil)
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Old", Slug: "old", Content: "<p>x</p>", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE generated_articles SET published_at = datetime('now', '-1 hour') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := m.UndoApprove(id, db.StatusPending, 30*time.Second); !errors.Is(err, db.ErrUndoExpired) {
		t.Fatalf("undo an hour later = %v, want ErrUndoExpired", err)
	}
}

// Republishing keeps the original publish date, and undoing it leaves the
// article unpublished with that date intact.
func TestRepublishKeepsPublishDate(t *testing.T) {
	database := setupFileDB(t)
	m := NewManager(database, nil)
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Old news", Slug: "old-news", Content: "<p>x</p>", Type: "deal",
		Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Approve(id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB().Exec(`UPDATE generated_articles SET published_at = '2026-06-01 09:00:00' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := m.Unpublish(id); err != nil {
		t.Fatal(err)
	}
	if err := m.ApproveRevision(id, 1); err != nil {
		t.Fatal(err)
	}
	a, _ := database.GetGeneratedArticle(id)
	if a.PublishedAt == nil || a.PublishedAt.Format("2006-01-02") != "2026-06-01" {
		t.Fatalf("republished published_at = %v, want the June date kept", a.PublishedAt)
	}
	if err := m.UndoApprove(id, db.StatusUnpublished, time.Minute); err != nil {
		t.Fatalf("undo republish: %v", err)
	}
	a, _ = database.GetGeneratedArticle(id)
	if a.Status != db.StatusUnpublished || a.PublishedAt == nil || a.PublishedAt.Format("2006-01-02") != "2026-06-01" {
		t.Fatalf("after undo: %q %v", a.Status, a.PublishedAt)
	}
}
