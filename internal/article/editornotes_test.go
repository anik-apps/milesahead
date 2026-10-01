package article

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
)

// promptRecorder answers like mockHFClient and keeps every prompt.
type promptRecorder struct {
	mockHFClient
	prompts []string
}

func (m *promptRecorder) Generate(model, prompt string, maxTokens int) (string, error) {
	m.prompts = append(m.prompts, prompt)
	return m.mockHFClient.Generate(model, prompt, maxTokens)
}

const (
	oldNote = "Could not find a link for: exact launch date"
	newNote = "Could not find a link for: the business-only terms page"
)

// A revision of an article stored with an inline note (as the March 2026
// revisions left them): the model is not shown the old note, the rewrite's
// own note is stored apart from the content, and the history row carries none.
func TestProcessRevision_KeepsEditorNotesOutOfContent(t *testing.T) {
	database := setupTestDB(t)
	mock := &promptRecorder{mockHFClient: mockHFClient{responses: []string{
		"Revised Title\n<p>Revised body.</p>\n<div class=\"editor-note\">" + newNote + "</div>",
		`{"score": 92, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}}}
	mgr := NewManager(database, ai.NewPipeline(ai.NewGenerator(mock, "m", "", nil), ai.NewCritic(mock, "m"), 80))

	srcID, err := database.CreateSource(&db.Source{Name: "S", URL: "https://example.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	rawID, err := database.CreateRawArticle(&db.RawArticle{SourceID: srcID, Title: "Raw", URL: "https://example.com/r",
		Content: "Source text.", PublishedAt: time.Now(), Hash: "editor-note-hash"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := database.CreateGeneratedArticle(&db.GeneratedArticle{Title: "T", Slug: "t-notes", Content: "x",
		Type: "deal", Status: db.StatusPending, SourcesJSON: "[]", RevisionNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.LinkArticleSources(id, []int64{rawID}); err != nil {
		t.Fatal(err)
	}
	legacy := `<p>Original body.</p><div class="editor-note">` + oldNote + `</div>`
	if _, err := database.DB().Exec(`UPDATE generated_articles SET content = ? WHERE id = ?`, legacy, id); err != nil {
		t.Fatal(err)
	}

	if err := mgr.RequestRevision(id, "Add the terms link", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := mgr.ProcessRevision(id); err != nil {
		t.Fatal(err)
	}

	if len(mock.prompts) == 0 || !strings.Contains(mock.prompts[0], "Original body.") {
		t.Fatalf("revision prompt missing the article: %v", mock.prompts)
	}
	if strings.Contains(mock.prompts[0], oldNote) {
		t.Error("the old note was sent back to the model as article text")
	}

	var content, notes string
	if err := database.DB().QueryRow(`SELECT content, editor_notes FROM generated_articles WHERE id = ?`, id).
		Scan(&content, &notes); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "editor-note") || !strings.Contains(content, "Revised body.") {
		t.Errorf("stored content %q", content)
	}
	if notes != newNote {
		t.Errorf("stored notes %q, want the rewrite's note", notes)
	}
	revs, err := database.ListRevisionsForArticle(id)
	if err != nil || len(revs) != 1 {
		t.Fatalf("revisions %v %v", revs, err)
	}
	if strings.Contains(revs[0].NewContent+revs[0].PreviousContent, "editor-note") {
		t.Errorf("history stored a note: %+v", revs[0])
	}
}

// A generated article's notes reach the editor_notes column, not the content.
func TestCreateFromPipeline_StoresEditorNotesApart(t *testing.T) {
	database := setupTestDB(t)
	mgr := NewManager(database, nil)
	rawID := createTestSourceAndRawArticle(t, database)
	id, err := mgr.CreateFromPipeline(&ai.PipelineResult{
		Content: "<p>Body.</p>", Title: "Notes Article", Type: "deal", EditorNotes: newNote,
		Verification: &ai.VerificationResult{Score: 90, FactualCheck: true, AttributionCheck: true,
			CompletenessCheck: true, HallucinationCheck: true},
		PassedThreshold: true,
	}, []int64{rawID}, "deal")
	if err != nil {
		t.Fatal(err)
	}
	a, err := database.GetGeneratedArticle(id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Content != "<p>Body.</p>" || a.EditorNotes != newNote {
		t.Errorf("content %q notes %q", a.Content, a.EditorNotes)
	}
}
