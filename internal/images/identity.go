package images

import (
	"net/url"
	"strings"
)

// Identity names the picture behind rawURL, the same for every size the
// host serves it at, so a picture an editor turned down is recognised again
// when the search comes back with it at another width:
//
//   - a Wikimedia file and each of its thumbnails;
//   - every size of one Flickr photo, which share the id before the secret
//     (the larger sizes carry a different secret);
//   - Unsplash's sizes of one photo, which differ only in their query;
//   - Pexels' sizes of one photo, which share the id in the path.
//
// Any other URL is its own identity, less a fragment, with the host in lower
// case. An empty or unparseable URL gives "".
func Identity(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if f, ok := parseWikimedia(rawURL); ok {
		return "wikimedia:" + f.project + "/" + f.hash + "/" + f.name
	}
	if f, ok := parseFlickr(rawURL); ok {
		id := f.prefix[strings.LastIndex(f.prefix, "/")+1:]
		id, _, _ = strings.Cut(id, "_")
		return "flickr:" + id
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case host == "images.unsplash.com" || host == "plus.unsplash.com":
		return "unsplash:" + strings.TrimSuffix(u.EscapedPath(), "/")
	case host == "images.pexels.com":
		// /photos/<id>/pexels-photo-<id>.jpeg?auto=compress&h=650
		seg := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		if len(seg) >= 2 && seg[0] == "photos" && seg[1] != "" {
			return "pexels:" + seg[1]
		}
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = host
	u.Scheme = strings.ToLower(u.Scheme)
	return u.String()
}

// Rejected is the set of pictures an editor has turned down for an article,
// by Identity.
type Rejected map[string]bool

// NewRejected builds the set from the URLs that were turned down: each
// display image and thumbnail the article had when a new one was asked for.
func NewRejected(urls []string) Rejected {
	r := Rejected{}
	for _, u := range urls {
		if id := Identity(u); id != "" {
			r[id] = true
		}
	}
	return r
}

// Has reports whether c is, at any size, a picture in the set.
func (r Rejected) Has(c ImageCandidate) bool {
	if len(r) == 0 {
		return false
	}
	return r[Identity(c.URL)] || r[Identity(c.Thumbnail)]
}
