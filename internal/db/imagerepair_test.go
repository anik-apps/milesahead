package db

import "testing"

// The homepage renders image_thumbnail, so these two rot patterns were
// directly visible as broken images: Openverse's proxy 424s intermittently,
// and Pixabay's signed hotlinks 400 once they expire.
func TestRepairRottedImages(t *testing.T) {
	d := openTestDB(t)

	openverse := makeArticle(t, d, "openverse")
	setFields(t, d, openverse,
		"image_url = 'https://live.staticflickr.com/1/2_b.jpg', "+
			"image_thumbnail = 'https://api.openverse.org/v1/images/abc/thumb/', "+
			"image_attribution = 'Photo by X via Openverse (flickr)'")

	// No direct URL to promote, so the proxy has to stay: clearing it would
	// lose the only image the article has.
	orphan := makeArticle(t, d, "openverse-orphan")
	setFields(t, d, orphan,
		"image_url = '', image_thumbnail = 'https://api.openverse.org/v1/images/def/thumb/'")

	pixabay := makeArticle(t, d, "pixabay")
	setFields(t, d, pixabay,
		"image_url = 'https://pixabay.com/get/gdead_640.jpg', "+
			"image_thumbnail = 'https://pixabay.com/get/gdead_640.jpg', "+
			"image_attribution = 'Photo by Y on Pixabay', enrichment_attempts = 3")

	healthy := makeArticle(t, d, "healthy")
	setFields(t, d, healthy,
		"image_url = 'https://images.pexels.com/photos/1/full.jpg', "+
			"image_thumbnail = 'https://images.pexels.com/photos/1/thumb.jpg', "+
			"enrichment_attempts = 2")

	// Idempotence matters: this runs on every startup.
	for pass := 1; pass <= 2; pass++ {
		if err := repairRottedImages(d.DB()); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	get := func(id int64) (url, thumb, attr string, attempts int) {
		t.Helper()
		err := d.DB().QueryRow(
			`SELECT image_url, image_thumbnail, image_attribution, enrichment_attempts
			 FROM generated_articles WHERE id = ?`, id).Scan(&url, &thumb, &attr, &attempts)
		if err != nil {
			t.Fatalf("read %d: %v", id, err)
		}
		return
	}

	if url, thumb, _, _ := get(openverse); thumb != url || thumb != "https://live.staticflickr.com/1/2_b.jpg" {
		t.Errorf("openverse thumbnail = %q, want the promoted direct URL", thumb)
	}
	if _, thumb, _, _ := get(orphan); thumb != "https://api.openverse.org/v1/images/def/thumb/" {
		t.Errorf("orphan thumbnail = %q, want it left alone", thumb)
	}

	url, thumb, attr, attempts := get(pixabay)
	if url != "" || thumb != "" || attr != "" {
		t.Errorf("pixabay row not cleared: url=%q thumb=%q attr=%q", url, thumb, attr)
	}
	if attempts != 0 {
		t.Errorf("pixabay enrichment_attempts = %d, want 0 so the reconciler retries", attempts)
	}

	url, thumb, _, attempts = get(healthy)
	if url != "https://images.pexels.com/photos/1/full.jpg" ||
		thumb != "https://images.pexels.com/photos/1/thumb.jpg" || attempts != 2 {
		t.Errorf("healthy row was modified: url=%q thumb=%q attempts=%d", url, thumb, attempts)
	}
}
