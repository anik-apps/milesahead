package scanner

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/mmcdole/gofeed"
)

// FetchedArticle is the common output format for all content fetchers (RSS, HTML scraper).
type FetchedArticle struct {
	Title       string
	URL         string
	Content     string
	PublishedAt time.Time
	Hash        string
}

// RSSFetcher fetches and parses RSS feeds into FetchedArticle slices.
type RSSFetcher struct {
	userAgent string
	parser    *gofeed.Parser
}

// NewRSSFetcher creates a new RSSFetcher with the given User-Agent string.
func NewRSSFetcher(userAgent string) *RSSFetcher {
	return &RSSFetcher{
		userAgent: userAgent,
		parser:    gofeed.NewParser(),
	}
}

// Fetch retrieves an RSS feed from feedURL and returns parsed articles.
func (f *RSSFetcher) Fetch(feedURL string) ([]FetchedArticle, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest(http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching feed %s: %w", feedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching feed %s: unexpected status code %d", feedURL, resp.StatusCode)
	}

	feed, err := f.parser.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing feed %s: %w", feedURL, err)
	}

	articles := make([]FetchedArticle, 0, len(feed.Items))
	for _, item := range feed.Items {
		content := item.Content
		if content == "" {
			content = item.Description
		}

		var publishedAt time.Time
		if item.PublishedParsed != nil {
			publishedAt = *item.PublishedParsed
		}

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(item.Title+item.Link)))

		articles = append(articles, FetchedArticle{
			Title:       item.Title,
			URL:         item.Link,
			Content:     content,
			PublishedAt: publishedAt,
			Hash:        hash,
		})
	}

	return articles, nil
}
