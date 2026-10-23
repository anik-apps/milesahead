package main

import (
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/linker"
	"github.com/kumaranik/milesahead/internal/metrics"
	"github.com/kumaranik/milesahead/internal/storydedup"
)

// clusterWindow is how recent a scanned article must be to be clustered.
const clusterWindow = 48 * time.Hour

// articleProcessor is the article pipeline (*ai.Pipeline).
type articleProcessor interface {
	Process(articleType string, sources []ai.SourceArticle) (*ai.PipelineResult, error)
}

// articleCreator stores a pipeline result (*article.Manager).
type articleCreator interface {
	CreateFromPipeline(result *ai.PipelineResult, rawArticleIDs []int64, articleType string) (int64, error)
}

// clusterGenerator is the second half of the scan job: cluster recent scanned
// articles and write an article for each multi-source cluster that is not a
// story the site already has.
type clusterGenerator struct {
	db        *db.Database
	pipeline  articleProcessor
	articles  articleCreator
	threshold float64
	// stories is the story-level duplicate check; storyMode is
	// scanner.story_dedup (off, shadow or block).
	stories   *storydedup.Checker
	storyMode string
	now       func() time.Time
	// criticDown is set when a cluster's generation failed because no critic
	// answered; run stops the scan's loop on it.
	criticDown bool
}

// newClusterGenerator wires the story check to the short-call chain, like the
// other side jobs: it is one small call per cluster, bounded by
// ai.ShortCallTimeout, and must not depend on the critic.
func newClusterGenerator(cfg *config.Config, database *db.Database, pipeline articleProcessor,
	articles articleCreator, client ai.HFClientInterface, models []string, threshold float64) *clusterGenerator {
	return &clusterGenerator{
		db:        database,
		pipeline:  pipeline,
		articles:  articles,
		threshold: threshold,
		stories:   &storydedup.Checker{Client: client, Models: models},
		storyMode: cfg.Scanner.StoryDedup,
		now:       time.Now,
	}
}

// run clusters the scanned articles from the last clusterWindow and generates
// from each multi-source cluster in turn. It returns how many articles it
// created.
//
// Clusters are generated one at a time and every duplicate check reads the
// database when it runs, so an article created earlier in the same pass counts
// against the clusters after it. That matters: most same-story duplicates were
// one event split into two clusters in the same scan.
func (g *clusterGenerator) run() int {
	sources, _ := g.db.ListActiveSources()
	var clusterArticles []cluster.Article
	for _, s := range sources {
		raws, _ := g.db.GetRawArticlesBySourceID(s.ID)
		for _, r := range raws {
			if g.now().Sub(r.CreatedAt) < clusterWindow {
				clusterArticles = append(clusterArticles, cluster.Article{
					ID: r.ID, Title: r.Title, SourceID: r.SourceID, Content: r.Content,
				})
			}
		}
	}
	if len(clusterArticles) == 0 {
		return 0
	}

	clusters := cluster.Cluster(clusterArticles, g.threshold)
	slog.Info("clustering complete", "articles", len(clusterArticles), "clusters", len(clusters))

	created := 0
	g.criticDown = false
	for i, c := range clusters {
		if g.generate(c) {
			created++
		}
		if g.criticDown {
			// Every remaining cluster would be generated and then fail the
			// same way: about 130 wasted generator calls a day during the
			// 2026-10-20 outage. The next scan tries again.
			slog.Error("generation stopped for this scan: every critic failed",
				"clusters_left", len(clusters)-i-1)
			break
		}
	}
	return created
}

// generate writes an article from one cluster unless it is single-source or
// a duplicate, reporting whether it created one.
func (g *clusterGenerator) generate(c cluster.ArticleCluster) bool {
	sourceIDs := make(map[int64]bool)
	for _, a := range c.Articles {
		sourceIDs[a.SourceID] = true
	}
	if len(sourceIDs) < 2 {
		return false
	}
	topic := c.Articles[0].Title

	var sa []ai.SourceArticle
	var story []storydedup.Source
	var rawIDs []int64
	for _, a := range c.Articles {
		raws, _ := g.db.GetRawArticlesByIDs([]int64{a.ID})
		if len(raws) == 0 {
			continue
		}
		r := raws[0]
		src, _ := g.db.GetSource(r.SourceID)
		name := "Unknown"
		if src != nil {
			name = src.Name
		}
		content := r.Content
		if len(content) > 1500 {
			content = content[:1500]
		}
		sa = append(sa, ai.SourceArticle{Name: name, URL: r.URL, Title: r.Title, Content: content})
		story = append(story, storydedup.Source{Title: r.Title, Content: r.Content})
		rawIDs = append(rawIDs, a.ID)
	}

	// Skip a cluster that reuses a raw article some article was written from
	// or already covers, and record the cluster's other raws as covered by
	// that article. Without the record, this cluster's late coverage was
	// forgotten: once the article's own sources aged out of the 48h window,
	// the late raws formed a cluster of their own that shared nothing with it,
	// and the story was written a second time.
	matched, found, err := g.db.ArticleForRawIDs(rawIDs)
	if err != nil {
		slog.Warn("skipping cluster, duplicate check failed", "topic", topic, "error", err)
		return false
	}
	if found {
		n, err := g.db.CoverRawArticles(matched, rawIDs, db.CoverReasonRawOverlap)
		if err != nil {
			slog.Warn("could not record covered raw articles", "article_id", matched, "error", err)
		}
		slog.Info("skipping cluster, articles already exist for these sources",
			"topic", topic, "article_id", matched, "cluster_raw_ids", rawIDs, "newly_covered", n)
		return false
	}

	decision := ""
	if g.storyMode != config.StoryDedupOff && g.stories != nil {
		var block bool
		decision, block = g.checkStory(topic, story, rawIDs)
		if block {
			return false
		}
	}

	result, err := g.pipeline.Process("auto", sa)
	if err != nil {
		if errors.Is(err, ai.ErrCriticUnavailable) {
			g.criticDown = true
		}
		slog.Warn("article generation skipped", "topic", topic, "error", err)
		return false
	}

	// Auto-link card mentions: insert inline <a> tags for first mention
	// of each card, cap RelatedCards to top 2 by mention count. Articles
	// with 3+ mentioned cards rely on admin curation via
	// /admin/articles/{id}/cards.
	refPages, _ := g.db.ListPublishedReferencePages("")
	if len(refPages) > 0 {
		linkedContent, _ := linker.AutoLink(result.Content, refPages)
		result.Content = linkedContent

		detected := linker.DetectMentions(result.Content, refPages)
		const autoLinkCap = 2
		top := linker.TopByMentionCount(detected, autoLinkCap)
		for _, dc := range top {
			result.RelatedCards = append(result.RelatedCards, ai.RelatedCard{
				Slug:  dc.Slug,
				Title: dc.Title,
			})
		}
		if len(detected) > 0 {
			slog.Info("auto-linked card mentions",
				"title", result.Title,
				"detected", len(detected),
				"linked", len(top),
			)
		}
	}

	id, err := g.articles.CreateFromPipeline(result, rawIDs, result.Type)
	if err != nil {
		slog.Error("article save failed", "error", err)
		return false
	}
	status := ""
	if art, _ := g.db.GetGeneratedArticle(id); art != nil {
		status = art.Status
	}
	score := 0
	if result.Verification != nil {
		score = result.Verification.Score
	}
	slog.Info("article created", "id", id, "title", result.Title, "score", score,
		"status", status, "cluster_raw_ids", rawIDs, "story_dedup", decision)
	return true
}

// checkStory runs the story-level duplicate check on a cluster that passed
// the raw-ID check. It returns the decision and whether to skip generation,
// which is only ever true in block mode.
//
// Every decision is logged as one self-contained "story dedup decision" line
// (cluster raw IDs, candidates and scores, the AI verdict or fallback, the
// matched article), so the shadow period's would_block calls can be tallied
// offline against what the admin later approved or rejected.
func (g *clusterGenerator) checkStory(topic string, sources []storydedup.Source, rawIDs []int64) (string, bool) {
	rows, err := g.db.ListStoryCandidates(g.now().Add(-storydedup.Window))
	if err != nil {
		slog.Warn("story dedup: could not list candidates; generating", "topic", topic, "error", err)
		return "", false
	}
	cands := make([]storydedup.Candidate, len(rows))
	for i, r := range rows {
		cands[i] = storydedup.Candidate{
			ID: r.ID, Title: r.Title, TLDR: r.TLDR, Status: r.Status,
			CreatedAt: r.CreatedAt, RawTitles: r.RawTitles,
		}
	}

	d := g.stories.Check(sources, cands)
	block := d.Duplicate && g.storyMode == config.StoryDedupBlock
	metrics.StoryDedupDecisionsTotal.WithLabelValues(d.Decision, g.storyMode).Inc()

	titles := make([]string, len(sources))
	for i, s := range sources {
		titles[i] = s.Title
	}
	scored := make([]storydedup.Scored, len(d.Candidates))
	for i, s := range d.Candidates {
		scored[i] = storydedup.Scored{ID: s.ID, Score: math.Round(s.Score*1000) / 1000}
	}
	attrs := []any{
		"mode", g.storyMode,
		"decision", d.Decision,
		"stage", d.Stage,
		"would_block", d.Duplicate,
		"blocked", block,
		"topic", topic,
		"cluster_raw_ids", rawIDs,
		"cluster_titles", titles,
		"candidates_considered", len(cands),
		"candidates", scored,
		"best_score", math.Round(d.BestScore*1000) / 1000,
		"matched_article_id", d.MatchedID,
	}
	if v := d.Verdict; v != nil {
		var same int64
		if v.SameStoryAs != nil {
			same = *v.SameStoryAs
		}
		attrs = append(attrs, "ai_same_story_as", same,
			"ai_materially_new", v.MateriallyNew, "ai_reason", v.Reason)
	}
	if d.InvalidMatchID != 0 {
		attrs = append(attrs, "ai_invalid_match_id", d.InvalidMatchID)
	}
	if d.Err != nil {
		attrs = append(attrs, "ai_error", d.Err.Error())
	}
	slog.Info("story dedup decision", attrs...)

	if block {
		// Record the cluster as covered by the matched article, so this
		// story's later coverage is stopped by the raw-ID check too.
		if _, err := g.db.CoverRawArticles(d.MatchedID, rawIDs, db.CoverReasonStoryMatch); err != nil {
			slog.Warn("could not record covered raw articles", "article_id", d.MatchedID, "error", err)
		}
	}
	return d.Decision, block
}
