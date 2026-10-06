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
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	// plus one article's worst case. Within an article only the image step
	// heeds it: it does not start past it, and its checks stop at it (see
	// findImage). Bounding the model calls needs a context threaded through
	// Generate, which it does not take yet, and warming thumbnails for image
	// scoring can add up to 15 s.
	MaxDuration time.Duration

	// Outlets lists the news sites and blogs articles are written from (the
	// sources table); it is read once per pass. Scout's Take and key details
	// must not name them (see ai.OutletMatcher), and nothing revises them
	// after this point, so a take that does is refused and retried, and such a
	// detail is dropped. The built-in outlets are checked even when it is nil.
	Outlets func() []ai.Outlet
	// outlets is this pass's check, built from Outlets in Run.
	outlets *ai.OutletMatcher

	// Infographics turns on the infographic step (ai.enrichment_infographics
	// "preview" or "on"). Off, an article missing only its chart is complete:
	// it is not listed, charged an attempt or charted.
	Infographics bool
	// InfographicClient is what the infographic call goes through; nil uses
	// the enrichment client. main gives it ai.InfographicCallTimeout and no
	// retries, since the answer is far longer than any other enrichment call
	// and would otherwise inherit the generator's timeout.
	InfographicClient ai.HFClientInterface
	// InfographicModels is the chain charts are drawn with
	// (ai.infographic_model); empty uses the enrichment chain.
	InfographicModels []string

	// startedAt records when the in-flight pass began, so a skip can report how
	// long the pass it is yielding to has been running.
	startedAt atomic.Int64

	// ResizeMaxDuration and ResizeMaxRows bound the stored-image resize that
	// opens each pass (see resizeStoredImages), so it cannot eat the time
	// enrichment needs. The duration is further capped at a quarter of
	// MaxDuration. No row starts after it, and each row's probes stop at
	// it. Zero means the default.
	ResizeMaxDuration time.Duration
	ResizeMaxRows     int

	// originalWidths caches the pixel widths of Wikimedia originals that
	// resizeStoredImages has looked up; 0 records a file Commons reports as
	// deleted. widthNoAnswer holds, until when, the originals a lookup had no
	// answer for. resizeRetries tracks rows whose resize keeps failing. All
	// are only touched inside Run, which never overlaps itself.
	originalWidths map[string]int
	widthNoAnswer  map[string]time.Time
	resizeRetries  map[int64]*resizeRetry

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
		db:             database,
		client:         client,
		models:         models,
		persona:        persona,
		searcher:       searcher,
		scorer:         scorer,
		MaxAttempts:    3,
		BatchSize:      10,
		Pace:           2 * time.Second,
		MaxDuration:    10 * time.Minute,
		originalWidths: map[string]int{},
		widthNoAnswer:  map[string]time.Time{},
		resizeRetries:  map[int64]*resizeRetry{},
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

	// Before any model calls, so a degraded provider cannot hold it up, and
	// on its own budget, so a slow image host cannot hold up enrichment.
	resizeDeadline := start.Add(s.resizeBudget())
	if resizeDeadline.After(deadline) {
		resizeDeadline = deadline
	}
	s.resizeStoredImages(resizeDeadline)

	list := s.db.ListArticlesNeedingEnrichment
	if s.Infographics {
		list = s.db.ListArticlesNeedingEnrichmentWithInfographics
	}
	articles, err := list(s.MaxAttempts, s.BatchSize)
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
	var table []ai.Outlet
	if s.Outlets != nil {
		table = s.Outlets()
	}
	s.outlets = ai.NewOutletMatcher(table)
	var filled, failed, done int
	hitDeadline := func() {
		metrics.EnrichmentPassesTotal.WithLabelValues("deadline").Inc()
		slog.Warn("enrichment pass hit its deadline",
			"processed", done, "abandoned", len(articles)-done,
			"elapsed", time.Since(start).Round(time.Second))
		// Deliberately NOT a liveness update: the pass did not finish its
		// work, and treating it as success would mask a provider that is
		// slow enough to prevent the backlog ever draining.
	}
	for i, a := range articles {
		// Pace before the deadline check, so no article starts after the
		// deadline because the sleep ran past it.
		if i > 0 && s.Pace > 0 {
			time.Sleep(s.Pace)
		}
		if time.Now().After(deadline) {
			hitDeadline()
			return
		}
		got, missed, putOff := s.enrichOne(a, deadline)
		filled += got
		failed += missed
		done++
		if putOff {
			// The deadline passed during this article and its image search
			// waits for the next pass. Any article after it would stop at the
			// check above; the last one would otherwise end the pass as
			// "completed" with work left undone.
			hitDeadline()
			return
		}
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
// failing step does not discard the others. deadline is the pass's: the image
// checks stop at it (see findImage).
//
// putOff reports that the image search or the infographic was put off
// because the deadline had passed; whatever else was filled is still saved.
func (s *Service) enrichOne(a db.ArticleNeedingEnrichment, deadline time.Time) (filled, failed int, putOff bool) {
	var u db.EnrichmentUpdate

	if a.MissingScoutsTake {
		take, err := ai.GenerateScoutsTake(s.client, s.models, s.persona, a.Title, a.TLDR, a.Content)
		if err == nil {
			err = s.checkOutlets(take, a.Title)
		}
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
		if err == nil {
			details, err = s.dropOutletDetails(details, a.Title)
		}
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

	var imageWhy string // why no image was found, for an admin's request
	if a.MissingImage {
		var res imageResult
		res, imageWhy = s.findImage(&u, a, deadline)
		switch res {
		case imageFound:
			filled++
			u.ImageRequestSeq = a.ImageRequestSeq
		case imageDeferred:
			putOff = true
		case imageNoMatch:
			failed++
			if a.ImageRequestSeq > 0 {
				// The search ran and nothing would do: the same words would
				// find the same pictures next pass, so the admin hears now.
				s.failImageRequest(a, imageWhy)
			}
		default:
			failed++
		}
	}

	// After the image, which matters more to the page: a chart is a few
	// seconds to a few minutes of model time, and should not be what leaves
	// an article without its picture when the pass is short of time.
	var chartPutOff bool
	if a.MissingInfographic && s.Infographics {
		switch {
		case !time.Now().Before(deadline):
			// Not tried, so not charged; it waits for the next pass like an
			// image search would.
			putOff = true
		case a.ImageRequestSeq > 0:
			// An admin is waiting on a new image, which is only saved once
			// this article is done; the chart can wait a pass instead.
			chartPutOff = true
		case time.Until(deadline) < s.infographicBudget():
			// Not enough of the pass left for the whole chain to answer:
			// starting would run the pass past its deadline. Later
			// articles still get their other fields this pass.
			chartPutOff = true
		default:
			switch ok, err := s.drawInfographic(&u, a); {
			case err != nil:
				s.recordFailure("infographic", a.ID, err)
				failed++
			case ok:
				filled++
			}
			// Neither filled nor failed when the article has nothing to
			// chart (a sentinel was written, so it is not asked again) or
			// has no key details yet (it comes back once it does).
		}
	}

	// An image search or chart put off was not tried, so it is not charged:
	// if nothing else failed, the attempt is not counted, and an article with
	// nothing to write is not touched at all. Only an image search put off by
	// the deadline ends the pass (putOff); a chart put off for want of time
	// lets later articles fill their quicker fields.
	if (putOff || chartPutOff) && failed == 0 {
		if filled == 0 {
			return 0, 0, putOff
		}
		u.Uncounted = true
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
		return filled, failed, putOff
	}
	attempt := a.Attempts + 1
	if u.Uncounted {
		attempt = a.Attempts
	}
	if a.ImageRequestSeq > 0 && u.ImageURL == nil && attempt >= s.MaxAttempts {
		// The article is not listed again with its attempts spent, so a
		// request still pending would wait for ever.
		why := "the image search kept failing"
		if imageWhy != "" {
			why = imageWhy
		} else if putOff {
			why = "the enrichment pass kept running out of time"
		}
		s.failImageRequest(a, fmt.Sprintf("gave up after %d tries: %s", attempt, why))
	}
	slog.Info("enrichment applied", "article_id", a.ID, "was_missing", a.Missing(),
		"filled", filled, "failed", failed, "attempt", attempt, "image_deferred", putOff)
	return filled, failed, putOff
}

// imageResult is what findImage did.
type imageResult int

const (
	imageFound    imageResult = iota // a candidate was picked
	imageFailed                      // something went wrong; worth another try
	imageNoMatch                     // the search ran and nothing could be used
	imageDeferred                    // the pass deadline had passed; nothing was tried
)

// findImage searches and scores candidates, reporting whether one was good
// enough to use and, when none was, why. A search that finds nothing
// relevant is a legitimate outcome rather than an error, but it still counts
// against the attempt budget so the article is not searched forever.
//
// An admin may have asked for a new image (a.ImageRequestSeq): the images
// they turned down for the article are never picked again, at any size, and
// their search words (a.ImageHint), when they gave some, are the query in
// place of the generated ones -- and are shown to the scorer as what the
// editor wants, alongside the article. Relevance, byte caps and the
// fetchability checks are the same either way.
//
// deadline is the pass's. If the article's earlier model calls ran past it,
// the search does not start: query generation, the search, warming (up to
// 15 s) and the vision calls would only lead to candidates it may not check.
// That returns imageDeferred, which does not use an attempt.
//
// Checking that candidates load also stops at deadline. A Wikimedia probe may
// take up to 30 s (it waits for a render and retries), so a run of hanging
// candidates could otherwise hold the pass minutes past its deadline and make
// the scheduler skip the next one. An article whose checks are cut short gets
// no image this pass; like any failed image step, that uses one of its
// attempts.
func (s *Service) findImage(u *db.EnrichmentUpdate, a db.ArticleNeedingEnrichment, deadline time.Time) (imageResult, string) {
	if s.searcher == nil {
		return imageFailed, "image search is not configured"
	}
	if !time.Now().Before(deadline) {
		slog.Info("enrichment: pass deadline reached before the image search; it waits for the next pass",
			"article_id", a.ID)
		return imageDeferred, ""
	}
	turnedDown, err := s.db.RejectedImageURLs(a.ID)
	if err != nil {
		// Without the list the search could put back the very image the
		// admin turned down.
		s.recordFailure("image", a.ID, err)
		return imageFailed, "could not read the images turned down before"
	}
	var queries []string
	if a.ImageHint != "" {
		queries = []string{a.ImageHint}
	} else if queries, err = ai.GenerateImageQueries(s.client, s.models, a.Title, a.TLDR); err != nil {
		slog.Warn("enrichment: image queries failed, falling back to title",
			"article_id", a.ID, "error", err)
		queries = []string{a.Title}
	}
	candidates, err := s.searcher.SearchExcluding(queries, images.NewRejected(turnedDown))
	if err != nil {
		s.recordFailure("image", a.ID, err)
		return imageFailed, "the image search failed"
	}
	if len(candidates) == 0 {
		s.recordFailure("image", a.ID, nil)
		if len(turnedDown) > 0 {
			return imageNoMatch, "the search found no images other than ones turned down"
		}
		return imageNoMatch, "the search found no images"
	}
	if s.scorer == nil {
		return imageFailed, "image scoring is not configured"
	}
	summary := a.TLDR
	if a.ImageHint != "" {
		summary = strings.TrimSpace(summary + "\nThe editor asked for a picture of: " + a.ImageHint)
	}
	scored, err := s.scorer.Score(a.Title, summary, candidates)
	if err != nil || len(scored) == 0 {
		s.recordFailure("image", a.ID, err)
		return imageFailed, "scoring the images failed"
	}
	if scored[0].Score <= 0 {
		// Scores run 1-10; none at all means the scoring calls failed and
		// the candidates came back as they went in. That is worth another
		// try, not a verdict on the pictures.
		s.recordFailure("image", a.ID, nil)
		return imageFailed, "scoring the images failed"
	}
	if scored[0].Score < MinImageScore {
		slog.Info("enrichment: no image met the relevance bar",
			"article_id", a.ID, "top_score", scored[0].Score)
		return imageNoMatch, fmt.Sprintf("none of %d images was relevant enough (best scored %.0f of 10)",
			len(scored), scored[0].Score)
	}
	// Walk down in score order until one is relevant enough, actually
	// fetchable and light enough for the page. Scoring alone is not enough: an
	// image host can return a URL that is already dead or that dies later, and
	// without this check the first anyone learns of it is a broken thumbnail
	// on the homepage -- or, as with the Wikimedia originals, a 13 MB one.
	for i := range scored {
		c := scored[i]
		if c.Score < MinImageScore {
			break
		}
		if !time.Now().Before(deadline) {
			slog.Warn("enrichment: pass deadline reached while checking image candidates",
				"article_id", a.ID, "checked", i)
			return imageFailed, "the enrichment pass ran out of time checking images"
		}
		if !s.searcher.Usable(c, deadline) {
			continue
		}
		u.ImageURL = &scored[i].URL
		u.ImageThumbnail = &scored[i].Thumbnail
		u.ImageAttribution = &scored[i].Attribution
		return imageFound, ""
	}
	slog.Info("enrichment: every relevant image candidate was unfetchable",
		"article_id", a.ID, "candidates", len(scored))
	return imageNoMatch, "every relevant image failed to load or was too large"
}

// drawInfographic asks a model to chart the article's key details.
//
// It runs after the key-details step in the same pass, so an article whose
// details were just extracted can be charted immediately -- but only from the
// value that was actually written, never from a half-filled row.
//
// The three outcomes are distinct on purpose. An error is a failure worth
// retrying; a drawing is a fill; and "this article has no numbers in it" is
// neither. That last case writes a sentinel rather than leaving the column
// empty, because otherwise every pass would pay for a model call to be told
// the same thing again, and an article that is otherwise complete would burn
// through its whole attempt budget on the one field it can never have.
func (s *Service) drawInfographic(u *db.EnrichmentUpdate, a db.ArticleNeedingEnrichment) (bool, error) {
	details := a.KeyDetails
	if u.KeyDetails != nil {
		details = *u.KeyDetails
	}
	if details == "" || details == "[]" || details == "null" {
		// Nothing to chart from yet. Leave the column empty so the article
		// comes back once key details exist.
		return false, nil
	}

	client := s.InfographicClient
	if client == nil {
		client = s.client
	}
	// The chart's words are public text derived from the article, like
	// Scout's Take, and nothing revises them after this point, so they get
	// the same guards: a chart naming a news site is refused, and so is one
	// showing a figure the article does not state, which would be published
	// as fact (see ai.UnsupportedChartNumbers). A refused chart goes to the
	// next model in the chain, then to a later pass.
	accept := func(words string) error {
		if ms := s.outletMatcher().DerivedTextMentions(words, a.Title); len(ms) > 0 {
			return fmt.Errorf("infographic names a news site (%s): %q", ms[0].Outlet, ms[0].Text)
		}
		if bad := ai.UnsupportedChartNumbers(words, a.Title, a.TLDR, details, ai.VisibleText(a.Content)); len(bad) > 0 {
			return fmt.Errorf("infographic shows numbers the article does not: %q", bad)
		}
		return nil
	}
	markup, err := ai.GenerateInfographic(client, s.infographicModels(), a.Title, a.TLDR, details, accept)
	if errors.Is(err, ai.ErrNoInfographic) {
		slog.Info("enrichment: article has nothing chartable",
			"article_id", a.ID)
		sentinel := db.NoInfographic
		u.InfographicSVG = &sentinel
		return false, nil
	}
	if err != nil {
		return false, err
	}
	u.InfographicSVG = &markup
	return true, nil
}

// infographicModels is the chain charts are drawn with.
func (s *Service) infographicModels() []string {
	if len(s.InfographicModels) > 0 {
		return s.InfographicModels
	}
	return s.models
}

// infographicBudget is how long a chart may take: every model in the chain
// timing out in turn (see ai.InfographicCallTimeout).
func (s *Service) infographicBudget() time.Duration {
	return ai.InfographicCallTimeout * time.Duration(max(len(s.infographicModels()), 1))
}

// failImageRequest tells the admin their request for a new image found
// nothing; the article keeps the image it has.
func (s *Service) failImageRequest(a db.ArticleNeedingEnrichment, why string) {
	metrics.EnrichmentFailuresTotal.WithLabelValues("new_image").Inc()
	slog.Info("enrichment: no new image found for the admin's request",
		"article_id", a.ID, "hint", a.ImageHint, "reason", why)
	if err := s.db.FailImageRequest(a.ID, a.ImageRequestSeq, why); err != nil {
		slog.Error("enrichment: could not record the failed image request", "article_id", a.ID, "error", err)
	}
}

// outletMatcher is this pass's outlet check, or the built-in outlets alone
// when enrichOne runs outside Run.
func (s *Service) outletMatcher() *ai.OutletMatcher {
	if s.outlets == nil {
		s.outlets = ai.NewOutletMatcher(nil)
	}
	return s.outlets
}

// checkOutlets refuses a Scout's Take that names a news site, so the field is
// retried on a later pass.
func (s *Service) checkOutlets(take, title string) error {
	if ms := s.outletMatcher().DerivedTextMentions(take, title); len(ms) > 0 {
		return fmt.Errorf("scouts take names a news site (%s): %q", ms[0].Outlet, ms[0].Text)
	}
	return nil
}

// dropOutletDetails drops key details that name a news site. If none are
// left the field fails, as when every detail had a numbered source reference,
// so it is retried rather than stored empty.
func (s *Service) dropOutletDetails(details []ai.KeyDetail, title string) ([]ai.KeyDetail, error) {
	var kept []ai.KeyDetail
	for _, d := range details {
		if ms := s.outletMatcher().DerivedTextMentions(d.Label+": "+d.Value, title); len(ms) > 0 {
			slog.Warn("dropped key detail that names a news site", "label", d.Label, "outlet", ms[0].Outlet)
			continue
		}
		kept = append(kept, d)
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("every key detail named a news site")
	}
	return kept, nil
}

func (s *Service) recordFailure(step string, articleID int64, err error) {
	metrics.EnrichmentFailuresTotal.WithLabelValues(step).Inc()
	slog.Warn("enrichment step failed", "step", step, "article_id", articleID, "error", err)
}
