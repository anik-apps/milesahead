package enrich

import (
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/metrics"
)

// Limits on the resize step's share of a pass. It runs before enrichment, so
// without them a slow or throttling image host could spend the whole pass
// deadline on probes: enrichment would get no time, the pass would end as
// "deadline", and the liveness timestamp would stop moving for as long as the
// host stayed slow.
const (
	// defaultResizeMaxDuration bounds the step, and it never takes more than
	// a quarter of the pass (see resizeBudget). It is checked between rows,
	// so the true ceiling is this plus one row's worst case.
	defaultResizeMaxDuration = 90 * time.Second
	// defaultResizeMaxRows bounds how many rows one pass sends to the
	// network. The rest wait for the next pass, 15 minutes later.
	defaultResizeMaxRows = 25
	// A row that fails this many passes running is set aside for
	// resizeBackoff instead of being probed on every pass.
	resizeFailuresBeforeBackoff = 2
	resizeBackoff               = 6 * time.Hour
)

// The widths tried for each field, best first. The second is used only when
// the first comes back as an image over the field's byte cap -- a detailed
// PNG can pass 1 MiB at 1280 px -- because any sized copy is far lighter than
// the multi-megabyte original the row would otherwise keep.
var (
	displaySteps   = []int{images.HeroWidth, 960}
	thumbnailSteps = []int{images.ThumbnailWidth, 330}
)

// resizeRetry tracks a row whose resize keeps failing.
type resizeRetry struct {
	failures  int
	notBefore time.Time
}

// resizeJob is one stored row the resize step has network work for.
type resizeJob struct {
	row db.StoredArticleImage
	// Sized URLs to try for each field, best first; nil if the field stays.
	urls, thumbs []string
	// deleted: Commons reports the display image's file as deleted.
	deleted bool
	// heavy: the row holds a full-size Wikimedia original, the case that is
	// megabytes rather than hundreds of kilobytes, so it goes first.
	heavy bool
}

// resizeOutcome is what resizeOne did with a row.
type resizeOutcome int

const (
	// resizeDone: every field that needed it now holds a confirmed sized URL.
	resizeDone resizeOutcome = iota
	// resizeCleared: a deleted Commons file was removed for re-enrichment.
	resizeCleared
	// resizeIncomplete: at least one field could not be confirmed and keeps
	// its current URL. The row counts towards the backoff.
	resizeIncomplete
	// resizeSkipped: the row changed under us, or the write failed; it is
	// looked at afresh next pass, without counting as a failure.
	resizeSkipped
)

// resizeStoredImages swaps stored article images that are far heavier than
// the page shows them for sized copies of the same picture.
//
// Until the image search learned to ask for sized files, an Openverse pick
// stored the provider's full-size original as both the image and its
// thumbnail. For Wikimedia that meant camera originals of 2.4 to 13.2 MB, two
// of which made the homepage weigh 16.5 MB to fill 100-360 px tiles; Flickr
// picks stored the 1024 px file as the thumbnail. Those rows are still in the
// database, so this runs at the start of every enrichment pass -- including
// the one ~90s after each deploy -- and converges without anyone running a
// command:
//
//   - Wikimedia originals become /thumb/ URLs at a standard step: 1280 px for
//     the display image, 500 px for the thumbnail. The original's width comes
//     from the Commons API; a step is only used when the original is wider,
//     so nothing is upscaled, and an original already narrower than its slot
//     is kept as it is. If that step is an image over the byte cap, the next
//     step down (960 / 330) is tried.
//   - Flickr thumbnails larger than 500 px use Flickr's 500 px size.
//   - Every new URL is fetched first and must answer 200/206 with an image
//     content type, within the field's byte cap (images.CheckFit). Any field
//     that cannot be confirmed keeps its current URL; a row is never pointed
//     at an image that has not just been seen to load.
//   - A Wikimedia file Commons reports as deleted, whose URL also answers 404
//     or 410, is already a broken image. It is cleared and handed back to
//     enrichment to pick a new one, as repairRottedImages does for expired
//     Pixabay links. If the URL still loads, it is left alone.
//
// It is bounded: it stops at deadline (Run passes the resize budget, not the
// whole pass's), sends at most ResizeMaxRows rows to the network per pass,
// heaviest and newest first, and sets aside for resizeBackoff any row that
// has failed resizeFailuresBeforeBackoff passes running.
//
// It is idempotent: a sized URL is left alone by images.Sized, so once a row
// is converted later passes skip it without any network traffic.
func (s *Service) resizeStoredImages(deadline time.Time) {
	if s.searcher == nil {
		return
	}
	stored, err := s.db.ListStoredArticleImages()
	if err != nil {
		slog.Warn("image resize: could not list article images", "error", err)
		return
	}
	s.lookUpOriginalWidths(stored)

	now := time.Now()
	var jobs []resizeJob
	unsized, setAside := 0, 0 // rows still to convert; rows backing off
	for _, a := range stored {
		job := resizeJob{
			row:     a,
			urls:    sizedSteps(a.URL, s.originalWidths[a.URL], displaySteps),
			thumbs:  sizedSteps(a.Thumbnail, s.originalWidths[a.Thumbnail], thumbnailSteps),
			deleted: s.deletedOnCommons(a.URL),
			heavy:   images.IsWikimediaOriginal(a.URL) || images.IsWikimediaOriginal(a.Thumbnail),
		}
		if job.urls == nil && job.thumbs == nil && !job.deleted {
			if s.widthUnknown(a.URL) || s.widthUnknown(a.Thumbnail) {
				unsized++ // waiting on a width lookup that failed
			}
			continue
		}
		unsized++
		if r := s.resizeRetries[a.ID]; r != nil && now.Before(r.notBefore) {
			setAside++
			continue
		}
		jobs = append(jobs, job)
	}
	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].heavy != jobs[j].heavy {
			return jobs[i].heavy
		}
		return jobs[i].row.ID > jobs[j].row.ID // the homepage shows the newest
	})
	deferred := 0
	if limit := s.resizeMaxRows(); len(jobs) > limit {
		deferred = len(jobs) - limit
		jobs = jobs[:limit]
	}

	var resized, unconfirmed, cleared int
	for i, job := range jobs {
		// Space out rows. Wikimedia throttles clients asking for thumbnails it
		// has not rendered yet.
		if i > 0 && s.Pace > 0 {
			time.Sleep(s.Pace)
		}
		if time.Now().After(deadline) {
			deferred += len(jobs) - i
			slog.Warn("image resize: time budget spent; the rest wait for the next pass",
				"rows_left", len(jobs)-i)
			break
		}
		switch s.resizeOne(job) {
		case resizeDone:
			resized++
			unsized--
		case resizeCleared:
			cleared++
			unsized--
		case resizeIncomplete:
			unconfirmed++
		}
	}

	metrics.ImageResizeTotal.WithLabelValues("resized").Add(float64(resized))
	metrics.ImageResizeTotal.WithLabelValues("unconfirmed").Add(float64(unconfirmed))
	metrics.ImageResizeTotal.WithLabelValues("cleared").Add(float64(cleared))
	metrics.ArticleImagesUnsized.Set(float64(unsized))
	if len(jobs) > 0 || setAside > 0 || deferred > 0 {
		slog.Info("image resize pass complete", "resized", resized,
			"left_unchanged_unconfirmed", unconfirmed, "cleared_deleted", cleared,
			"deferred_to_next_pass", deferred, "set_aside_after_failures", setAside,
			"still_unsized", unsized)
	}
}

// resizeOne converts one row, writing only what it could confirm.
func (s *Service) resizeOne(job resizeJob) resizeOutcome {
	a := job.row
	incomplete := false
	if job.deleted {
		if s.clearIfGone(a) {
			delete(s.resizeRetries, a.ID)
			return resizeCleared
		}
		// Commons calls the file missing, but its URL did not answer 404 or
		// 410: a CDN still serving it, or a request that failed. Either way it
		// is not a broken image, so it stays. Its width is unknown, so it
		// cannot be sized; the thumbnail below still can be. The row counts as
		// a failure so it is rechecked on the backoff schedule, not every pass.
		incomplete = true
	}

	newURL, newThumb := a.URL, a.Thumbnail
	if job.urls != nil {
		if u, ok := s.firstThatFits(job.urls, images.MaxImageBytes); ok {
			newURL = u
		} else {
			incomplete = true
		}
	}
	if job.thumbs != nil {
		if u, ok := s.firstThatFits(job.thumbs, images.MaxThumbnailBytes); ok {
			newThumb = u
		} else {
			incomplete = true
		}
	}

	if newURL != a.URL || newThumb != a.Thumbnail {
		ok, err := s.db.ReplaceArticleImage(a, newURL, newThumb)
		if err != nil {
			slog.Warn("image resize: could not save", "article_id", a.ID, "error", err)
			return resizeSkipped
		}
		if !ok {
			// The row changed under us; next pass will look at it afresh.
			return resizeSkipped
		}
		slog.Info("image resize: article image replaced with a sized copy", "article_id", a.ID,
			"image_url_from", a.URL, "image_url_to", newURL,
			"thumbnail_from", a.Thumbnail, "thumbnail_to", newThumb)
	}

	if incomplete {
		s.noteResizeFailure(a.ID)
		return resizeIncomplete
	}
	delete(s.resizeRetries, a.ID)
	return resizeDone
}

// firstThatFits returns the first of candidates that is an image within
// limit. A step that is too heavy moves on to the next, narrower one; a step
// that does not load at all ends the search, since a narrower copy of the
// same file on the same host would not fare better this pass.
func (s *Service) firstThatFits(candidates []string, limit int64) (string, bool) {
	for _, u := range candidates {
		switch s.searcher.CheckFit(u, limit) {
		case images.Fits:
			return u, true
		case images.TooHeavy:
			continue
		default:
			return "", false
		}
	}
	return "", false
}

// noteResizeFailure counts a failed pass for a row and, once it has failed
// resizeFailuresBeforeBackoff passes running, sets it aside for resizeBackoff.
// The record lives in memory: a restart gives every row a fresh start.
func (s *Service) noteResizeFailure(id int64) {
	if s.resizeRetries == nil {
		s.resizeRetries = map[int64]*resizeRetry{}
	}
	r := s.resizeRetries[id]
	if r == nil {
		r = &resizeRetry{}
		s.resizeRetries[id] = r
	}
	r.failures++
	if r.failures >= resizeFailuresBeforeBackoff {
		r.notBefore = time.Now().Add(resizeBackoff)
		slog.Warn("image resize: row keeps failing; setting it aside", "article_id", id,
			"failures", r.failures, "retry_after", resizeBackoff)
	}
}

// sizedSteps returns the sized URLs to try for one stored field, best first,
// or nil when the field should stay as it is.
func sizedSteps(raw string, originalWidth int, steps []int) []string {
	first, ok := images.Sized(raw, originalWidth, steps[0])
	if !ok {
		return nil
	}
	out := []string{first}
	for _, w := range steps[1:] {
		if u, ok := images.Sized(raw, originalWidth, w); ok && u != out[len(out)-1] {
			out = append(out, u)
		}
	}
	return out
}

// resizeBudget is how long the resize step may run in one pass.
func (s *Service) resizeBudget() time.Duration {
	d := s.ResizeMaxDuration
	if d <= 0 {
		d = defaultResizeMaxDuration
	}
	return min(d, s.maxDuration()/4)
}

func (s *Service) resizeMaxRows() int {
	if s.ResizeMaxRows > 0 {
		return s.ResizeMaxRows
	}
	return defaultResizeMaxRows
}

// lookUpOriginalWidths fills s.originalWidths for any Commons original in
// stored it has not seen. Widths do not change, so each is asked for once per
// process; a failed lookup records nothing and is retried next pass.
func (s *Service) lookUpOriginalWidths(stored []db.StoredArticleImage) {
	if s.originalWidths == nil {
		s.originalWidths = map[string]int{}
	}
	var unknown []string
	for _, a := range stored {
		for _, u := range []string{a.URL, a.Thumbnail} {
			if s.widthUnknown(u) {
				unknown = append(unknown, u)
			}
		}
	}
	if len(unknown) == 0 {
		return
	}
	widths, err := s.searcher.WikimediaWidths(unknown)
	if err != nil {
		slog.Warn("image resize: Wikimedia width lookup failed; those images wait for the next pass",
			"error", err)
	}
	for u, w := range widths {
		s.originalWidths[u] = w
	}
}

// widthUnknown reports whether u is a Commons original whose width has not
// been looked up yet (or whose lookup failed).
func (s *Service) widthUnknown(u string) bool {
	_, known := s.originalWidths[u]
	return !known && images.IsWikimediaOriginal(u)
}

// deletedOnCommons reports whether the Commons API said this original's file
// page does not exist.
func (s *Service) deletedOnCommons(u string) bool {
	w, known := s.originalWidths[u]
	return known && w == 0
}

// clearIfGone clears an article image whose Commons file was deleted, but only
// once the URL itself is seen to answer 404 or 410 -- the API saying so is
// not enough on its own to take an image off a live article.
func (s *Service) clearIfGone(a db.StoredArticleImage) bool {
	p := s.searcher.Probe(a.URL, 0)
	if p.Status != http.StatusNotFound && p.Status != http.StatusGone {
		slog.Info("image resize: Commons reports the file deleted but its URL did not answer 404/410; keeping it",
			"article_id", a.ID, "image_url", a.URL, "status", p.Status)
		return false
	}
	ok, err := s.db.ClearArticleImage(a)
	if err != nil {
		slog.Warn("image resize: could not clear deleted image", "article_id", a.ID, "error", err)
		return false
	}
	if ok {
		slog.Warn("image resize: article image was deleted from Wikimedia Commons; "+
			"cleared so enrichment picks a new one", "article_id", a.ID, "image_url", a.URL)
	}
	return ok
}
