package images

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kumaranik/milesahead/internal/metrics"
)

// ImageCandidate represents a potential image for an article.
type ImageCandidate struct {
	URL         string  // Direct image URL (for display)
	Thumbnail   string  // Smaller version for homepage cards
	Source      string  // "unsplash", "pexels", "wikimedia"
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

// Searcher searches multiple image APIs.
type Searcher struct {
	unsplashKey string
	pexelsKey   string
	pixabayKey  string
	httpClient  *http.Client
}

// NewSearcher creates a new Searcher with the given API keys.
func NewSearcher(unsplashKey, pexelsKey, pixabayKey string) *Searcher {
	return &Searcher{
		unsplashKey: unsplashKey,
		pexelsKey:   pexelsKey,
		pixabayKey:  pixabayKey,
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
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

		// Search Pixabay
		if s.pixabayKey != "" {
			wg.Add(1)
			mu.Lock()
			totalAttempts++
			mu.Unlock()
			go func() {
				defer wg.Done()
				results, err := s.searchPixabay(q)
				if err != nil {
					slog.Warn("pixabay search failed", "query", q, "error", err)
					metrics.ImageSearchTotal.WithLabelValues("pixabay", "error").Inc()
					mu.Lock()
					totalErrors++
					mu.Unlock()
					return
				}
				metrics.ImageSearchTotal.WithLabelValues("pixabay", "success").Inc()
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

	req, err := http.NewRequest("GET", reqURL, nil)
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

	req, err := http.NewRequest("GET", reqURL, nil)
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

// pixabayResponse represents the Pixabay API response.
type pixabayResponse struct {
	Hits []struct {
		LargeImageURL string `json:"largeImageURL"`
		WebformatURL  string `json:"webformatURL"`
		Tags          string `json:"tags"`
		User          string `json:"user"`
		PageURL       string `json:"pageURL"`
	} `json:"hits"`
}

func (s *Searcher) searchPixabay(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf("https://pixabay.com/api/?key=%s&q=%s&per_page=3&orientation=horizontal&image_type=photo",
		s.pixabayKey, url.QueryEscape(query))

	resp, err := s.httpClient.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("pixabay API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result pixabayResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	var candidates []ImageCandidate
	for _, h := range result.Hits {
		candidates = append(candidates, ImageCandidate{
			URL:         h.WebformatURL,
			Thumbnail:   h.WebformatURL,
			Source:      "pixabay",
			Attribution: fmt.Sprintf("Photo by %s on Pixabay", h.User),
			Description: h.Tags,
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
		Tags      []struct {
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"results"`
}

func (s *Searcher) searchOpenverse(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf("https://api.openverse.org/v1/images/?q=%s&page_size=3",
		url.QueryEscape(query))

	resp, err := s.httpClient.Get(reqURL)
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
		thumb := r.Thumbnail
		if thumb == "" {
			thumb = r.URL
		}
		creator := r.Creator
		if creator == "" {
			creator = "Unknown"
		}
		candidates = append(candidates, ImageCandidate{
			URL:         r.URL,
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
			} `json:"imageinfo"`
		} `json:"pages"`
	} `json:"query"`
}

func (s *Searcher) searchWikimedia(query string) ([]ImageCandidate, error) {
	reqURL := fmt.Sprintf(
		"https://commons.wikimedia.org/w/api.php?action=query&generator=search&gsrsearch=%s&gsrnamespace=6&gsrlimit=3&prop=imageinfo&iiprop=url&iiurlwidth=800&format=json",
		url.QueryEscape(query),
	)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", "MilesAhead/1.0 (image search)")

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

		imageURL := info.ThumbURL
		if imageURL == "" {
			imageURL = info.URL
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
			URL:         imageURL,
			Thumbnail:   imageURL,
			Source:      "wikimedia",
			Attribution: "Wikimedia Commons",
			Description: desc,
			Query:       query,
		})
	}

	return candidates, nil
}
