# Miles Ahead Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-hosted miles/points content aggregation platform with dual-AI pipeline, admin review workflow, and magazine-style public site.

**Architecture:** Go monolith with SQLite (WAL mode). RSS/HTML scanner feeds into a two-model AI pipeline (Hugging Face). Admin reviews via dashboard or email replies. Public magazine-grid site with light/dark mode. Deployed on OCI free tier.

**Tech Stack:** Go 1.22+, SQLite (modernc.org/sqlite), Go html/template, gofeed, goquery, go-imap, gomail, robfig/cron, bcrypt

**Spec:** `docs/superpowers/specs/2026-03-24-milesahead-design.md`

---

## File Structure

```
scanner/
  cmd/milesahead/main.go
  internal/
    config/config.go, config_test.go
    db/db.go, db_test.go, models.go, migrations.go
    scanner/scanner.go, scanner_test.go, rss.go, rss_test.go, scraper.go, scraper_test.go
    cluster/cluster.go, cluster_test.go
    ai/pipeline.go, pipeline_test.go, generator.go, generator_test.go, critic.go, critic_test.go, prompts.go, cache.go, cache_test.go, hfclient.go, hfclient_test.go
    article/manager.go, manager_test.go, revision.go, revision_test.go
    mail/outbound.go, outbound_test.go, inbound.go, inbound_test.go, templates.go
    newsletter/newsletter.go, newsletter_test.go
    vote/vote.go, vote_test.go
    web/server.go, routes.go, public.go, public_test.go, admin.go, admin_test.go, auth.go, auth_test.go, api.go, api_test.go, middleware.go
    scheduler/scheduler.go, scheduler_test.go
  templates/
    layout.html
    public/home.html, article.html, subscribe.html
    admin/login.html, dashboard.html, published.html, sources.html, subscribers.html, newsletter.html, settings.html
    email/notification.html, newsletter.html, confirm.html
  static/css/style.css, js/main.js, img/logo.svg
  config.example.yaml
  go.mod, go.sum, Makefile, .gitignore
  .github/workflows/ci.yml, deploy.yml
```

---

### Task 1: Project Scaffold & Configuration

**Files:**
- Create: `go.mod`
- Create: `cmd/milesahead/main.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `config.example.yaml`
- Create: `.gitignore`
- Create: `Makefile`

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/anik/Experiment/scanner
go mod init github.com/kumaranik/milesahead
```

- [ ] **Step 2: Create .gitignore**

Create `.gitignore`:
```
config.yaml
*.db
*.db-wal
*.db-shm
.env
/milesahead
/dist/
```

- [ ] **Step 3: Create config.example.yaml**

Create `config.example.yaml` with the full config structure from spec Section 9. All sensitive values use `${ENV_VAR}` placeholders.

- [ ] **Step 4: Write failing test for config loading**

Create `internal/config/config_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	// Write a temp config file
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  port: 8080
  domain: milesahead.kumaranik.com
admin:
  password_hash: "$2a$12$test"
db:
  path: /tmp/test.db
scanner:
  default_interval: 180
  user_agent: "MilesAhead/1.0"
ai:
  generator_model: "mistralai/Mistral-7B-Instruct-v0.3"
  critic_model: "meta-llama/Meta-Llama-3-8B-Instruct"
  hf_api_token: "test-token"
  critic_threshold: 70
  max_retries: 3
  cache_ttl: 3600
mail:
  smtp:
    host: smtp.example.com
    port: 587
    user: "user"
    password: "pass"
    from: "noreply@example.com"
  imap:
    host: imap.gmail.com
    port: 993
    user: "user"
    password: "pass"
    poll_interval: 120
newsletter:
  send_time: "08:00"
  timezone: "America/New_York"
votes:
  alert_threshold: 10
  alert_ratio: 0.4
  alert_window: 24
`
	os.WriteFile(path, []byte(yaml), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.AI.CriticThreshold != 70 {
		t.Errorf("expected threshold 70, got %d", cfg.AI.CriticThreshold)
	}
}

func TestLoadConfig_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
server:
  port: 8080
  domain: test.com
admin:
  password_hash: "hash"
db:
  path: /tmp/test.db
scanner:
  default_interval: 180
  user_agent: "Test/1.0"
ai:
  generator_model: "model"
  critic_model: "model"
  hf_api_token: "${HF_API_TOKEN}"
  critic_threshold: 70
  max_retries: 3
  cache_ttl: 3600
mail:
  smtp:
    host: smtp.example.com
    port: 587
    user: "user"
    password: "pass"
    from: "from@example.com"
  imap:
    host: imap.gmail.com
    port: 993
    user: "user"
    password: "pass"
    poll_interval: 120
newsletter:
  send_time: "08:00"
  timezone: "America/New_York"
votes:
  alert_threshold: 10
  alert_ratio: 0.4
  alert_window: 24
`
	os.WriteFile(path, []byte(yaml), 0644)
	os.Setenv("HF_API_TOKEN", "env-token-123")
	defer os.Unsetenv("HF_API_TOKEN")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.AI.HFAPIToken != "env-token-123" {
		t.Errorf("expected env-token-123, got %s", cfg.AI.HFAPIToken)
	}
}
```

- [ ] **Step 5: Run test to verify it fails**

```bash
go test ./internal/config/ -v
```
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 6: Implement config loading**

Create `internal/config/config.go`:
```go
package config

import (
	"os"
	"regexp"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Admin      AdminConfig      `yaml:"admin"`
	DB         DBConfig         `yaml:"db"`
	Scanner    ScannerConfig    `yaml:"scanner"`
	AI         AIConfig         `yaml:"ai"`
	Mail       MailConfig       `yaml:"mail"`
	Newsletter NewsletterConfig `yaml:"newsletter"`
	Votes      VotesConfig      `yaml:"votes"`
}

type ServerConfig struct {
	Port   int    `yaml:"port"`
	Domain string `yaml:"domain"`
}

type AdminConfig struct {
	PasswordHash string `yaml:"password_hash"`
}

type DBConfig struct {
	Path string `yaml:"path"`
}

type ScannerConfig struct {
	DefaultInterval int    `yaml:"default_interval"`
	UserAgent       string `yaml:"user_agent"`
}

type AIConfig struct {
	GeneratorModel string `yaml:"generator_model"`
	CriticModel    string `yaml:"critic_model"`
	HFAPIToken     string `yaml:"hf_api_token"`
	CriticThreshold int   `yaml:"critic_threshold"`
	MaxRetries     int    `yaml:"max_retries"`
	CacheTTL       int    `yaml:"cache_ttl"`
}

type MailConfig struct {
	SMTP SMTPConfig `yaml:"smtp"`
	IMAP IMAPConfig `yaml:"imap"`
}

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type IMAPConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	PollInterval int    `yaml:"poll_interval"`
}

type NewsletterConfig struct {
	SendTime string `yaml:"send_time"`
	Timezone string `yaml:"timezone"`
}

type VotesConfig struct {
	AlertThreshold int     `yaml:"alert_threshold"`
	AlertRatio     float64 `yaml:"alert_ratio"`
	AlertWindow    int     `yaml:"alert_window"`
}

var envPattern = regexp.MustCompile(`\$\{(\w+)\}`)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Replace ${ENV_VAR} patterns with environment values
	expanded := envPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		varName := envPattern.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
```

- [ ] **Step 7: Install dependency and run tests**

```bash
go get gopkg.in/yaml.v3
go test ./internal/config/ -v
```
Expected: PASS

- [ ] **Step 8: Create minimal main.go**

Create `cmd/milesahead/main.go`:
```go
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/kumaranik/milesahead/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	fmt.Fprintf(os.Stdout, "Miles Ahead starting on port %d\n", cfg.Server.Port)
}
```

- [ ] **Step 9: Create Makefile**

Create `Makefile`:
```makefile
.PHONY: build test run clean

build:
	go build -o milesahead ./cmd/milesahead

test:
	go test ./... -v

run: build
	./milesahead -config config.yaml

clean:
	rm -f milesahead

lint:
	go vet ./...
```

- [ ] **Step 10: Verify build**

```bash
make build
```
Expected: Binary compiles successfully.

- [ ] **Step 11: Commit**

```bash
git init
git add .
git commit -m "feat: project scaffold with config loading and env var support"
```

---

### Task 2: Database Layer & Models

**Files:**
- Create: `internal/db/db.go`
- Create: `internal/db/db_test.go`
- Create: `internal/db/models.go`
- Create: `internal/db/migrations.go`

- [ ] **Step 1: Install SQLite dependency**

```bash
go get modernc.org/sqlite
```

- [ ] **Step 2: Write failing test for DB initialization and migrations**

Create `internal/db/db_test.go`:
```go
package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Verify tables exist
	tables := []string{
		"sources", "raw_articles", "generated_articles",
		"article_sources", "verification_results", "admin_feedback",
		"article_revisions", "article_votes", "newsletters",
		"subscribers", "admin_sessions",
	}
	for _, table := range tables {
		var name string
		err := database.DB.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s not found: %v", table, err)
		}
	}
}

func TestSourceCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Insert
	src := &Source{
		Name:   "The Points Guy",
		URL:    "https://thepointsguy.com",
		RSSURL: strPtr("https://thepointsguy.com/feed/"),
		Active: true,
	}
	id, err := database.CreateSource(src)
	if err != nil {
		t.Fatalf("CreateSource failed: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero id")
	}

	// Read
	got, err := database.GetSource(id)
	if err != nil {
		t.Fatalf("GetSource failed: %v", err)
	}
	if got.Name != "The Points Guy" {
		t.Errorf("expected The Points Guy, got %s", got.Name)
	}

	// List active
	sources, err := database.ListActiveSources()
	if err != nil {
		t.Fatalf("ListActiveSources failed: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("expected 1 source, got %d", len(sources))
	}
}

func strPtr(s string) *string { return &s }
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/db/ -v
```
Expected: FAIL

- [ ] **Step 4: Create models**

Create `internal/db/models.go` with Go structs for all tables from spec Section 3:
- `Source`, `RawArticle`, `GeneratedArticle`, `ArticleSource`, `VerificationResult`, `AdminFeedback`, `ArticleRevision`, `ArticleVote`, `Newsletter`, `Subscriber`, `AdminSession`
- Each struct uses `time.Time` for datetime fields, `*string` for nullable text fields
- `GeneratedArticle.Status` as a string type with constants: `StatusDraft`, `StatusPending`, `StatusApproved`, `StatusRejected`, `StatusRevisionRequested`, `StatusUnpublished`

- [ ] **Step 5: Create migrations**

Create `internal/db/migrations.go` with a `migrate()` function containing a single SQL string that creates all 11 tables with proper foreign keys, indexes on `raw_articles.hash`, `generated_articles.slug`, `generated_articles.status`, and `subscribers.token`.

- [ ] **Step 6: Create database open/close and Source CRUD**

Create `internal/db/db.go`:
- `Database` struct wrapping `*sql.DB`
- `Open(path string) (*Database, error)` — opens SQLite with WAL mode (`_journal_mode=WAL` pragma), runs migrations
- `Close() error`
- `CreateSource`, `GetSource`, `ListActiveSources`, `UpdateSource`, `DeleteSource`

- [ ] **Step 7: Run tests**

```bash
go test ./internal/db/ -v
```
Expected: PASS

- [ ] **Step 8: Add RawArticle CRUD + dedup test**

Add to `db_test.go`:
```go
func TestRawArticleDedup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	srcID, _ := database.CreateSource(&Source{
		Name: "Test", URL: "https://test.com", Active: true,
	})

	article := &RawArticle{
		SourceID: srcID,
		Title:    "Test Article",
		URL:      "https://test.com/article",
		Content:  "Content here",
		Hash:     "abc123",
	}

	_, err = database.CreateRawArticle(article)
	if err != nil {
		t.Fatalf("first insert failed: %v", err)
	}

	// Duplicate should return false, nil (not error)
	created, err := database.CreateRawArticleIfNew(article)
	if err != nil {
		t.Fatalf("dedup check failed: %v", err)
	}
	if created {
		t.Error("expected duplicate to return false")
	}
}
```

- [ ] **Step 9: Implement RawArticle CRUD with dedup**

Add to `db.go`:
- `CreateRawArticle` — insert, returns id
- `CreateRawArticleIfNew` — INSERT OR IGNORE on hash, returns (bool, error)
- `GetRawArticlesBySourceID` — list by source
- `GetRawArticlesByIDs` — batch fetch for AI pipeline

- [ ] **Step 10: Run tests**

```bash
go test ./internal/db/ -v
```
Expected: PASS

- [ ] **Step 11: Add GeneratedArticle CRUD + status transitions**

Add tests for:
- Creating a generated article with status "draft"
- Transitioning status: draft→pending, pending→approved, pending→rejected, pending→revision_requested, approved→unpublished
- Listing articles by status
- Listing approved articles for public site (ordered by published_at DESC)

Implement in `db.go`:
- `CreateGeneratedArticle`, `GetGeneratedArticle`, `UpdateArticleStatus`, `ListArticlesByStatus`, `ListPublishedArticles(limit, offset int)`
- `LinkArticleSources(generatedID int64, rawIDs []int64)`

- [ ] **Step 12: Run tests**

```bash
go test ./internal/db/ -v
```
Expected: PASS

- [ ] **Step 13: Add remaining CRUD operations**

Add and test CRUD for:
- `VerificationResult`: `CreateVerificationResult`, `GetLatestVerification(articleID)`
- `AdminFeedback`: `CreateAdminFeedback`, `ListFeedbackForArticle`
- `ArticleRevision`: `CreateArticleRevision`, `ListRevisionsForArticle`
- `ArticleVote`: `CreateVote`, `GetVoteCounts(articleID)`, `HasVoted(articleID, ipHash)`
- `Newsletter`: `CreateNewsletter`, `GetNewsletterByDate`, `MarkNewsletterSent`
- `Subscriber`: `CreateSubscriber`, `ConfirmSubscriber(token)`, `ListConfirmedSubscribers`, `Unsubscribe(token)`
- `AdminSession`: `CreateSession`, `GetSession(token)`, `DeleteExpiredSessions`

- [ ] **Step 14: Run all tests**

```bash
go test ./internal/db/ -v -count=1
```
Expected: All PASS

- [ ] **Step 15: Commit**

```bash
git add .
git commit -m "feat: database layer with SQLite, migrations, and full CRUD"
```

---

### Task 3: RSS Scanner

**Files:**
- Create: `internal/scanner/rss.go`
- Create: `internal/scanner/rss_test.go`

- [ ] **Step 1: Install gofeed**

```bash
go get github.com/mmcdole/gofeed
```

- [ ] **Step 2: Write failing test for RSS parsing**

Create `internal/scanner/rss_test.go`:
```go
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
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/scanner/ -run TestFetchRSS -v
```
Expected: FAIL

- [ ] **Step 4: Implement RSS fetcher**

Create `internal/scanner/rss.go`:
```go
package scanner

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"

	"github.com/mmcdole/gofeed"
)

type FetchedArticle struct {
	Title       string
	URL         string
	Content     string
	PublishedAt time.Time
	Hash        string
}

type RSSFetcher struct {
	userAgent string
	parser    *gofeed.Parser
}

func NewRSSFetcher(userAgent string) *RSSFetcher {
	return &RSSFetcher{
		userAgent: userAgent,
		parser:    gofeed.NewParser(),
	}
}

func (f *RSSFetcher) Fetch(feedURL string) ([]FetchedArticle, error) {
	req, err := http.NewRequest("GET", feedURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	feed, err := f.parser.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse RSS: %w", err)
	}

	var articles []FetchedArticle
	for _, item := range feed.Items {
		published := time.Now()
		if item.PublishedParsed != nil {
			published = *item.PublishedParsed
		}
		content := item.Description
		if item.Content != "" {
			content = item.Content
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(item.Title+item.Link)))
		articles = append(articles, FetchedArticle{
			Title:       item.Title,
			URL:         item.Link,
			Content:     content,
			PublishedAt: published,
			Hash:        hash,
		})
	}
	return articles, nil
}
```

- [ ] **Step 5: Run tests**

```bash
go test ./internal/scanner/ -v
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat: RSS feed fetcher with dedup hashing"
```

---

### Task 4: HTML Scraper

**Files:**
- Create: `internal/scanner/scraper.go`
- Create: `internal/scanner/scraper_test.go`

- [ ] **Step 1: Install goquery**

```bash
go get github.com/PuerkitoBio/goquery
```

- [ ] **Step 2: Write failing test for HTML scraping**

Create `internal/scanner/scraper_test.go`:
```go
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
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/scanner/ -run TestScrape -v
```
Expected: FAIL

- [ ] **Step 4: Implement HTML scraper**

Create `internal/scanner/scraper.go`:
- `ScrapeSelectors` struct: ArticleContainer, Title, Link, Content, Time (all CSS selectors)
- `HTMLScraper` struct with userAgent
- `NewHTMLScraper(userAgent string) *HTMLScraper`
- `Scrape(pageURL string, selectors ScrapeSelectors) ([]FetchedArticle, error)` — uses goquery to parse page, extracts articles using selectors, resolves relative URLs, computes SHA-256 hash

- [ ] **Step 5: Run tests**

```bash
go test ./internal/scanner/ -v
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat: HTML scraper with configurable CSS selectors"
```

---

### Task 5: Scanner Orchestrator

**Files:**
- Create: `internal/scanner/scanner.go`
- Create: `internal/scanner/scanner_test.go` (extend)

- [ ] **Step 1: Write failing test for scanner orchestrator**

Add to `internal/scanner/scanner_test.go`:
```go
func TestScannerOrchestrator(t *testing.T) {
	// Setup: test RSS server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(testRSS))
	}))
	defer ts.Close()

	// Setup: in-memory DB
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, _ := db.Open(dbPath)
	defer database.Close()

	srcID, _ := database.CreateSource(&db.Source{
		Name:   "Test Source",
		URL:    ts.URL,
		RSSURL: strPtr(ts.URL),
		Active: true,
	})

	scanner := NewScanner(database, "TestAgent/1.0")
	results, err := scanner.ScanSource(srcID)
	if err != nil {
		t.Fatalf("ScanSource failed: %v", err)
	}
	if results.NewArticles != 2 {
		t.Errorf("expected 2 new articles, got %d", results.NewArticles)
	}

	// Second scan should find 0 new (dedup)
	results2, _ := scanner.ScanSource(srcID)
	if results2.NewArticles != 0 {
		t.Errorf("expected 0 new articles on rescan, got %d", results2.NewArticles)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/scanner/ -run TestScannerOrchestrator -v
```
Expected: FAIL

- [ ] **Step 3: Implement scanner orchestrator**

Create `internal/scanner/scanner.go`:
- `Scanner` struct with `db *db.Database`, `rss *RSSFetcher`, `scraper *HTMLScraper`
- `NewScanner(db, userAgent) *Scanner`
- `ScanSource(sourceID int64) (*ScanResult, error)` — loads source from DB, tries RSS first, falls back to HTML scraper, deduplicates via `CreateRawArticleIfNew`, returns `ScanResult{NewArticles int, Errors []error}`
- `ScanAll() ([]ScanResult, error)` — scans all active sources

- [ ] **Step 4: Run tests**

```bash
go test ./internal/scanner/ -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: scanner orchestrator with RSS-first, HTML fallback, and dedup"
```

---

### Task 6: Topic Clustering

**Files:**
- Create: `internal/cluster/cluster.go`
- Create: `internal/cluster/cluster_test.go`

- [ ] **Step 1: Write failing test for clustering**

Create `internal/cluster/cluster_test.go`:
```go
package cluster

import (
	"testing"
)

func TestClusterByTitle(t *testing.T) {
	articles := []Article{
		{ID: 1, Title: "Amex 40% Transfer Bonus to ANA Mileage Club", SourceID: 1},
		{ID: 2, Title: "Amex Membership Rewards 40% Bonus Transfer to ANA", SourceID: 2},
		{ID: 3, Title: "Chase Sapphire Preferred 80K Sign-Up Bonus Returns", SourceID: 1},
		{ID: 4, Title: "Chase Sapphire 80K Offer Is Back", SourceID: 3},
		{ID: 5, Title: "Hilton Honors Devaluation Analysis", SourceID: 2},
	}

	clusters := Cluster(articles, 0.3)

	// Should get 3 clusters: Amex (2), Chase (2), Hilton (1)
	if len(clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(clusters))
	}

	// Find the Amex cluster
	var amexCluster *ArticleCluster
	for i, c := range clusters {
		for _, a := range c.Articles {
			if a.ID == 1 {
				amexCluster = &clusters[i]
				break
			}
		}
	}
	if amexCluster == nil || len(amexCluster.Articles) != 2 {
		t.Error("expected Amex cluster with 2 articles")
	}
}

func TestClusterSingleArticle(t *testing.T) {
	articles := []Article{
		{ID: 1, Title: "Unique Article About Something", SourceID: 1},
	}
	clusters := Cluster(articles, 0.3)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if len(clusters[0].Articles) != 1 {
		t.Error("expected single article in cluster")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/cluster/ -v
```
Expected: FAIL

- [ ] **Step 3: Implement clustering**

Create `internal/cluster/cluster.go`:
- `Article` struct: ID, Title, SourceID, Content
- `ArticleCluster` struct: Articles []Article
- `Cluster(articles []Article, threshold float64) []ArticleCluster`
- Simple TF-IDF on titles: tokenize (lowercase, split on non-alpha), compute term frequency, compute IDF across all titles, cosine similarity between all pairs, greedy agglomerative clustering with threshold

Keep it simple — no external dependencies. This is ~100 lines of Go.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/cluster/ -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: topic clustering with TF-IDF cosine similarity"
```

---

### Task 7: Hugging Face Client

**Files:**
- Create: `internal/ai/hfclient.go`
- Create: `internal/ai/hfclient_test.go`
- Create: `internal/ai/cache.go`
- Create: `internal/ai/cache_test.go`

- [ ] **Step 1: Write failing test for HF client**

Create `internal/ai/hfclient_test.go`:
```go
package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHFClient_Generate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth header")
		}
		resp := []map[string]string{{"generated_text": "This is a generated article."}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL // override for testing

	result, err := client.Generate("test-model", "Write an article", 500)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result != "This is a generated article." {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestHFClient_RateLimit(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		resp := []map[string]string{{"generated_text": "Success after retry."}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL

	result, err := client.Generate("test-model", "prompt", 500)
	if err != nil {
		t.Fatalf("expected retry success, got: %v", err)
	}
	if result != "Success after retry." {
		t.Errorf("unexpected: %s", result)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/ai/ -run TestHFClient -v
```
Expected: FAIL

- [ ] **Step 3: Implement HF client**

Create `internal/ai/hfclient.go`:
- `HFClient` struct: token, maxRetries, BaseURL (default: `https://api-inference.huggingface.co`)
- `NewHFClient(token string, maxRetries int) *HFClient`
- `Generate(model, prompt string, maxTokens int) (string, error)` — POST to `/models/{model}` with JSON body, parse response, exponential backoff on 429/503

- [ ] **Step 4: Write failing test for cache**

Create `internal/ai/cache_test.go`:
```go
package ai

import (
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	cache := NewCache(1 * time.Hour)

	cache.Set("key1", "value1")
	val, ok := cache.Get("key1")
	if !ok || val != "value1" {
		t.Error("expected cache hit")
	}

	_, ok = cache.Get("missing")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := NewCache(1 * time.Millisecond)
	cache.Set("key1", "value1")
	time.Sleep(5 * time.Millisecond)
	_, ok := cache.Get("key1")
	if ok {
		t.Error("expected expired cache miss")
	}
}
```

- [ ] **Step 5: Implement cache**

Create `internal/ai/cache.go`:
- In-memory map with TTL. `Cache` struct with `sync.RWMutex`, entries map, TTL duration.
- `NewCache(ttl time.Duration) *Cache`
- `Get(key string) (string, bool)`
- `Set(key, value string)`
- Key is SHA-256 of the prompt input.

- [ ] **Step 6: Run all AI tests**

```bash
go test ./internal/ai/ -v
```
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add .
git commit -m "feat: Hugging Face inference client with retry and response cache"
```

---

### Task 8: AI Pipeline — Generator & Critic

**Files:**
- Create: `internal/ai/prompts.go`
- Create: `internal/ai/generator.go`
- Create: `internal/ai/generator_test.go`
- Create: `internal/ai/critic.go`
- Create: `internal/ai/critic_test.go`
- Create: `internal/ai/pipeline.go`
- Create: `internal/ai/pipeline_test.go`

- [ ] **Step 1: Create prompt templates**

Create `internal/ai/prompts.go`:
- `DealAlertPrompt(sources []SourceArticle) string` — instructs model to extract key details (program, bonus, dates), write concise post, include inline citations `[SourceName]`
- `RoundupPrompt(sources []SourceArticle) string` — compare, rank, cross-reference
- `SummaryPrompt(sources []SourceArticle) string` — synthesize perspectives, balanced
- `CriticPrompt(generatedArticle string, sources []SourceArticle) string` — check factual accuracy, attribution, completeness, hallucination, orphan citations. Return JSON with score and issues array.
- `RevisionPrompt(sources []SourceArticle, previousOutput, feedback, criticIssues string) string` — address feedback points, keep source citations
- `SourceArticle` struct: Name, URL, Title, Content

- [ ] **Step 2: Write failing test for generator**

Create `internal/ai/generator_test.go` — mock HF client (interface-based), test that generator calls correct prompt for each article type, returns generated content.

- [ ] **Step 3: Implement generator**

Create `internal/ai/generator.go`:
- `Generator` struct with `client HFClientInterface`, `model string`, `cache *Cache`
- `Generate(articleType string, sources []SourceArticle) (string, error)` — selects prompt by type, calls HF client, caches response
- `GenerateWithFeedback(sources []SourceArticle, previousOutput, feedback, criticIssues string) (string, error)` — uses revision prompt

- [ ] **Step 4: Run generator tests**

```bash
go test ./internal/ai/ -run TestGenerator -v
```
Expected: PASS

- [ ] **Step 5: Write failing test for critic**

Create `internal/ai/critic_test.go` — mock HF client returns JSON with score and issues, test that critic parses response correctly.

- [ ] **Step 6: Implement critic**

Create `internal/ai/critic.go`:
- `Critic` struct with `client HFClientInterface`, `model string`
- `VerificationResult` struct: Score int, Issues []string, FactualCheck, AttributionCheck, CompletenessCheck, HallucinationCheck bool
- `Verify(generatedArticle string, sources []SourceArticle) (*VerificationResult, error)` — calls HF with critic prompt, parses JSON response

- [ ] **Step 7: Run critic tests**

```bash
go test ./internal/ai/ -run TestCritic -v
```
Expected: PASS

- [ ] **Step 8: Write failing test for pipeline**

Create `internal/ai/pipeline_test.go` — test full flow: sources → generator → critic → result with status determination based on threshold.

- [ ] **Step 9: Implement pipeline**

Create `internal/ai/pipeline.go`:
- `Pipeline` struct: generator, critic, threshold
- `NewPipeline(generator *Generator, critic *Critic, threshold int) *Pipeline`
- `Process(articleType string, sources []SourceArticle) (*PipelineResult, error)` — generate → verify → return PipelineResult{Content, Title, Verification, PassedThreshold}
- `ProcessWithFeedback(sources []SourceArticle, previousOutput, feedback, criticIssues string) (*PipelineResult, error)` — revision flow

- [ ] **Step 10: Run all AI tests**

```bash
go test ./internal/ai/ -v
```
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add .
git commit -m "feat: AI pipeline with generator, critic, prompts, and revision support"
```

---

### Task 9: Article Manager (State Machine)

**Files:**
- Create: `internal/article/manager.go`
- Create: `internal/article/manager_test.go`
- Create: `internal/article/revision.go`
- Create: `internal/article/revision_test.go`

- [ ] **Step 1: Write failing test for article manager**

Test state transitions: valid transitions succeed, invalid transitions return error. Test that `Approve` sets `published_at`, `Unpublish` clears it.

- [ ] **Step 2: Implement article manager**

Create `internal/article/manager.go`:
- `Manager` struct with `db *db.Database`, `pipeline *ai.Pipeline`
- `CreateFromPipeline(result *ai.PipelineResult, rawArticleIDs []int64, articleType string) (int64, error)` — creates generated article + links sources + stores verification result, sets status based on threshold
- `Approve(articleID int64) error` — validate current status is pending, transition to approved, set published_at
- `Reject(articleID int64) error`
- `RequestRevision(articleID int64, feedback string, source string) error` — set status to revision_requested, store feedback
- `Unpublish(articleID int64) error` — validate current status is approved, transition to unpublished
- `UnpublishAndRevise(articleID int64, feedback string, source string) error` — unpublish + request revision in one step
- Slug generation: lowercase title, replace spaces with hyphens, strip non-alphanumeric, truncate to 80 chars, append short random suffix for uniqueness

- [ ] **Step 3: Run tests**

```bash
go test ./internal/article/ -v
```
Expected: PASS

- [ ] **Step 4: Write failing test for revision flow**

Test: create article → request revision with feedback → process revision → verify new content is stored with incremented revision_number and revision history.

- [ ] **Step 5: Implement revision handler**

Create `internal/article/revision.go`:
- `ProcessRevision(articleID int64) error` — loads article + feedback + original sources, calls pipeline.ProcessWithFeedback, stores new content + revision record, updates status based on critic result

- [ ] **Step 6: Run tests**

```bash
go test ./internal/article/ -v
```
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add .
git commit -m "feat: article manager with state machine and revision workflow"
```

---

### Task 10: Web Server Foundation & Admin Auth

**Files:**
- Create: `internal/web/server.go`
- Create: `internal/web/routes.go`
- Create: `internal/web/middleware.go`
- Create: `internal/web/auth.go`
- Create: `internal/web/auth_test.go`
- Create: `templates/layout.html`
- Create: `templates/admin/login.html`

- [ ] **Step 1: Write failing test for auth**

Create `internal/web/auth_test.go`:
```go
package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLogin_Success(t *testing.T) {
	app := setupTestApp(t)
	form := url.Values{"password": {"test-password"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	app.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect, got %d", w.Code)
	}
	cookies := w.Result().Cookies()
	found := false
	for _, c := range cookies {
		if c.Name == "session" {
			found = true
		}
	}
	if !found {
		t.Error("expected session cookie")
	}
}

func TestLogin_BadPassword(t *testing.T) {
	app := setupTestApp(t)
	form := url.Values{"password": {"wrong-password"}}
	req := httptest.NewRequest("POST", "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	app.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAdminMiddleware_NoSession(t *testing.T) {
	app := setupTestApp(t)
	req := httptest.NewRequest("GET", "/admin/dashboard", nil)
	w := httptest.NewRecorder()

	app.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected redirect to login, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/web/ -run TestLogin -v
```
Expected: FAIL

- [ ] **Step 3: Implement web server, routes, middleware, and auth**

Create `internal/web/server.go`:
- `App` struct: db, config, templates, mux
- `NewApp(cfg *config.Config, database *db.Database) (*App, error)` — loads templates, sets up routes
- `ServeHTTP(w, r)` — delegates to mux

Create `internal/web/middleware.go`:
- `csrfMiddleware` — generates and validates CSRF tokens (stored in session)
- `adminAuthMiddleware` — checks session cookie, redirects to login if missing/expired
- `rateLimitMiddleware` — simple in-memory rate limiter for login endpoint

Create `internal/web/auth.go`:
- `handleLogin(w, r)` — GET shows form, POST validates password against bcrypt hash, creates session, sets cookie
- `handleLogout(w, r)` — clears session
- `setupTestApp(t)` helper for tests — creates temp DB, test config with known password hash

Create `internal/web/routes.go`:
- Register all routes (public + admin) with appropriate middleware

- [ ] **Step 4: Create layout and login templates**

Create `templates/layout.html` — base HTML with head, nav, footer, theme toggle JS, CSRF token in forms.

Create `templates/admin/login.html` — simple password form.

- [ ] **Step 5: Run auth tests**

```bash
go test ./internal/web/ -v
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat: web server with admin auth, CSRF, and rate limiting"
```

---

### Task 11: Admin Dashboard

**Files:**
- Create: `internal/web/admin.go`
- Create: `internal/web/admin_test.go`
- Create: `templates/admin/dashboard.html`
- Create: `templates/admin/published.html`
- Create: `templates/admin/sources.html`
- Create: `templates/admin/subscribers.html`
- Create: `templates/admin/settings.html`

- [ ] **Step 1: Write failing tests for admin handlers**

Test: GET /admin/dashboard returns pending articles, POST /admin/articles/{id}/approve transitions status, POST /admin/articles/{id}/reject transitions status, POST /admin/articles/{id}/revise with feedback text triggers revision_requested.

- [ ] **Step 2: Implement admin handlers**

Create `internal/web/admin.go`:
- `handleDashboard` — list pending articles with verification scores, source counts
- `handlePublished` — list approved articles with vote counts, flag high-downvote articles
- `handleApprove` — call article manager Approve
- `handleReject` — call article manager Reject
- `handleRequestRevision` — call article manager RequestRevision with feedback from form
- `handleUnpublish` — call article manager Unpublish
- `handleUnpublishAndRevise` — call article manager UnpublishAndRevise
- `handleSources` — CRUD for sources (list, add, edit, toggle active)
- `handleSubscribers` — list, export CSV
- `handleSettings` — view/update config (display only for now, config changes require restart)

- [ ] **Step 3: Create admin templates**

Create all admin templates from the spec template list. Each extends `layout.html`. Use Go template actions for dynamic content.

`dashboard.html`: Table of pending articles, expandable rows with full content, critic issues, revision history. Action buttons for approve/reject/revise.

`published.html`: Table with title, type, published date, vote counts (up/down), flag icon for threshold breakers. Unpublish button.

`sources.html`: Table with name, URL, RSS URL, interval, active toggle. Add/edit form.

`subscribers.html`: Table with email, confirmed status, dates. Export CSV button.

`settings.html`: Read-only display of current config (with sensitive values masked).

- [ ] **Step 4: Run tests**

```bash
go test ./internal/web/ -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: admin dashboard with article review, sources, subscribers, settings"
```

---

### Task 12: Public Site

**Files:**
- Create: `internal/web/public.go`
- Create: `internal/web/public_test.go`
- Create: `templates/public/home.html`
- Create: `templates/public/article.html`
- Create: `templates/public/subscribe.html`
- Create: `static/css/style.css`
- Create: `static/js/main.js`
- Create: `static/img/logo.svg`

- [ ] **Step 1: Write failing tests for public handlers**

Test: GET / returns homepage with approved articles only (no drafts/pending), GET /article/{slug} returns article page with source citations, GET /article/{slug} for unpublished article shows "under review" placeholder, GET /feed.xml returns valid RSS.

- [ ] **Step 2: Implement public handlers**

Create `internal/web/public.go`:
- `handleHome` — fetch featured article + recent approved articles, render magazine grid
- `handleArticle` — fetch by slug, render article page with sources and vote buttons, show "under review" for unpublished
- `handleSubscribe` — GET shows form, POST creates subscriber with confirmation token
- `handleConfirmSubscription` — validates token, sets confirmed=true
- `handleUnsubscribe` — validates token, removes subscriber
- `handleRSSFeed` — generates RSS XML with approved article excerpts

- [ ] **Step 3: Create public templates**

`home.html`: Magazine Grid layout from design (style C). Featured article large on left, sidebar stack of 3 on right, bottom grid of 3. Category filter tabs (All, Deals, Roundups, Alerts, Summaries) — filter via query param `?type=deal`. Responsive.

`article.html`: Article title, type badge, published date, full content with inline citation links, thumbs up/down buttons (JS-powered), related articles at bottom.

`subscribe.html`: Email input, submit button, confirmation message.

- [ ] **Step 4: Create CSS with light/dark mode**

Create `static/css/style.css`:
- CSS custom properties for theming: `--bg`, `--text`, `--card-bg`, `--accent`, etc.
- `:root` for light mode defaults, `[data-theme="dark"]` overrides
- Magazine grid layout using CSS Grid
- Article type badge colors: deal=#e94560, roundup=#4ecdc4, alert=#f9a826, summary=#2563eb
- Featured article styling (large card with gradient)
- Responsive breakpoints: mobile (<768px) single column, tablet (768-1024) two column, desktop full grid
- Admin dashboard table styles
- Vote button styles (active state on voted)

- [ ] **Step 5: Create JavaScript**

Create `static/js/main.js`:
- Theme toggle: reads `data-theme` from localStorage, applies to `<html>`, toggle button switches and persists
- Vote handler: POST to `/api/vote` on click, update count display, disable buttons after vote, store in localStorage
- Category filter: update URL query param, highlight active tab

- [ ] **Step 6: Create logo SVG**

Create `static/img/logo.svg` — simple "MA" monogram or airplane/arrow icon in SVG.

- [ ] **Step 7: Run tests**

```bash
go test ./internal/web/ -v
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add .
git commit -m "feat: public site with magazine grid, light/dark mode, and RSS feed"
```

---

### Task 13: Vote API

**Files:**
- Create: `internal/vote/vote.go`
- Create: `internal/vote/vote_test.go`
- Create: `internal/web/api.go`
- Create: `internal/web/api_test.go`

- [ ] **Step 1: Write failing test for vote service**

Create `internal/vote/vote_test.go`:
```go
package vote

import (
	"testing"
)

func TestCastVote(t *testing.T) {
	svc := setupTestVoteService(t)

	err := svc.CastVote(1, "up", "iphash123")
	if err != nil {
		t.Fatalf("CastVote failed: %v", err)
	}

	// Duplicate vote should be rejected
	err = svc.CastVote(1, "down", "iphash123")
	if err != ErrAlreadyVoted {
		t.Errorf("expected ErrAlreadyVoted, got: %v", err)
	}

	counts, _ := svc.GetCounts(1)
	if counts.Up != 1 || counts.Down != 0 {
		t.Errorf("unexpected counts: %+v", counts)
	}
}

func TestVoteThresholdAlert(t *testing.T) {
	svc := setupTestVoteService(t) // threshold=3, ratio=0.5

	// Track alert via callback
	alerted := false
	svc.OnAlert = func(articleID int64) { alerted = true }

	svc.CastVote(1, "down", "ip1")
	svc.CastVote(1, "down", "ip2")
	svc.CastVote(1, "down", "ip3")

	if !alerted {
		t.Error("expected alert callback to fire after 3 downvotes (threshold=3)")
	}
}
```

- [ ] **Step 2: Implement vote service**

Create `internal/vote/vote.go`:
- `Service` struct: db, alertThreshold, alertRatio, alertWindow, onAlert callback
- `CastVote(articleID int64, vote, ipHash string) error` — check HasVoted, insert, check thresholds
- `GetCounts(articleID int64) (*VoteCounts, error)`
- `CheckThreshold(articleID int64) (bool, error)` — count down votes in window, check against threshold and ratio
- `ErrAlreadyVoted` sentinel error

- [ ] **Step 3: Write failing test for vote API endpoint**

Test: POST /api/vote with JSON body `{article_id: 1, vote: "up"}` returns 200 with counts. Duplicate returns 409.

- [ ] **Step 4: Implement vote API**

Create `internal/web/api.go`:
- `handleVote` — parse JSON, extract IP, hash it, call vote service, return counts as JSON

- [ ] **Step 5: Run tests**

```bash
go test ./internal/vote/ -v && go test ./internal/web/ -run TestVote -v
```
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .
git commit -m "feat: voting system with threshold alerting and API endpoint"
```

---

### Task 14: Email Outbound (SMTP)

**Files:**
- Create: `internal/mail/outbound.go`
- Create: `internal/mail/outbound_test.go`
- Create: `internal/mail/templates.go`
- Create: `templates/email/notification.html`
- Create: `templates/email/confirm.html`

- [ ] **Step 1: Install gomail**

```bash
go get gopkg.in/gomail.v2
```

- [ ] **Step 2: Write failing test for email rendering**

Test that `RenderNotification` produces HTML containing article title, content, verification score, source links, and instructions for reply actions (approve/reject/unpublish/feedback).

- [ ] **Step 3: Implement email templates and outbound**

Create `internal/mail/templates.go`:
- `RenderNotification(article, verification, token) (string, error)` — render admin notification
- `RenderConfirmation(token, domain) (string, error)` — subscriber confirmation
- Load templates from `templates/email/`

Create `internal/mail/outbound.go`:
- `Sender` struct with SMTP config
- `NewSender(cfg config.SMTPConfig) *Sender`
- `SendAdminNotification(article, verification, reviewToken string) error` — sends to admin email with reply-to `review+{token}@gmail.com`
- `SendConfirmation(email, token string) error`
- `SendNewsletter(subject, htmlContent string, recipients []string) error` — sends to all confirmed subscribers

Create `templates/email/notification.html`:
- Article title, type badge, verification score bar
- Full article content with source citations
- Instructions: "Reply with: approve / reject / unpublish / or your feedback"
- Reply-to set to `review+{token}@gmail.com`

Create `templates/email/confirm.html`:
- "Confirm your subscription" with link to `/subscribe/confirm?token={token}`
- Unsubscribe link

- [ ] **Step 4: Run tests**

```bash
go test ./internal/mail/ -v
```
Expected: PASS (template rendering tests, SMTP send mocked or skipped)

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: email outbound with admin notifications and subscriber confirmation"
```

---

### Task 15: Email Inbound (IMAP Polling)

**Files:**
- Create: `internal/mail/inbound.go`
- Create: `internal/mail/inbound_test.go`

- [ ] **Step 1: Install go-imap**

```bash
go get github.com/emersion/go-imap/v2
go get github.com/emersion/go-imap/v2/imapclient
```

- [ ] **Step 2: Write failing test for reply parsing**

Create `internal/mail/inbound_test.go`:
```go
package mail

import (
	"testing"
)

func TestParseReply_Approve(t *testing.T) {
	action, feedback := ParseReply("Approve\n\nSent from my iPhone")
	if action != ActionApprove {
		t.Errorf("expected approve, got %s", action)
	}
	if feedback != "" {
		t.Errorf("expected no feedback, got %s", feedback)
	}
}

func TestParseReply_Reject(t *testing.T) {
	action, _ := ParseReply("reject")
	if action != ActionReject {
		t.Errorf("expected reject, got %s", action)
	}
}

func TestParseReply_Unpublish(t *testing.T) {
	action, _ := ParseReply("Unpublish")
	if action != ActionUnpublish {
		t.Errorf("expected unpublish, got %s", action)
	}
}

func TestParseReply_Feedback(t *testing.T) {
	action, feedback := ParseReply("The article should mention the expiration date more prominently")
	if action != ActionFeedback {
		t.Errorf("expected feedback, got %s", action)
	}
	if feedback != "The article should mention the expiration date more prominently" {
		t.Errorf("unexpected feedback: %s", feedback)
	}
}

func TestExtractToken(t *testing.T) {
	token := ExtractToken("review+abc123def@gmail.com")
	if token != "abc123def" {
		t.Errorf("expected abc123def, got %s", token)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

```bash
go test ./internal/mail/ -run TestParse -v
```
Expected: FAIL

- [ ] **Step 4: Implement reply parsing**

Add to `internal/mail/inbound.go`:
- Constants: `ActionApprove`, `ActionReject`, `ActionUnpublish`, `ActionFeedback`
- `ParseReply(body string) (action string, feedback string)` — trim whitespace, strip email signatures (lines after "-- " or "Sent from"), check first word case-insensitively
- `ExtractToken(replyToAddress string) string` — extract token from `review+{token}@...` using plus addressing

- [ ] **Step 5: Run parse tests**

```bash
go test ./internal/mail/ -run TestParse -v && go test ./internal/mail/ -run TestExtract -v
```
Expected: PASS

- [ ] **Step 6: Implement IMAP poller**

Add to `internal/mail/inbound.go`:
- `Poller` struct: imapConfig, articleManager, onError callback
- `NewPoller(cfg config.IMAPConfig, manager *article.Manager) *Poller`
- `Poll() error` — connect to Gmail IMAP, fetch unread messages, for each: extract token from To/Reply-To address, parse reply body, call appropriate article manager method, mark email as read
- `Start(ctx context.Context)` — runs Poll in a loop at configured interval

- [ ] **Step 7: Run all mail tests**

```bash
go test ./internal/mail/ -v
```
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add .
git commit -m "feat: IMAP inbound polling with reply parsing for approve/reject/unpublish/feedback"
```

---

### Task 16: Newsletter

**Files:**
- Create: `internal/newsletter/newsletter.go`
- Create: `internal/newsletter/newsletter_test.go`
- Create: `templates/email/newsletter.html`

- [ ] **Step 1: Write failing test for newsletter generation**

Test: create 3 approved articles, generate newsletter, verify HTML contains all 3 titles in newspaper-style layout.

- [ ] **Step 2: Create newsletter email template**

Create `templates/email/newsletter.html` — newspaper-style layout:
- Header: "MILES SCANNER · THE DAILY POINTS & MILES DIGEST · {date}"
- Lead story (featured or highest-voted article) with full excerpt
- Sidebar items (remaining articles) with title + one-line summary
- Footer with unsubscribe link

- [ ] **Step 3: Implement newsletter service**

Create `internal/newsletter/newsletter.go`:
- `Service` struct: db, mailSender, timezone, sendTime
- `GenerateDaily() (*db.Newsletter, error)` — collect approved articles from last 24h, skip if none, render template, store in newsletters table
- `SendDaily() error` — generate if not yet generated for today, fetch confirmed subscribers, send via mail.Sender, mark as sent
- `PreviewNext() (string, error)` — generate HTML without saving, for admin preview

- [ ] **Step 4: Run tests**

```bash
go test ./internal/newsletter/ -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: daily newsletter with newspaper-style template"
```

---

### Task 17: Scheduler & Main Wiring

**Files:**
- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`
- Modify: `cmd/milesahead/main.go`

- [ ] **Step 1: Install cron library**

```bash
go get github.com/robfig/cron/v3
```

- [ ] **Step 2: Write failing test for scheduler**

Test that scheduler registers jobs and can be started/stopped without panicking. Test that scan job calls scanner.ScanAll.

- [ ] **Step 3: Implement scheduler**

Create `internal/scheduler/scheduler.go`:
- `Scheduler` struct wrapping `*cron.Cron`
- `New() *Scheduler`
- `AddScanJob(interval time.Duration, scanner, clusterer, pipeline, articleManager, mailSender)` — scan all sources → cluster new articles → run AI pipeline → create articles → notify admin
- `AddMailPollJob(interval time.Duration, poller *mail.Poller)`
- `AddNewsletterJob(sendTime string, timezone string, newsletter *newsletter.Service)`
- `AddRevisionJob(interval time.Duration, articleManager)` — process pending revisions
- `Start()`, `Stop()`

- [ ] **Step 4: Wire everything in main.go**

Update `cmd/milesahead/main.go`:
```go
func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Database
	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	// AI
	hfClient := ai.NewHFClient(cfg.AI.HFAPIToken, cfg.AI.MaxRetries)
	cache := ai.NewCache(time.Duration(cfg.AI.CacheTTL) * time.Second)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)

	// Services
	scanner := scanner.NewScanner(database, cfg.Scanner.UserAgent)
	articleMgr := article.NewManager(database, pipeline)
	mailSender := mail.NewSender(cfg.Mail.SMTP)
	mailPoller := mail.NewPoller(cfg.Mail.IMAP, articleMgr)
	voteSvc := vote.NewService(database, cfg.Votes, func(articleID int64) {
		// Alert callback: send email to admin
		mailSender.SendVoteAlert(articleID)
	})
	newsletterSvc := newsletter.NewService(database, mailSender, cfg.Newsletter)

	// Scheduler
	sched := scheduler.New()
	clusterer := cluster.New()
	sched.AddScanJob(time.Duration(cfg.Scanner.DefaultInterval)*time.Minute, scanner, clusterer, pipeline, articleMgr, mailSender)
	sched.AddMailPollJob(time.Duration(cfg.Mail.IMAP.PollInterval)*time.Second, mailPoller)
	sched.AddNewsletterJob(cfg.Newsletter.SendTime, cfg.Newsletter.Timezone, newsletterSvc)
	sched.Start()
	defer sched.Stop()

	// Web server
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc, newsletterSvc, mailSender)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	log.Printf("Miles Ahead running on :%d", cfg.Server.Port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", cfg.Server.Port), app))
}
```

- [ ] **Step 5: Run full test suite**

```bash
make test
```
Expected: All PASS

- [ ] **Step 6: Test build**

```bash
make build
```
Expected: Binary compiles successfully.

- [ ] **Step 7: Commit**

```bash
git add .
git commit -m "feat: scheduler and main wiring — all components connected"
```

---

### Task 18: Admin Newsletter Page

**Files:**
- Create: `templates/admin/newsletter.html`
- Modify: `internal/web/admin.go`

- [ ] **Step 1: Write failing test for newsletter admin handlers**

Test: GET /admin/newsletter returns preview HTML, POST /admin/newsletter/send triggers send, GET /admin/newsletter/history lists sent newsletters.

- [ ] **Step 2: Implement newsletter admin handlers**

Add to `internal/web/admin.go`:
- `handleNewsletterPreview` — calls newsletterSvc.PreviewNext(), renders in iframe
- `handleNewsletterSend` — calls newsletterSvc.SendDaily(), redirects with success message
- `handleNewsletterHistory` — lists past newsletters with date, subject, article count, sent time

- [ ] **Step 3: Create newsletter admin template**

`templates/admin/newsletter.html`: Preview panel (iframe with rendered newsletter), Send Now button, history table below.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/web/ -run TestNewsletter -v
```
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: admin newsletter preview, manual send, and history"
```

---

### Task 19: CI/CD (GitHub Actions)

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/deploy.yml`

- [ ] **Step 1: Create CI workflow**

Create `.github/workflows/ci.yml`:
```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go vet ./...
      - run: go test ./... -v -race
      - run: go build ./cmd/milesahead
```

- [ ] **Step 2: Create deploy workflow**

Create `.github/workflows/deploy.yml`:
```yaml
name: Deploy
on:
  push:
    tags: ['v*']

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Build
        run: |
          GOOS=linux GOARCH=arm64 go build -o milesahead ./cmd/milesahead
      - name: Deploy to OCI
        env:
          SSH_KEY: ${{ secrets.OCI_SSH_KEY }}
          OCI_HOST: ${{ secrets.OCI_HOST }}
          OCI_USER: ${{ secrets.OCI_USER }}
        run: |
          mkdir -p ~/.ssh
          echo "$SSH_KEY" > ~/.ssh/id_ed25519
          chmod 600 ~/.ssh/id_ed25519
          ssh-keyscan $OCI_HOST >> ~/.ssh/known_hosts
          scp milesahead $OCI_USER@$OCI_HOST:/opt/milesahead/milesahead.new
          ssh $OCI_USER@$OCI_HOST "sudo mv /opt/milesahead/milesahead.new /opt/milesahead/milesahead && sudo systemctl restart milesahead"
```

- [ ] **Step 3: Commit**

```bash
git add .
git commit -m "feat: GitHub Actions CI/CD for build, test, and OCI deployment"
```

---

### Task 20: Deployment Configuration

**Files:**
- Create: `deploy/milesahead.service`
- Create: `deploy/nginx.conf`
- Create: `deploy/backup.sh`

- [ ] **Step 1: Create systemd service file**

Create `deploy/milesahead.service`:
```ini
[Unit]
Description=Miles Ahead - Points & Miles Aggregator
After=network.target

[Service]
Type=simple
User=milesahead
WorkingDirectory=/opt/milesahead
ExecStart=/opt/milesahead/milesahead -config /opt/milesahead/config.yaml
Restart=on-failure
RestartSec=5
Environment=HF_API_TOKEN=
Environment=SMTP_USER=
Environment=SMTP_PASSWORD=
Environment=IMAP_USER=
Environment=IMAP_APP_PASSWORD=

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Create nginx config**

Create `deploy/nginx.conf`:
```nginx
server {
    listen 80;
    server_name milesahead.kumaranik.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name milesahead.kumaranik.com;

    ssl_certificate /etc/letsencrypt/live/milesahead.kumaranik.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/milesahead.kumaranik.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /static/ {
        alias /opt/milesahead/static/;
        expires 7d;
        add_header Cache-Control "public, immutable";
    }
}
```

- [ ] **Step 3: Create backup script**

Create `deploy/backup.sh`:
```bash
#!/bin/bash
set -e
DB_PATH="/opt/milesahead/data/milesahead.db"
BACKUP_DIR="/tmp/milesahead-backup"
BUCKET="milesahead-backups"
DATE=$(date +%Y-%m-%d)

mkdir -p "$BACKUP_DIR"
sqlite3 "$DB_PATH" ".backup '$BACKUP_DIR/milesahead-$DATE.db'"
oci os object put --bucket-name "$BUCKET" --file "$BACKUP_DIR/milesahead-$DATE.db" --name "backups/milesahead-$DATE.db"
rm -f "$BACKUP_DIR/milesahead-$DATE.db"

# Keep only last 30 backups
oci os object list --bucket-name "$BUCKET" --prefix "backups/" --query "sort_by(data, &\"time-created\")[:-30].name" --output table | tail -n +4 | while read name; do
  oci os object delete --bucket-name "$BUCKET" --object-name "$name" --force
done
```

- [ ] **Step 4: Commit**

```bash
git add .
git commit -m "feat: deployment configs — systemd, nginx, and OCI backup"
```

---

### Task 21: Integration Test & Smoke Test

**Files:**
- Create: `tests/integration_test.go`

- [ ] **Step 1: Write integration test**

Create `tests/integration_test.go`:
- Full pipeline test: create source → mock RSS server → scan → cluster → mock HF API → generate article → verify status is pending → approve → verify public visibility → vote → check counts
- Uses httptest for both RSS and HF API servers
- Tests the full article lifecycle end-to-end

```go
package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"
	// ... imports
)

func TestFullPipeline(t *testing.T) {
	// 1. Setup: temp DB, mock RSS server, mock HF API
	// 2. Create source pointing to mock RSS
	// 3. Run scanner → verify raw_articles created
	// 4. Run cluster → verify clusters formed
	// 5. Run pipeline → verify generated_article with status pending
	// 6. Approve article → verify status approved
	// 7. GET homepage → verify article appears
	// 8. Vote → verify counts
	// 9. Unpublish → verify article shows "under review"
}

func TestRevisionCycle(t *testing.T) {
	// 1. Create pending article
	// 2. Request revision with feedback
	// 3. Process revision (mock HF)
	// 4. Verify new version is pending with incremented revision_number
	// 5. Approve → verify published
}

func TestEmailReplyFlow(t *testing.T) {
	// 1. Create pending article with token
	// 2. Parse "approve" reply → verify article approved
	// 3. Parse feedback reply on approved article → verify unpublished + revision_requested
}
```

- [ ] **Step 2: Run integration tests**

```bash
go test ./tests/ -v -count=1
```
Expected: PASS

- [ ] **Step 3: Run full test suite**

```bash
make test
```
Expected: All PASS

- [ ] **Step 4: Final build check**

```bash
make build
```
Expected: Binary compiles.

- [ ] **Step 5: Commit**

```bash
git add .
git commit -m "feat: integration tests for full pipeline, revision cycle, and email flow"
```

---

## Summary

| Task | Component | Est. Steps |
|---|---|---|
| 1 | Project scaffold & config | 11 |
| 2 | Database & models | 15 |
| 3 | RSS scanner | 6 |
| 4 | HTML scraper | 6 |
| 5 | Scanner orchestrator | 5 |
| 6 | Topic clustering | 5 |
| 7 | HF client & cache | 7 |
| 8 | AI pipeline (gen/critic) | 11 |
| 9 | Article manager | 7 |
| 10 | Web server & auth | 6 |
| 11 | Admin dashboard | 5 |
| 12 | Public site | 8 |
| 13 | Vote system | 6 |
| 14 | Email outbound | 5 |
| 15 | Email inbound (IMAP) | 8 |
| 16 | Newsletter | 5 |
| 17 | Scheduler & main wiring | 7 |
| 18 | Admin newsletter page | 5 |
| 19 | CI/CD | 3 |
| 20 | Deployment config | 4 |
| 21 | Integration tests | 5 |
| **Total** | | **145 steps** |
