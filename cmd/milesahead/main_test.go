package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/images"
)

// scoreAll is a text model that gives every candidate the same score.
type scoreAll struct{}

func (scoreAll) Generate(_, _ string, _ int) (string, error) {
	return `{"scores":[{"index":0,"score":8,"reason":"r"}]}`, nil
}

// toHost sends every request to one test server.
type toHost struct{ addr string }

func (h toHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = h.addr
	return http.DefaultTransport.RoundTrip(req)
}

// Integration, production wiring: with vision scoring configured, the
// thumbnail of a Wikimedia candidate is requested from Wikimedia before the
// vision endpoint is asked to score it. Without the warm-up the vision
// endpoint was the first client to ask for a new size, met a slow render or
// a 429, and the candidate took the x0.6 penalty.
func TestImageScoring_WarmsThumbnailsBeforeVision(t *testing.T) {
	const thumb = "/wikipedia/commons/thumb/6/6c/Lobby.jpg/500px-Lobby.jpg"
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	wikimedia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("wikimedia " + r.URL.Path)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("j"))
	}))
	defer wikimedia.Close()
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record("vision")
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"the lobby\"}"}}]}`))
	}))
	defer vision.Close()

	cfg := &config.Config{AI: config.AIConfig{HFBaseURL: vision.URL, HFAPIToken: "token", CriticModel: "text"}}
	searcher, scorer := newImageScoring(cfg, scoreAll{}, "vision")
	searcher.SetTransport(toHost{wikimedia.Listener.Addr().String()})

	const wm = "https://upload.wikimedia.org"
	if _, err := scorer.Score("A hotel lobby", "Lobby", []images.ImageCandidate{
		{URL: wm + "/wikipedia/commons/thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg", Thumbnail: wm + thumb, Source: "wikimedia"},
	}); err != nil {
		t.Fatalf("Score: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ", "); got != fmt.Sprintf("wikimedia %s, vision", thumb) {
		t.Errorf("requests: %s; want the thumbnail warmed, then the vision call", got)
	}
}
