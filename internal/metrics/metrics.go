package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	ScansTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_scans_total",
		Help: "Total scans completed",
	}, []string{"source", "status"})

	ArticlesGeneratedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_articles_generated_total",
		Help: "Total articles generated",
	}, []string{"type", "status"})

	ArticlesApprovedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_articles_approved_total",
		Help: "Total articles approved",
	})

	ArticlesRejectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_articles_rejected_total",
		Help: "Total articles rejected",
	})

	RevisionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_revisions_total",
		Help: "Total revisions processed",
	}, []string{"status"}) // improved, unchanged, failed

	// RevisionReconcileTotal counts what the revision reconciler did with each
	// article it looked at: processed (rewrite stored), failed (rewrite
	// attempted and failed), skipped (already being revised, or no longer
	// waiting), error (could not read its retry state; not attempted).
	// pass_skipped counts whole passes skipped because the previous one was
	// still running. Revisions used to run only from the Revise button's
	// one-shot goroutine, so a failure or a deploy mid-run left the article in
	// "Revising" for good with nothing counting it.
	RevisionReconcileTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_revision_reconcile_total",
		Help: "Revision reconciler outcomes per article (processed, failed, skipped, error) and pass_skipped",
	}, []string{"outcome"})

	// RevisionsWaiting is the current number of articles in revision_requested:
	// due (will be tried next pass), backoff (failed, waiting to retry) and
	// exhausted (out of retries, needs the admin). exhausted > 0 is alertable.
	RevisionsWaiting = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "milesahead_revisions_waiting",
		Help: "Articles waiting for a revision, by state (due, backoff, exhausted)",
	}, []string{"state"})

	AIAPIDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_ai_api_duration_seconds",
		Help:    "AI API call duration including retries",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
	})

	// EnrichmentFailuresTotal counts per-step enrichment failures. Before this
	// existed, a failed step logged WARN and the article published with a hole
	// in it -- 24% of articles for two and a half months, with no metric that
	// would have shown it.
	EnrichmentFailuresTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_enrichment_failures_total",
		Help: "Enrichment step failures by step",
	}, []string{"step"})

	// ArticlesDegraded is the current number of articles missing at least one
	// enrichment field, split into those the reconciler will retry and those
	// it has given up on.
	ArticlesDegraded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "milesahead_articles_degraded",
		Help: "Articles missing enrichment, by whether retries remain",
	}, []string{"state"})

	// EnrichmentPassesTotal counts reconciliation passes by how they ended.
	// "skipped" is the important one: it means the previous pass was still
	// running, which on a degraded provider is how enrichment stalls without
	// anything looking wrong.
	EnrichmentPassesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_enrichment_passes_total",
		Help: "Enrichment passes by outcome (completed, deadline, skipped, error)",
	}, []string{"outcome"})

	// EnrichmentLastSuccessTimestamp is the liveness signal. A gauge of
	// "articles still degraded" cannot distinguish a healthy idle system from a
	// stalled one -- both look static. This goes stale instead, so
	// time() - this > threshold is alertable.
	EnrichmentLastSuccessTimestamp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "milesahead_enrichment_last_success_timestamp_seconds",
		Help: "Unix time of the last enrichment pass that ran to completion",
	})

	// EnrichmentPassDuration shows how close passes run to their deadline.
	EnrichmentPassDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_enrichment_pass_duration_seconds",
		Help:    "Wall-clock duration of an enrichment pass",
		Buckets: []float64{10, 30, 60, 120, 300, 600, 1200},
	})

	// AIPreflightModelsReachable is how many configured models answered the
	// startup preflight, by role (generator, fallback, critic, vision,
	// enrichment). role="critic" == 0 means nothing can publish: every article
	// fails verification. Set once per process, when the preflight finishes.
	AIPreflightModelsReachable = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "milesahead_ai_preflight_models_reachable",
		Help: "Configured AI models that answered the startup preflight, by role",
	}, []string{"role"})

	AIAPIErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_ai_api_errors_total",
		Help: "Total AI API final failures",
	})

	CriticScore = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_critic_score",
		Help:    "Critic verification scores",
		Buckets: []float64{25, 50, 75, 85, 95},
	})

	VotesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_votes_total",
		Help: "Total votes cast",
	}, []string{"direction"}) // up, down

	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "milesahead_http_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// Image pipeline metrics
	ImageSearchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_image_search_total",
		Help: "Total image searches by source and status",
	}, []string{"source", "status"}) // source=unsplash/pexels/openverse/wikimedia, status=success/error

	ImageSearchCandidates = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_image_search_candidates",
		Help:    "Number of image candidates found per article",
		Buckets: []float64{0, 3, 5, 10, 15},
	})

	ImageScoringTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_image_scoring_total",
		Help: "Total image scoring attempts by phase and status",
	}, []string{"phase", "status"}) // phase=text/vision, status=success/error

	ImageSelectedScore = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_image_selected_score",
		Help:    "Score of selected images",
		Buckets: []float64{1, 3, 5, 6, 7, 8, 9, 10},
	})

	ImageSelectedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_image_selected_total",
		Help: "Total image selection outcomes",
	}, []string{"outcome"}) // selected, no_match, search_failed

	// ImageResizeTotal counts what the stored-image resize did with each row
	// it tried: swapped for sized copies, left unchanged because a sized URL
	// could not be confirmed, cleared because the file was deleted (or does not
	// load and every sized copy is too heavy to replace it), or found
	// unconvertible (see ArticleImagesUnconvertible).
	ImageResizeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_image_resize_total",
		Help: "Stored article images the resize step tried, by outcome (resized, unconfirmed, cleared, unconvertible)",
	}, []string{"outcome"})

	// ArticleImagesUnsized is how many article images the resize step could
	// still swap for a sized copy after its last pass. It should reach 0
	// within a few passes of a deploy and stay there; a value that stays up
	// means the conversion is stuck (hosts refusing, width lookups failing).
	// Rows that can never convert are counted in ArticleImagesUnconvertible
	// instead, so they do not hold this above 0.
	ArticleImagesUnsized = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "milesahead_article_images_unsized",
		Help: "Article images still waiting to be swapped for a sized copy (excluding unconvertible ones)",
	})

	// ArticleImagesUnconvertible is how many article images the resize step
	// has found it cannot size, as of its last pass: every sized copy is
	// heavier than the file the row holds, Commons reports the file deleted
	// but its URL still loads, or Commons has no width for it. They keep
	// their current, working file and are rechecked every six hours.
	ArticleImagesUnconvertible = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "milesahead_article_images_unconvertible",
		Help: "Article images the resize step cannot swap for a sized copy (no lighter copy, deleted but still served, or no width from Commons)",
	})

	ResponseBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "milesahead_http_response_bytes",
		Help:    "HTTP response body size in bytes",
		Buckets: []float64{256, 1024, 4096, 16384, 65536, 262144, 1048576},
	}, []string{"method", "path"})

	DailyUniqueVisitors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_daily_unique_visitors_total",
		Help: "Approximate unique visitors (increments on first-seen IP hash)",
	})
)

func Init() {
	prometheus.MustRegister(
		ScansTotal, ArticlesGeneratedTotal, ArticlesApprovedTotal,
		EnrichmentFailuresTotal, ArticlesDegraded,
		EnrichmentPassesTotal, EnrichmentLastSuccessTimestamp, EnrichmentPassDuration,
		ArticlesRejectedTotal, RevisionsTotal, AIAPIDuration,
		RevisionReconcileTotal, RevisionsWaiting,
		AIAPIErrorsTotal, AIPreflightModelsReachable, CriticScore, VotesTotal,
		HTTPRequestsTotal, HTTPDuration,
		ImageSearchTotal, ImageSearchCandidates, ImageScoringTotal,
		ImageSelectedScore, ImageSelectedTotal,
		ImageResizeTotal, ArticleImagesUnsized, ArticleImagesUnconvertible,
		ResponseBytes, DailyUniqueVisitors,
	)
}

// RegisterSubscribersGauge registers a gauge that polls the DB for subscriber count.
func RegisterSubscribersGauge(countFn func() float64) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "milesahead_subscribers_total",
			Help: "Current confirmed subscriber count",
		},
		countFn,
	))
}
