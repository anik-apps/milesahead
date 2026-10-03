package images

import (
	"net/http"
	"strings"
	"testing"
)

func TestIdentity_SameForEverySizeOfOnePicture(t *testing.T) {
	const wm = "https://upload.wikimedia.org/wikipedia/commons/"
	for name, urls := range map[string][]string{
		"wikimedia original and its steps": {
			wm + "6/6c/Lobby.jpg",
			wm + "thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg",
			wm + "thumb/6/6c/Lobby.jpg/500px-Lobby.jpg",
			"https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/Lobby.jpg/960px-Lobby.jpg",
			wm + "thumb/6/6c/Lobby.jpg/800px-Lobby.jpg", // a width no longer served
		},
		"flickr sizes, hosts and secrets": {
			"https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg",
			"https://live.staticflickr.com/65535/54420209608_0aa8ea537a.jpg",
			"https://farm66.staticflickr.com/65535/54420209608_0aa8ea537a_z.jpg",
		},
		"unsplash API sizes": {
			"https://images.unsplash.com/photo-1566073771259?ixid=abc&w=1080&fm=jpg&q=80",
			"https://images.unsplash.com/photo-1566073771259?ixid=abc&w=400&fm=jpg",
		},
		"pexels sizes": {
			"https://images.pexels.com/photos/258154/pexels-photo-258154.jpeg?auto=compress&cs=tinysrgb&h=650&w=940",
			"https://images.pexels.com/photos/258154/pexels-photo-258154.jpeg?auto=compress&cs=tinysrgb&h=350",
		},
		"other hosts, up to the fragment and the host's case": {
			"https://cdn.Example.com/a/b.jpg?v=1#top",
			"https://cdn.example.com/a/b.jpg?v=1",
		},
	} {
		want := Identity(urls[0])
		if want == "" {
			t.Errorf("%s: no identity for %q", name, urls[0])
			continue
		}
		for _, u := range urls[1:] {
			if got := Identity(u); got != want {
				t.Errorf("%s: Identity(%q) = %q, want %q", name, u, got, want)
			}
		}
	}
}

func TestIdentity_TellsPicturesApart(t *testing.T) {
	const wm = "https://upload.wikimedia.org/wikipedia/commons/"
	for _, pair := range [][2]string{
		{wm + "6/6c/Lobby.jpg", wm + "6/6d/Pool.jpg"},
		{"https://live.staticflickr.com/65535/111_aaa_b.jpg", "https://live.staticflickr.com/65535/222_aaa_b.jpg"},
		{"https://images.unsplash.com/photo-1?w=1080", "https://images.unsplash.com/photo-2?w=1080"},
		{"https://images.pexels.com/photos/1/a.jpeg", "https://images.pexels.com/photos/2/a.jpeg"},
		{"https://cdn.example.com/a.jpg?id=1", "https://cdn.example.com/a.jpg?id=2"},
	} {
		if Identity(pair[0]) == Identity(pair[1]) {
			t.Errorf("%q and %q share an identity", pair[0], pair[1])
		}
	}
	for _, u := range []string{"", "  ", "not a url", "/relative/path.jpg"} {
		if got := Identity(u); got != "" {
			t.Errorf("Identity(%q) = %q, want none", u, got)
		}
	}
}

func TestRejected_MatchesEitherURLOfACandidate(t *testing.T) {
	const wm = "https://upload.wikimedia.org/wikipedia/commons/"
	r := NewRejected([]string{wm + "thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg", "", "https://live.staticflickr.com/65535/9_ab_b.jpg"})
	for _, c := range []ImageCandidate{
		{URL: wm + "6/6c/Lobby.jpg", Thumbnail: "https://api.openverse.org/v1/images/x/thumb/"},
		{URL: "https://example.org/other.jpg", Thumbnail: wm + "thumb/6/6c/Lobby.jpg/500px-Lobby.jpg"},
		{URL: "https://live.staticflickr.com/65535/9_ab_c.jpg", Thumbnail: "https://live.staticflickr.com/65535/9_ab.jpg"},
	} {
		if !r.Has(c) {
			t.Errorf("not recognised as turned down: %+v", c)
		}
	}
	if r.Has(ImageCandidate{URL: wm + "6/6d/Pool.jpg", Thumbnail: ""}) {
		t.Error("another picture was taken for a turned-down one")
	}
	if (Rejected(nil)).Has(ImageCandidate{URL: "https://a/b.jpg"}) {
		t.Error("an empty set has something in it")
	}
}

// Integration: the search drops turned-down pictures before its caps, so
// they neither come back nor take a slot from a picture still on offer.
func TestSearchExcluding_DropsTurnedDownPicturesBeforeTheCap(t *testing.T) {
	const wm = "https://upload.wikimedia.org/wikipedia/commons/"
	var queries []string
	s := stubSearcher(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/images") {
			queries = append(queries, r.URL.Query().Get("q"))
			// Openverse: four Wikimedia files, the first two turned down
			// (at another size than the one stored).
			w.Write([]byte(`{"results":[
				{"title":"Old lobby","url":"` + wm + `6/6c/Lobby.jpg","creator":"a","source":"wikimedia","width":6048},
				{"title":"Old lobby, cropped","url":"` + wm + `thumb/6/6c/Lobby.jpg/960px-Lobby.jpg","creator":"a","source":"wikimedia"},
				{"title":"Pool","url":"` + wm + `6/6d/Pool.jpg","creator":"b","source":"wikimedia","width":4000},
				{"title":"Bar","url":"` + wm + `6/6e/Bar.jpg","creator":"c","source":"wikimedia","width":4000},
				{"title":"Spa","url":"` + wm + `6/6f/Spa.jpg","creator":"d","source":"wikimedia","width":4000}]}`))
			return
		}
		w.Write([]byte(`{}`)) // Commons search: nothing
	})
	rejected := NewRejected([]string{wm + "thumb/6/6c/Lobby.jpg/1280px-Lobby.jpg"})
	got, err := s.SearchExcluding([]string{"Hyatt Regency lobby"}, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 1 || queries[0] != "Hyatt Regency lobby" {
		t.Errorf("searched for %q, want the admin's words", queries)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want the 3 not turned down (the cap is 3 a source): %+v", len(got), got)
	}
	for _, c := range got {
		if strings.Contains(c.URL, "Lobby.jpg") || strings.Contains(c.Thumbnail, "Lobby.jpg") {
			t.Errorf("turned-down picture came back: %+v", c)
		}
	}
	// Search is SearchExcluding with nothing turned down.
	queries = nil
	if all, _ := s.Search([]string{"q"}); len(all) != 3 || !strings.Contains(all[0].URL, "Lobby.jpg") {
		t.Errorf("Search without a rejected set: %+v", all)
	}
}
