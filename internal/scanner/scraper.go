package scanner

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// ScrapeSelectors holds CSS selectors used to extract article data from HTML pages.
type ScrapeSelectors struct {
	ArticleContainer string
	Title            string
	Link             string
	Content          string
	Time             string
}

// HTMLScraper fetches and parses HTML pages into FetchedArticle slices.
type HTMLScraper struct {
	userAgent string
}

// NewHTMLScraper creates a new HTMLScraper with the given User-Agent string.
func NewHTMLScraper(userAgent string) *HTMLScraper {
	return &HTMLScraper{userAgent: userAgent}
}

// Scrape fetches the page at pageURL and extracts articles using the provided CSS selectors.
func (s *HTMLScraper) Scrape(pageURL string, selectors ScrapeSelectors) ([]FetchedArticle, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", s.userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching page %s: %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching page %s: unexpected status code %d", pageURL, resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing HTML from %s: %w", pageURL, err)
	}

	baseURL, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL %s: %w", pageURL, err)
	}

	var articles []FetchedArticle

	doc.Find(selectors.ArticleContainer).Each(func(i int, sel *goquery.Selection) {
		title := sel.Find(selectors.Title).First().Text()

		href, _ := sel.Find(selectors.Link).First().Attr("href")
		absoluteURL := resolveURL(baseURL, href)

		content := sel.Find(selectors.Content).First().Text()

		var publishedAt time.Time
		if datetimeAttr, exists := sel.Find(selectors.Time).First().Attr("datetime"); exists {
			if parsed, parseErr := time.Parse(time.RFC3339, datetimeAttr); parseErr == nil {
				publishedAt = parsed
			}
		}

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(title+absoluteURL)))

		articles = append(articles, FetchedArticle{
			Title:       title,
			URL:         absoluteURL,
			Content:     content,
			PublishedAt: publishedAt,
			Hash:        hash,
		})
	})

	return articles, nil
}

// resolveURL resolves a possibly-relative href against a base URL.
func resolveURL(base *url.URL, href string) string {
	if href == "" {
		return base.String()
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}
