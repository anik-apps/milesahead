package scanner

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testHTML = `<html>
<body>
  <div class="post-list">
    <article class="post">
      <h2><a href="/amex-bonus">Amex 40% Transfer Bonus</a></h2>
      <div class="post-content">Transfer bonus details here.</div>
      <time datetime="2026-03-24T10:00:00Z">March 24, 2026</time>
    </article>
    <article class="post">
      <h2><a href="/chase-80k">Chase Sapphire 80K Offer</a></h2>
      <div class="post-content">New elevated offer.</div>
      <time datetime="2026-03-24T08:00:00Z">March 24, 2026</time>
    </article>
  </div>
</body>
</html>`

func TestScrape(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testHTML))
	}))
	defer ts.Close()

	scraper := NewHTMLScraper("TestAgent/1.0")
	selectors := ScrapeSelectors{
		ArticleContainer: "article.post",
		Title:            "h2 a",
		Link:             "h2 a",
		Content:          ".post-content",
		Time:             "time",
	}
	articles, err := scraper.Scrape(ts.URL, selectors)
	if err != nil {
		t.Fatalf("Scrape failed: %v", err)
	}
	if len(articles) != 2 {
		t.Fatalf("expected 2 articles, got %d", len(articles))
	}
	if articles[0].Title != "Amex 40% Transfer Bonus" {
		t.Errorf("unexpected title: %s", articles[0].Title)
	}
}
