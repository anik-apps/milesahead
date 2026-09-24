package images

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProbe_Status(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    bool
	}{
		{
			name:    "200 is fetchable",
			handler: func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("jpegbytes")) },
			want:    true,
		},
		{
			name: "206 is fetchable",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte("j"))
			},
			want: true,
		},
		{
			// Openverse's proxy answers 424 when its upstream fetch fails.
			name:    "424 is not fetchable",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusFailedDependency) },
			want:    false,
		},
		{
			// Pixabay's expired hotlinks answer 400.
			name:    "400 is not fetchable",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			want:    false,
		},
		{
			name:    "404 is not fetchable",
			handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
			want:    false,
		},
	}

	s := NewSearcher("", "")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			if got := s.Probe(srv.URL+"/img.jpg", 0).OK; got != tt.want {
				t.Errorf("Probe OK = %v, want %v", got, tt.want)
			}
		})
	}
}

// Several image CDNs reject HEAD while serving GET, so probing with HEAD
// would discard good candidates.
func TestProbe_UsesRangedGETNotHEAD(t *testing.T) {
	var gotMethod, gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotRange = r.Method, r.Header.Get("Range")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Write([]byte("jpegbytes"))
	}))
	defer srv.Close()

	if !NewSearcher("", "").Probe(srv.URL+"/img.jpg", 0).OK {
		t.Fatal("Probe not OK for a host that serves GET but rejects HEAD")
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotRange == "" {
		t.Error("no Range header sent; the probe would download whole images")
	}
}

func TestProbe_EmptyAndUnparseable(t *testing.T) {
	s := NewSearcher("", "")
	for _, u := range []string{"", "://not a url", "http://"} {
		if s.Probe(u, 0).OK {
			t.Errorf("Probe(%q) OK, want not", u)
		}
	}
}

// The Openverse decoder must prefer the provider's own CDN link over the
// api.openverse.org thumbnail proxy, which is what rotted on the homepage.
func TestOpenverseCandidates_PreferDirectURLOverProxy(t *testing.T) {
	body := `{"results":[
		{"title":"Lobby","url":"https://live.staticflickr.com/1/2_b.jpg",
		 "thumbnail":"https://api.openverse.org/v1/images/abc/thumb/",
		 "creator":"Someone","source":"flickr","tags":[{"name":"hotel"}]},
		{"title":"No direct","url":"",
		 "thumbnail":"https://api.openverse.org/v1/images/def/thumb/",
		 "creator":"","source":"flickr","tags":[]}
	]}`
	got := decodeOpenverse(t, body)

	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Thumbnail != "https://live.staticflickr.com/1/2_b.jpg" {
		t.Errorf("thumbnail = %q, want the direct CDN URL", got[0].Thumbnail)
	}
	if strings.Contains(got[0].Thumbnail, "api.openverse.org") {
		t.Error("thumbnail still points at the Openverse proxy")
	}
	// With no direct URL there is nothing better than the proxy -- but both
	// fields must be filled, or the candidate is unusable downstream.
	if got[1].Thumbnail != "https://api.openverse.org/v1/images/def/thumb/" {
		t.Errorf("fallback thumbnail = %q, want the proxy", got[1].Thumbnail)
	}
	if got[1].URL != got[1].Thumbnail {
		t.Errorf("fallback URL = %q, want the proxy so the candidate stays usable", got[1].URL)
	}
}

// decodeOpenverse runs searchOpenverse's body against a stub server by
// pointing the searcher's HTTP client at it via a rewriting transport.
func decodeOpenverse(t *testing.T, body string) []ImageCandidate {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	s := NewSearcher("", "")
	s.httpClient = srv.Client()
	s.httpClient.Transport = rewriteHost{srv.Listener.Addr().String(), s.httpClient.Transport}

	got, err := s.searchOpenverse("hotel lobby")
	if err != nil {
		t.Fatalf("searchOpenverse: %v", err)
	}
	return got
}

// rewriteHost sends every request to addr regardless of the URL the caller
// built, so provider functions with hardcoded endpoints can be tested.
type rewriteHost struct {
	addr string
	next http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = r.addr
	next := r.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

// Openverse returns the provider's full-size file. For Wikimedia that was a
// 6048px, 13.2 MB camera original stored as both image and thumbnail.
func TestOpenverseCandidates_AskForSizedFiles(t *testing.T) {
	body := `{"results":[
		{"title":"Lobby","source":"wikimedia","width":6048,
		 "url":"https://upload.wikimedia.org/wikipedia/commons/6/6c/Lobby.jpg",
		 "thumbnail":"https://api.openverse.org/v1/images/a/thumb/"},
		{"title":"Unknown width","source":"wikimedia",
		 "url":"https://upload.wikimedia.org/wikipedia/commons/1/1d/Gate.jpg",
		 "thumbnail":"https://api.openverse.org/v1/images/b/thumb/"},
		{"title":"Lounge","source":"flickr","width":1024,
		 "url":"https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg",
		 "thumbnail":"https://api.openverse.org/v1/images/c/thumb/"}
	]}`
	got := decodeOpenverse(t, body)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}

	const lobby = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/"
	if got[0].URL != lobby+"1280px-Lobby.jpg" || got[0].Thumbnail != lobby+"500px-Lobby.jpg" {
		t.Errorf("wikimedia original: URL %q, thumbnail %q; want the 1280 and 500 steps",
			got[0].URL, got[0].Thumbnail)
	}
	// With no width, any step might upscale: keep the original and let the
	// byte cap at pick time decide whether it is usable at all.
	if got[1].URL != "https://upload.wikimedia.org/wikipedia/commons/1/1d/Gate.jpg" || got[1].Thumbnail != got[1].URL {
		t.Errorf("unknown width: URL %q, thumbnail %q; want the original kept", got[1].URL, got[1].Thumbnail)
	}
	if got[2].URL != "https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg" ||
		got[2].Thumbnail != "https://live.staticflickr.com/65535/54420209608_0aa8ea537a.jpg" {
		t.Errorf("flickr: URL %q, thumbnail %q; want _b kept and the 500px size as thumbnail",
			got[2].URL, got[2].Thumbnail)
	}
}

// The Commons API now appends utm_* parameters and serves thumbnails from
// thumb.wikimedia.org. Raster files are sized from the original; formats that
// cannot be (SVG here) use the API's own rendering, minus the tracking.
func TestWikimediaCandidates_SizedFromOriginal(t *testing.T) {
	body := `{"query":{"pages":{
		"1":{"title":"File:Lobby.jpg","imageinfo":[{"width":6048,
			"url":"https://upload.wikimedia.org/wikipedia/commons/6/6c/Lobby.jpg?utm_source=commons.wikimedia.org&utm_content=original",
			"thumburl":"https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/500px-Lobby.jpg?utm_source=commons.wikimedia.org"}]},
		"2":{"title":"File:Map.svg","imageinfo":[{"width":4000,
			"url":"https://upload.wikimedia.org/wikipedia/commons/a/ab/Map.svg?utm_source=x",
			"thumburl":"https://thumb.wikimedia.org/wikipedia/commons/thumb/a/ab/Map.svg/500px-Map.svg.png?utm_source=x"}]}
	}}}`
	var gotQuery string
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(body))
	})
	got, err := s.searchWikimedia("hotel lobby")
	if err != nil {
		t.Fatalf("searchWikimedia: %v", err)
	}
	if !strings.Contains(gotQuery, "iiprop=url|size") && !strings.Contains(gotQuery, "iiprop=url%7Csize") {
		t.Errorf("query %q does not ask for the original's size", gotQuery)
	}
	byName := map[string]ImageCandidate{}
	for _, c := range got {
		byName[c.Description] = c
	}
	lobby := byName["Lobby"]
	if lobby.URL != "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg" ||
		lobby.Thumbnail != "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/500px-Lobby.jpg" {
		t.Errorf("Lobby: URL %q, thumbnail %q", lobby.URL, lobby.Thumbnail)
	}
	svg := byName["Map"]
	if svg.URL != "https://thumb.wikimedia.org/wikipedia/commons/thumb/a/ab/Map.svg/500px-Map.svg.png" || svg.Thumbnail != svg.URL {
		t.Errorf("Map.svg: URL %q, thumbnail %q; want the API's rendering without utm_*", svg.URL, svg.Thumbnail)
	}
}

// Wikimedia answers Go's default User-Agent with 403, so without our own
// every Wikimedia candidate failed its probe.
func TestProbe_SendsUserAgentAndReadsContentRange(t *testing.T) {
	var ua string
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		ua = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", "bytes 0-0/13218990")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	})
	p := s.Probe("https://upload.wikimedia.org/wikipedia/commons/6/6c/Lobby.jpg", MaxImageBytes)
	if !p.OK || p.Size != 13218990 || !p.IsImage() {
		t.Errorf("Probe = %+v, want OK, 13218990 bytes, an image", p)
	}
	if !strings.HasPrefix(ua, "MilesAhead/") {
		t.Errorf("User-Agent = %q, want the MilesAhead agent", ua)
	}
}

// Flickr and rawpixel ignore Range and state no length, so the size has to be
// measured -- but never more than one byte past the cap.
func TestProbe_MeasuresUndeclaredSize(t *testing.T) {
	const size = 3000
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < size; i += 100 {
			w.Write([]byte(strings.Repeat("x", 100)))
			w.(http.Flusher).Flush() // chunked: no Content-Length
		}
	})
	if p := s.Probe("https://live.staticflickr.com/1/2_abc_b.jpg", 10_000); !p.OK || p.Size != size {
		t.Errorf("under the cap: Probe = %+v, want the measured %d bytes", p, size)
	}
	if p := s.Probe("https://live.staticflickr.com/1/2_abc_b.jpg", 1000); p.Size != 1001 {
		t.Errorf("over the cap: Size = %d, want reading to stop at cap+1 (1001)", p.Size)
	}
}

// Wikimedia answers 429 with Retry-After: 1 while it renders a size nobody
// has asked for yet, then serves it.
func TestProbe_RetriesThrottledRequests(t *testing.T) {
	calls := 0
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("j"))
	})
	var waited []time.Duration
	s.sleep = func(d time.Duration) { waited = append(waited, d) }

	if !s.Probe("https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/L.jpg/500px-L.jpg", 0).OK {
		t.Fatal("Probe not OK after the throttle cleared")
	}
	if calls != 3 || len(waited) != 2 || waited[0] != time.Second {
		t.Errorf("calls = %d, waits = %v; want 3 calls and two 1s waits", calls, waited)
	}

	// A host that never stops throttling is given up on, not waited on forever.
	calls = -100
	if s.Probe("https://upload.wikimedia.org/x.jpg", 0).OK {
		t.Error("Probe OK for a host that only ever answers 429")
	}
}

// slowHost answers every request with a tiny image, after the delay planned
// for that path's nth request (none once the plan runs out), and counts the
// requests per path.
type slowHost struct {
	mu    sync.Mutex
	plan  map[string][]time.Duration
	calls map[string]int
}

func (h *slowHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	n := h.calls[r.URL.Path]
	h.calls[r.URL.Path]++
	var delay time.Duration
	if plan := h.plan[r.URL.Path]; n < len(plan) {
		delay = plan[n]
	}
	h.mu.Unlock()
	time.Sleep(delay)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write([]byte("j"))
}

func (h *slowHost) count(path string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[path]
}

// Wikimedia renders a thumbnail size the first time anyone asks for it. On
// ~30 fresh sizes that took 0.2-3 s mostly, 4-7 s several times and ~10 s
// once, and the probe gave up at 8 s without a retry, so the best Wikimedia
// candidate was sometimes refused for being slow once. Its thumbnails get a
// longer timeout and one retry after a timeout, within a bound on the whole
// probe; everything else keeps the short timeout and no retry.
func TestProbe_WaitsForWikimediaRenders(t *testing.T) {
	const thumb = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/L.jpg/"
	slow, slower := 100*time.Millisecond, 250*time.Millisecond
	host := &slowHost{calls: map[string]int{}, plan: map[string][]time.Duration{
		"/wikipedia/commons/thumb/6/6c/L.jpg/500px-L.jpg":  {slow},
		"/wikipedia/commons/thumb/6/6c/L.jpg/960px-L.jpg":  {slower},
		"/wikipedia/commons/thumb/6/6c/L.jpg/1280px-L.jpg": {slower, slower, slower},
		"/wikipedia/commons/6/6c/L.jpg":                    {slow},
		"/other.jpg":                                       {slow},
	}}
	s := stubSearcher(t, host.ServeHTTP)
	s.probeTimeout = 50 * time.Millisecond
	s.renderTimeout = 150 * time.Millisecond
	s.probeBudget = time.Second

	for _, tt := range []struct {
		name  string
		url   string
		ok    bool
		calls int
	}{
		{"render slower than the usual timeout", thumb + "500px-L.jpg", true, 1},
		{"render slower than the render timeout, then done", thumb + "960px-L.jpg", true, 2},
		{"retried once, not again", thumb + "1280px-L.jpg", false, 2},
		// Originals and other hosts serve stored files: nothing to render.
		{"Wikimedia original", "https://upload.wikimedia.org/wikipedia/commons/6/6c/L.jpg", false, 1},
		{"other host", "https://img.example/other.jpg", false, 1},
	} {
		u, _ := url.Parse(tt.url)
		if got := s.Probe(tt.url, 0).OK; got != tt.ok {
			t.Errorf("%s: OK = %v, want %v", tt.name, got, tt.ok)
		}
		if got := host.count(u.Path); got != tt.calls {
			t.Errorf("%s: %d requests, want %d", tt.name, got, tt.calls)
		}
	}
}

// budgetHost stands in for an image host that hangs: a request to a path
// under /hang/ gets no answer until the client gives up; /throttle/ always
// answers 429 with Retry-After: 1; /fits/ is a 1 KB image; /unknown/
// answers a range with a total of "*", and /unknown-stall/ does the same but
// stalls its body. It counts ranged and plain requests separately.
type budgetHost struct {
	mu            sync.Mutex
	ranged, plain int
}

func (h *budgetHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if r.Header.Get("Range") != "" {
		h.ranged++
	} else {
		h.plain++
	}
	h.mu.Unlock()
	hang := func() {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	switch {
	case strings.Contains(r.URL.Path, "/hang/"):
		hang()
	case strings.Contains(r.URL.Path, "/throttle/"):
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	case strings.Contains(r.URL.Path, "/fits/"):
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", "bytes 0-0/1000")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	case r.Header.Get("Range") == "":
		hang() // the plain GET that would measure the file
	default:
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", "bytes 0-0/*")
		w.WriteHeader(http.StatusPartialContent)
		if strings.Contains(r.URL.Path, "/unknown-stall/") {
			w.(http.Flusher).Flush()
			hang()
		}
		w.Write([]byte("j"))
	}
}

func (h *budgetHost) counts() (ranged, plain int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ranged, h.plain
}

// maxProbeDuration bounds a whole probe: every request, the retry after a
// timeout, 429 waits, and the plain GET that measures a file of unknown size.
// Each case would run far past the budget, or make a request it should not,
// if one of those ignored it. (The render timeout and the plain GET's own
// timeout are set well above the budget, so only the budget can stop them.)
func TestProbe_StaysWithinItsBudget(t *testing.T) {
	const budget = 150 * time.Millisecond
	const wm = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/L.jpg/"
	for _, tt := range []struct {
		name        string
		url         string
		measureUpTo int64
		sleep       func(time.Duration)
		probeBudget time.Duration
		ranged      int // requests expected
		plain       int
		waits       int
	}{
		// Without the budget: 2 s, then a 2 s retry.
		{name: "render that never finishes", url: wm + "hang/500px-L.jpg", ranged: 1},
		// Without it: two 1 s waits and three requests.
		{name: "throttled past the budget", url: "https://img.example/throttle/a.jpg", ranged: 1},
		// A wait that overruns (a busy machine) leaves no time for another
		// request. Without the check, that request has no timeout at all.
		{name: "no request after the budget is spent", url: "https://img.example/throttle/b.jpg",
			sleep: func(time.Duration) { time.Sleep(1200 * time.Millisecond) }, probeBudget: 1100 * time.Millisecond,
			ranged: 1, waits: 1},
		// Without it the plain GET waits its own 2 s.
		{name: "measuring an unknown size", url: "https://img.example/unknown/a.jpg", measureUpTo: 1 << 20,
			ranged: 1, plain: 1},
		// The ranged answer's body stalls past the budget: nothing is left
		// for the plain GET, which is not sent.
		{name: "no measuring after the budget is spent", url: "https://img.example/unknown-stall/a.jpg", measureUpTo: 1 << 20,
			ranged: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			host := &budgetHost{}
			s := stubSearcher(t, host.ServeHTTP)
			s.probeTimeout, s.renderTimeout, s.probeBudget = 2*time.Second, 2*time.Second, budget
			if tt.probeBudget > 0 {
				s.probeBudget = tt.probeBudget
			}
			var waits int
			s.sleep = func(d time.Duration) {
				waits++
				if tt.sleep != nil {
					tt.sleep(d)
				}
			}
			start := time.Now()
			p := s.Probe(tt.url, tt.measureUpTo)
			elapsed := time.Since(start)
			if p.OK && p.Size >= 0 {
				t.Errorf("Probe = %+v; want no confirmed image", p)
			}
			if limit := s.probeBudget + 600*time.Millisecond; elapsed > limit {
				t.Errorf("probe took %v; its budget is %v", elapsed, s.probeBudget)
			}
			ranged, plain := host.counts()
			if ranged != tt.ranged || plain != tt.plain || waits != tt.waits {
				t.Errorf("%d ranged, %d plain requests and %d waits; want %d, %d and %d",
					ranged, plain, waits, tt.ranged, tt.plain, tt.waits)
			}
		})
	}
}

// Usable is given the pass's deadline, and no probe runs past it: a hanging
// host cannot hold a pass beyond its end, however long a single probe may
// take on its own.
func TestUsable_StopsAtItsDeadline(t *testing.T) {
	host := &budgetHost{}
	s := stubSearcher(t, host.ServeHTTP) // production timeouts: 15 s a render, 30 s a probe
	const wm = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/L.jpg/"
	c := ImageCandidate{URL: wm + "hang/1280px-L.jpg", Thumbnail: wm + "hang/500px-L.jpg"}

	// The thumbnail hangs; or it loads, and the display image hangs.
	for _, c := range []ImageCandidate{c, {URL: c.URL, Thumbnail: wm + "fits/500px-L.jpg"}} {
		start := time.Now()
		if s.Usable(c, start.Add(150*time.Millisecond)) {
			t.Errorf("Usable(%s) = true for a host that never answers", c.Thumbnail)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("Usable(%s) took %v; its deadline was 150 ms away", c.Thumbnail, elapsed)
		}
	}
	// A deadline already past sends nothing.
	if s.Usable(c, time.Now().Add(-time.Second)) {
		t.Error("Usable = true past its deadline")
	}
	if ranged, _ := host.counts(); ranged != 3 {
		t.Errorf("%d requests; want 3, and none once the deadline had passed", ranged)
	}
}

// Warm runs outside any pass deadline, so it waits for one render timeout
// and no longer: no retry after a timeout, as a probe would make. A render
// cut off by the timeout carries on at Wikimedia regardless.
func TestWarm_WaitsOneRenderTimeout(t *testing.T) {
	const path = "/wikipedia/commons/thumb/6/6c/L.jpg/500px-L.jpg"
	host := &slowHost{calls: map[string]int{}, plan: map[string][]time.Duration{
		path: {300 * time.Millisecond},
	}}
	s := stubSearcher(t, host.ServeHTTP)
	s.renderTimeout = 100 * time.Millisecond
	s.probeBudget = time.Second

	start := time.Now()
	s.Warm([]string{"https://upload.wikimedia.org" + path})
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("Warm took %v; want about the 100 ms render timeout", elapsed)
	}
	if got := host.count(path); got != 1 {
		t.Errorf("%d requests; want 1, no retry", got)
	}
}

// Warm asks for the Wikimedia thumbnails in a list, once each, so the vision
// model is not the first client to request a size that has to be rendered.
// Other URLs are served from stored files and are left alone.
func TestWarm_RequestsOnlyWikimediaThumbnails(t *testing.T) {
	host := &slowHost{calls: map[string]int{}}
	s := stubSearcher(t, host.ServeHTTP)
	a := "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/A.jpg/500px-A.jpg"
	b := "https://thumb.wikimedia.org/wikipedia/commons/thumb/7/7e/B.jpg/500px-B.jpg"
	s.Warm([]string{a, b, a,
		"https://upload.wikimedia.org/wikipedia/commons/6/6c/A.jpg",
		"https://live.staticflickr.com/1/2_abc.jpg",
		"",
	})
	for path, want := range map[string]int{
		"/wikipedia/commons/thumb/6/6c/A.jpg/500px-A.jpg": 1,
		"/wikipedia/commons/thumb/7/7e/B.jpg/500px-B.jpg": 1,
		"/wikipedia/commons/6/6c/A.jpg":                   0,
		"/1/2_abc.jpg":                                    0,
	} {
		if got := host.count(path); got != want {
			t.Errorf("%s: %d requests, want %d", path, got, want)
		}
	}
}

// Usable is the pick-time guard: whatever the URL looks like, a file heavier
// than its slot is not stored.
func TestUsable_EnforcesByteCaps(t *testing.T) {
	sizes := map[string]int64{
		"/thumb-ok.jpg": 60_000, "/thumb-huge.jpg": 2_975_317,
		"/hero-ok.jpg": 300_000, "/hero-huge.jpg": 13_218_990,
	}
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", sizes[r.URL.Path]))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	})
	h := "https://img.example/"
	tests := []struct {
		name string
		c    ImageCandidate
		want bool
	}{
		{"both within caps", ImageCandidate{URL: h + "hero-ok.jpg", Thumbnail: h + "thumb-ok.jpg"}, true},
		{"hero too heavy", ImageCandidate{URL: h + "hero-huge.jpg", Thumbnail: h + "thumb-ok.jpg"}, false},
		{"thumbnail too heavy", ImageCandidate{URL: h + "hero-ok.jpg", Thumbnail: h + "thumb-huge.jpg"}, false},
		// One URL in both fields is shown as a thumbnail, so the tighter cap
		// applies: 300 KB passes, 3 MB (the Even Hotel original) does not.
		{"shared URL, light", ImageCandidate{URL: h + "hero-ok.jpg", Thumbnail: h + "hero-ok.jpg"}, true},
		{"shared URL, heavy", ImageCandidate{URL: h + "thumb-huge.jpg", Thumbnail: h + "thumb-huge.jpg"}, false},
	}
	for _, tt := range tests {
		if got := s.Usable(tt.c, time.Time{}); got != tt.want {
			t.Errorf("%s: Usable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// CheckFit is the guard every stored image URL passes. A 200 that is not an
// image -- a host's HTML error or login page -- would be a broken <img>, so it
// is refused at pick time just as a 404 is.
func TestCheckFit(t *testing.T) {
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/error-page.jpg":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html>Service unavailable</html>"))
			return
		case "/gone.jpg":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/unlabelled.jpg":
			w.Header()["Content-Type"] = nil // no Content-Type header at all
		default:
			w.Header().Set("Content-Type", "image/jpeg")
		}
		size := 60_000
		if r.URL.Path == "/huge.jpg" {
			size = 2_000_000
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", size))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	})
	h := "https://img.example/"
	for _, tt := range []struct {
		path string
		want Fit
	}{
		{"ok.jpg", Fits},
		{"huge.jpg", TooHeavy},
		{"error-page.jpg", Unusable},
		{"unlabelled.jpg", Unusable},
		{"gone.jpg", Unusable},
	} {
		if got := s.CheckFit(h+tt.path, MaxThumbnailBytes); got != tt.want {
			t.Errorf("CheckFit(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if s.Usable(ImageCandidate{URL: h + "ok.jpg", Thumbnail: h + "error-page.jpg"}, time.Time{}) {
		t.Error("Usable accepted a thumbnail that answers with an HTML page")
	}
	if s.Usable(ImageCandidate{URL: h + "error-page.jpg", Thumbnail: h + "ok.jpg"}, time.Time{}) {
		t.Error("Usable accepted a display image that answers with an HTML page")
	}
}

// A 206 whose Content-Range total is "*" says nothing about the file's size.
// Read as -1, it passed every byte cap, so a multi-megabyte file could be
// stored; the size has to be measured like a 200 that states no length.
func TestCheckFit_UnknownRangeTotalIsMeasured(t *testing.T) {
	sizes := map[string]int{"/light.jpg": 60_000, "/heavy.jpg": 2_000_000}
	var unranged int
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-0/*")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("j"))
			return
		}
		unranged++
		if r.URL.Path == "/unmeasurable.jpg" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(strings.Repeat("x", sizes[r.URL.Path])))
	})
	h := "https://img.example/"
	for _, tt := range []struct {
		path string
		want Fit
	}{
		{"light.jpg", Fits},
		{"heavy.jpg", TooHeavy},
		// Neither the range nor a plain GET says how large it is.
		{"unmeasurable.jpg", Unusable},
	} {
		if got := s.CheckFit(h+tt.path, MaxThumbnailBytes); got != tt.want {
			t.Errorf("CheckFit(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if unranged != 3 {
		t.Errorf("made %d unranged requests, want one per URL to measure it", unranged)
	}
	// A status-only probe does not need the size and does not fetch the file.
	unranged = 0
	if p := s.Probe(h+"heavy.jpg", 0); !p.OK || unranged != 0 {
		t.Errorf("Probe(_, 0) = %+v with %d unranged requests; want OK and none", p, unranged)
	}
}

// Weigh reports a size only when it is exact: the resize compares an over-cap
// copy with the file a row holds, and a count that stopped at the cap is only
// a lower bound.
func TestWeigh_ReportsOnlyExactSizes(t *testing.T) {
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		switch r.URL.Path {
		case "/stated.jpg":
			w.Header().Set("Content-Range", "bytes 0-0/2000000")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("j"))
		case "/counted.jpg", "/small.jpg":
			n := 2_000_000
			if r.URL.Path == "/small.jpg" {
				n = 3000
			}
			w.WriteHeader(http.StatusOK)
			for i := 0; i < n; i += 1000 {
				w.Write([]byte(strings.Repeat("x", 1000)))
				w.(http.Flusher).Flush() // chunked: no Content-Length
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	h := "https://img.example/"
	for _, tt := range []struct {
		path string
		fit  Fit
		size int64
	}{
		{"stated.jpg", TooHeavy, 2_000_000},
		{"counted.jpg", TooHeavy, -1},
		{"small.jpg", Fits, 3000},
		{"missing.jpg", Unusable, -1},
	} {
		if fit, size := s.Weigh(h+tt.path, MaxImageBytes); fit != tt.fit || size != tt.size {
			t.Errorf("Weigh(%s) = %v, %d; want %v, %d", tt.path, fit, size, tt.fit, tt.size)
		}
	}
}

func TestWikimediaWidths(t *testing.T) {
	var titles string
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		titles = r.URL.Query().Get("titles")
		w.Write([]byte(`{"query":{"pages":[
			{"title":"File:DZ6 0471 Lobby.jpg","imageinfo":[{"width":6048}]},
			{"title":"File:Capital One advert, Newport.jpg","missing":true}
		]}}`))
	})
	lobby := "https://upload.wikimedia.org/wikipedia/commons/6/6c/DZ6_0471_Lobby.jpg"
	gone := "https://upload.wikimedia.org/wikipedia/commons/c/c7/Capital_One_advert%2C_Newport.jpg"
	got, err := s.WikimediaWidths([]string{
		lobby, gone,
		"https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/DZ6_0471_Lobby.jpg/500px-DZ6_0471_Lobby.jpg",
		"https://live.staticflickr.com/1/2_abc_b.jpg",
	})
	if err != nil {
		t.Fatalf("WikimediaWidths: %v", err)
	}
	if titles != "File:DZ6 0471 Lobby.jpg|File:Capital One advert, Newport.jpg" {
		t.Errorf("titles = %q; want only the two originals, decoded", titles)
	}
	if got[lobby] != 6048 {
		t.Errorf("lobby width = %d, want 6048", got[lobby])
	}
	if w, known := got[gone]; !known || w != 0 {
		t.Errorf("deleted file = %d, %v; want a known 0", w, known)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want only the two originals: %v", len(got), got)
	}
}

// Commons normalizes the titles it is asked for (Unicode NFC among other
// things) and answers under the normalized title, reporting the mapping. A
// name decoded from a URL in another form used to get no answer at all.
func TestWikimediaWidths_FollowsNormalizedTitles(t *testing.T) {
	const nfd, nfc = "File:Cafe\u0301 lounge.jpg", "File:Caf\u00e9 lounge.jpg"
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("titles") != nfd {
			t.Errorf("titles = %q, want %q", r.URL.Query().Get("titles"), nfd)
		}
		w.Write([]byte(`{"query":{
			"normalized":[{"fromencoded":false,"from":"` + nfd + `","to":"` + nfc + `"}],
			"pages":[{"title":"` + nfc + `","imageinfo":[{"width":3000}]}]}}`))
	})
	u := "https://upload.wikimedia.org/wikipedia/commons/5/5e/Cafe%CC%81_lounge.jpg"
	got, err := s.WikimediaWidths([]string{u})
	if err != nil {
		t.Fatalf("WikimediaWidths: %v", err)
	}
	if got[u] != 3000 {
		t.Errorf("width = %d, %v; want 3000 through the normalized title", got[u], got)
	}
}

// stubSearcher returns a Searcher whose every request, whatever its host, is
// answered by handler.
func stubSearcher(t *testing.T, handler http.HandlerFunc) *Searcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := NewSearcher("", "")
	s.SetTransport(rewriteHost{srv.Listener.Addr().String(), nil})
	s.sleep = func(time.Duration) {}
	return s
}
