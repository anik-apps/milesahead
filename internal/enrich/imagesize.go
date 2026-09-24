package enrich

import (
	"log/slog"
	"net/http"
	"slices"
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

// The widths tried for each field, best first. A narrower one is used only
// when the wider ones come back as images over the field's byte cap -- a
// detailed PNG can pass 1 MiB at 1280 and 960 px -- because any sized copy
// is far lighter than the multi-megabyte original the row would otherwise
// keep. If every width is over the cap, see pickSized.
var (
	displaySteps   = []int{images.HeroWidth, 960, images.ThumbnailWidth}
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
	// first: the row holds a full-size Wikimedia original, the case that is
	// megabytes rather than hundreds of kilobytes, or a thumbnail width
	// Wikimedia no longer serves, which is a broken image; either goes first.
	first bool
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
	s.lookUpOriginalWidths(stored, deadline)

	now := time.Now()
	var jobs []resizeJob
	unsized, setAside := 0, 0 // rows still to convert; rows backing off
	for _, a := range stored {
		job := resizeJob{
			row:     a,
			urls:    s.displayCandidates(a.URL),
			thumbs:  sizedSteps(a.Thumbnail, s.originalWidths[a.Thumbnail], thumbnailSteps),
			deleted: s.deletedOnCommons(displayWidthKey(a.URL)),
			first: images.IsWikimediaOriginal(a.URL) || images.IsWikimediaOriginal(a.Thumbnail) ||
				images.IsUnservedWikimediaThumb(a.URL) || images.IsUnservedWikimediaThumb(a.Thumbnail),
		}
		if job.urls == nil && job.thumbs == nil && !job.deleted {
			if s.waitingOnWidth(a) {
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
		if jobs[i].first != jobs[j].first {
			return jobs[i].first
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
		if u, found := s.pickSized(job.urls, images.MaxImageBytes, a.URL); found == sizedFound {
			newURL = u
		} else {
			incomplete = true
		}
	}
	if job.thumbs != nil {
		if u, found := s.pickSized(job.thumbs, images.MaxThumbnailBytes, a.Thumbnail); found == sizedFound {
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

// sizedFind is what pickSized found for one field.
type sizedFind int

const (
	// sizedFound: a sized copy was confirmed.
	sizedFound sizedFind = iota
	// sizedUnconfirmed: a copy did not load, or the comparison with the
	// current file could not be made; worth another try later.
	sizedUnconfirmed
	// sizedNoLighter: every copy is an image over the cap, and none is
	// known to be lighter than the file the field holds.
	sizedNoLighter
)

// pickSized returns the URL to store in a field that holds current, trying
// candidates best first.
//
// The first candidate that is an image within limit wins. A candidate that is
// too heavy moves on to the next, narrower one; one that does not load at all
// ends the search, since a narrower copy of the same file on the same host
// would not fare better this pass.
//
// If every candidate is over the cap, the lightest one whose size is known
// exactly is taken as long as it is lighter than current: a 1.2 MB copy of a
// 13 MB original is still a tenth of the weight, and without it the row
// would keep the original and be retried every resizeBackoff for good. It is
// never swapped for something heavier.
func (s *Service) pickSized(candidates []string, limit int64, current string) (string, sizedFind) {
	lightest, lightestSize := "", int64(-1)
	for _, u := range candidates {
		fit, size := s.searcher.Weigh(u, limit)
		switch fit {
		case images.Fits:
			return u, sizedFound
		case images.TooHeavy:
			if size >= 0 && (lightestSize < 0 || size < lightestSize) {
				lightest, lightestSize = u, size
			}
		default:
			return "", sizedUnconfirmed
		}
	}
	if lightest == "" {
		return "", sizedNoLighter
	}
	cur := s.searcher.Probe(current, 0)
	switch {
	case cur.Status == 0:
		return "", sizedUnconfirmed // the request failed; compare next time
	case !cur.OK || cur.Size < 0 || lightestSize >= cur.Size:
		slog.Info("image resize: every sized copy is over its cap and none is lighter; keeping the current file",
			"url", current, "bytes", cur.Size, "lightest_copy", lightest, "lightest_bytes", lightestSize)
		return "", sizedNoLighter
	}
	slog.Info("image resize: every sized copy is over its cap; taking the lightest, still lighter than the current file",
		"url", lightest, "bytes", lightestSize, "limit", limit, "current_bytes", cur.Size)
	return lightest, sizedFound
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

// displayCandidates returns the sized URLs to try for a stored display image,
// best first, or nil when it stays as it is (for now).
//
// A Commons thumbnail at a width Wikimedia no longer serves -- the 800px
// renderings the image search once stored -- is sized from its original
// rather than from its own width: the widest step below 800 is 500, far too
// few pixels for the featured card and the article hero. That waits for the
// original's width; with it, the display image gets the 1280 or 960 step, or
// the original itself when it is no wider than the slot and no step comes
// close to its width.
func (s *Service) displayCandidates(u string) []string {
	if !images.IsUnservedWikimediaThumb(u) {
		return sizedSteps(u, s.originalWidths[u], displaySteps)
	}
	orig := displayWidthKey(u)
	w := s.originalWidths[orig]
	if w <= 0 {
		return nil // not looked up yet, or deleted (see resizeJob.deleted)
	}
	first, ok := images.Sized(orig, w, displaySteps[0])
	if !ok {
		first = orig
	}
	out := []string{first}
	for _, step := range displaySteps[1:] {
		if sized, ok := images.Sized(orig, w, step); ok && !slices.Contains(out, sized) {
			out = append(out, sized)
		}
	}
	return out
}

// displayWidthKey returns the Commons original whose width sizing the display
// image u needs, or "": u itself if it is an original, or the original behind
// a thumbnail at a width Wikimedia no longer serves.
func displayWidthKey(u string) string {
	if images.IsWikimediaOriginal(u) {
		return u
	}
	if images.IsUnservedWikimediaThumb(u) {
		orig, _ := images.WikimediaOriginalOf(u)
		return orig
	}
	return ""
}

// widthKeys returns the Commons originals whose widths sizing a needs. A
// thumbnail at an unserved width does not need one: its own width already
// bounds the original's from below, and the card slot is narrower still.
func widthKeys(a db.StoredArticleImage) []string {
	var keys []string
	if k := displayWidthKey(a.URL); k != "" {
		keys = append(keys, k)
	}
	if images.IsWikimediaOriginal(a.Thumbnail) && !slices.Contains(keys, a.Thumbnail) {
		keys = append(keys, a.Thumbnail)
	}
	return keys
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

// widthLookupsPerPass caps how many originals one pass looks up: at most one
// Commons request (images.Searcher asks for 20 titles per request), so the
// lookup cannot eat the resize budget however many originals are stored.
const widthLookupsPerPass = 20

// lookUpOriginalWidths fills s.originalWidths for Commons originals in stored
// it has not seen, newest articles (the homepage) first and at most
// widthLookupsPerPass a pass; the rest are looked up on later passes. Widths
// do not change, so each is asked for once per process; a failed lookup
// records nothing and is retried next pass. Nothing is asked once deadline
// has passed.
func (s *Service) lookUpOriginalWidths(stored []db.StoredArticleImage, deadline time.Time) {
	if s.originalWidths == nil {
		s.originalWidths = map[string]int{}
	}
	if time.Now().After(deadline) {
		return
	}
	newest := append([]db.StoredArticleImage(nil), stored...)
	sort.SliceStable(newest, func(i, j int) bool { return newest[i].ID > newest[j].ID })
	var unknown []string
	for _, a := range newest {
		for _, u := range widthKeys(a) {
			if _, known := s.originalWidths[u]; !known && !slices.Contains(unknown, u) {
				unknown = append(unknown, u)
			}
		}
	}
	if len(unknown) == 0 {
		return
	}
	if len(unknown) > widthLookupsPerPass {
		unknown = unknown[:widthLookupsPerPass]
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

// waitingOnWidth reports whether sizing a needs a Commons width that has not
// been looked up yet (or whose lookup failed).
func (s *Service) waitingOnWidth(a db.StoredArticleImage) bool {
	for _, k := range widthKeys(a) {
		if _, known := s.originalWidths[k]; !known {
			return true
		}
	}
	return false
}

// deletedOnCommons reports whether the Commons API said this original's file
// page does not exist.
func (s *Service) deletedOnCommons(orig string) bool {
	if orig == "" {
		return false
	}
	w, known := s.originalWidths[orig]
	return known && w == 0
}

// clearIfGone clears an article image whose Commons file was deleted, but only
// once the file itself is seen to answer 404 or 410 -- the API saying so is
// not enough on its own to take an image off a live article. For a thumbnail
// at a width Wikimedia no longer serves, which answers 400 whether or not the
// file exists, the original is asked instead.
func (s *Service) clearIfGone(a db.StoredArticleImage) bool {
	p := s.searcher.Probe(displayWidthKey(a.URL), 0)
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
