package scanner

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <item>
      <title>Amex 40% Transfer Bonus</title>
      <link>https://example.com/amex-bonus</link>
      <description>Transfer bonus details here.</description>
      <pubDate>Mon, 24 Mar 2026 10:00:00 GMT</pubDate>
    </item>
    <item>
      <title>Chase Sapphire 80K Offer</title>
      <link>https://example.com/chase-80k</link>
      <description>New elevated offer.</description>
      <pubDate>Mon, 24 Mar 2026 08:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

func TestFetchRSS(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer ts.Close()

	fetcher := NewRSSFetcher("TestAgent/1.0")
	articles, err := fetcher.Fetch(ts.URL)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles, got %d", len(articles))
	}
	if articles[0].Title != "Amex 40% Transfer Bonus" {
		t.Errorf("unexpected title: %s", articles[0].Title)
	}
	if articles[0].URL != "https://example.com/amex-bonus" {
		t.Errorf("unexpected URL: %s", articles[0].URL)
	}
}

func TestFetchRSS_Error(t *testing.T) {
	fetcher := NewRSSFetcher("TestAgent/1.0")
	_, err := fetcher.Fetch("http://localhost:1/nonexistent")
	if err == nil {
		t.Error("expected error for bad URL")
	}
}
