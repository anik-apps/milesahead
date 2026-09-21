// Package enrich fills in article fields that generation left empty.
//
// Scout's Take, key details, the image and the tags used to be produced inline
// in the article-creation loop: five consecutive best-effort calls to one model,
// each logging a warning and continuing on failure. That made any transient
// provider problem a permanent hole in the article. From 2026-07-01 the
// provider's latency crossed the client timeout on roughly a quarter of those
// calls and the site published degraded articles for two and a half months
// before anyone noticed.
//
// Reconciling instead of enriching inline changes the failure mode from
// permanent to transient: a field missed this pass is simply picked up on the
// next one. It also takes four model calls off the critical path, and lets the
// work run against a different model from the critic so the two do not share a
// fate.
package enrich

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// MinImageScore is the relevance score below which a candidate image is
// rejected. Matches the threshold the inline path used.
const MinImageScore = 6.0

// Service reconciles missing enrichment on existing articles.
type Service struct {
	db       *db.Database
	client   ai.HFClientInterface
	models   []string
	persona  string
	searcher *images.Searcher
	scorer   *images.Scorer

	// MaxAttempts bounds how many passes an article gets before it is left
	// alone. Without it, an article the model simply cannot enrich would be
	// retried on every pass forever, burning tokens indefinitely.
	MaxAttempts int
	// BatchSize bounds the work per pass so one run cannot monopolise the API.
	BatchSize int
	// Pace is the delay between articles. The inline path had none, which is
	// part of why several calls in a row failed together.
	Pace time.Duration
}

// New builds a reconciler. models is tried in order, so pass the preferred
// model first followed by fallbacks.
func New(database *db.Database, client ai.HFClientInterface, models []string,
	persona string, searcher *images.Searcher, scorer *images.Scorer) *Service {
	return &Service{
		db:          database,
		client:      client,
		models:      models,
		persona:     persona,
		searcher:    searcher,
		scorer:      scorer,
		MaxAttempts: 3,
		BatchSize:   10,
		Pace:        2 * time.Second,
	}
}

// Run reconciles one batch. It is safe to call on a schedule; each pass picks
// up whatever is still missing.
func (s *Service) Run() {
	articles, err := s.db.ListArticlesNeedingEnrichment(s.MaxAttempts, s.BatchSize)
	if err != nil {
		slog.Error("enrichment: could not list articles", "error", err)
		return
	}
	if len(articles) == 0 {
		if pending, exhausted, err := s.db.CountDegradedArticles(s.MaxAttempts); err == nil {
			metrics.ArticlesDegraded.WithLabelValues("pending").Set(float64(pending))
			metrics.ArticlesDegraded.WithLabelValues("exhausted").Set(float64(exhausted))
		}
		return
	}

	slog.Info("enrichment pass starting", "articles", len(articles))
	var filled, failed int
	for i, a := range articles {
		if i > 0 && s.Pace > 0 {
			time.Sleep(s.Pace)
		}
		got, missed := s.enrichOne(a)
		filled += got
		failed += missed
	}
	slog.Info("enrichment pass complete", "articles", len(articles),
		"fields_filled", filled, "fields_failed", failed)

	if pending, exhausted, err := s.db.CountDegradedArticles(s.MaxAttempts); err == nil {
		metrics.ArticlesDegraded.WithLabelValues("pending").Set(float64(pending))
		metrics.ArticlesDegraded.WithLabelValues("exhausted").Set(float64(exhausted))
	}
}

// enrichOne fills whatever it can for a single article and records the attempt.
// A partial success is kept: each field is written independently, so one
// failing step does not discard the others.
func (s *Service) enrichOne(a db.ArticleNeedingEnrichment) (filled, failed int) {
	var u db.EnrichmentUpdate

	if a.MissingScoutsTake {
		take, err := ai.GenerateScoutsTake(s.client, s.models, s.persona, a.Title, a.TLDR, a.Content)
		if err != nil {
			s.recordFailure("scouts_take", a.ID, err)
			failed++
		} else {
			u.ScoutsTake = &take
			filled++
		}
	}

	if a.MissingKeyDetails {
		details, _, err := ai.ExtractDetails(s.client, s.models, a.Title, a.Content)
		if err != nil {
			s.recordFailure("key_details", a.ID, err)
			failed++
		} else if encoded, err := json.Marshal(details); err != nil {
			s.recordFailure("key_details", a.ID, err)
			failed++
		} else {
			str := string(encoded)
			u.KeyDetails = &str
			filled++
		}
	}

	if a.MissingTags {
		tags, err := ai.ExtractTags(s.client, s.models, a.Title, a.Content)
		if err != nil {
			s.recordFailure("tags", a.ID, err)
			failed++
		} else if encoded, err := json.Marshal(tags); err != nil {
			s.recordFailure("tags", a.ID, err)
			failed++
		} else {
			str := string(encoded)
			u.TagsJSON = &str
			filled++
		}
	}

	if a.MissingImage {
		if s.findImage(&u, a) {
			filled++
		} else {
			failed++
		}
	}

	if err := s.db.ApplyEnrichment(a.ID, u); err != nil {
		slog.Error("enrichment: could not save", "article_id", a.ID, "error", err)
		return filled, failed
	}
	slog.Info("enrichment applied", "article_id", a.ID, "was_missing", a.Missing(),
		"filled", filled, "failed", failed, "attempt", a.Attempts+1)
	return filled, failed
}

// findImage searches and scores candidates, reporting whether one was good
// enough to use. A search that finds nothing relevant is a legitimate outcome
// rather than an error, but it still counts against the attempt budget so the
// article is not searched forever.
func (s *Service) findImage(u *db.EnrichmentUpdate, a db.ArticleNeedingEnrichment) bool {
	if s.searcher == nil {
		return false
	}
	queries, err := ai.GenerateImageQueries(s.client, s.models, a.Title, a.TLDR)
	if err != nil {
		slog.Warn("enrichment: image queries failed, falling back to title",
			"article_id", a.ID, "error", err)
		queries = []string{a.Title}
	}
	candidates, err := s.searcher.Search(queries)
	if err != nil || len(candidates) == 0 {
		s.recordFailure("image", a.ID, err)
		return false
	}
	if s.scorer == nil {
		return false
	}
	scored, err := s.scorer.Score(a.Title, a.TLDR, candidates)
	if err != nil || len(scored) == 0 {
		s.recordFailure("image", a.ID, err)
		return false
	}
	if scored[0].Score < MinImageScore {
		slog.Info("enrichment: no image met the relevance bar",
			"article_id", a.ID, "top_score", scored[0].Score)
		return false
	}
	u.ImageURL = &scored[0].URL
	u.ImageThumbnail = &scored[0].Thumbnail
	u.ImageAttribution = &scored[0].Attribution
	return true
}

func (s *Service) recordFailure(step string, articleID int64, err error) {
	metrics.EnrichmentFailuresTotal.WithLabelValues(step).Inc()
	slog.Warn("enrichment step failed", "step", step, "article_id", articleID, "error", err)
}
