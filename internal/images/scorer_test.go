package images

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/kumaranik/milesahead/internal/ai"
)

// textScores is a text model that scores every candidate description in
// prompt order.
type textScores []float64

func (t textScores) Generate(_, _ string, _ int) (string, error) {
	var parts []string
	for i, s := range t {
		parts = append(parts, fmt.Sprintf(`{"index":%d,"score":%v,"reason":"r"}`, i, s))
	}
	return `{"scores":[` + strings.Join(parts, ",") + `]}`, nil
}

// renderingHost stands in for upload.wikimedia.org: it records which
// thumbnail sizes have been requested, i.e. rendered.
type renderingHost struct {
	mu       sync.Mutex
	rendered map[string]bool
}

func (h *renderingHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.rendered[r.URL.Path] = true
	h.mu.Unlock()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write([]byte("j"))
}

func (h *renderingHost) isRendered(rawURL string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := strings.Index(rawURL, "/wikipedia/")
	return i >= 0 && h.rendered[rawURL[i:]]
}

// The vision model fetches each candidate's thumbnail itself. For a sized
// Wikimedia URL it was usually the first client to ask for that size, so it
// met Wikimedia rendering it -- a slow answer or a 429 -- and the candidate
// took the x0.6 penalty however good the image was. Score now has the top
// thumbnails requested before any is sent to the vision model.
func TestScore_WarmsThumbnailsBeforeVision(t *testing.T) {
	host := &renderingHost{rendered: map[string]bool{}}
	searcher := stubSearcher(t, host.ServeHTTP)

	// The vision API fails to fetch a size Wikimedia has not rendered yet.
	var visionCalls int
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls++
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("vision request: %v", err)
		}
		image := req.Messages[0].Content[1].ImageURL.URL
		if !host.isRendered(image) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"could not fetch image: 429 Too Many Requests"}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"the lobby\"}"}}]}`))
	}))
	defer vision.Close()

	scorer := NewScorer(textScores{8, 7}, []string{"text"})
	scorer.SetVisionModel(vision.URL, "token", "vision", searcher)

	const wm = "https://upload.wikimedia.org/wikipedia/commons/thumb/"
	candidates := []ImageCandidate{
		{URL: wm + "6/6c/Lobby.jpg/1280px-Lobby.jpg", Thumbnail: wm + "6/6c/Lobby.jpg/500px-Lobby.jpg", Source: "wikimedia"},
		{URL: wm + "7/7e/Gate.jpg/1280px-Gate.jpg", Thumbnail: wm + "7/7e/Gate.jpg/500px-Gate.jpg", Source: "wikimedia"},
	}
	scored, err := scorer.Score("A hotel lobby", "Lobby", candidates)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if visionCalls != 2 {
		t.Fatalf("vision calls = %d, want 2", visionCalls)
	}
	for _, c := range scored {
		if c.Score != 9 {
			t.Errorf("%s scored %v, want the vision score 9: its thumbnail was not rendered before the vision model asked",
				c.Thumbnail, c.Score)
		}
	}
	// Only the thumbnails were requested, not the display images.
	if host.isRendered(candidates[0].URL) {
		t.Error("warming requested the display image too")
	}
}

// chainClient answers from live, fails every model in gone with a 410, and
// records the order models were called in.
type chainClient struct {
	mu    sync.Mutex
	calls []string
	gone  map[string]bool
	live  string
}

func (c *chainClient) Generate(model, _ string, _ int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, model)
	if c.gone[model] {
		return "", &ai.APIError{StatusCode: 410, Model: model, Body: "end of life"}
	}
	return c.live, nil
}

// TestScorer_TextScoringFallsOverARetiredModel: text scoring used to name one
// model (the critic), so its retirement cost every image its text score.
func TestScorer_TextScoringFallsOverARetiredModel(t *testing.T) {
	client := &chainClient{
		gone: map[string]bool{"retired": true},
		live: `{"scores":[{"index":0,"score":3,"reason":"r"},{"index":1,"score":9,"reason":"r"}]}`,
	}
	scorer := NewScorer(client, []string{"retired", "live"})

	scored, err := scorer.Score("Lounge opens", "A new lounge", []ImageCandidate{
		{URL: "a", Description: "a car park"},
		{URL: "b", Description: "an airport lounge"},
	})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if got := strings.Join(client.calls, ","); got != "retired,live" {
		t.Errorf("calls = %s; want retired then live", got)
	}
	if scored[0].URL != "b" || scored[0].Score != 9 {
		t.Errorf("top = %+v; want the fallback model's scores applied", scored[0])
	}
}

type visionLog struct {
	mu    sync.Mutex
	calls []ai.ModelCall
}

func (l *visionLog) RecordModelCall(c ai.ModelCall) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, c)
}

// visionServer answers as a vision model would: 410 for dead-vision, a score
// for anything else. It records which model each request named.
func visionServer(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	models := []string{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		if req.Model == "dead-vision" {
			w.WriteHeader(410)
			w.Write([]byte(`{"detail":"end of life"}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"brand shown\"}"}}]}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &models
}

func TestVisionScore_MovesToTheNextModelWhenOneIsRetired(t *testing.T) {
	ts, models := visionServer(t)
	s := NewScorer(nil, nil)
	s.SetVisionModel(ts.URL, "token", "dead-vision, live-vision", nil)
	log := &visionLog{}
	s.SetRecorder(log)

	out := s.visionScore("Title", "Summary", []ImageCandidate{
		{Thumbnail: "https://img.example/1.jpg", Score: 5},
		{Thumbnail: "https://img.example/2.jpg", Score: 4},
	})

	if want := []string{"dead-vision", "live-vision", "live-vision"}; !reflect.DeepEqual(*models, want) {
		t.Errorf("models asked %v, want %v", *models, want)
	}
	for i, c := range out {
		if c.Score == 5 || c.Score == 4 {
			t.Errorf("candidate %d kept its text score %v: vision did not score it", i, c.Score)
		}
	}
	if len(log.calls) != 3 || log.calls[0].Outcome != ai.OutcomeRetired || log.calls[1].Outcome != ai.OutcomeOK ||
		log.calls[0].Step != VisionStep {
		t.Errorf("recorded %+v", log.calls)
	}
}

func TestVisionScore_AllModelsGoneKeepsTextScores(t *testing.T) {
	ts, _ := visionServer(t)
	s := NewScorer(nil, nil)
	s.SetVisionModel(ts.URL, "token", "dead-vision", nil)
	out := s.visionScore("Title", "Summary", []ImageCandidate{
		{Thumbnail: "https://img.example/1.jpg", Score: 5},
		{Thumbnail: "https://img.example/2.jpg", Score: 4},
	})
	if out[0].Score != 5 || out[1].Score != 4 {
		t.Errorf("scores %v, %v; want the text scores 5 and 4 restored", out[0].Score, out[1].Score)
	}
}
