package db

import (
	"errors"
	"strings"
	"testing"
)

const enriched = "scouts_take = 'take', key_details = '[{\"Label\":\"a\"}]', image_url = 'https://img.example/a-1280.jpg', image_thumbnail = 'https://img.example/a-500.jpg', image_attribution = 'by A', tags_json = '[\"deals\"]'"

func TestCleanImageHint(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"  Hyatt   Regency\n\tlobby  ": "Hyatt Regency lobby",
		strings.Repeat("ab ", 60):      strings.TrimSpace(strings.Repeat("ab ", 60)[:MaxImageHintRunes]),
	} {
		if got := CleanImageHint(in); got != want {
			t.Errorf("CleanImageHint(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CleanImageHint(strings.Repeat("é", 150)); len([]rune(got)) != MaxImageHintRunes {
		t.Errorf("cut %d runes, want %d", len([]rune(got)), MaxImageHintRunes)
	}
}

// A request puts a fully enriched article whose budget was spent back in the
// reconciler's list, first, with its words -- and keeps its image.
func TestRequestNewImage_QueuesFirstWithAFreshBudget(t *testing.T) {
	d := openTestDB(t)
	older := makeArticle(t, d, "older")
	setFields(t, d, older, enriched+", enrichment_attempts = 3, created_at = datetime('now', '-30 days')")
	newer := makeArticle(t, d, "newer") // missing everything

	got, _ := d.ListArticlesNeedingEnrichment(3, 10)
	if len(got) != 1 || got[0].ID != newer {
		t.Fatalf("before: %+v, want only the unenriched article", got)
	}

	req, err := d.RequestNewImage(older, "pool at night")
	if err != nil {
		t.Fatal(err)
	}
	if !req.Pending() || req.Hint != "pool at night" || req.Seq != 1 || req.RequestedAt.IsZero() {
		t.Errorf("request = %+v", req)
	}
	got, err = d.ListArticlesNeedingEnrichment(3, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("after: %+v (%v)", got, err)
	}
	a := got[0]
	if a.ID != older || !a.MissingImage || a.ImageRequestSeq != 1 || a.ImageHint != "pool at night" || a.Attempts != 0 {
		t.Errorf("first = %+v, want the requested article first, needing an image, budget reset", a)
	}
	if a.MissingScoutsTake || a.MissingKeyDetails || a.MissingTags {
		t.Errorf("only the image should be wanted, got %q", a.Missing())
	}
	if a.Missing() != "new_image" {
		t.Errorf("Missing() = %q", a.Missing())
	}
	var img string
	d.DB().QueryRow(`SELECT image_url FROM generated_articles WHERE id = ?`, older).Scan(&img)
	if img != "https://img.example/a-1280.jpg" {
		t.Errorf("image = %q, want it kept until replaced", img)
	}
	urls, _ := d.RejectedImageURLs(older)
	if len(urls) != 2 {
		t.Errorf("turned down %v, want the image and its thumbnail", urls)
	}

	// No image, nothing turned down.
	if _, err := d.RequestNewImage(newer, ""); err != nil {
		t.Fatal(err)
	}
	if urls, _ := d.RejectedImageURLs(newer); len(urls) != 0 {
		t.Errorf("an article with no image turned down %v", urls)
	}

	rejected := makeArticle(t, d, "rejected")
	setFields(t, d, rejected, "status = ?", StatusRejected)
	if _, err := d.RequestNewImage(rejected, ""); !errors.Is(err, ErrNoImageRequest) {
		t.Errorf("rejected article: %v, want ErrNoImageRequest", err)
	}
	if _, err := d.RequestNewImage(9999, ""); !errors.Is(err, ErrNoImageRequest) {
		t.Errorf("missing article: %v, want ErrNoImageRequest", err)
	}
}

// The new image and the end of its request land in one write; a pass
// working on an earlier request neither closes nor fails a later one.
func TestImageRequest_ClosedOnlyByTheRequestItAnswers(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "swap")
	setFields(t, d, id, enriched)
	d.RequestNewImage(id, "first")
	d.RequestNewImage(id, "second") // seq 2

	// A pass that started on seq 1 cannot fail seq 2.
	if err := d.FailImageRequest(id, 1, "nothing"); err != nil {
		t.Fatal(err)
	}
	if r := mustRequest(t, d, id); !r.Pending() || r.Seq != 2 {
		t.Fatalf("after a stale failure: %+v", r)
	}

	// Nor close it: its image lands, the request stays.
	img, thumb, by := "https://img.example/b-1280.jpg", "https://img.example/b-500.jpg", "by B"
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{ImageURL: &img, ImageThumbnail: &thumb, ImageAttribution: &by, ImageRequestSeq: 1}); err != nil {
		t.Fatal(err)
	}
	if r := mustRequest(t, d, id); !r.Pending() {
		t.Fatalf("a stale pass closed the newer request: %+v", r)
	}

	// The pass for seq 2 closes it, swapping image, thumbnail and credit.
	img, thumb, by = "https://img.example/c-1280.jpg", "https://img.example/c-500.jpg", "by C"
	if err := d.ApplyEnrichment(id, EnrichmentUpdate{ImageURL: &img, ImageThumbnail: &thumb, ImageAttribution: &by, ImageRequestSeq: 2}); err != nil {
		t.Fatal(err)
	}
	if r := mustRequest(t, d, id); r.State != "" {
		t.Errorf("request still there after its image landed: %+v", r)
	}
	var gotImg, gotThumb, gotBy string
	d.DB().QueryRow(`SELECT image_url, image_thumbnail, image_attribution FROM generated_articles WHERE id = ?`, id).Scan(&gotImg, &gotThumb, &gotBy)
	if gotImg != img || gotThumb != thumb || gotBy != by {
		t.Errorf("stored %q %q %q", gotImg, gotThumb, gotBy)
	}
	if got, _ := d.ListArticlesNeedingEnrichment(3, 10); len(got) != 0 {
		t.Errorf("still listed after the swap: %+v", got)
	}
}

// A failed request stays visible, leaves the image alone, and is no longer
// worked on; a new image ends it.
func TestImageRequest_Failure(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "fail")
	setFields(t, d, id, enriched)
	d.RequestNewImage(id, "zeppelin")
	if err := d.FailImageRequest(id, 1, "the search found no images"); err != nil {
		t.Fatal(err)
	}
	r := mustRequest(t, d, id)
	if !r.Failed() || r.LastError != "the search found no images" || r.FinishedAt.IsZero() || r.Hint != "zeppelin" {
		t.Errorf("request = %+v", r)
	}
	if got, _ := d.ListArticlesNeedingEnrichment(3, 10); len(got) != 0 {
		t.Errorf("a failed request is still worked on: %+v", got)
	}

	// Asking again starts over.
	d.RequestNewImage(id, "airship")
	if r := mustRequest(t, d, id); !r.Pending() || r.LastError != "" || !r.FinishedAt.IsZero() || r.Seq != 2 {
		t.Errorf("request again = %+v", r)
	}
}

// The dashboard's poll notices a request starting and its image landing.
func TestQueueFingerprint_FollowsImageRequests(t *testing.T) {
	d := openTestDB(t)
	id := makeArticle(t, d, "fp")
	setFields(t, d, id, enriched)
	before, _ := d.QueueFingerprint()
	d.RequestNewImage(id, "")
	pending, _ := d.QueueFingerprint()
	if pending == before {
		t.Error("fingerprint did not change when a new image was asked for")
	}
	img := "https://img.example/d.jpg"
	d.ApplyEnrichment(id, EnrichmentUpdate{ImageURL: &img, ImageThumbnail: &img, ImageRequestSeq: 1})
	if after, _ := d.QueueFingerprint(); after == pending {
		t.Error("fingerprint did not change when the new image landed")
	}
}

func mustRequest(t *testing.T, d *Database, id int64) ImageRequest {
	t.Helper()
	reqs, err := d.ImageRequestsFor([]int64{id})
	if err != nil {
		t.Fatal(err)
	}
	return reqs[id]
}
