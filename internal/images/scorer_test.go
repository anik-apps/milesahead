package images

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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

	scorer := NewScorer(textScores{8, 7}, "text")
	scorer.SetVisionModel(vision.URL, "token", "vision")
	scorer.SetWarmer(searcher.Warm)

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
