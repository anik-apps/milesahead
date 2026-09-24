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
	flickrB   = "https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg"
	unsplash  = "https://images.unsplash.com/photo-1?w=1080"
)

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
			{"title":"File:Capital One advert, Newport.jpg","missing":true}]}}`))
	case strings.Contains(p, "Uncached.jpg/"):
		// A thumbnail the host keeps failing to render.
		w.WriteHeader(http.StatusServiceUnavailable)
	case strings.Contains(p, "Capital_One"):
		w.WriteHeader(http.StatusNotFound)
	case strings.Contains(p, "/thumb/"):
		for _, ok := range []string{"/500px-", "/960px-", "/1280px-"} {
			if strings.Contains(p, ok) {
				image(60_000)
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest) // "Use thumbnail sizes listed on ..."
	case r.Host == "live.staticflickr.com":
		image(70_000)
	default:
		image(13_218_990) // an original
	}
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

	// Never stored a URL at a width Wikimedia refuses.
	if n := hosts.count("px-") - hosts.count("/500px-") - hosts.count("/960px-") - hosts.count("/1280px-"); n != 0 {
		t.Errorf("%d requests for non-standard thumbnail widths", n)
	}

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
