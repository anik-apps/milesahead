package images

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if got := s.Usable(tt.c); got != tt.want {
			t.Errorf("%s: Usable = %v, want %v", tt.name, got, tt.want)
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
