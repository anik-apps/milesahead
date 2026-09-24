package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/vote"
)

// setupRealTemplateVoteApp is the real templates plus a real vote service, so a
// test can render the article page and call /api/vote with what it renders.
func setupRealTemplateVoteApp(t *testing.T) *App {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	cfg := &config.Config{Server: config.ServerConfig{Port: 8080, Domain: "localhost"}, Votes: config.VotesConfig{AlertThreshold: 5}}
	app, err := NewAppWithTemplateDir(cfg, database, nil, vote.NewService(database, cfg.Votes, nil), nil, nil, context.Background(), filepath.Join("..", "..", "templates"))
	if err != nil {
		t.Fatalf("failed to create app with real templates: %v", err)
	}
	t.Cleanup(app.WaitBackground)
	return app
}

// Integration: the article page renders the vote group the script looks for,
// with vote values the API accepts. The widget was dead because the script
// looked for .vote-section and sent 1/-1, which the API rejects.
func TestArticleVote_TemplateScriptAndAPIAgree(t *testing.T) {
	app := setupRealTemplateVoteApp(t)
	id, slug := createApprovedArticle(t, app.db, "Vote Contract Article", "deal", false)
	body := getPage(t, app, "/article/"+slug)

	js, err := os.ReadFile(filepath.Join("..", "..", "static", "js", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), `document.querySelector('.vote-buttons[data-article-id]')`) {
		t.Fatal("main.js should select the .vote-buttons group")
	}
	if !regexp.MustCompile(`class="vote-buttons"[^>]*data-article-id="\d+"`).MatchString(body) {
		t.Fatal("the article page should render .vote-buttons with data-article-id")
	}
	if !strings.Contains(body, `<p class="vote-status" role="status"></p>`) {
		t.Error("the vote card needs a status line to confirm the vote")
	}

	values := regexp.MustCompile(`data-vote="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(values) != 2 {
		t.Fatalf("expected two vote buttons, found %d", len(values))
	}
	for i, v := range values {
		req := httptest.NewRequest(http.MethodPost, "/api/vote",
			strings.NewReader(`{"article_id":`+jsonInt64(id)+`,"vote":"`+v[1]+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "192.168.1." + string(rune('1'+i)) + ":1234"
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("the API rejected the template's vote value %q: %d %s", v[1], rec.Code, rec.Body.String())
		}
	}
}

// Counts stay hidden until an article has enough votes to mean something.
func TestArticleVote_CountsHiddenUntilTenVotes(t *testing.T) {
	app := setupRealTemplateVoteApp(t)
	id, slug := createApprovedArticle(t, app.db, "Vote Count Article", "deal", false)

	if body := getPage(t, app, "/article/"+slug); strings.Contains(body, `class="vote-count`) {
		t.Error("with no votes, the page should not show 0 / 0 counts")
	}
	for i := 0; i < minVotesToShowCounts; i++ {
		if _, err := app.db.CreateVote(&db.ArticleVote{ArticleID: id, Vote: 1, IPHash: "ip" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	body := getPage(t, app, "/article/"+slug)
	if !strings.Contains(body, `<span class="vote-count upvotes">10</span>`) {
		t.Error("with ten votes, the counts should be shown")
	}
}
