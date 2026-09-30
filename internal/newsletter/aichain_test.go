package newsletter

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
)

// chainClient answers from live, fails every model in gone with a 404, and
// records the order models were called in.
type chainClient struct {
	calls []string
	gone  map[string]bool
	live  string
}

func (c *chainClient) Generate(model, _ string, _ int) (string, error) {
	c.calls = append(c.calls, model)
	if c.gone[model] {
		return "", &ai.APIError{StatusCode: 404, Model: model, Body: "Not Found"}
	}
	return c.live, nil
}

// TestRankArticles_FallsOverAMissingModel: the pick used to name one model
// (the critic), so its retirement left every issue in default order.
func TestRankArticles_FallsOverAMissingModel(t *testing.T) {
	client := &chainClient{gone: map[string]bool{"missing": true}, live: "[2, 1]"}
	svc := NewServiceWithAI(nil, &mockSender{}, config.NewsletterConfig{Timezone: "UTC"},
		client, []string{"missing", "live"}, "milesahead.example")

	got := svc.rankArticles([]db.GeneratedArticle{{ID: 1, Title: "One"}, {ID: 2, Title: "Two"}})

	if calls := strings.Join(client.calls, ","); calls != "missing,live" {
		t.Errorf("calls = %s; want missing then live", calls)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Errorf("ranking = %v; want the fallback model's [2 1]", got)
	}
}
