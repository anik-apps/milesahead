package enrich

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/metrics"
)

const (
	wm        = "https://upload.wikimedia.org/wikipedia/commons/"
	lobbyFile = "DZ6_0471_Luxurious_hotel_lobby.jpg"
	lobby     = wm + "6/6c/" + lobbyFile
	deltaFile = "Delta_aircraft.jpg"
	delta     = wm + "0/02/" + deltaFile
	stuckFile = "Uncached.jpg"
	stuck     = wm + "7/7e/" + stuckFile
	goneFile  = "Capital_One_advert%2C_Newport.jpg"
	gone      = wm + "c/c7/" + goneFile
	brisbane  = wm + "thumb/f/fb/Brisbane.jpg/960px-Brisbane.jpg"

	// Files whose sized copies do not all confirm; see stepBytes.
	htmlFile     = "Html_error.jpg"
	htmlErr      = wm + "1/1a/" + htmlFile
	heavyFile    = "Heavy_render.png"
	heavy        = wm + "2/2b/" + heavyFile
	detailedFile = "Detailed_map.png"
	detailed     = wm + "3/3c/" + detailedFile
	stillFile    = "Still_served.jpg"
	stillServed  = wm + "4/4d/" + stillFile
	flickrB      = "https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg"
	unsplash     = "https://images.unsplash.com/photo-1?w=1080"
)

// stepBytes is what a thumbnail step weighs where it is not the usual 60 KB:
// files whose sized copies are still over a field's byte cap.
var stepBytes = map[string]int{
	// Over 1 MiB at 1280 and at 960: the display image cannot be sized.
	"1280px-" + heavyFile: 2_000_000,
	"960px-" + heavyFile:  2_000_000,
	// Over the cap at the first step, within it at the next one down.
	"1280px-" + detailedFile: 1_500_000,
	"960px-" + detailedFile:  700_000,
	"500px-" + detailedFile:  600_000,
	"330px-" + detailedFile:  200_000,
}

// standardSteps are the only widths upload.wikimedia.org serves.
var standardSteps = map[string]bool{"20": true, "40": true, "60": true, "120": true, "250": true,
	"330": true, "500": true, "960": true, "1280": true, "1920": true, "3840": true}

// imageHosts stands in for Wikimedia, Flickr and the Commons API, answering
// the way they were seen to answer on 2026-09-23: standard thumbnail steps
// serve images, any other width is a 400, a deleted file is a 404.
type imageHosts struct {
	mu       sync.Mutex
	requests []string
}

func (h *imageHosts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, r.Host+r.URL.Path+"?"+r.URL.RawQuery)
	h.mu.Unlock()

	image := func(size int) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", size))
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("j"))
	}
	p := r.URL.Path
	switch {
	case r.Host == "commons.wikimedia.org" && r.URL.Query().Get("titles") != "":
		// A width lookup, in the formatversion=2 shape; widths as on Commons.
		w.Write([]byte(`{"query":{"pages":[
			{"title":"File:DZ6 0471 Luxurious hotel lobby.jpg","imageinfo":[{"width":6048}]},
			{"title":"File:Delta aircraft.jpg","imageinfo":[{"width":1024}]},
			{"title":"File:Uncached.jpg","imageinfo":[{"width":4000}]},
			{"title":"File:Html error.jpg","imageinfo":[{"width":4000}]},
			{"title":"File:Heavy render.png","imageinfo":[{"width":5000}]},
			{"title":"File:Detailed map.png","imageinfo":[{"width":3000}]},
			{"title":"File:Still served.jpg","missing":true},
			{"title":"File:Capital One advert, Newport.jpg","missing":true}]}}`))
	case strings.Contains(p, "Uncached.jpg/"):
		// A thumbnail the host keeps failing to render.
		w.WriteHeader(http.StatusServiceUnavailable)
	case strings.Contains(p, htmlFile+"/"):
		// A 200 that is not an image, like an error page from a CDN.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html>upstream error</html>"))
	case strings.Contains(p, "Capital_One"):
		w.WriteHeader(http.StatusNotFound)
	case strings.Contains(p, "/thumb/"):
		name := p[strings.LastIndex(p, "/")+1:]
		width, _, _ := strings.Cut(name, "px-")
		if !standardSteps[width] {
			w.WriteHeader(http.StatusBadRequest) // "Use thumbnail sizes listed on ..."
			return
		}
		if b, ok := stepBytes[name]; ok {
			image(b)
			return
		}
		image(60_000)
	case r.Host == "live.staticflickr.com":
		image(70_000)
	default:
		image(13_218_990) // an original
	}
}

// noNonStandardWidths fails the test if any thumbnail was requested at a
// width Wikimedia does not serve.
func (h *imageHosts) noNonStandardWidths(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.requests {
		if m := stepInPath.FindStringSubmatch(r); m != nil && !standardSteps[m[1]] {
			t.Errorf("requested a non-standard thumbnail width: %s", r)
		}
	}
}

var stepInPath = regexp.MustCompile(`/(\d+)px-`)

// since returns the requests made after the first n.
func (h *imageHosts) since(n int) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests[n:]...)
}

func (h *imageHosts) total() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

func (h *imageHosts) count(substr string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

// hostRouter sends every request to the stand-in server, keeping the
// original Host so the handler can tell the providers apart.
type hostRouter struct{ addr string }

func (rt hostRouter) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Host = req.URL.Host
	req.URL.Scheme = "http"
	req.URL.Host = rt.addr
	return http.DefaultTransport.RoundTrip(req)
}

func seedImage(t *testing.T, d *db.Database, url, thumb string) int64 {
	t.Helper()
	id := seedArticle(t, d)
	// Fully enriched, so the only work a pass finds is the image resize.
	if _, err := d.DB().Exec(`UPDATE generated_articles SET image_url = ?, image_thumbnail = ?,
		image_attribution = 'Photo credit', scouts_take = 'Take', key_details = '[{"label":"a","value":"b"}]',
		tags_json = '["hotels"]', status = ? WHERE id = ?`, url, thumb, db.StatusApproved, id); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	return id
}

func storedImage(t *testing.T, d *db.Database, id int64) (url, thumb string, attempts int) {
	t.Helper()
	if err := d.DB().QueryRow(`SELECT image_url, image_thumbnail, enrichment_attempts
		FROM generated_articles WHERE id = ?`, id).Scan(&url, &thumb, &attempts); err != nil {
		t.Fatalf("read %d: %v", id, err)
	}
	return url, thumb, attempts
}

// TestRun_ResizesStoredImages is what happens on the first pass after deploy:
// the rows production holds are converted in place, each new URL is fetched
// before it is written, and anything that cannot be confirmed is left alone.
func TestRun_ResizesStoredImages(t *testing.T) {
	d := testDB(t)
	hosts := &imageHosts{}
	srv := httptest.NewServer(hosts)
	defer srv.Close()
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})

	lobbyRow := seedImage(t, d, lobby, lobby)
	deltaRow := seedImage(t, d, delta, delta)
	brisbaneRow := seedImage(t, d, brisbane, brisbane)
	flickrRow := seedImage(t, d, flickrB, flickrB)
	unsplashRow := seedImage(t, d, unsplash, unsplash+"&w=400")
	stuckRow := seedImage(t, d, stuck, stuck)
	goneRow := seedImage(t, d, gone, gone)

	client := &scriptedClient{fail: map[string]error{"": fmt.Errorf("models are down")}}
	s := New(d, client, []string{"m1"}, "Scout", searcher, nil)
	s.Pace = 0
	s.Run()

	thumbs := func(dir, file string) string { return wm + "thumb/" + dir + "/" + file + "/" }
	for _, c := range []struct {
		name        string
		id          int64
		url, thumb  string
		attemptsMax int
	}{
		// The 13.2 MB homepage thumbnail.
		{"huge original", lobbyRow, thumbs("6/6c", lobbyFile) + "1280px-" + lobbyFile, thumbs("6/6c", lobbyFile) + "500px-" + lobbyFile, 0},
		// 1024px wide: 960 for the display image (1280 would be an upscale).
		{"narrow original", deltaRow, thumbs("0/02", deltaFile) + "960px-" + deltaFile, thumbs("0/02", deltaFile) + "500px-" + deltaFile, 0},
		{"960 thumbnail", brisbaneRow, brisbane, wm + "thumb/f/fb/Brisbane.jpg/500px-Brisbane.jpg", 0},
		{"flickr", flickrRow, flickrB, "https://live.staticflickr.com/65535/54420209608_0aa8ea537a.jpg", 0},
		{"other host untouched", unsplashRow, unsplash, unsplash + "&w=400", 0},
		// Sized URLs that never confirm: the original stays rather than risk
		// a broken image, and is retried next pass.
		{"unconfirmed", stuckRow, stuck, stuck, 0},
	} {
		url, thumb, attempts := storedImage(t, d, c.id)
		if url != c.url || thumb != c.thumb {
			t.Errorf("%s:\n url   %q\n want  %q\n thumb %q\n want  %q", c.name, url, c.url, thumb, c.thumb)
		}
		if attempts > c.attemptsMax {
			t.Errorf("%s: enrichment_attempts = %d; resizing is not an enrichment attempt", c.name, attempts)
		}
	}

	// Deleted on Commons and 404 at the URL: cleared and handed back, and the
	// same pass already tried to find a replacement.
	url, thumb, attempts := storedImage(t, d, goneRow)
	if url != "" || thumb != "" {
		t.Errorf("deleted file: image %q / %q, want cleared", url, thumb)
	}
	if attempts != 1 {
		t.Errorf("deleted file: enrichment_attempts = %d, want 1 (reset, then one re-pick attempt)", attempts)
	}

	// Never asked for a width Wikimedia refuses.
	hosts.noNonStandardWidths(t)

	// Second pass: converted rows are fixed points, widths are cached, and
	// only the unconfirmed row is tried again. (The cleared article's image
	// search also runs; it is not part of the resize.)
	lookups := hosts.count("titles=")
	if lookups != 1 {
		t.Errorf("first pass made %d width lookups, want one batched request", lookups)
	}
	before := len(hosts.requests)
	s.Run()
	if n := hosts.count("titles="); n != lookups {
		t.Errorf("second pass asked Commons for widths again (%d -> %d lookups)", lookups, n)
	}
	var again []string
	for _, r := range hosts.requests[before:] {
		search := strings.Contains(r, "api.openverse.org") || strings.Contains(r, "generator=search")
		if !strings.Contains(r, "Uncached") && !search {
			again = append(again, r)
		}
	}
	if len(again) != 0 {
		t.Errorf("second pass re-probed converted rows: %v", again)
	}
	if url, thumb, _ := storedImage(t, d, lobbyRow); !strings.HasSuffix(url, "/1280px-"+lobbyFile) || !strings.HasSuffix(thumb, "/500px-"+lobbyFile) {
		t.Errorf("second pass changed a converted row: %q / %q", url, thumb)
	}
}

// A failed width lookup must leave originals alone: without the width any
// step could upscale.
func TestRun_ResizeWithoutWidthsLeavesOriginals(t *testing.T) {
	d := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "commons.wikimedia.org" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("j"))
	}))
	defer srv.Close()
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})

	id := seedImage(t, d, lobby, lobby)
	s := New(d, &scriptedClient{}, []string{"m1"}, "Scout", searcher, nil)
	s.Pace = 0
	s.Run()

	if url, thumb, _ := storedImage(t, d, id); url != lobby || thumb != lobby {
		t.Errorf("image = %q / %q, want the original kept until its width is known", url, thumb)
	}
	if len(s.originalWidths) != 0 {
		t.Errorf("failed lookup was cached: %v", s.originalWidths)
	}
}

// newResizeService returns a reconciler whose image requests all go to
// handler, with no pacing, and whose models always fail.
func newResizeService(t *testing.T, d *db.Database, handler http.Handler) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})
	s := New(d, &scriptedClient{}, []string{"m1"}, "Scout", searcher, nil)
	s.Pace = 0
	return s
}

func resizeCount(t *testing.T, outcome string) float64 {
	t.Helper()
	var m dto.Metric
	c, err := metrics.ImageResizeTotal.GetMetricWithLabelValues(outcome)
	if err != nil {
		t.Fatalf("counter lookup: %v", err)
	}
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter read: %v", err)
	}
	return m.GetCounter().GetValue()
}

func unsizedGauge(t *testing.T) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.ArticleImagesUnsized.Write(&m); err != nil {
		t.Fatalf("gauge read: %v", err)
	}
	return m.GetGauge().GetValue()
}

// The guarantees the resize makes before it writes a URL, one row per way a
// sized URL can fail to be a usable image: a field whose sized URL is not
// confirmed keeps what it had, and a deleted-on-Commons file whose URL still
// loads is not taken off the article.
func TestRun_ResizeWritesOnlyConfirmedImages(t *testing.T) {
	d := testDB(t)
	hosts := &imageHosts{}
	s := newResizeService(t, d, hosts)

	htmlRow := seedImage(t, d, htmlErr, htmlErr)
	heavyRow := seedImage(t, d, heavy, heavy)
	detailedRow := seedImage(t, d, detailed, detailed)
	stillThumb := wm + "thumb/4/4d/" + stillFile + "/960px-" + stillFile
	stillRow := seedImage(t, d, stillServed, stillThumb)

	resizedBefore, unconfirmedBefore, clearedBefore := resizeCount(t, "resized"), resizeCount(t, "unconfirmed"), resizeCount(t, "cleared")
	s.Run()

	thumbs := func(dir, file string) string { return wm + "thumb/" + dir + "/" + file + "/" }
	for _, c := range []struct {
		name       string
		id         int64
		url, thumb string
	}{
		// 200 text/html for every sized URL: nothing is written.
		{"sized URL is an HTML page", htmlRow, htmlErr, htmlErr},
		// 1280 and 960 are both over 1 MiB: the display image stays; the
		// thumbnail, which fits, is still converted.
		{"sized display image over its cap", heavyRow, heavy, thumbs("2/2b", heavyFile) + "500px-" + heavyFile},
		// Over the cap at the first step, within it one step down.
		{"steps down when too heavy", detailedRow, thumbs("3/3c", detailedFile) + "960px-" + detailedFile, thumbs("3/3c", detailedFile) + "330px-" + detailedFile},
		// Commons says missing, but the URL answers with an image: kept, and
		// its thumbnail is still sized.
		{"missing on Commons but still served", stillRow, stillServed, thumbs("4/4d", stillFile) + "500px-" + stillFile},
	} {
		url, thumb, attempts := storedImage(t, d, c.id)
		if url != c.url || thumb != c.thumb {
			t.Errorf("%s:\n url   %q\n want  %q\n thumb %q\n want  %q", c.name, url, c.url, thumb, c.thumb)
		}
		if attempts != 0 {
			t.Errorf("%s: enrichment_attempts = %d; the image was not cleared", c.name, attempts)
		}
	}
	hosts.noNonStandardWidths(t)

	if got := resizeCount(t, "resized") - resizedBefore; got != 1 {
		t.Errorf("resized counter +%v, want +1 (only the row where every field converted)", got)
	}
	if got := resizeCount(t, "unconfirmed") - unconfirmedBefore; got != 3 {
		t.Errorf("unconfirmed counter +%v, want +3", got)
	}
	if got := resizeCount(t, "cleared") - clearedBefore; got != 0 {
		t.Errorf("cleared counter +%v, want 0", got)
	}
	if got := unsizedGauge(t); got != 3 {
		t.Errorf("unsized gauge = %v, want 3 rows still waiting", got)
	}

	// Second pass: the three rows that failed are tried once more...
	before := hosts.total()
	s.Run()
	for _, file := range []string{htmlFile, heavyFile, stillFile} {
		if n := countIn(hosts.since(before), file); n == 0 {
			t.Errorf("second pass did not retry %s", file)
		}
	}
	// ...and, having failed twice running, are set aside rather than probed
	// on every pass.
	before = hosts.total()
	s.Run()
	if again := hosts.since(before); len(again) != 0 {
		t.Errorf("third pass probed rows that are backing off: %v", again)
	}
	if got := unsizedGauge(t); got != 3 {
		t.Errorf("unsized gauge = %v after backing off, want 3 (still unconverted)", got)
	}
}

func countIn(requests []string, substr string) int {
	n := 0
	for _, r := range requests {
		if strings.Contains(r, substr) {
			n++
		}
	}
	return n
}

// A slow or throttling image host must not eat the pass. The resize runs
// first, so without its own budget it would run to the pass deadline and
// enrichment would never start: the pass would end as "deadline" and the
// liveness timestamp would stop moving.
func TestRun_ResizeCannotStarveEnrichment(t *testing.T) {
	d := testDB(t)
	const rows = 10
	var pages []string
	for i := 0; i < rows; i++ {
		file := fmt.Sprintf("Slow_%d.jpg", i)
		seedImage(t, d, wm+"5/5e/"+file, wm+"5/5e/"+file)
		pages = append(pages, fmt.Sprintf(`{"title":"File:Slow %d.jpg","imageinfo":[{"width":4000}]}`, i))
	}
	needsEnrichment := seedArticle(t, d)

	var probed sync.Map
	s := newResizeService(t, d, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("titles") != "":
			w.Write([]byte(`{"query":{"pages":[` + strings.Join(pages, ",") + `]}}`))
		case strings.Contains(r.URL.Path, "/thumb/"):
			probed.Store(r.URL.Path[:strings.LastIndex(r.URL.Path, "/")], true)
			time.Sleep(150 * time.Millisecond)
			w.WriteHeader(http.StatusServiceUnavailable)
		default: // image searches for the article being enriched
			w.Write([]byte(`{}`))
		}
	}))
	// Ten rows at ~300 ms each would take 3 s, past the 2 s pass deadline.
	s.MaxDuration = 2 * time.Second
	s.ResizeMaxDuration = 400 * time.Millisecond

	completed := passCount(t, "completed")
	deadline := passCount(t, "deadline")
	start := time.Now()
	s.Run()

	n := 0
	probed.Range(func(_, _ any) bool { n++; return true })
	if n == 0 || n >= rows {
		t.Errorf("resize probed %d of %d rows; want it to stop at its budget", n, rows)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("pass took %v; the resize should have stopped near 400 ms", elapsed)
	}
	if _, _, attempts := storedImage(t, d, needsEnrichment); attempts != 1 {
		t.Errorf("article needing enrichment has %d attempts, want 1: enrichment did not run", attempts)
	}
	if got := passCount(t, "completed") - completed; got != 1 {
		t.Errorf("completed passes +%v, want +1", got)
	}
	if got := passCount(t, "deadline") - deadline; got != 0 {
		t.Errorf("deadline passes +%v, want 0", got)
	}
	if got := unsizedGauge(t); got != rows {
		t.Errorf("unsized gauge = %v, want %d", got, rows)
	}
}

func passCount(t *testing.T, outcome string) float64 {
	t.Helper()
	var m dto.Metric
	c, err := metrics.EnrichmentPassesTotal.GetMetricWithLabelValues(outcome)
	if err != nil {
		t.Fatalf("counter lookup: %v", err)
	}
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter read: %v", err)
	}
	return m.GetCounter().GetValue()
}

// Each pass sends at most ResizeMaxRows rows to the network, the multi-
// megabyte Wikimedia originals first and the newest (homepage) rows first
// among those; the rest converge over the following passes.
func TestRun_ResizeCapsRowsPerPass(t *testing.T) {
	d := testDB(t)
	hosts := &imageHosts{}
	s := newResizeService(t, d, hosts)
	s.ResizeMaxRows = 1

	lobbyRow := seedImage(t, d, lobby, lobby)
	deltaRow := seedImage(t, d, delta, delta)
	// Newest, but only a 1024 px Flickr file, so it waits for the originals.
	flickrRow := seedImage(t, d, flickrB, flickrB)

	converted := func(id int64) bool {
		url, thumb, _ := storedImage(t, d, id)
		return strings.Contains(url, "/thumb/") || thumb != url
	}
	for pass, want := range [][]int64{{deltaRow}, {deltaRow, lobbyRow}, {deltaRow, lobbyRow, flickrRow}} {
		s.Run()
		for _, id := range []int64{flickrRow, lobbyRow, deltaRow} {
			wantDone := false
			for _, w := range want {
				wantDone = wantDone || w == id
			}
			if got := converted(id); got != wantDone {
				t.Errorf("after pass %d: row %d converted = %v, want %v", pass+1, id, got, wantDone)
			}
		}
		if got, want := unsizedGauge(t), float64(3-len(want)); got != want {
			t.Errorf("after pass %d: unsized gauge = %v, want %v", pass+1, got, want)
		}
	}
}

// Integration, pick path: the real searcher, scorer and reconciler against
// stand-ins for Openverse and the image hosts. The two best-scored candidates
// cannot go on the page -- one is over its byte cap, one answers with an HTML
// page -- so the third is stored, as sized Wikimedia URLs.
func TestFindImage_SkipsCandidatesThatDoNotFit(t *testing.T) {
	d := testDB(t)
	id := seedArticle(t, d)

	const heavyFlickr = "https://live.staticflickr.com/65535/111_0aa8ea537a_b.jpg"
	const htmlPage = "https://images.example.org/error.jpg"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Host == "api.openverse.org":
			w.Write([]byte(`{"results":[
				{"title":"Lounge","url":"` + heavyFlickr + `","creator":"a","source":"flickr","width":1024},
				{"title":"Lobby","url":"` + htmlPage + `","creator":"b","source":"example","width":2000},
				{"title":"Hotel lobby","url":"` + lobby + `","creator":"c","source":"wikimedia","width":6048}]}`))
		case r.Host == "commons.wikimedia.org":
			w.Write([]byte(`{}`)) // no Commons search results
		case r.Host == "images.example.org":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>Sign in</html>"))
		default:
			size := 60_000
			if strings.HasSuffix(r.URL.Path, "_b.jpg") {
				size = 1_500_000 // over the 1 MiB display cap
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", size))
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("j"))
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	searcher := images.NewSearcher("", "")
	searcher.SetTransport(hostRouter{srv.Listener.Addr().String()})
	client := &scriptedClient{reply: map[string]string{
		"image editor": `{"scores":[{"index":0,"score":9},{"index":1,"score":8},{"index":2,"score":7}]}`,
	}}
	s := New(d, client, []string{"m1"}, "Scout", searcher, images.NewScorer(client, "m1"))
	s.Pace = 0
	s.Run()

	url, thumb, _ := storedImage(t, d, id)
	base := wm + "thumb/6/6c/" + lobbyFile + "/"
	if url != base+"1280px-"+lobbyFile || thumb != base+"500px-"+lobbyFile {
		t.Errorf("stored image:\n url   %q\n thumb %q\nwant the sized Wikimedia pair", url, thumb)
	}
}
