package images

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReachable(t *testing.T) {
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
			if got := s.Reachable(srv.URL + "/img.jpg"); got != tt.want {
				t.Errorf("Reachable = %v, want %v", got, tt.want)
			}
		})
	}
}

// Several image CDNs reject HEAD while serving GET, so probing with HEAD
// would discard good candidates.
func TestReachable_UsesRangedGETNotHEAD(t *testing.T) {
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

	if !NewSearcher("", "").Reachable(srv.URL + "/img.jpg") {
		t.Fatal("Reachable = false for a host that serves GET but rejects HEAD")
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotRange == "" {
		t.Error("no Range header sent; the probe would download whole images")
	}
}

func TestReachable_EmptyAndUnparseable(t *testing.T) {
	s := NewSearcher("", "")
	for _, u := range []string{"", "://not a url", "http://"} {
		if s.Reachable(u) {
			t.Errorf("Reachable(%q) = true, want false", u)
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
	// With no direct URL there is nothing better than the proxy.
	if got[1].Thumbnail != "https://api.openverse.org/v1/images/def/thumb/" {
		t.Errorf("fallback thumbnail = %q, want the proxy", got[1].Thumbnail)
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
