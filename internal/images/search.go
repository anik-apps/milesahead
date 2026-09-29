package images

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// ImageCandidate represents a potential image for an article.
type ImageCandidate struct {
	URL         string  // Direct image URL (for display)
	Thumbnail   string  // Smaller version for homepage cards
	Source      string  // "unsplash", "pexels", "openverse/*", "wikimedia"
	Attribution string  // "Photo by X on Unsplash" etc
	Description string  // Alt text, tags, or description from the API
	Query       string  // What search term found this
	Score       float64 // AI relevance score 1-10 (filled later)
}

// ImageResult is the final chosen image for an article.
type ImageResult struct {
	URL         string
	Thumbnail   string
	Attribution string
	Query       string
}

// userAgent identifies every request the searcher makes. Wikimedia refuses
// generic client agents outright -- Go's default "Go-http-client/1.1" gets 403
// "Please set a user-agent and respect our robot policy" from both
// upload.wikimedia.org and the Commons API -- so without this every Wikimedia
// candidate failed its liveness probe and could never be picked.
const userAgent = "MilesAhead/1.0 (+https://milesahead.kumaranik.com)"

// Searcher searches multiple image APIs.
type Searcher struct {
	unsplashKey string
	pexelsKey   string
	httpClient  *http.Client

	// sleep waits before retrying a throttled probe; replaced in tests.
	sleep func(time.Duration)
	// Probe timing: reachableTimeout, renderTimeout and maxProbeDuration,
	// held here so tests can shorten them.
	probeTimeout, renderTimeout, probeBudget time.Duration
}

// NewSearcher creates a new Searcher with the given API keys.
func NewSearcher(unsplashKey, pexelsKey string) *Searcher {
	return &Searcher{
		unsplashKey:   unsplashKey,
		pexelsKey:     pexelsKey,
		httpClient:    &http.Client{Timeout: 15 * time.Second},
		sleep:         time.Sleep,
		probeTimeout:  reachableTimeout,
		renderTimeout: renderTimeout,
		probeBudget:   maxProbeDuration,
	}
}

// SetTransport routes every request the searcher makes -- searches, probes
// and lookups -- through rt. It exists so tests can stand in for the image
// hosts without the network.
func (s *Searcher) SetTransport(rt http.RoundTripper) {
	s.httpClient.Transport = rt
}

// newRequest builds a GET carrying the searcher's User-Agent.
func (s *Searcher) newRequest(rawURL string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// Search queries all APIs in parallel and returns deduplicated candidates.
func (s *Searcher) Search(queries []string) ([]ImageCandidate, error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	var allCandidates []ImageCandidate
	var totalErrors int
	var totalAttempts int

	for _, query := range queries {
		q := query

		// Search Unsplash
		if s.unsplashKey != "" {
			wg.Add(1)
			mu.Lock()
			totalAttempts++
			mu.Unlock()
			go func() {
				defer wg.Done()
				results, err := s.searchUnsplash(q)
				if err != nil {
					slog.Warn("unsplash search failed", "query", q, "error", err)
					metrics.ImageSearchTotal.WithLabelValues("unsplash", "error").Inc()
					mu.Lock()
					totalErrors++
					mu.Unlock()
					return
				}
				metrics.ImageSearchTotal.WithLabelValues("unsplash", "success").Inc()
				mu.Lock()
				allCandidates = append(allCandidates, results...)
				mu.Unlock()
			}()
		}

		// Search Pexels
		if s.pexelsKey != "" {
			wg.Add(1)
			mu.Lock()
			totalAttempts++
			mu.Unlock()
			go func() {
				defer wg.Done()
				results, err := s.searchPexels(q)
				if err != nil {
					slog.Warn("pexels search failed", "query", q, "error", err)
					metrics.ImageSearchTotal.WithLabelValues("pexels", "error").Inc()
					mu.Lock()
					totalErrors++
					mu.Unlock()
					return
				}
				metrics.ImageSearchTotal.WithLabelValues("pexels", "success").Inc()
				mu.Lock()
				allCandidates = append(allCandidates, results...)
				mu.Unlock()
			}()
		}

		// Search Openverse (aggregates Flickr, Wikimedia, and more — no key needed)
		wg.Add(1)
		mu.Lock()
		totalAttempts++
		mu.Unlock()
		go func() {
			defer wg.Done()
			results, err := s.searchOpenverse(q)
			if err != nil {
				slog.Warn("openverse search failed", "query", q, "error", err)
				metrics.ImageSearchTotal.WithLabelValues("openverse", "error").Inc()
				mu.Lock()
				totalErrors++
				mu.Unlock()
				return
			}
			metrics.ImageSearchTotal.WithLabelValues("openverse", "success").Inc()
			mu.Lock()
			allCandidates = append(allCandidates, results...)
			mu.Unlock()
		}()

		// Search Wikimedia
		wg.Add(1)
		mu.Lock()
		totalAttempts++
		mu.Unlock()
		go func() {
			defer wg.Done()
			results, err := s.searchWikimedia(q)
			if err != nil {
				slog.Warn("wikimedia search failed", "query", q, "error", err)
				metrics.ImageSearchTotal.WithLabelValues("wikimedia", "error").Inc()
				mu.Lock()
				totalErrors++
				mu.Unlock()
				return
			}
			metrics.ImageSearchTotal.WithLabelValues("wikimedia", "success").Inc()
			mu.Lock()
			allCandidates = append(allCandidates, results...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	if totalAttempts > 0 && totalErrors == totalAttempts {
		return nil, fmt.Errorf("all %d image search API calls failed", totalAttempts)
	}

	// Deduplicate by URL
	seen := make(map[string]bool)
	var deduped []ImageCandidate
	for _, c := range allCandidates {
		if !seen[c.URL] {
			seen[c.URL] = true
			deduped = append(deduped, c)
		}
	}

	// Source diversity: max 3 per source, then cap at 15
	maxPerSource := 3
	sourceCount := map[string]int{}
	var diverse []ImageCandidate
	for _, c := range deduped {
		if sourceCount[c.Source] < maxPerSource {
			diverse = append(diverse, c)
			sourceCount[c.Source]++
		}
	}
	if len(diverse) > 15 {
		diverse = diverse[:15]
	}

	metrics.ImageSearchCandidates.Observe(float64(len(diverse)))
	return diverse, nil
}

// unsplashResponse represents the Unsplash API search response.
type unsplashResponse struct {
	Results []struct {
		URLs struct {
			Regular string `json:"regular"`
			Small   string `json:"small"`
		} `json:"urls"`
		User struct {
			Name string `json:"name"`
		} `json:"user"`
		Description    string `json:"description"`
		AltDescription string `json:"alt_description"`
		Tags           []struct {
			Title string `json:"title"`
		} `json:"tags"`
	} `json:"results"`
}

func (s *Searcher) searchUnsplash(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf("https://api.unsplash.com/search/photos?query=%s&per_page=3&orientation=landscape",
		url.QueryEscape(query))

	req, err := s.newRequest(reqURL)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Client-ID "+s.unsplashKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("unsplash API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result unsplashResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var candidates []ImageCandidate
	for _, r := range result.Results {
		desc := r.AltDescription
		if desc == "" {
			desc = r.Description
		}
		var tags []string
		for _, t := range r.Tags {
			tags = append(tags, t.Title)
		}
		if len(tags) > 0 && desc != "" {
			desc += " | Tags: " + fmt.Sprintf("%v", tags)
		}
		candidates = append(candidates, ImageCandidate{
			URL:         r.URLs.Regular,
			Thumbnail:   r.URLs.Small,
			Source:      "unsplash",
			Attribution: fmt.Sprintf("Photo by %s on Unsplash", r.User.Name),
			Description: desc,
			Query:       query,
		})
	}

	return candidates, nil
}

// pexelsResponse represents the Pexels API search response.
type pexelsResponse struct {
	Photos []struct {
		Src struct {
			Large  string `json:"large"`
			Medium string `json:"medium"`
		} `json:"src"`
		Photographer string `json:"photographer"`
		Alt          string `json:"alt"`
	} `json:"photos"`
}

func (s *Searcher) searchPexels(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf("https://api.pexels.com/v1/search?query=%s&per_page=3&orientation=landscape",
		url.QueryEscape(query))

	req, err := s.newRequest(reqURL)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", s.pexelsKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("pexels API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result pexelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var candidates []ImageCandidate
	for _, p := range result.Photos {
		candidates = append(candidates, ImageCandidate{
			URL:         p.Src.Large,
			Thumbnail:   p.Src.Medium,
			Source:      "pexels",
			Attribution: fmt.Sprintf("Photo by %s on Pexels", p.Photographer),
			Description: p.Alt,
			Query:       query,
		})
	}

	return candidates, nil
}

// openverseResponse represents the Openverse API response.
type openverseResponse struct {
	Results []struct {
		Title     string `json:"title"`
		URL       string `json:"url"`
		Thumbnail string `json:"thumbnail"`
		Creator   string `json:"creator"`
		Source    string `json:"source"`
		// Width is the pixel width of the image at URL; for Wikimedia that
		// is the full-size original. Absent (0) for some results.
		Width int `json:"width"`
		Tags  []struct {
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"results"`
}

func (s *Searcher) searchOpenverse(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf("https://api.openverse.org/v1/images/?q=%s&page_size=3",
		url.QueryEscape(query))

	req, err := s.newRequest(reqURL)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openverse API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result openverseResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var candidates []ImageCandidate
	for _, r := range result.Results {
		var tags []string
		for _, t := range r.Tags {
			tags = append(tags, t.Name)
		}
		desc := r.Title
		if len(tags) > 0 {
			desc += " | Tags: " + fmt.Sprintf("%v", tags[:min(5, len(tags))])
		}
		// Openverse's /thumb/ endpoint is a proxy that fetches from the
		// upstream provider on demand and returns 424 whenever that fetch
		// fails -- which it does intermittently, long after the article is
		// published. The direct URL points at the provider's own CDN
		// (live.staticflickr.com, upload.wikimedia.org) and is stable, so
		// prefer it and keep the proxy only as a last resort.
		//
		// The direct URL is the provider's full-size file, though, which for
		// Wikimedia is often a multi-megabyte camera original. Where the host
		// can serve a smaller copy, ask for one sized to each slot.
		direct, thumb := sizedPair(r.URL, r.Width)
		if r.URL == "" {
			// Nothing better exists for this result, so both fields fall back
			// to the proxy. Leaving URL empty would make the candidate
			// unusable downstream, where an article with no image_url is
			// indistinguishable from one that was never enriched.
			direct, thumb = r.Thumbnail, r.Thumbnail
		}
		creator := r.Creator
		if creator == "" {
			creator = "Unknown"
		}
		candidates = append(candidates, ImageCandidate{
			URL:         direct,
			Thumbnail:   thumb,
			Source:      "openverse/" + r.Source,
			Attribution: fmt.Sprintf("Photo by %s via Openverse (%s)", creator, r.Source),
			Description: desc,
			Query:       query,
		})
	}

	return candidates, nil
}

// wikimediaResponse represents the Wikimedia Commons API response.
type wikimediaResponse struct {
	Query struct {
		Pages map[string]struct {
			Title     string `json:"title"`
			ImageInfo []struct {
				URL      string `json:"url"`
				ThumbURL string `json:"thumburl"`
				Width    int    `json:"width"`
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
}

// commonsAPI is the Wikimedia Commons API endpoint.
const commonsAPI = "https://commons.wikimedia.org/w/api.php"

func (s *Searcher) searchWikimedia(query string) ([]ImageCandidate, error) {
	// iiurlwidth asks the API to render a thumbnail too. It is used only for
	// formats Sized leaves alone (SVG, TIFF, PDF), which a browser either
	// cannot show as-is or should not be sent at full size.
	reqURL := fmt.Sprintf(
		"%s?action=query&generator=search&gsrsearch=%s&gsrnamespace=6&gsrlimit=3&prop=imageinfo&iiprop=url|size&iiurlwidth=%d&format=json",
		commonsAPI, url.QueryEscape(query), ThumbnailWidth,
	)

	req, err := s.newRequest(reqURL)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("wikimedia API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result wikimediaResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var candidates []ImageCandidate
	for _, page := range result.Query.Pages {
		if len(page.ImageInfo) == 0 {
			continue
		}
		info := page.ImageInfo[0]

		original := withoutQuery(info.URL)
		display, thumb := sizedPair(original, info.Width)
		if _, known := parseWikimedia(original); !known {
			// Not a format Sized can resize. The API's own rendering is the
			// safe choice; it never upscales, returning the original instead.
			display = withoutQuery(info.ThumbURL)
			if display == "" {
				display = original
			}
			thumb = display
		}

		// Use the file title as description (strip "File:" prefix)
		desc := page.Title
		if strings.HasPrefix(desc, "File:") {
			desc = strings.TrimPrefix(desc, "File:")
			desc = strings.TrimSuffix(desc, ".jpg")
			desc = strings.TrimSuffix(desc, ".jpeg")
			desc = strings.TrimSuffix(desc, ".png")
			desc = strings.TrimSuffix(desc, ".svg")
			desc = strings.ReplaceAll(desc, "_", " ")
		}

		candidates = append(candidates, ImageCandidate{
			URL:         display,
			Thumbnail:   thumb,
			Source:      "wikimedia",
			Attribution: "Wikimedia Commons",
			Description: desc,
			Query:       query,
		})
	}

	return candidates, nil
}

// reachableTimeout bounds one request of a liveness probe. It is deliberately
// short: a provider that cannot answer in a few seconds is not one we want on
// the homepage, and findImage may probe several candidates in a row.
const reachableTimeout = 8 * time.Second

// Wikimedia renders a thumbnail size the first time anyone asks for it, and
// the sized URLs the search builds are often sizes nobody has asked for. On
// ~30 fresh sizes that took 0.2-3 s mostly, 4-7 s several times and ~10 s
// once -- past reachableTimeout, so the best candidate was sometimes refused
// for being slow once. A Wikimedia thumbnail therefore gets renderTimeout per
// request and, like a 429, one retry after a timeout. maxProbeDuration bounds
// a whole probe, retries and waits included: no retry starts, and no request
// runs, past it.
const (
	renderTimeout    = 15 * time.Second
	maxProbeDuration = 30 * time.Second
)

// Byte caps for a picked image. Sized URLs sit far below them -- a 1280px
// Wikimedia step is ~300 KB, a 500px one ~60 KB, Unsplash and Pexels ship
// 30-200 KB and Flickr's 1024px size up to ~260 KB -- while the originals
// that made the homepage weigh 16.5 MB were 2.4 to 13.2 MB each.
const (
	// MaxImageBytes caps the display image: featured card, article hero and
	// og:image, the last of which Facebook refuses above 8 MB.
	MaxImageBytes int64 = 1 << 20
	// MaxThumbnailBytes caps a homepage card thumbnail.
	MaxThumbnailBytes int64 = 512 << 10
)

// A throttled probe is retried, because Wikimedia answers 429 (Retry-After: 1)
// while it renders a thumbnail size nobody has asked for yet, then serves it.
const (
	maxProbeAttempts = 3
	maxRetryWait     = 5 * time.Second
)

// Probe is what a ranged GET of an image URL showed.
type Probe struct {
	// OK means 200 or 206: a browser could fetch the URL.
	OK          bool
	Status      int // 0 when the request itself failed
	ContentType string
	// Size is the whole file's size in bytes, or -1 when the host did not
	// say and it was not measured.
	Size int64
	// Measured means Size was counted from the body rather than stated by
	// the host. Counting stops one byte past the cap, so a measured size
	// over the cap is only known to exceed it.
	Measured bool
}

// IsImage reports whether the host labelled the response as an image.
func (p Probe) IsImage() bool {
	return strings.HasPrefix(strings.ToLower(p.ContentType), "image/")
}

// Probe reports whether a browser could fetch rawURL right now, and how large
// the file is.
//
// Image hosts hand out links that are signed, proxied, or generated on demand,
// and such a URL is indistinguishable from a durable one until it is already
// on the page. Probing at pick time is what keeps that discovery out of the
// reader's browser.
//
// This issues a ranged GET rather than a HEAD because several image CDNs --
// Openverse's thumbnail proxy among them -- answer HEAD with 405 while serving
// GET perfectly well, which would reject good candidates. The size comes from
// Content-Range, or Content-Length when the host ignores Range. Some hosts
// (Flickr, rawpixel) state neither, and some answer the range with a total of
// "*"; with measureUpTo > 0 the body is then read up to one byte past that
// limit, so an oversized file is caught either way.
func (s *Searcher) Probe(rawURL string, measureUpTo int64) Probe {
	return s.probeUntil(rawURL, measureUpTo, time.Time{})
}

// probeUntil is Probe ending by the earlier of maxProbeDuration from now and
// by (a zero by adds no bound). A probe that runs out of time reports the URL
// as not fetchable.
func (s *Searcher) probeUntil(rawURL string, measureUpTo int64, by time.Time) Probe {
	if rawURL == "" {
		return Probe{Size: -1}
	}
	perRequest, renders := s.probeTimeout, rendersOnDemand(rawURL)
	if renders {
		perRequest = s.renderTimeout
	}
	deadline := time.Now().Add(s.probeBudget)
	if !by.IsZero() && by.Before(deadline) {
		deadline = by
	}
	timeoutRetried := false
	for attempt := 1; ; attempt++ {
		req, err := s.newRequest(rawURL)
		if err != nil {
			return Probe{Size: -1}
		}
		req.Header.Set("Range", "bytes=0-0")

		remaining := time.Until(deadline)
		if remaining <= 0 {
			// After a wait or retry, or when by had already passed. A zero
			// or negative Timeout would mean none at all.
			slog.Warn("image liveness probe ran out of time", "url", rawURL)
			return Probe{Size: -1}
		}
		client := &http.Client{Timeout: min(perRequest, remaining), Transport: s.httpClient.Transport}
		resp, err := client.Do(req)
		if err != nil {
			if renders && isTimeout(err) && !timeoutRetried &&
				attempt < maxProbeAttempts && time.Until(deadline) > 0 {
				timeoutRetried = true
				slog.Info("image probe timed out while Wikimedia may be rendering the size; retrying",
					"url", rawURL, "timeout", client.Timeout)
				continue
			}
			slog.Warn("image liveness probe failed", "url", rawURL, "error", err)
			return Probe{Size: -1}
		}
		p := Probe{
			Status:      resp.StatusCode,
			ContentType: resp.Header.Get("Content-Type"),
			Size:        declaredSize(resp),
			OK:          resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent,
		}
		measure := p.OK && p.Size < 0 && measureUpTo > 0
		switch {
		case measure && resp.StatusCode == http.StatusOK:
			p.Size, p.OK = measureBody(rawURL, resp.Body, measureUpTo)
			p.Measured = true
			resp.Body.Close()
		case measure:
			// A 206 whose Content-Range total is "*", or missing: the host
			// honoured the range without saying how large the file is. Ask
			// again without a range and count, as for a 200 with no length.
			// Left at -1, the size would pass any byte cap.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			p.Size, p.Measured = s.measureWhole(rawURL, measureUpTo, deadline)
		default:
			// The response may be the whole file if the host ignored Range,
			// so drain only what is needed to let the connection be reused.
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxProbeAttempts {
			if wait := retryAfter(resp.Header.Get("Retry-After")); time.Until(deadline) > wait {
				s.sleep(wait)
				continue
			}
		}
		if !p.OK && p.Status != 0 {
			slog.Warn("image URL is not fetchable", "url", rawURL, "status", p.Status)
		}
		return p
	}
}

// isTimeout reports whether a request failed by running out of time.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// rendersOnDemand reports whether rawURL is a Wikimedia thumbnail, which the
// host renders the first time that size is asked for.
func rendersOnDemand(rawURL string) bool {
	f, ok := parseWikimedia(rawURL)
	return ok && f.width > 0
}

// wikimediaConcurrency is the most requests Warm has in flight at once:
// Wikimedia's robot policy asks clients of upload.wikimedia.org to keep "a
// total concurrency of at most 2" (wikitech.wikimedia.org/wiki/Robot_policy).
const wikimediaConcurrency = 2

// Warm requests each Wikimedia thumbnail in urls once, so that the size has
// been rendered before another client asks for it. The vision scorer fetches
// candidates' thumbnails itself, and as the first client to ask for a new
// size it met a slow render or a 429 -- and the candidate took the scorer's
// failure penalty however good the image was. Other URLs are served from
// stored files and are skipped.
//
// The requests run side by side, at most wikimediaConcurrency at a time, and
// each is given renderTimeout with no retry after a timeout: warming is a
// courtesy that runs outside any pass deadline, and a render cut off by the
// timeout carries on at Wikimedia anyway. All of it, 429 waits included,
// ends renderTimeout after it starts; a thumbnail whose turn has not come by
// then is not requested.
func (s *Searcher) Warm(urls []string) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, wikimediaConcurrency)
	seen := map[string]bool{}
	by := time.Now().Add(s.renderTimeout)
	for _, u := range urls {
		if seen[u] || !rendersOnDemand(u) {
			continue
		}
		seen[u] = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if time.Now().Before(by) {
				s.probeUntil(u, 0, by)
			}
		}()
	}
	wg.Wait()
}

// measureBody counts body up to one byte past limit. ok is false if it cannot
// be read to the end, which a browser would not manage either.
func measureBody(rawURL string, body io.Reader, limit int64) (size int64, ok bool) {
	n, err := io.Copy(io.Discard, io.LimitReader(body, limit+1))
	if err != nil {
		slog.Warn("image body could not be read", "url", rawURL, "error", err)
		return n, false
	}
	return n, true
}

// measureWhole fetches rawURL without a range and returns its stated length,
// or counts it up to one byte past limit (counted is then true), by deadline.
// It returns -1 if neither works.
func (s *Searcher) measureWhole(rawURL string, limit int64, deadline time.Time) (size int64, counted bool) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return -1, false
	}
	req, err := s.newRequest(rawURL)
	if err != nil {
		return -1, false
	}
	client := &http.Client{Timeout: min(s.probeTimeout, remaining), Transport: s.httpClient.Transport}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("image size could not be measured", "url", rawURL, "error", err)
		return -1, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("image size could not be measured", "url", rawURL, "status", resp.StatusCode)
		return -1, false
	}
	if n := resp.ContentLength; n >= 0 {
		return n, false
	}
	n, ok := measureBody(rawURL, resp.Body, limit)
	if !ok {
		return -1, false
	}
	return n, true
}

// Usable reports whether a candidate can go on the page: its URLs fetch as
// images, and neither is heavier than its slot allows. When both fields hold
// the same URL it is shown as a thumbnail, so the tighter cap applies.
//
// No request runs past by (a zero by adds no bound), so a caller with a
// deadline of its own is not held past it by a slow host; a candidate that
// cannot be confirmed by then is not usable.
func (s *Searcher) Usable(c ImageCandidate, by time.Time) bool {
	if fit, _ := s.weighUntil(c.Thumbnail, MaxThumbnailBytes, by); fit != Fits {
		return false
	}
	if c.URL == c.Thumbnail {
		return true
	}
	fit, _ := s.weighUntil(c.URL, MaxImageBytes, by)
	return fit == Fits
}

// Fit is how a URL compares with the slot it would fill.
type Fit int

const (
	// Unusable means the URL did not answer 200/206 with an image: a dead
	// link, a throttled or failed request, or an HTML error page. Browsers
	// will not render a cross-origin HTML response as an image, so a 200
	// that is not labelled image/* is as broken as a 404. (Every image host
	// the search uses labels its files image/*, checked 2026-09-23.) An
	// image whose size neither the host states nor a plain GET can measure
	// is unusable too: its weight cannot be confirmed.
	Unusable Fit = iota
	// TooHeavy means an image larger than the slot's byte cap.
	TooHeavy
	// Fits means an image within the slot's byte cap.
	Fits
)

// CheckFit fetches rawURL as Probe does and reports whether a browser would
// get an image from it no heavier than limit bytes. Both the pick path and the
// stored-image resize go through it before a URL is written to an article.
func (s *Searcher) CheckFit(rawURL string, limit int64) Fit {
	fit, _ := s.Weigh(rawURL, limit)
	return fit
}

// Weigh is CheckFit that also returns the file's size in bytes when it is
// known exactly, or -1: for an unusable URL, and for a file whose size was
// counted from the body and so is only known to exceed limit.
func (s *Searcher) Weigh(rawURL string, limit int64) (Fit, int64) {
	return s.weighUntil(rawURL, limit, time.Time{})
}

// weighUntil is Weigh with its probe ending by by, as in probeUntil.
func (s *Searcher) weighUntil(rawURL string, limit int64, by time.Time) (Fit, int64) {
	p := s.probeUntil(rawURL, limit, by)
	switch {
	case !p.OK:
		return Unusable, -1
	case !p.IsImage():
		slog.Warn("image URL did not return an image",
			"url", rawURL, "status", p.Status, "content_type", p.ContentType)
		return Unusable, -1
	case p.Size < 0:
		// Neither stated nor measurable: its weight cannot be confirmed.
		slog.Warn("image size is unknown", "url", rawURL, "status", p.Status)
		return Unusable, -1
	case p.Size > limit:
		slog.Info("image is too heavy for its slot",
			"url", rawURL, "bytes", p.Size, "limit", limit)
		if p.Measured {
			return TooHeavy, -1
		}
		return TooHeavy, p.Size
	}
	return Fits, p.Size
}

// declaredSize reads the file size a response states: the total in
// Content-Range for a 206, or Content-Length for a 200. -1 if neither.
func declaredSize(resp *http.Response) int64 {
	if resp.StatusCode == http.StatusPartialContent {
		// "bytes 0-0/13218990"; the total may be "*" when unknown.
		if _, total, found := strings.Cut(resp.Header.Get("Content-Range"), "/"); found {
			if n, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64); err == nil {
				return n
			}
		}
		return -1
	}
	return resp.ContentLength
}

// retryAfter reads a Retry-After header given in seconds, bounded so one slow
// host cannot stall a pass. Anything unparseable waits one second.
func retryAfter(h string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || secs <= 0 {
		return time.Second
	}
	return min(time.Duration(secs)*time.Second, maxRetryWait)
}

// commonsTitlesPerRequest keeps a lookup's URL comfortably short; Commons
// file names can run past 200 bytes each.
const commonsTitlesPerRequest = 20

// WikimediaWidths looks up the pixel widths of Commons originals, which their
// URLs do not carry but Sized needs to avoid upscaling. The result is keyed by
// the URLs passed in. A width of 0 means Commons reports the file as missing,
// i.e. deleted. URLs that are not Commons originals, or that the API gave no
// answer for, are absent: unknown, not zero.
func (s *Searcher) WikimediaWidths(originals []string) (map[string]int, error) {
	byTitle := map[string][]string{}
	var titles []string
	for _, u := range originals {
		if !IsWikimediaOriginal(u) {
			continue
		}
		f, _ := parseWikimedia(u)
		name, err := url.PathUnescape(f.name)
		if err != nil {
			continue
		}
		title := "File:" + strings.ReplaceAll(name, "_", " ")
		if _, seen := byTitle[title]; !seen {
			titles = append(titles, title)
		}
		byTitle[title] = append(byTitle[title], u)
	}

	out := map[string]int{}
	for start := 0; start < len(titles); start += commonsTitlesPerRequest {
		batch := titles[start:min(start+commonsTitlesPerRequest, len(titles))]
		q := url.Values{
			"action":        {"query"},
			"titles":        {strings.Join(batch, "|")},
			"prop":          {"imageinfo"},
			"iiprop":        {"size"},
			"format":        {"json"},
			"formatversion": {"2"},
		}
		req, err := s.newRequest(commonsAPI + "?" + q.Encode())
		if err != nil {
			return out, err
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return out, fmt.Errorf("commons API: %w", err)
		}
		var result struct {
			Query struct {
				// Commons answers under the title as it normalizes it
				// (Unicode NFC among other things) and lists each change.
				Normalized []struct {
					From string `json:"from"`
					To   string `json:"to"`
				} `json:"normalized"`
				Pages []struct {
					Title     string `json:"title"`
					Missing   bool   `json:"missing"`
					ImageInfo []struct {
						Width int `json:"width"`
					} `json:"imageinfo"`
				} `json:"pages"`
			} `json:"query"`
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return out, fmt.Errorf("commons API: status %d", resp.StatusCode)
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return out, fmt.Errorf("commons API: decoding: %w", err)
		}
		asked := map[string][]string{}
		for _, t := range batch {
			asked[t] = byTitle[t]
		}
		for _, n := range result.Query.Normalized {
			if urls, ok := asked[n.From]; ok && n.To != n.From {
				asked[n.To] = append(asked[n.To], urls...)
			}
		}
		for _, p := range result.Query.Pages {
			for _, u := range asked[p.Title] {
				switch {
				case p.Missing:
					out[u] = 0
				case len(p.ImageInfo) > 0 && p.ImageInfo[0].Width > 0:
					out[u] = p.ImageInfo[0].Width
				}
			}
		}
	}
	return out, nil
}

// withoutQuery drops a URL's query string. The Commons API now appends utm_*
// tracking parameters to every URL it returns.
func withoutQuery(raw string) string {
	base, _, _ := strings.Cut(raw, "?")
	return base
}
