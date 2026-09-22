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
	"sync/atomic"
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

	// MaxDuration bounds one pass. Without it a pass has no limit of its own:
	// on a degraded provider each field can burn its full retry budget across
	// the model chain, and a batch of ten articles can outlast its interval
	// many times over. The guard below then turns that into silence -- every
	// later tick is skipped, and the degraded gauge stays frozen at whatever it
	// last read, so a stalled reconciler is indistinguishable from an idle one.
	//
	// The bound is checked between articles, so the true ceiling is MaxDuration
	// plus one article's worst case. Bounding mid-article needs a context
	// threaded through Generate, which it does not take yet.
	MaxDuration time.Duration

	// startedAt records when the in-flight pass began, so a skip can report how
	// long the pass it is yielding to has been running.
	startedAt atomic.Int64

	// running guards against overlapping passes. robfig/cron's "@every" does
	// NOT skip a tick when the previous run is still going, and a pass can
	// outlast its interval when the provider is degraded -- each field may
	// retry for minutes. Without this, passes pile up, multiply load on an
	// already-struggling provider, and race each other's attempt counters.
	running atomic.Bool
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
		MaxDuration: 10 * time.Minute,
	}
}

// Run reconciles one batch. It is safe to call on a schedule; each pass picks
// up whatever is still missing.
func (s *Service) Run() {
	if !s.running.CompareAndSwap(false, true) {
		metrics.EnrichmentPassesTotal.WithLabelValues("skipped").Inc()
		// Refresh the gauge here too. This branch returns before the deferred
		// refresh below is even registered, and a run of skips is exactly when
		// the backlog most needs reporting -- leaving it frozen on the skip path
		// would reproduce, on the one path a stall actually takes, the staleness
		// this change exists to remove.
		s.refreshDegradedGauge()
		// WARN, not INFO. A skip is not routine -- it means the previous pass
		// has outlasted a whole interval, which is the shape a stall takes.
		slog.Warn("enrichment pass skipped: previous pass still running",
			"previous_running_for", time.Since(time.Unix(s.startedAt.Load(), 0)).Round(time.Second))
		return
	}
	defer s.running.Store(false)

	start := time.Now()
	s.startedAt.Store(start.Unix())
	deadline := start.Add(s.maxDuration())

	// Always refresh the degraded gauge and the pass duration, whatever the
	// outcome. Updating them only on the success path is what let the gauge sit
	// frozen at a healthy-looking value while nothing was actually progressing.
	defer func() {
		metrics.EnrichmentPassDuration.Observe(time.Since(start).Seconds())
		s.refreshDegradedGauge()
	}()

	articles, err := s.db.ListArticlesNeedingEnrichment(s.MaxAttempts, s.BatchSize)
	if err != nil {
		metrics.EnrichmentPassesTotal.WithLabelValues("error").Inc()
		slog.Error("enrichment: could not list articles", "error", err)
		return
	}
	if len(articles) == 0 {
		// Nothing to do is a healthy outcome, so it counts as liveness: the
		// reconciler ran and found the backlog empty.
		metrics.EnrichmentPassesTotal.WithLabelValues("completed").Inc()
		metrics.EnrichmentLastSuccessTimestamp.SetToCurrentTime()
		return
	}

	slog.Info("enrichment pass starting", "articles", len(articles),
		"deadline", s.maxDuration())
	var filled, failed, done int
	for i, a := range articles {
		if time.Now().After(deadline) {
			metrics.EnrichmentPassesTotal.WithLabelValues("deadline").Inc()
			slog.Warn("enrichment pass hit its deadline",
				"processed", done, "abandoned", len(articles)-done,
				"elapsed", time.Since(start).Round(time.Second))
			// Deliberately NOT a liveness update: the pass did not finish its
			// work, and treating it as success would mask a provider that is
			// slow enough to prevent the backlog ever draining.
			return
		}
		if i > 0 && s.Pace > 0 {
			time.Sleep(s.Pace)
		}
		got, missed := s.enrichOne(a)
		filled += got
		failed += missed
		done++
	}

	metrics.EnrichmentPassesTotal.WithLabelValues("completed").Inc()
	metrics.EnrichmentLastSuccessTimestamp.SetToCurrentTime()
	slog.Info("enrichment pass complete", "articles", len(articles),
		"fields_filled", filled, "fields_failed", failed,
		"elapsed", time.Since(start).Round(time.Second))
}

// maxDuration returns the configured pass bound, or the default if unset.
func (s *Service) maxDuration() time.Duration {
	if s.MaxDuration > 0 {
		return s.MaxDuration
	}
	return 10 * time.Minute
}

// refreshDegradedGauge republishes how many articles are still missing fields.
func (s *Service) refreshDegradedGauge() {
	pending, exhausted, err := s.db.CountDegradedArticles(s.MaxAttempts)
	if err != nil {
		slog.Warn("enrichment: could not count degraded articles", "error", err)
		return
	}
	metrics.ArticlesDegraded.WithLabelValues("pending").Set(float64(pending))
	metrics.ArticlesDegraded.WithLabelValues("exhausted").Set(float64(exhausted))
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
		// The attempt counter lives in this same UPDATE, so a failure here
		// means the work is discarded AND the article is not charged an
		// attempt -- it comes back next pass and the model calls are paid for
		// again, forever, with MaxAttempts providing no ceiling. Retrying a
		// transient lock is right; doing so invisibly is not, so it is counted.
		metrics.EnrichmentFailuresTotal.WithLabelValues("save").Inc()
		slog.Error("enrichment: could not save, work discarded and attempt not counted",
			"article_id", a.ID, "fields_filled", filled, "error", err)
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
	// Walk down in score order until one is both relevant enough and actually
	// fetchable. Scoring alone is not enough: an image host can return a URL
	// that is already dead or that dies later, and without this check the
	// first anyone learns of it is a broken thumbnail on the homepage.
	for i := range scored {
		c := scored[i]
		if c.Score < MinImageScore {
			break
		}
		if !s.searcher.Reachable(c.Thumbnail) {
			continue
		}
		if c.URL != c.Thumbnail && !s.searcher.Reachable(c.URL) {
			continue
		}
		u.ImageURL = &scored[i].URL
		u.ImageThumbnail = &scored[i].Thumbnail
		u.ImageAttribution = &scored[i].Attribution
		return true
	}
	slog.Info("enrichment: every relevant image candidate was unfetchable",
		"article_id", a.ID, "candidates", len(scored))
	return false
}

func (s *Service) recordFailure(step string, articleID int64, err error) {
	metrics.EnrichmentFailuresTotal.WithLabelValues(step).Inc()
	slog.Warn("enrichment step failed", "step", step, "article_id", articleID, "error", err)
}
