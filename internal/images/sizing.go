package images

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The widths article images are stored at. Openverse hands back the provider's
// full-size original, which for Wikimedia Commons can be a 6000px, 13 MB
// camera file; stored as-is it was 96% of the homepage's 16.5 MB. Both numbers
// are Wikimedia standard thumbnail steps, so they can be requested from
// upload.wikimedia.org unchanged.
const (
	// ThumbnailWidth is for the homepage cards, which show images at up to
	// ~360 CSS px wide.
	ThumbnailWidth = 500
	// HeroWidth is for the featured card, the article hero and og:image.
	HeroWidth = 1280
)

// wikimediaSteps are the only thumbnail widths upload.wikimedia.org serves to
// hotlinkers (https://w.wiki/GHai, phab T414805). Verified live on 2026-09-23:
//
//   - any other width answers 400 "Use thumbnail sizes listed on ..."
//   - a step wider than the original is NOT refused: 1280px of a 1024px
//     original came back as a 1280x760 upscale, bigger and blurrier than the
//     original, so the width check has to happen on our side
//   - a step not yet rendered can answer 429 with Retry-After: 1 for a few
//     seconds before it succeeds
var wikimediaSteps = []int{20, 40, 60, 120, 250, 330, 500, 960, 1280, 1920, 3840}

// Sized returns a URL for the same picture at most target pixels wide, for
// hosts whose URL scheme lets us ask for one: upload.wikimedia.org and Flickr.
//
// originalWidth is the pixel width of the full-size image when the caller
// knows it (Openverse reports it), else 0. It is needed only for a Wikimedia
// original, where the URL alone does not say how wide the file is.
//
// Wikimedia URLs only ever get a standard step, never one wider than the
// original. An original no wider than target still moves to the step at or
// just below its width -- the same pixels, re-encoded at a fraction of the
// bytes -- unless that step is more than a quarter narrower.
//
// ok is false when rawURL should be kept as it is: an unrecognised host or
// format, an image that already fits, or a Wikimedia original of unknown
// width -- asking for a step then could silently upscale it.
func Sized(rawURL string, originalWidth, target int) (sized string, ok bool) {
	if f, isWiki := parseWikimedia(rawURL); isWiki {
		return f.sized(originalWidth, target)
	}
	if f, isFlickr := parseFlickr(rawURL); isFlickr {
		return f.sized(target)
	}
	return "", false
}

// IsWikimediaOriginal reports whether rawURL is a full-size Commons file, the
// one case where Sized needs to be told the image's width.
func IsWikimediaOriginal(rawURL string) bool {
	f, ok := parseWikimedia(rawURL)
	return ok && f.width == 0 && f.project == "wikipedia/commons"
}

// sizedPair returns the display and thumbnail URLs for one picture, each
// downsized where the host allows it.
func sizedPair(rawURL string, originalWidth int) (display, thumb string) {
	display, thumb = rawURL, rawURL
	if u, ok := Sized(rawURL, originalWidth, HeroWidth); ok {
		display = u
	}
	if u, ok := Sized(rawURL, originalWidth, ThumbnailWidth); ok {
		thumb = u
	}
	return display, thumb
}

// wikimediaFile is an upload.wikimedia.org image URL taken apart:
//
//	original: /wikipedia/commons/6/6c/Name.jpg
//	thumb:    /wikipedia/commons/thumb/6/6c/Name.jpg/500px-Name.jpg
type wikimediaFile struct {
	host    string // "upload.wikimedia.org", or "thumb.wikimedia.org" for a thumbnail
	project string // "wikipedia/commons"
	hash    string // "6/6c"
	name    string // file name, percent-encoded exactly as in the URL
	width   int    // thumbnail width in the URL; 0 for the original
}

// wikimediaRaster lists the formats whose thumbnails keep the original's file
// name and type. Others are rendered to a different name (SVG to .svg.png,
// TIFF and PDF to lossy-page1-...), so they are left alone rather than guessed.
var wikimediaRaster = map[string]bool{".jpg": true, ".jpeg": true, ".png": true}

func parseWikimedia(raw string) (wikimediaFile, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return wikimediaFile{}, false
	}
	// The Commons API has started returning thumbnails on thumb.wikimedia.org;
	// it serves the same files as upload.wikimedia.org/.../thumb/ but only
	// redirects originals.
	if u.Host != "upload.wikimedia.org" && u.Host != "thumb.wikimedia.org" {
		return wikimediaFile{}, false
	}
	seg := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(seg) < 5 {
		return wikimediaFile{}, false
	}
	f := wikimediaFile{host: u.Host, project: seg[0] + "/" + seg[1]}
	rest := seg[2:]
	isThumb := rest[0] == "thumb"
	if isThumb {
		rest = rest[1:]
	} else if u.Host != "upload.wikimedia.org" {
		return wikimediaFile{}, false
	}
	want := 3
	if isThumb {
		want = 4
	}
	if len(rest) != want || !isHashDirs(rest[0], rest[1]) || rest[2] == "" {
		return wikimediaFile{}, false
	}
	f.hash, f.name = rest[0]+"/"+rest[1], rest[2]
	if !wikimediaRaster[strings.ToLower(extension(f.name))] {
		return wikimediaFile{}, false
	}
	if isThumb {
		prefix, name, found := strings.Cut(rest[3], "px-")
		w, err := strconv.Atoi(prefix)
		if !found || err != nil || w <= 0 || name != f.name {
			return wikimediaFile{}, false
		}
		f.width = w
	}
	return f, true
}

// isHashDirs checks the two hash directories MediaWiki files live under: the
// first hex digit of the name's MD5, then the first two.
func isHashDirs(a, ab string) bool {
	return len(a) == 1 && len(ab) == 2 && ab[0] == a[0] && isHex(ab)
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func extension(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

func (f wikimediaFile) thumbURL(width int) string {
	return fmt.Sprintf("https://%s/%s/thumb/%s/%s/%dpx-%s",
		f.host, f.project, f.hash, f.name, width, f.name)
}

func (f wikimediaFile) sized(originalWidth, target int) (string, bool) {
	if f.width > 0 {
		// Already a thumbnail. Keep it if it is a step that fits the slot;
		// otherwise move to the widest step that does. Any width that is not
		// a step is refused outright, so this also repairs one.
		if f.width <= target && isWikimediaStep(f.width) {
			return "", false
		}
		if step := widestStepAtMost(min(f.width, target)); step > 0 {
			return f.thumbURL(step), true
		}
		return "", false
	}
	if originalWidth <= 0 {
		return "", false // an original of unknown width: any step could upscale it
	}
	// A step no wider than the original is never an upscale; one equal to it
	// comes back re-encoded (1280px of a 1280px, 585 KB original: 216 KB).
	step := widestStepAtMost(min(originalWidth, target))
	if step == 0 || (originalWidth <= target && step*4 < originalWidth*3) {
		// Either nothing fits below the original, or the nearest step would
		// cost more than a quarter of its width and leave the slot short of
		// pixels. An original no wider than the slot is not a heavy file, so
		// it is kept.
		return "", false
	}
	return f.thumbURL(step), true
}

func isWikimediaStep(w int) bool {
	for _, s := range wikimediaSteps {
		if s == w {
			return true
		}
	}
	return false
}

// widestStepAtMost returns the largest standard step no wider than limit, or 0.
func widestStepAtMost(limit int) int {
	best := 0
	for _, s := range wikimediaSteps {
		if s <= limit {
			best = s
		}
	}
	return best
}

// flickrFile is a Flickr static image URL taken apart:
// https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg
type flickrFile struct {
	prefix string // everything up to and including "<id>_<secret>"
	suffix string // "_b", "" (500px), ...
	ext    string
}

// flickrSizes are the size suffixes that share a photo's main secret, with the
// length of the longest side each is served at. The larger _h, _k and _o use
// separate secrets, so no URL for them can be derived; wrong guesses answer
// 410 (verified 2026-09-23).
var flickrSizes = map[string]int{
	"_s": 75, "_q": 150, "_t": 100, "_m": 240, "_n": 320, "_w": 400,
	"": 500, "_z": 640, "_c": 800, "_b": 1024,
}

// flickrLadder is flickrSizes' downsizing order, largest first.
var flickrLadder = []string{"_b", "_c", "_z", "", "_w", "_n", "_m"}

var flickrURL = regexp.MustCompile(`^(https?://(?:live|farm\d+)\.staticflickr\.com/(?:\d+/)+\d+_[0-9a-f]+)(_[a-z])?(\.(?:jpg|jpeg|png))$`)

func parseFlickr(raw string) (flickrFile, bool) {
	m := flickrURL.FindStringSubmatch(raw)
	if m == nil {
		return flickrFile{}, false
	}
	if _, known := flickrSizes[m[2]]; !known {
		return flickrFile{}, false
	}
	return flickrFile{prefix: m[1], suffix: m[2], ext: m[3]}, true
}

func (f flickrFile) sized(target int) (string, bool) {
	if flickrSizes[f.suffix] <= target {
		return "", false
	}
	for _, s := range flickrLadder {
		if flickrSizes[s] <= target {
			return f.prefix + s + f.ext, true
		}
	}
	return "", false
}
