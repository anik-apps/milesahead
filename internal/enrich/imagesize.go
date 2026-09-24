package enrich

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/images"
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
//     is kept as it is.
//   - Flickr thumbnails larger than 500 px use Flickr's 500 px size.
//   - Every new URL is fetched first and must answer 200/206 with an image
//     content type, within the slot's byte cap. Any field that cannot be
//     confirmed keeps its current URL and is retried next pass; a row is never
//     pointed at an image that has not just been seen to load.
//   - A Wikimedia file Commons reports as deleted, whose URL also answers 404,
//     is already a broken image. It is cleared and handed back to enrichment
//     to pick a new one, as repairRottedImages does for expired Pixabay links.
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

	var resized, unconfirmed, cleared int
	probed := false
	for _, a := range stored {
		url, urlSized := images.Sized(a.URL, s.originalWidths[a.URL], images.HeroWidth)
		thumb, thumbSized := images.Sized(a.Thumbnail, s.originalWidths[a.Thumbnail], images.ThumbnailWidth)
		deleted := s.deletedOnCommons(a.URL)
		if !urlSized && !thumbSized && !deleted {
			continue
		}
		if time.Now().After(deadline) {
			slog.Warn("image resize: pass deadline reached; the rest wait for the next pass")
			break
		}
		// Space out rows that need the network. Wikimedia throttles clients
		// asking for thumbnails it has not rendered yet.
		if probed && s.Pace > 0 {
			time.Sleep(s.Pace)
		}
		probed = true

		if deleted {
			if s.clearIfGone(a) {
				cleared++
			}
			continue
		}

		newURL, newThumb := a.URL, a.Thumbnail
		if urlSized && s.confirmed(url, images.MaxImageBytes) {
			newURL = url
		}
		if thumbSized && s.confirmed(thumb, images.MaxThumbnailBytes) {
			newThumb = thumb
		}
		if (urlSized && newURL == a.URL) || (thumbSized && newThumb == a.Thumbnail) {
			unconfirmed++
		}
		if newURL == a.URL && newThumb == a.Thumbnail {
			continue
		}
		ok, err := s.db.ReplaceArticleImage(a, newURL, newThumb)
		if err != nil {
			slog.Warn("image resize: could not save", "article_id", a.ID, "error", err)
			continue
		}
		if !ok {
			// The row changed under us; next pass will look at it afresh.
			continue
		}
		resized++
		slog.Info("image resize: article image replaced with a sized copy", "article_id", a.ID,
			"image_url_from", a.URL, "image_url_to", newURL,
			"thumbnail_from", a.Thumbnail, "thumbnail_to", newThumb)
	}
	if resized+unconfirmed+cleared > 0 {
		slog.Info("image resize pass complete", "resized", resized,
			"left_unchanged_unconfirmed", unconfirmed, "cleared_deleted", cleared)
	}
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
			if _, known := s.originalWidths[u]; !known && images.IsWikimediaOriginal(u) {
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

// confirmed fetches a rewritten URL and accepts it only if a browser would get
// an image from it, no heavier than the slot allows.
func (s *Service) confirmed(u string, limit int64) bool {
	p := s.searcher.Probe(u, limit)
	switch {
	case !p.OK:
		return false
	case !p.IsImage():
		slog.Warn("image resize: sized URL did not return an image",
			"url", u, "content_type", p.ContentType)
		return false
	case p.Size > limit:
		slog.Warn("image resize: sized URL is still over its byte cap",
			"url", u, "bytes", p.Size, "limit", limit)
		return false
	}
	return true
}
