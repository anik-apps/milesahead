package scanner

import (
	"encoding/json"
	"fmt"

	"github.com/kumaranik/milesahead/internal/db"
)

// ScanResult holds the outcome of scanning a single source.
type ScanResult struct {
	SourceID   int64
	SourceName string
	NewArticles int
	Errors     []error
}

// Scanner orchestrates RSS fetching and HTML scraping to populate raw_articles.
type Scanner struct {
	db      *db.Database
	rss     *RSSFetcher
	scraper *HTMLScraper
}

// NewScanner creates a new Scanner with the given database and user agent.
func NewScanner(database *db.Database, userAgent string) *Scanner {
	return &Scanner{
		db:      database,
		rss:     NewRSSFetcher(userAgent),
		scraper: NewHTMLScraper(userAgent),
	}
}

// ScanSource scans a single source by ID, trying RSS first then falling back to HTML scraping.
func (s *Scanner) ScanSource(sourceID int64) (*ScanResult, error) {
	source, err := s.db.GetSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("getting source %d: %w", sourceID, err)
	}

	result := &ScanResult{
		SourceID:   source.ID,
		SourceName: source.Name,
	}

	var articles []FetchedArticle
	var fetchErr error

	// If source has rss_url, try RSS fetcher first.
	if source.RSSURL != nil && *source.RSSURL != "" {
		articles, fetchErr = s.rss.Fetch(*source.RSSURL)
	}

	// If RSS failed or no rss_url, fall back to HTML scraper.
	if fetchErr != nil || articles == nil {
		if source.ScrapeSelector != nil && *source.ScrapeSelector != "" {
			var selectors ScrapeSelectors
			if jsonErr := json.Unmarshal([]byte(*source.ScrapeSelector), &selectors); jsonErr != nil {
				return nil, fmt.Errorf("parsing scrape selectors for source %d: %w", sourceID, jsonErr)
			}
			articles, fetchErr = s.scraper.Scrape(source.URL, selectors)
		}
	}

	if fetchErr != nil {
		result.Errors = append(result.Errors, fetchErr)
		return result, nil
	}

	// Store each fetched article in the database.
	for _, article := range articles {
		inserted, err := s.db.CreateRawArticleIfNew(&db.RawArticle{
			SourceID:    sourceID,
			Title:       article.Title,
			URL:         article.URL,
			Content:     article.Content,
			PublishedAt: article.PublishedAt,
			Hash:        article.Hash,
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("inserting article %q: %w", article.Title, err))
			continue
		}
		if inserted {
			result.NewArticles++
		}
	}

	return result, nil
}

// ScanAll scans all active sources and returns the results.
func (s *Scanner) ScanAll() ([]ScanResult, error) {
	sources, err := s.db.ListActiveSources()
	if err != nil {
		return nil, fmt.Errorf("listing active sources: %w", err)
	}

	var results []ScanResult
	for _, source := range sources {
		result, err := s.ScanSource(source.ID)
		if err != nil {
			results = append(results, ScanResult{
				SourceID:   source.ID,
				SourceName: source.Name,
				Errors:     []error{err},
			})
			continue
		}
		results = append(results, *result)
	}

	return results, nil
}
