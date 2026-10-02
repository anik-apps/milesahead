package enrich

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
)

// promptLog is scriptedClient that also keeps the prompts it was sent.
type promptLog struct {
	scriptedClient
	mu      sync.Mutex
	prompts []string
}

func (c *promptLog) Generate(model, prompt string, maxTokens int) (string, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	return c.scriptedClient.Generate(model, prompt, maxTokens)
}

func (c *promptLog) sent(frag string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, p := range c.prompts {
		if strings.Contains(p, frag) {
			out = append(out, p)
		}
	}
	return out
}

// The stored image an admin turns down: the lobby, at the steps the pick
// path stores.
var (
	storedLobby      = wm + "thumb/6/6c/" + lobbyFile + "/1280px-" + lobbyFile
	storedLobbyThumb = wm + "thumb/6/6c/" + lobbyFile + "/500px-" + lobbyFile
)

const (
	poolFile = "Hotel_pool.jpg"
	pool     = wm + "6/6b/" + poolFile
)

// newImageHosts stands in for Openverse (answering openverse, recording each
// query), the Commons search (nothing) and the image hosts (every file a
// light image). failSearch makes both searches fail.
type newImageHosts struct {
	mu         sync.Mutex
	queries    []string
	openverse  string
	failSearch bool
}

func (h *newImageHosts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Host {
	case "api.openverse.org":
		h.mu.Lock()
		h.queries = append(h.queries, r.URL.Query().Get("q"))
		fail, body := h.failSearch, h.openverse
		h.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(body))
	case "commons.wikimedia.org":
		if h.failSearch && r.URL.Query().Get("titles") == "" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{}`))
	default:
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", "bytes 0-0/60000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	}
}

func (h *newImageHosts) searched() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.queries...)
}

// openverseResults answers an Openverse search with Wikimedia originals.
func openverseResults(urls ...string) string {
	var rs []string
	for i, u := range urls {
		rs = append(rs, fmt.Sprintf(`{"title":"Picture %d","url":%q,"creator":"Creator %d","source":"wikimedia","width":6048}`, i, u, i))
	}
	return `{"results":[` + strings.Join(rs, ",") + `]}`
}

func newImageService(t *testing.T, d *db.Database, hosts *newImageHosts, client *promptLog) *Service {
	t.Helper()
	srv := httptest.NewServer(hosts)
	t.Cleanup(srv.Close)
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})
	s := New(d, client, []string{"m1"}, "Scout", searcher, images.NewScorer(client, []string{"m1"}))
	s.Pace = 0
	return s
}

// seedTurnedDown seeds a published, fully enriched article whose budget was
// spent long ago, and asks for a new image for it.
func seedTurnedDown(t *testing.T, d *db.Database, hint string) int64 {
	t.Helper()
	id := seedImage(t, d, storedLobby, storedLobbyThumb)
	if _, err := d.DB().Exec(`UPDATE generated_articles SET enrichment_attempts = 3 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RequestNewImage(id, hint); err != nil {
		t.Fatal(err)
	}
	return id
}

func storedCredit(t *testing.T, d *db.Database, id int64) string {
	t.Helper()
	var by string
	if err := d.DB().QueryRow(`SELECT image_attribution FROM generated_articles WHERE id = ?`, id).Scan(&by); err != nil {
		t.Fatal(err)
	}
	return by
}

func requestOf(t *testing.T, d *db.Database, id int64) db.ImageRequest {
	t.Helper()
	reqs, err := d.ImageRequestsFor([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	return reqs[id]
}

// Integration: the reconciler serves an admin's request on a published
// article whose budget was spent. It searches with the admin's words, never
// offers the turned-down picture (which the search returns, at another
// size, and which would otherwise score best), tells the scorer what the
// editor asked for, and swaps image, thumbnail and credit in one write that
// also closes the request.
func TestRun_NewImageRequestSwapsWhenFound(t *testing.T) {
	d := testDB(t)
	id := seedTurnedDown(t, d, "Hyatt Regency lobby")

	hosts := &newImageHosts{openverse: openverseResults(lobby, pool)}
	// Openverse order is kept: the lobby would be index 0, the pool 1. With
	// the lobby gone, the pool is index 0, and is the only one scored.
	client := &promptLog{scriptedClient: scriptedClient{reply: map[string]string{
		"image editor": `{"scores":[{"index":0,"score":8}]}`,
	}}}
	s := newImageService(t, d, hosts, client)
	s.Run()

	if q := hosts.searched(); len(q) != 1 || q[0] != "Hyatt Regency lobby" {
		t.Errorf("searched for %q, want only the admin's words", q)
	}
	if len(client.sent("image search queries")) != 0 {
		t.Error("generated search queries although the admin gave words")
	}
	scoring := client.sent("image editor")
	if len(scoring) != 1 || !strings.Contains(scoring[0], "The editor asked for a picture of: Hyatt Regency lobby") {
		t.Errorf("the scorer was not told what the editor asked for: %q", scoring)
	}
	if strings.Contains(strings.Join(scoring, ""), lobbyFile) {
		t.Error("the turned-down picture was offered to the scorer")
	}

	url, thumb, attempts := storedImage(t, d, id)
	base := wm + "thumb/6/6b/" + poolFile + "/"
	if url != base+"1280px-"+poolFile || thumb != base+"500px-"+poolFile {
		t.Errorf("stored %q / %q, want the pool at the sized steps", url, thumb)
	}
	if by := storedCredit(t, d, id); by == "Photo credit" || by == "" {
		t.Errorf("credit = %q, want the new picture's", by)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (a fresh budget, one pass)", attempts)
	}
	if r := requestOf(t, d, id); r.State != "" {
		t.Errorf("request still open after its image landed: %+v", r)
	}
}

// Integration: when nothing but the turned-down picture comes back, the
// request fails at once, visibly, and the article keeps its image. Without
// words of the admin's, the generated queries are used.
func TestRun_NewImageRequestFailsAndKeepsTheImage(t *testing.T) {
	d := testDB(t)
	id := seedTurnedDown(t, d, "")

	hosts := &newImageHosts{openverse: openverseResults(lobby)}
	client := &promptLog{scriptedClient: scriptedClient{reply: map[string]string{
		"image search queries": `["hotel lobby"]`,
		"image editor":         `{"scores":[{"index":0,"score":9}]}`,
	}}}
	s := newImageService(t, d, hosts, client)
	s.Run()

	if q := hosts.searched(); len(q) != 1 || q[0] != "hotel lobby" {
		t.Errorf("searched for %q, want the generated query", q)
	}
	url, thumb, attempts := storedImage(t, d, id)
	if url != storedLobby || thumb != storedLobbyThumb || storedCredit(t, d, id) != "Photo credit" {
		t.Errorf("image changed to %q / %q though nothing was found", url, thumb)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	r := requestOf(t, d, id)
	if !r.Failed() || !strings.Contains(r.LastError, "no images other than ones turned down") {
		t.Errorf("request = %+v, want failed with the reason", r)
	}

	// Not worked on again: the next pass searches nothing.
	s.Run()
	if q := hosts.searched(); len(q) != 1 {
		t.Errorf("a failed request was searched again: %q", q)
	}
}

// Integration: a search that errors is worth retrying, so the request stays
// pending within the attempt budget and fails only when it is spent -- an
// article out of attempts is never listed again, and a request left pending
// would wait for ever.
func TestRun_NewImageRequestRetriesWithinItsBudget(t *testing.T) {
	d := testDB(t)
	id := seedTurnedDown(t, d, "pool")

	hosts := &newImageHosts{failSearch: true}
	client := &promptLog{}
	s := newImageService(t, d, hosts, client)
	s.MaxAttempts = 2

	s.Run()
	if r := requestOf(t, d, id); !r.Pending() {
		t.Fatalf("after one failed search: %+v, want still pending", r)
	}
	s.Run()
	r := requestOf(t, d, id)
	if !r.Failed() || !strings.Contains(r.LastError, "gave up after 2 tries: the image search failed") {
		t.Errorf("request = %+v, want failed once the budget was spent", r)
	}
	if url, _, attempts := storedImage(t, d, id); url != storedLobby || attempts != 2 {
		t.Errorf("image %q after %d attempts, want the old one after 2", url, attempts)
	}
}

// A scorer that could not score (its calls failed, so every candidate comes
// back unscored) is not a verdict on the pictures: the request stays pending
// for another pass instead of failing with "best scored 0".
func TestRun_NewImageRequestSurvivesAScoringOutage(t *testing.T) {
	d := testDB(t)
	id := seedTurnedDown(t, d, "pool")

	hosts := &newImageHosts{openverse: openverseResults(pool)}
	client := &promptLog{scriptedClient: scriptedClient{fail: map[string]error{"image editor": fmt.Errorf("503")}}}
	s := newImageService(t, d, hosts, client)
	s.Run()

	if r := requestOf(t, d, id); !r.Pending() {
		t.Errorf("request = %+v, want still pending after a scoring outage", r)
	}
	if url, _, attempts := storedImage(t, d, id); url != storedLobby || attempts != 1 {
		t.Errorf("image %q after %d attempts, want the old one after 1", url, attempts)
	}
}

// askAgain is promptLog that, while the pass scores the candidates, has the
// admin ask for a new image again.
type askAgain struct {
	*promptLog
	once  sync.Once
	again func()
}

func (c *askAgain) Generate(model, prompt string, maxTokens int) (string, error) {
	if strings.Contains(prompt, "image editor") {
		c.once.Do(c.again)
	}
	return c.promptLog.Generate(model, prompt, maxTokens)
}

// Integration: the admin asks again while a pass is working on their first
// request. The pass finds a picture, but the request it answers has been
// superseded, so the article keeps its image and the newer request stays
// pending, with its words, for the next pass.
func TestRun_NewImageRequestSupersededMidPass(t *testing.T) {
	d := testDB(t)
	id := seedTurnedDown(t, d, "Hyatt Regency lobby")

	hosts := &newImageHosts{openverse: openverseResults(lobby, pool)}
	client := &askAgain{
		promptLog: &promptLog{scriptedClient: scriptedClient{reply: map[string]string{
			"image editor": `{"scores":[{"index":0,"score":8}]}`,
		}}},
		again: func() {
			if _, err := d.RequestNewImage(id, "rooftop pool"); err != nil {
				t.Error(err)
			}
		},
	}
	srv := httptest.NewServer(hosts)
	t.Cleanup(srv.Close)
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})
	s := New(d, client, []string{"m1"}, "Scout", searcher, images.NewScorer(client, []string{"m1"}))
	s.Pace = 0
	s.Run()

	if url, thumb, _ := storedImage(t, d, id); url != storedLobby || thumb != storedLobbyThumb || storedCredit(t, d, id) != "Photo credit" {
		t.Errorf("a superseded pass swapped its image in: %q / %q", url, thumb)
	}
	if r := requestOf(t, d, id); !r.Pending() || r.Seq != 2 || r.Hint != "rooftop pool" {
		t.Errorf("request = %+v, want the newer one still pending", r)
	}
	if list, _ := d.ListArticlesNeedingEnrichment(3, 10); len(list) != 1 || list[0].ImageRequestSeq != 2 {
		t.Errorf("the newer request is not queued for the next pass: %+v", list)
	}
}
