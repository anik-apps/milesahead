package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/scanner"
	"github.com/kumaranik/milesahead/internal/vote"
	"github.com/kumaranik/milesahead/internal/web"
)

// mockRSSFeed returns valid RSS XML with two items.
func mockRSSFeed() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Test Feed</title>
    <link>http://example.com</link>
    <description>Test RSS Feed</description>
    <item>
      <title>Amex Transfer Bonus to Delta SkyMiles</title>
      <link>http://example.com/amex-delta-bonus</link>
      <description>American Express is offering a 30% transfer bonus to Delta SkyMiles through March 2026.</description>
      <pubDate>Mon, 23 Mar 2026 12:00:00 +0000</pubDate>
    </item>
    <item>
      <title>Chase Ultimate Rewards Transfer Bonus to Hyatt</title>
      <link>http://example.com/chase-hyatt-bonus</link>
      <description>Chase is offering a 25% transfer bonus to World of Hyatt points through April 2026.</description>
      <pubDate>Mon, 23 Mar 2026 13:00:00 +0000</pubDate>
    </item>
  </channel>
</rss>`
}

// mockHFResponse returns a valid HF chat completions API response wrapping the given text.
func mockHFResponse(text string) string {
	resp := map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": text}},
		},
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// generatedArticleContent returns mock generated article text.
func generatedArticleContent() string {
	return "Transfer Bonus Roundup: Amex and Chase Offers\nAmerican Express and Chase are both running transfer bonuses. Amex offers 30% to Delta [TestFeed]. Chase offers 25% to Hyatt [TestFeed]."
}

// criticResponse returns a mock critic JSON response with a high score.
func criticResponse(score int) string {
	return mockHFResponse(fmt.Sprintf(`{"score": %d, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`, score))
}

// revisedArticleContent returns mock revised article text.
func revisedArticleContent() string {
	return "Revised: Transfer Bonus Roundup\nUpdated content with corrections per feedback."
}

// setupTestDB creates a fresh SQLite database in a temp directory.
func setupTestDB(t *testing.T) *db.Database {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// setupMockHFServer creates an httptest server that serves HF API responses.
// It tracks request count and alternates between generator and critic responses.
func setupMockHFServer(t *testing.T, generatorResp, criticResp string) *httptest.Server {
	t.Helper()
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		// Odd calls -> generator, even calls -> critic.
		if callCount%2 == 1 {
			fmt.Fprint(w, generatorResp)
		} else {
			fmt.Fprint(w, criticResp)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// testConfig returns a minimal config for testing.
func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Port:   0,
			Domain: "localhost",
		},
		Admin: config.AdminConfig{
			PasswordHash: "$2a$10$abcdefghijklmnopqrstuvwxyz012345678901234567890123456",
		},
		AI: config.AIConfig{
			GeneratorModel:  "test-gen",
			CriticModel:     "test-critic",
			CriticThreshold: 70,
			MaxRetries:      0,
			CacheTTL:        60,
		},
		Votes: config.VotesConfig{
			AlertThreshold: 10,
			AlertRatio:     0.7,
			AlertWindow:    24,
		},
	}
}

func TestFullPipeline(t *testing.T) {
	// 1. Setup: temp DB, mock RSS server, mock HF API.
	database := setupTestDB(t)
	cfg := testConfig()

	// Create mock RSS server.
	rssServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, mockRSSFeed())
	}))
	defer rssServer.Close()

	// Create mock HF API server.
	hfServer := setupMockHFServer(t,
		mockHFResponse(generatedArticleContent()),
		criticResponse(85),
	)

	// 2. Create two sources pointing to mock RSS (multi-source required).
	rssURL := rssServer.URL + "/feed.xml"
	sourceID1, err := database.CreateSource(&db.Source{
		Name:   "TestFeed",
		URL:    rssServer.URL,
		RSSURL: &rssURL,
		Active: true,
	})
	if err != nil {
		t.Fatalf("creating source 1: %v", err)
	}
	sourceID2, err := database.CreateSource(&db.Source{
		Name:   "TestFeed2",
		URL:    rssServer.URL,
		RSSURL: &rssURL,
		Active: true,
	})
	if err != nil {
		t.Fatalf("creating source 2: %v", err)
	}

	// 3. Run scanner on both sources.
	sc := scanner.NewScanner(database, "test-agent")
	result, err := sc.ScanSource(sourceID1)
	if err != nil {
		t.Fatalf("scanning source 1: %v", err)
	}
	if result.NewArticles != 2 {
		t.Fatalf("expected 2 new articles from source 1, got %d", result.NewArticles)
	}
	// Source 2 gets same RSS but different source_id (different hash since title+url same → dedup).
	// Manually insert a matching article for source 2 to ensure multi-source cluster.
	rawArticles1, _ := database.GetRawArticlesBySourceID(sourceID1)
	database.CreateRawArticle(&db.RawArticle{
		SourceID:    sourceID2,
		Title:       rawArticles1[0].Title,
		URL:         rawArticles1[0].URL + "?src=2",
		Content:     rawArticles1[0].Content,
		PublishedAt: rawArticles1[0].PublishedAt,
		Hash:        "alt-hash-source2",
	})

	var rawArticles []db.RawArticle
	r1, _ := database.GetRawArticlesBySourceID(sourceID1)
	r2, _ := database.GetRawArticlesBySourceID(sourceID2)
	rawArticles = append(rawArticles, r1...)
	rawArticles = append(rawArticles, r2...)

	// 4. Run cluster on new articles -> verify clusters formed.
	clusterArticles := make([]cluster.Article, len(rawArticles))
	for i, ra := range rawArticles {
		clusterArticles[i] = cluster.Article{
			ID:       ra.ID,
			Title:    ra.Title,
			SourceID: ra.SourceID,
			Content:  ra.Content,
		}
	}

	// Use a low threshold so both articles (about transfer bonuses) cluster together.
	clusters := cluster.Cluster(clusterArticles, 0.1)
	if len(clusters) == 0 {
		t.Fatal("expected at least one cluster, got none")
	}
	t.Logf("formed %d cluster(s)", len(clusters))

	// Pick the cluster with the most articles.
	bestCluster := clusters[0]
	for _, c := range clusters[1:] {
		if len(c.Articles) > len(bestCluster.Articles) {
			bestCluster = c
		}
	}

	// 5. Run pipeline on cluster -> verify generated_article created.
	hfClient := ai.NewHFClient("test-token", 0)
	hfClient.BaseURL = hfServer.URL

	cache := ai.NewCache(time.Hour)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)

	// Build source articles for the pipeline.
	var sourceArticles []ai.SourceArticle
	var rawIDs []int64
	for _, ca := range bestCluster.Articles {
		for _, ra := range rawArticles {
			if ra.ID == ca.ID {
				src, _ := database.GetSource(ra.SourceID)
				name := "Unknown"
				if src != nil {
					name = src.Name
				}
				sourceArticles = append(sourceArticles, ai.SourceArticle{
					Name:    name,
					URL:     ra.URL,
					Title:   ra.Title,
					Content: ra.Content,
				})
				rawIDs = append(rawIDs, ra.ID)
			}
		}
	}

	pipelineResult, err := pipeline.Process("roundup", sourceArticles)
	if err != nil {
		t.Fatalf("pipeline process: %v", err)
	}

	if pipelineResult.Content == "" {
		t.Fatal("pipeline returned empty content")
	}

	mgr := article.NewManager(database, pipeline)
	articleID, err := mgr.CreateFromPipeline(pipelineResult, rawIDs, "roundup")
	if err != nil {
		t.Fatalf("creating article from pipeline: %v", err)
	}

	// 6. Check article status is pending (score 85 >= threshold 70).
	gen, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting generated article: %v", err)
	}
	if !pipelineResult.PassedThreshold {
		t.Fatal("expected pipeline result to pass threshold")
	}
	if gen.Status != db.StatusPending {
		t.Fatalf("expected status %q, got %q", db.StatusPending, gen.Status)
	}

	// 7. Approve article -> verify status approved, published_at set.
	if err := mgr.Approve(articleID); err != nil {
		t.Fatalf("approving article: %v", err)
	}
	gen, err = database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting approved article: %v", err)
	}
	if gen.Status != db.StatusApproved {
		t.Fatalf("expected status %q, got %q", db.StatusApproved, gen.Status)
	}
	if gen.PublishedAt == nil {
		t.Fatal("expected published_at to be set after approval")
	}

	// 8. GET homepage via web app -> verify article appears.
	templateDir := filepath.Join("..", "templates")
	voteSvc := vote.NewService(database, cfg.Votes, nil)
	app, err := web.NewAppWithTemplateDir(cfg, database, mgr, voteSvc, nil, templateDir)
	if err != nil {
		t.Fatalf("creating web app: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("homepage returned status %d, expected 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, gen.Title) {
		t.Fatalf("homepage does not contain article title %q", gen.Title)
	}

	// 9. Cast vote -> verify counts.
	voteBody := fmt.Sprintf(`{"article_id":%d,"vote":"up"}`, articleID)
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/vote", bytes.NewBufferString(voteBody))
	req.Header.Set("Content-Type", "application/json")
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("vote API returned status %d, body: %s", rr.Code, rr.Body.String())
	}

	var voteResp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &voteResp); err != nil {
		t.Fatalf("parsing vote response: %v", err)
	}
	if voteResp["status"] != "ok" {
		t.Fatalf("vote response status: %v", voteResp["status"])
	}
	if int(voteResp["upvotes"].(float64)) != 1 {
		t.Fatalf("expected 1 upvote, got %v", voteResp["upvotes"])
	}

	// 10. Unpublish -> verify status unpublished.
	if err := mgr.Unpublish(articleID); err != nil {
		t.Fatalf("unpublishing article: %v", err)
	}
	gen, err = database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting unpublished article: %v", err)
	}
	if gen.Status != db.StatusUnpublished {
		t.Fatalf("expected status %q, got %q", db.StatusUnpublished, gen.Status)
	}
}

func TestRevisionCycle(t *testing.T) {
	database := setupTestDB(t)
	cfg := testConfig()

	// Create mock HF server that returns revised content.
	callCount := 0
	hfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount%2 == 1 {
			// Generator call - return revised content.
			fmt.Fprint(w, mockHFResponse(revisedArticleContent()))
		} else {
			// Critic call - return high score.
			fmt.Fprint(w, criticResponse(90))
		}
	}))
	defer hfServer.Close()

	hfClient := ai.NewHFClient("test-token", 0)
	hfClient.BaseURL = hfServer.URL
	cache := ai.NewCache(time.Hour)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)
	mgr := article.NewManager(database, pipeline)

	// 1. Create a source and raw article so we have linked sources.
	sourceID, err := database.CreateSource(&db.Source{
		Name:   "RevisionTestSource",
		URL:    "http://example.com",
		Active: true,
	})
	if err != nil {
		t.Fatalf("creating source: %v", err)
	}

	rawID, err := database.CreateRawArticle(&db.RawArticle{
		SourceID:    sourceID,
		Title:       "Test Raw Article for Revision",
		URL:         "http://example.com/test-revision",
		Content:     "Original raw article content about transfer bonuses.",
		PublishedAt: time.Now(),
		Hash:        "revision-test-hash-001",
	})
	if err != nil {
		t.Fatalf("creating raw article: %v", err)
	}

	// Create article in pending status (mock pipeline result).
	genArticle := &db.GeneratedArticle{
		Title:          "Original Article Title",
		Slug:           "original-article-title-abc123",
		Content:        "Original article content about transfer bonuses.",
		Type:           "roundup",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	}
	articleID, err := database.CreateGeneratedArticle(genArticle)
	if err != nil {
		t.Fatalf("creating generated article: %v", err)
	}

	// Link source.
	if err := database.LinkArticleSources(articleID, []int64{rawID}); err != nil {
		t.Fatalf("linking sources: %v", err)
	}

	// Store initial verification result.
	_, err = database.CreateVerificationResult(&db.VerificationResult{
		GeneratedArticleID: articleID,
		RevisionNumber:     1,
		Score:              75,
		IssuesJSON:         `["Missing citation for bonus amount"]`,
		FactualCheck:       "pass",
		AttributionCheck:   "fail",
		CompletenessCheck:  "pass",
		HallucinationCheck: "pass",
	})
	if err != nil {
		t.Fatalf("creating verification result: %v", err)
	}

	// 2. Request revision with feedback text.
	err = mgr.RequestRevision(articleID, "Please add proper citations for the bonus amounts.", "admin")
	if err != nil {
		t.Fatalf("requesting revision: %v", err)
	}

	revised, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting revised article: %v", err)
	}
	if revised.Status != db.StatusRevisionRequested {
		t.Fatalf("expected status %q, got %q", db.StatusRevisionRequested, revised.Status)
	}

	// 3. Process revision (mock HF returns new content).
	err = mgr.ProcessRevision(articleID)
	if err != nil {
		t.Fatalf("processing revision: %v", err)
	}

	// 4. Verify new version is pending with revision_number=2.
	updated, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting updated article: %v", err)
	}
	if updated.RevisionNumber != 2 {
		t.Fatalf("expected revision_number 2, got %d", updated.RevisionNumber)
	}
	// Since score 90 >= threshold 70, it should be pending.
	if updated.Status != db.StatusPending {
		t.Fatalf("expected status %q after revision, got %q", db.StatusPending, updated.Status)
	}

	// 5. Verify revision history exists.
	revisions, err := database.ListRevisionsForArticle(articleID)
	if err != nil {
		t.Fatalf("listing revisions: %v", err)
	}
	if len(revisions) != 1 {
		t.Fatalf("expected 1 revision record, got %d", len(revisions))
	}
	if revisions[0].RevisionNumber != 2 {
		t.Fatalf("expected revision record number 2, got %d", revisions[0].RevisionNumber)
	}

	// 6. Approve -> verify published.
	err = mgr.Approve(articleID)
	if err != nil {
		t.Fatalf("approving revised article: %v", err)
	}
	final, err := database.GetGeneratedArticle(articleID)
	if err != nil {
		t.Fatalf("getting final article: %v", err)
	}
	if final.Status != db.StatusApproved {
		t.Fatalf("expected status %q, got %q", db.StatusApproved, final.Status)
	}
	if final.PublishedAt == nil {
		t.Fatal("expected published_at to be set after approval")
	}
}

func TestEmailReplyFlow(t *testing.T) {
	// 1. Test ParseReply("approve") -> ActionApprove.
	action, feedback := mail.ParseReply("approve")
	if action != mail.ActionApprove {
		t.Fatalf("expected action %q, got %q", mail.ActionApprove, action)
	}
	if feedback != "" {
		t.Fatalf("expected empty feedback, got %q", feedback)
	}

	// 2. Test ParseReply with feedback -> ActionFeedback.
	action, feedback = mail.ParseReply("Please add more details about the Chase bonus.\n-- \nSent from my iPhone")
	if action != mail.ActionFeedback {
		t.Fatalf("expected action %q, got %q", mail.ActionFeedback, action)
	}
	if !strings.Contains(feedback, "Chase bonus") {
		t.Fatalf("expected feedback to contain 'Chase bonus', got %q", feedback)
	}
	// Signature should be stripped.
	if strings.Contains(feedback, "Sent from") {
		t.Fatalf("expected signature to be stripped, but found 'Sent from' in %q", feedback)
	}

	// 3. Test ParseReply("reject") -> ActionReject.
	action, _ = mail.ParseReply("reject")
	if action != mail.ActionReject {
		t.Fatalf("expected action %q, got %q", mail.ActionReject, action)
	}

	// 4. Test ParseReply("unpublish") -> ActionUnpublish.
	action, _ = mail.ParseReply("unpublish")
	if action != mail.ActionUnpublish {
		t.Fatalf("expected action %q, got %q", mail.ActionUnpublish, action)
	}

	// 5. Test ExtractToken on plus-addressed email.
	token := mail.ExtractToken("review+abc123def@gmail.com")
	if token != "abc123def" {
		t.Fatalf("expected token %q, got %q", "abc123def", token)
	}

	// No token case.
	token = mail.ExtractToken("noreply@gmail.com")
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
}

func TestScannerDeduplication(t *testing.T) {
	// Verify scanning the same feed twice does not create duplicate raw articles.
	database := setupTestDB(t)

	rssServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, mockRSSFeed())
	}))
	defer rssServer.Close()

	rssURL := rssServer.URL + "/feed.xml"
	sourceID, err := database.CreateSource(&db.Source{
		Name:   "DedupTest",
		URL:    rssServer.URL,
		RSSURL: &rssURL,
		Active: true,
	})
	if err != nil {
		t.Fatalf("creating source: %v", err)
	}

	sc := scanner.NewScanner(database, "test-agent")

	// First scan.
	result1, err := sc.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if result1.NewArticles != 2 {
		t.Fatalf("first scan: expected 2 new articles, got %d", result1.NewArticles)
	}

	// Second scan - should find 0 new articles.
	result2, err := sc.ScanSource(sourceID)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if result2.NewArticles != 0 {
		t.Fatalf("second scan: expected 0 new articles, got %d", result2.NewArticles)
	}
}

func TestVoteDeduplication(t *testing.T) {
	database := setupTestDB(t)
	cfg := testConfig()
	voteSvc := vote.NewService(database, cfg.Votes, nil)

	// Create an approved article to vote on.
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Vote Test Article",
		Slug:           "vote-test-article-xyz789",
		Content:        "Content for vote test.",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("creating article: %v", err)
	}
	if err := database.UpdateArticleStatus(articleID, db.StatusApproved); err != nil {
		t.Fatalf("approving article: %v", err)
	}

	// First vote should succeed.
	err = voteSvc.CastVote(articleID, "up", "iphash-001")
	if err != nil {
		t.Fatalf("first vote: %v", err)
	}

	// Second vote from same IP should fail with ErrAlreadyVoted.
	err = voteSvc.CastVote(articleID, "down", "iphash-001")
	if err != vote.ErrAlreadyVoted {
		t.Fatalf("expected ErrAlreadyVoted, got: %v", err)
	}

	// Vote from different IP should succeed.
	err = voteSvc.CastVote(articleID, "down", "iphash-002")
	if err != nil {
		t.Fatalf("vote from different IP: %v", err)
	}

	// Verify counts.
	counts, err := voteSvc.GetCounts(articleID)
	if err != nil {
		t.Fatalf("getting counts: %v", err)
	}
	if counts.Up != 1 || counts.Down != 1 {
		t.Fatalf("expected 1 up / 1 down, got %d up / %d down", counts.Up, counts.Down)
	}
}

func TestArticleStatusTransitions(t *testing.T) {
	database := setupTestDB(t)

	// Create a draft article and verify invalid transitions are rejected.
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Status Test Article",
		Slug:           "status-test-article-aaa111",
		Content:        "Test content.",
		Type:           "summary",
		Status:         db.StatusDraft,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("creating article: %v", err)
	}

	// Draft -> pending is valid.
	err = database.UpdateArticleStatus(articleID, db.StatusPending)
	if err != nil {
		t.Fatalf("draft -> pending: %v", err)
	}

	// Pending -> approved is valid.
	err = database.UpdateArticleStatus(articleID, db.StatusApproved)
	if err != nil {
		t.Fatalf("pending -> approved: %v", err)
	}

	// Approved -> unpublished is valid.
	err = database.UpdateArticleStatus(articleID, db.StatusUnpublished)
	if err != nil {
		t.Fatalf("approved -> unpublished: %v", err)
	}

	// Unpublished -> approved is valid (re-publish).
	err = database.UpdateArticleStatus(articleID, db.StatusApproved)
	if err != nil {
		t.Fatalf("expected unpublished -> approved to succeed, got: %v", err)
	}
}

func TestClusterSimilarArticles(t *testing.T) {
	// Articles with similar titles should cluster together.
	articles := []cluster.Article{
		{ID: 1, Title: "Amex Transfer Bonus to Delta SkyMiles 30%", SourceID: 1},
		{ID: 2, Title: "American Express 30% Delta SkyMiles Transfer Bonus", SourceID: 2},
		{ID: 3, Title: "Best Credit Cards for Groceries in 2026", SourceID: 3},
	}

	clusters := cluster.Cluster(articles, 0.3)

	// We should have 2 clusters: one with articles 1+2, one with article 3.
	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}

	// Find the cluster with 2 articles.
	var multiCluster *cluster.ArticleCluster
	for i := range clusters {
		if len(clusters[i].Articles) == 2 {
			multiCluster = &clusters[i]
			break
		}
	}
	if multiCluster == nil {
		t.Fatal("expected a cluster with 2 articles")
	}
}

func TestWebFeedXML(t *testing.T) {
	database := setupTestDB(t)
	cfg := testConfig()

	// Create and approve an article.
	articleID, err := database.CreateGeneratedArticle(&db.GeneratedArticle{
		Title:          "Feed Test Article",
		Slug:           "feed-test-article-bbb222",
		Content:        "Content that appears in the RSS feed.",
		Type:           "deal",
		Status:         db.StatusPending,
		SourcesJSON:    "[]",
		RevisionNumber: 1,
	})
	if err != nil {
		t.Fatalf("creating article: %v", err)
	}
	if err := database.UpdateArticleStatus(articleID, db.StatusApproved); err != nil {
		t.Fatalf("approving article: %v", err)
	}

	pipeline := ai.NewPipeline(nil, nil, 70)
	mgr := article.NewManager(database, pipeline)
	voteSvc := vote.NewService(database, cfg.Votes, nil)
	templateDir := filepath.Join("..", "templates")
	app, err := web.NewAppWithTemplateDir(cfg, database, mgr, voteSvc, nil, templateDir)
	if err != nil {
		t.Fatalf("creating web app: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/feed.xml", nil)
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("feed returned status %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Feed Test Article") {
		t.Fatal("feed does not contain the article title")
	}
	if !strings.Contains(body, "<rss") {
		t.Fatal("feed response is not valid RSS XML")
	}
}
