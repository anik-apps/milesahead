package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func TestScanSource_RSS(t *testing.T) {
	// Set up a mock RSS server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer ts.Close()

	// Create a temp SQLite database.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer database.Close()

	// Create a source pointing to the mock RSS server.
	rssURL := ts.URL
	source := &db.Source{
		Name:   "Test RSS Source",
		URL:    "https://example.com",
		RSSURL: &rssURL,
		Active: true,
	}
	sourceID, err := database.CreateSource(source)
	if err != nil {
		t.Fatalf("creating source: %v", err)
	}

	// Create a Scanner and call ScanSource.
	scanner := NewScanner(database, "TestAgent/1.0")
	result, err := scanner.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("ScanSource failed: %v", err)
	}

	// Verify it found 2 new articles.
	if result.NewArticles != 2 {
		t.Errorf("expected 2 new articles, got %d", result.NewArticles)
	}
	if result.SourceID != sourceID {
		t.Errorf("expected source ID %d, got %d", sourceID, result.SourceID)
	}
	if result.SourceName != "Test RSS Source" {
		t.Errorf("expected source name %q, got %q", "Test RSS Source", result.SourceName)
	}

	// Verify articles are in the database.
	articles, err := database.GetRawArticlesBySourceID(sourceID)
	if err != nil {
		t.Fatalf("getting raw articles: %v", err)
	}
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles in DB, got %d", len(articles))
	}

	// Scan again — verify 0 new articles (dedup).
	result2, err := scanner.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("second ScanSource failed: %v", err)
	}
	if result2.NewArticles != 0 {
		t.Errorf("expected 0 new articles on second scan, got %d", result2.NewArticles)
	}

	// Verify still only 2 articles in DB.
	articles2, err := database.GetRawArticlesBySourceID(sourceID)
	if err != nil {
		t.Fatalf("getting raw articles after second scan: %v", err)
	}
	if len(articles2) != 2 {
		t.Fatalf("expected 2 articles in DB after dedup, got %d", len(articles2))
	}
}

func TestScanSource_HTMLFallback(t *testing.T) {
	// Set up a mock HTML server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testHTML))
	}))
	defer ts.Close()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer database.Close()

	// Create a source with no RSS URL but with scrape selectors.
	selectors := ScrapeSelectors{
		ArticleContainer: "article.post",
		Title:            "h2 a",
		Link:             "h2 a",
		Content:          ".post-content",
		Time:             "time",
	}
	selectorsJSON, _ := json.Marshal(selectors)
	selectorStr := string(selectorsJSON)

	source := &db.Source{
		Name:           "Test HTML Source",
		URL:            ts.URL,
		ScrapeSelector: &selectorStr,
		Active:         true,
	}
	sourceID, err := database.CreateSource(source)
	if err != nil {
		t.Fatalf("creating source: %v", err)
	}

	scanner := NewScanner(database, "TestAgent/1.0")
	result, err := scanner.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("ScanSource failed: %v", err)
	}

	if result.NewArticles != 2 {
		t.Errorf("expected 2 new articles, got %d", result.NewArticles)
	}
}

func TestScanSource_RSSFallbackToHTML(t *testing.T) {
	// RSS server returns an error, HTML server works.
	rssServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer rssServer.Close()

	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testHTML))
	}))
	defer htmlServer.Close()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer database.Close()

	selectors := ScrapeSelectors{
		ArticleContainer: "article.post",
		Title:            "h2 a",
		Link:             "h2 a",
		Content:          ".post-content",
		Time:             "time",
	}
	selectorsJSON, _ := json.Marshal(selectors)
	selectorStr := string(selectorsJSON)
	rssURL := rssServer.URL

	source := &db.Source{
		Name:           "Test Fallback Source",
		URL:            htmlServer.URL,
		RSSURL:         &rssURL,
		ScrapeSelector: &selectorStr,
		Active:         true,
	}
	sourceID, err := database.CreateSource(source)
	if err != nil {
		t.Fatalf("creating source: %v", err)
	}

	scanner := NewScanner(database, "TestAgent/1.0")
	result, err := scanner.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("ScanSource failed: %v", err)
	}

	if result.NewArticles != 2 {
		t.Errorf("expected 2 new articles via HTML fallback, got %d", result.NewArticles)
	}
}

const testRSS2 = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed 2</title>
    <item>
      <title>Delta SkyMiles Devaluation</title>
      <link>https://example.com/delta-devalue</link>
      <description>Award chart changes coming.</description>
      <pubDate>Mon, 24 Mar 2026 12:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Hilton 100K Bonus</title>
      <link>https://example.com/hilton-100k</link>
      <description>New Hilton card offer.</description>
      <pubDate>Mon, 24 Mar 2026 11:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

func TestScanAll(t *testing.T) {
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer ts1.Close()

	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS2))
	}))
	defer ts2.Close()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	defer database.Close()

	rssURL1 := ts1.URL
	rssURL2 := ts2.URL
	urls := []*string{&rssURL1, &rssURL2}
	for i := 0; i < 2; i++ {
		source := &db.Source{
			Name:   fmt.Sprintf("Source %d", i+1),
			URL:    "https://example.com",
			RSSURL: urls[i],
			Active: true,
		}
		if _, err := database.CreateSource(source); err != nil {
			t.Fatalf("creating source: %v", err)
		}
	}

	scanner := NewScanner(database, "TestAgent/1.0")
	results, err := scanner.ScanAll()
	if err != nil {
		t.Fatalf("ScanAll failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.NewArticles != 2 {
			t.Errorf("result[%d]: expected 2 new articles, got %d", i, r.NewArticles)
		}
	}
}
