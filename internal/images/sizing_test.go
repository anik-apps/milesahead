package images

import "testing"

// The URLs below are production's own. Every expected output was fetched live
// on 2026-09-23 and answered 200 image/jpeg (see the commit message); the
// shapes that fail -- non-standard widths, upscales -- are the reason for the
// rules these cases pin down.
const (
	lobby       = "https://upload.wikimedia.org/wikipedia/commons/6/6c/DZ6_0471_Luxurious_hotel_lobby_with_a_carved_stone_fountain_centerpiece_polished_wood_reception_desk_and_warm_ambient_lighting.jpg"
	lobbyName   = "DZ6_0471_Luxurious_hotel_lobby_with_a_carved_stone_fountain_centerpiece_polished_wood_reception_desk_and_warm_ambient_lighting.jpg"
	lobbyThumbs = "https://upload.wikimedia.org/wikipedia/commons/thumb/6/6c/" + lobbyName + "/"
	evenHotel   = "https://upload.wikimedia.org/wikipedia/commons/0/0e/Even_Hotel_Brooklyn%2C_an_IHG_Hotel_2023_jeh.jpg"
	delta       = "https://upload.wikimedia.org/wikipedia/commons/0/02/Delta_Airlines_aircraft_over_Eastfield_from_the_air_-_geograph.org.uk_-_6039028.jpg"
	deltaName   = "Delta_Airlines_aircraft_over_Eastfield_from_the_air_-_geograph.org.uk_-_6039028.jpg"
	brisbane960 = "https://upload.wikimedia.org/wikipedia/commons/thumb/f/fb/Flight_departure_board_at_Brisbane_Airport%2C_December_2022.jpg/960px-Flight_departure_board_at_Brisbane_Airport%2C_December_2022.jpg"
	flickrLarge = "https://live.staticflickr.com/65535/54420209608_0aa8ea537a_b.jpg"
	// 161 bytes: MediaWiki names this file's thumbnails "<width>px-thumbnail.jpg".
	longName   = "DZ6_0956_A_glowing_blue_nighttime_scene_of_an_outdoor_bar_or_lounge_decorated_with_carnival_masks_and_illuminated_tents_creating_a_festive_surreal_atmosphere.jpg"
	longThumbs = "https://upload.wikimedia.org/wikipedia/commons/thumb/4/4e/" + longName + "/"
)

func TestSized(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		originalWidth int
		target        int
		want          string // "" means: keep the URL as it is
	}{
		// The 13.2 MB, 6048px lobby photo that was 80% of the homepage.
		{"huge original, hero", lobby, 6048, HeroWidth, lobbyThumbs + "1280px-" + lobbyName},
		{"huge original, thumbnail", lobby, 6048, ThumbnailWidth, lobbyThumbs + "500px-" + lobbyName},
		// Percent-encoding in the name is carried over untouched.
		{"encoded name", evenHotel, 3000, ThumbnailWidth,
			"https://upload.wikimedia.org/wikipedia/commons/thumb/0/0e/Even_Hotel_Brooklyn%2C_an_IHG_Hotel_2023_jeh.jpg/500px-Even_Hotel_Brooklyn%2C_an_IHG_Hotel_2023_jeh.jpg"},
		// Without the width nothing is safe to ask for: Wikimedia would
		// upscale rather than refuse.
		{"original of unknown width", lobby, 0, ThumbnailWidth, ""},
		// A 1024px original is narrower than the hero slot. 1280px would be an
		// upscale; 960px is the same picture at under half the bytes.
		{"original narrower than slot, close step", delta, 1024, HeroWidth,
			"https://upload.wikimedia.org/wikipedia/commons/thumb/0/02/" + deltaName + "/960px-" + deltaName},
		// 900px: the nearest step is 500, far too few pixels for a hero.
		{"original narrower than slot, distant step", delta, 900, HeroWidth, ""},
		// A step equal to the original's width is served, re-encoded.
		{"original exactly a step wide", delta, 1280, HeroWidth,
			"https://upload.wikimedia.org/wikipedia/commons/thumb/0/02/" + deltaName + "/1280px-" + deltaName},
		{"tiny original", delta, 15, ThumbnailWidth, ""},
		// Already a thumbnail: fine as a hero, too big for a card.
		{"960 thumb as hero", brisbane960, 0, HeroWidth, ""},
		{"960 thumb as thumbnail", brisbane960, 0, ThumbnailWidth,
			"https://upload.wikimedia.org/wikipedia/commons/thumb/f/fb/Flight_departure_board_at_Brisbane_Airport%2C_December_2022.jpg/500px-Flight_departure_board_at_Brisbane_Airport%2C_December_2022.jpg"},
		// 800px is not a step, so upload.wikimedia.org answers 400; move to
		// the widest step below it rather than keep a broken URL.
		{"non-step thumb", lobbyThumbs + "800px-" + lobbyName, 0, HeroWidth, lobbyThumbs + "500px-" + lobbyName},
		// A file name over 160 bytes is not repeated in its thumbnails' names.
		// The old image search stored these at 800px, which answers 400.
		{"long name, unserved width", longThumbs + "800px-thumbnail.jpg", 0, ThumbnailWidth, longThumbs + "500px-" + longName},
		{"long name, served width", longThumbs + "500px-thumbnail.jpg", 0, ThumbnailWidth, ""},
		{"thumbnail of another file", lobbyThumbs + "800px-Other.jpg", 0, ThumbnailWidth, ""},
		// The Commons API now hands out thumb.wikimedia.org; keep its host.
		{"thumb host", "https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/" + lobbyName + "/960px-" + lobbyName + "?utm_source=commons.wikimedia.org", 0, ThumbnailWidth,
			"https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/" + lobbyName + "/500px-" + lobbyName},
		// Formats whose thumbnails are renamed are not guessed at.
		{"svg", "https://upload.wikimedia.org/wikipedia/commons/a/ab/Map.svg", 4000, ThumbnailWidth, ""},
		{"tiff", "https://upload.wikimedia.org/wikipedia/commons/9/96/Scan.tiff", 4000, ThumbnailWidth, ""},
		{"uppercase JPG", "https://upload.wikimedia.org/wikipedia/commons/b/b7/Tallinn_airplane_hangars2.JPG", 2048, ThumbnailWidth,
			"https://upload.wikimedia.org/wikipedia/commons/thumb/b/b7/Tallinn_airplane_hangars2.JPG/500px-Tallinn_airplane_hangars2.JPG"},
		{"not a hash path", "https://upload.wikimedia.org/wikipedia/commons/zz/6c/X.jpg", 4000, ThumbnailWidth, ""},

		// Flickr: _b is 1024px on its longest side, fine for the hero; the
		// 500px size shares the secret and has no suffix.
		{"flickr _b as hero", flickrLarge, 1024, HeroWidth, ""},
		{"flickr _b as thumbnail", flickrLarge, 1024, ThumbnailWidth, "https://live.staticflickr.com/65535/54420209608_0aa8ea537a.jpg"},
		{"flickr _z as thumbnail", "https://live.staticflickr.com/1/2_abc_z.jpg", 0, ThumbnailWidth, "https://live.staticflickr.com/1/2_abc.jpg"},
		{"flickr 500 already", "https://live.staticflickr.com/1/2_abc.jpg", 500, ThumbnailWidth, ""},
		{"flickr farm host", "https://farm4.staticflickr.com/3050/2991549037_ba321fa581_b.jpg", 0, ThumbnailWidth, "https://farm4.staticflickr.com/3050/2991549037_ba321fa581.jpg"},
		// _h/_k/_o carry their own secrets, so smaller sizes cannot be derived.
		{"flickr _k", "https://live.staticflickr.com/1/2_abc_k.jpg", 0, ThumbnailWidth, ""},

		{"other host", "https://images.unsplash.com/photo-1?w=1080", 1080, ThumbnailWidth, ""},
		{"empty", "", 0, ThumbnailWidth, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Sized(tt.url, tt.originalWidth, tt.target)
			if tt.want == "" {
				if ok {
					t.Fatalf("Sized = %q, want the URL kept as it is", got)
				}
				return
			}
			if !ok || got != tt.want {
				t.Fatalf("Sized = %q, %v\n want %q", got, ok, tt.want)
			}
		})
	}
}

// A rewrite must be a fixed point, or the startup pass would keep rewriting
// the same rows forever.
func TestSized_IsIdempotent(t *testing.T) {
	for _, target := range []int{ThumbnailWidth, HeroWidth} {
		for _, u := range []string{lobby, delta, flickrLarge} {
			first, ok := Sized(u, 6048, target)
			if !ok {
				continue
			}
			if again, changed := Sized(first, 0, target); changed {
				t.Errorf("Sized(%q) = %q, then %q again", u, first, again)
			}
		}
	}
}

// The old image search stored 800px Commons renderings, a width Wikimedia no
// longer serves. The resize sizes their display image from the original, so
// it needs the original's URL (for the width lookup) from either name form.
func TestUnservedWikimediaThumbs(t *testing.T) {
	for _, tt := range []struct {
		url      string
		unserved bool
		original string
	}{
		{lobbyThumbs + "800px-" + lobbyName, true, lobby},
		{longThumbs + "800px-thumbnail.jpg", true, "https://upload.wikimedia.org/wikipedia/commons/4/4e/" + longName},
		{"https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/" + lobbyName + "/800px-" + lobbyName, true, lobby},
		{lobbyThumbs + "500px-" + lobbyName, false, lobby},
		{brisbane960, false, "https://upload.wikimedia.org/wikipedia/commons/f/fb/Flight_departure_board_at_Brisbane_Airport%2C_December_2022.jpg"},
		{lobby, false, ""},
		{"https://upload.wikimedia.org/wikipedia/en/thumb/a/ab/Local.jpg/800px-Local.jpg", false, ""}, // not on Commons
		{flickrLarge, false, ""},
	} {
		if got := IsUnservedWikimediaThumb(tt.url); got != tt.unserved {
			t.Errorf("IsUnservedWikimediaThumb(%q) = %v, want %v", tt.url, got, tt.unserved)
		}
		got, ok := WikimediaOriginalOf(tt.url)
		if got != tt.original || ok != (tt.original != "") {
			t.Errorf("WikimediaOriginalOf(%q) = %q, %v; want %q", tt.url, got, ok, tt.original)
		}
	}
}

func TestIsWikimediaOriginal(t *testing.T) {
	for u, want := range map[string]bool{
		lobby:       true,
		brisbane960: false,
		flickrLarge: false,
		"https://upload.wikimedia.org/wikipedia/en/a/ab/Local.jpg": false, // not on Commons
	} {
		if got := IsWikimediaOriginal(u); got != want {
			t.Errorf("IsWikimediaOriginal(%q) = %v, want %v", u, got, want)
		}
	}
}

// ServedWidth is what a srcset may say about a stored URL: only widths the
// URL itself fixes, and only for URLs that serve one.
func TestServedWidth(t *testing.T) {
	for _, tt := range []struct {
		url   string
		width int // 0: unknown
	}{
		{lobbyThumbs + "500px-" + lobbyName, 500},
		{lobbyThumbs + "1280px-" + lobbyName, 1280},
		{brisbane960, 960},
		{"https://thumb.wikimedia.org/wikipedia/commons/thumb/6/6c/" + lobbyName + "/1280px-" + lobbyName, 1280},
		{longThumbs + "500px-thumbnail.jpg", 500},
		// 800px answers 400: not an image at all.
		{lobbyThumbs + "800px-" + lobbyName, 0},
		// An original's width is not in its URL.
		{lobby, 0},
		{flickrLarge, 1024},
		{"https://live.staticflickr.com/65535/54420209608_0aa8ea537a.jpg", 500},
		{"https://live.staticflickr.com/1/2_abc_k.jpg", 0},
		// Unsplash's API sizes: regular and small.
		{"https://images.unsplash.com/photo-1?crop=entropy&fit=max&fm=jpg&q=80&w=1080", 1080},
		{"https://images.unsplash.com/photo-1?ixlib=rb-4.1.0&q=80&w=400", 400},
		{"https://images.unsplash.com/photo-1?q=80", 0},
		// Pexels fixes the height, not the width.
		{"https://images.pexels.com/photos/1/pexels-photo-1.jpeg?auto=compress&cs=tinysrgb&h=350", 0},
		{"https://images.pexels.com/photos/1/pexels-photo-1.jpeg?auto=compress&cs=tinysrgb&h=650&w=940", 0},
		{"", 0},
	} {
		got, ok := ServedWidth(tt.url)
		if got != tt.width || ok != (tt.width > 0) {
			t.Errorf("ServedWidth(%q) = %d, %v; want %d", tt.url, got, ok, tt.width)
		}
	}
}
