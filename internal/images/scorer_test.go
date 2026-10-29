package images

import (
	"bytes"
	"encoding/base64"
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

// The scorer downloads each candidate's thumbnail just before scoring it. For
// a sized Wikimedia URL that was usually the first request for that size, so
// it met Wikimedia rendering it -- a slow answer or a 429 -- and the
// candidate took the x0.6 penalty however good the image was. Score now has
// the top thumbnails requested before any is scored.
func TestScore_WarmsThumbnailsBeforeVision(t *testing.T) {
	host := &renderingHost{rendered: map[string]bool{}}
	searcher := stubSearcher(t, host.ServeHTTP)

	const wm = "https://upload.wikimedia.org/wikipedia/commons/thumb/"
	candidates := []ImageCandidate{
		{URL: wm + "6/6c/Lobby.jpg/1280px-Lobby.jpg", Thumbnail: wm + "6/6c/Lobby.jpg/500px-Lobby.jpg", Source: "wikimedia"},
		{URL: wm + "7/7e/Gate.jpg/1280px-Gate.jpg", Thumbnail: wm + "7/7e/Gate.jpg/500px-Gate.jpg", Source: "wikimedia"},
	}
	thumbs := []string{candidates[0].Thumbnail, candidates[1].Thumbnail}

	// By the first vision call, every thumbnail must have been rendered --
	// the second one included, which the scorer has not downloaded yet.
	var visionCalls int
	vision := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls++
		for _, u := range thumbs {
			if !host.isRendered(u) {
				t.Errorf("vision call %d: %s not rendered yet", visionCalls, u)
			}
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"the lobby\"}"}}]}`))
	}))
	defer vision.Close()

	scorer := NewScorer(textScores{8, 7}, []string{"text"})
	scorer.SetVisionModel(vision.URL, "token", "vision", searcher)
	scorer.SetImageTransport(searcher.httpClient.Transport)

	scored, err := scorer.Score("A hotel lobby", "Lobby", candidates)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if visionCalls != 2 {
		t.Fatalf("vision calls = %d, want 2", visionCalls)
	}
	for _, c := range scored {
		if c.Score != 9 {
			t.Errorf("%s scored %v, want the vision score 9", c.Thumbnail, c.Score)
		}
	}
	// Only the thumbnails were requested, not the display images.
	if host.isRendered(candidates[0].URL) {
		t.Error("warming requested the display image too")
	}
}

// imageHost answers every thumbnail download without the network: status,
// Content-Type and body as given, and it records each request's User-Agent.
type imageHost struct {
	status int
	ctype  string
	body   []byte

	mu     sync.Mutex
	agents []string
}

func (h *imageHost) RoundTrip(r *http.Request) (*http.Response, error) {
	h.mu.Lock()
	h.agents = append(h.agents, r.Header.Get("User-Agent"))
	h.mu.Unlock()
	return &http.Response{StatusCode: h.status, Header: http.Header{"Content-Type": {h.ctype}},
		Body: io.NopCloser(bytes.NewReader(h.body)), Request: r}, nil
}

func jpegHost() *imageHost {
	return &imageHost{status: http.StatusOK, ctype: "image/jpeg", body: []byte("jpeg bytes")}
}

// visionRequests is a vision model that scores 9 and keeps each request body.
func visionRequests(t *testing.T) (*httptest.Server, *[]visionRequest) {
	var mu sync.Mutex
	reqs := []visionRequest{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req visionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("vision request: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"score\": 9, \"reason\": \"brand shown\"}"}}]}`))
	}))
	t.Cleanup(ts.Close)
	return ts, &reqs
}

// imageSent is the image a vision request carried: a data URI or a URL.
func imageSent(t *testing.T, req visionRequest) string {
	t.Helper()
	var parts []struct {
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(req.Messages[0].Content, &parts); err != nil || len(parts) != 2 {
		t.Fatalf("vision content %s: %v", req.Messages[0].Content, err)
	}
	return parts[1].ImageURL.URL
}

// The provider used to fetch each thumbnail by URL, and Wikimedia refuses its
// fetcher (403 from one model, 500 from the other, each recorded against the
// model). The scorer now downloads the thumbnail with its own User-Agent and
// sends the bytes, and asks the model not to think first.
func TestVisionScore_SendsTheThumbnailInlineWithThinkingOff(t *testing.T) {
	ts, reqs := visionRequests(t)
	host := jpegHost()
	s := NewScorer(nil, nil)
	s.SetVisionModel(ts.URL, "token", "vision", nil)
	s.SetImageTransport(host)

	s.visionScore("Title", "Summary", []ImageCandidate{{Thumbnail: "https://upload.wikimedia.org/t/500px-a.jpg", Score: 5}})

	if len(*reqs) != 1 {
		t.Fatalf("vision calls = %d, want 1", len(*reqs))
	}
	want := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(host.body)
	if got := imageSent(t, (*reqs)[0]); got != want {
		t.Errorf("image sent = %q, want %q", got, want)
	}
	if got := (*reqs)[0].ChatTemplateKwargs; !reflect.DeepEqual(got, map[string]any{"enable_thinking": false}) {
		t.Errorf("chat_template_kwargs = %v, want enable_thinking false", got)
	}
	if !reflect.DeepEqual(host.agents, []string{userAgent}) {
		t.Errorf("download User-Agents = %v, want [%s]", host.agents, userAgent)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A thumbnail the scorer cannot download is not sent: no model is asked about
// it or blamed for it, and it takes the failure penalty.
func TestVisionScore_ThumbnailThatCannotBeDownloadedIsNotSent(t *testing.T) {
	for _, refused := range []struct {
		status int
		ctype  string
	}{
		{http.StatusForbidden, "text/html"}, // Wikimedia without a User-Agent
		{http.StatusOK, "text/html"},        // a page, not an image
	} {
		ts, reqs := visionRequests(t)
		s := NewScorer(nil, nil)
		s.SetVisionModel(ts.URL, "token", "vision", nil)
		good := jpegHost()
		s.SetImageTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "refused.example" {
				return &http.Response{StatusCode: refused.status, Header: http.Header{"Content-Type": {refused.ctype}},
					Body: io.NopCloser(strings.NewReader("no")), Request: r}, nil
			}
			return good.RoundTrip(r)
		}))
		log := &visionLog{}
		s.SetRecorder(log)

		// The second candidate scores, so the penalty is not undone by the
		// all-failed restore.
		out := s.visionScore("Title", "Summary", []ImageCandidate{
			{Thumbnail: "https://refused.example/x.jpg", Score: 5},
			{Thumbnail: "https://ok.example/y.jpg", Score: 4},
		})
		if len(*reqs) != 1 || len(log.calls) != 1 {
			t.Errorf("%d %s: %d vision calls, %d recorded; want 1 each (the downloadable one)",
				refused.status, refused.ctype, len(*reqs), len(log.calls))
		}
		for _, c := range out {
			if want := map[string]float64{"https://refused.example/x.jpg": 3, "https://ok.example/y.jpg": 9}[c.Thumbnail]; c.Score != want {
				t.Errorf("%d %s: %s scored %v, want %v", refused.status, refused.ctype, c.Thumbnail, c.Score, want)
			}
		}
	}
}

// A thumbnail too large to send inline is sent by URL, as before.
func TestVisionScore_LargeThumbnailIsSentByURL(t *testing.T) {
	ts, reqs := visionRequests(t)
	s := NewScorer(nil, nil)
	s.SetVisionModel(ts.URL, "token", "vision", nil)
	s.SetImageTransport(&imageHost{status: http.StatusOK, ctype: "image/jpeg", body: make([]byte, maxInlineImage+1)})

	const thumb = "https://images.example/big.jpg"
	s.visionScore("Title", "Summary", []ImageCandidate{{Thumbnail: thumb, Score: 5}})

	if len(*reqs) != 1 {
		t.Fatalf("vision calls = %d, want 1", len(*reqs))
	}
	if got := imageSent(t, (*reqs)[0]); got != thumb {
		t.Errorf("image sent = %.60q, want the URL %q", got, thumb)
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
	s.SetImageTransport(jpegHost())
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
	s.SetImageTransport(jpegHost())
	out := s.visionScore("Title", "Summary", []ImageCandidate{
		{Thumbnail: "https://img.example/1.jpg", Score: 5},
		{Thumbnail: "https://img.example/2.jpg", Score: 4},
	})
	if out[0].Score != 5 || out[1].Score != 4 {
		t.Errorf("scores %v, %v; want the text scores 5 and 4 restored", out[0].Score, out[1].Score)
	}
}
