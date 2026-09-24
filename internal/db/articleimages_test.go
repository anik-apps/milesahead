package db

import "testing"

func TestListStoredArticleImages(t *testing.T) {
	d := openTestDB(t)

	withImage := makeArticle(t, d, "with-image")
	setFields(t, d, withImage, "image_url = 'https://a/full.jpg', image_thumbnail = 'https://a/thumb.jpg'")
	thumbOnly := makeArticle(t, d, "thumb-only")
	setFields(t, d, thumbOnly, "image_url = '', image_thumbnail = 'https://a/proxy/'")
	makeArticle(t, d, "no-image")
	rejected := makeArticle(t, d, "rejected")
	setFields(t, d, rejected, "image_url = 'https://a/r.jpg', image_thumbnail = 'https://a/r.jpg', status = ?", StatusRejected)

	got, err := d.ListStoredArticleImages()
	if err != nil {
		t.Fatalf("ListStoredArticleImages: %v", err)
	}
	want := []StoredArticleImage{
		{ID: withImage, URL: "https://a/full.jpg", Thumbnail: "https://a/thumb.jpg"},
		{ID: thumbOnly, URL: "", Thumbnail: "https://a/proxy/"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The swap is compare-and-set: a row edited since it was read -- an admin
// changing the image, or enrichment picking a new one -- is left alone.
func TestReplaceArticleImage_OnlyIfUnchanged(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "resize")
	setFields(t, d, id, "image_url = 'https://a/orig.jpg', image_thumbnail = 'https://a/orig.jpg', "+
		"image_attribution = 'Photo by X', enrichment_attempts = 2")
	read := StoredArticleImage{ID: id, URL: "https://a/orig.jpg", Thumbnail: "https://a/orig.jpg"}

	stale := read
	stale.Thumbnail = "https://a/something-else.jpg"
	if ok, err := d.ReplaceArticleImage(stale, "https://a/1280.jpg", "https://a/500.jpg"); err != nil || ok {
		t.Fatalf("stale replace = %v, %v; want no write", ok, err)
	}

	if ok, err := d.ReplaceArticleImage(read, "https://a/1280.jpg", "https://a/500.jpg"); err != nil || !ok {
		t.Fatalf("replace = %v, %v; want a write", ok, err)
	}
	var url, thumb, attr string
	var attempts int
	if err := d.DB().QueryRow(`SELECT image_url, image_thumbnail, image_attribution, enrichment_attempts
		FROM generated_articles WHERE id = ?`, id).Scan(&url, &thumb, &attr, &attempts); err != nil {
		t.Fatal(err)
	}
	if url != "https://a/1280.jpg" || thumb != "https://a/500.jpg" {
		t.Errorf("image = %q / %q, want the sized pair", url, thumb)
	}
	// Same picture, so the credit stays; and this is not an enrichment attempt.
	if attr != "Photo by X" || attempts != 2 {
		t.Errorf("attribution %q, attempts %d; want both untouched", attr, attempts)
	}
}

func TestClearArticleImage_HandsBackToEnrichment(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "deleted-file")
	setFields(t, d, id, "image_url = 'https://a/gone.jpg', image_thumbnail = 'https://a/gone.jpg', "+
		"image_attribution = 'Wikimedia Commons', enrichment_attempts = 3")

	ok, err := d.ClearArticleImage(StoredArticleImage{ID: id, URL: "https://a/gone.jpg", Thumbnail: "https://a/gone.jpg"})
	if err != nil || !ok {
		t.Fatalf("clear = %v, %v", ok, err)
	}
	var url, thumb, attr string
	var attempts int
	if err := d.DB().QueryRow(`SELECT image_url, image_thumbnail, image_attribution, enrichment_attempts
		FROM generated_articles WHERE id = ?`, id).Scan(&url, &thumb, &attr, &attempts); err != nil {
		t.Fatal(err)
	}
	if url != "" || thumb != "" || attr != "" || attempts != 0 {
		t.Errorf("after clear: %q %q %q attempts=%d; want empty and 0", url, thumb, attr, attempts)
	}

	// The article is now eligible for a new image.
	need, err := d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(need) != 1 || need[0].ID != id || !need[0].MissingImage {
		t.Errorf("ListArticlesNeedingEnrichment = %+v, want the cleared article missing its image", need)
	}
}
