package cluster

import (
	"testing"
)

func TestClusterByTitle(t *testing.T) {
	articles := []Article{
		{ID: 1, Title: "Amex 40% Transfer Bonus to ANA Mileage Club", SourceID: 1},
		{ID: 2, Title: "Amex Membership Rewards 40% Bonus Transfer to ANA", SourceID: 2},
		{ID: 3, Title: "Chase Sapphire Preferred 80K Sign-Up Bonus Returns", SourceID: 1},
		{ID: 4, Title: "Chase Sapphire 80K Offer Is Back", SourceID: 3},
		{ID: 5, Title: "Hilton Honors Devaluation Analysis", SourceID: 2},
	}

	clusters := Cluster(articles, 0.3)

	// Should get 3 clusters: Amex (2), Chase (2), Hilton (1)
	if len(clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(clusters))
	}

	// Find the Amex cluster
	var amexCluster *ArticleCluster
	for i, c := range clusters {
		for _, a := range c.Articles {
			if a.ID == 1 {
				amexCluster = &clusters[i]
				break
			}
		}
	}
	if amexCluster == nil || len(amexCluster.Articles) != 2 {
		t.Error("expected Amex cluster with 2 articles")
	}
}

func TestClusterSingleArticle(t *testing.T) {
	articles := []Article{
		{ID: 1, Title: "Unique Article About Something", SourceID: 1},
	}
	clusters := Cluster(articles, 0.3)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if len(clusters[0].Articles) != 1 {
		t.Error("expected single article in cluster")
	}
}

// TestDefaultThreshold_GroupsOneStoryFromManySites is the regression test for
// the duplicate articles published on 2026-09-21.
//
// Five sites covered the same Citi/JAL announcement. At the old 0.5 threshold
// they formed two separate clusters, so the pipeline generated two articles
// about one story -- and the raw-ID dedupe could not catch it, because disjoint
// clusters share no source article by construction. Deduplication has to happen
// at clustering time or not at all.
func TestDefaultThreshold_GroupsOneStoryFromManySites(t *testing.T) {
	sameStory := []string{
		"Citi Adds Japan Airlines Mileage Bank as Transfer Partner",
		"Japan Airlines Mileage Bank Added As Citi ThankYou Transfer Partner, Plus A 30% Bonus",
		"Citi Adds Japan Airlines As 1:1 Transfer Partner (Limited 30% Transfer Bonus)",
		"Citi adds Japan Airlines as a 1:1 partner, launches 30% transfer bonus",
		"Another One: Citi Adds Japan Airlines as Transfer Partner (With a 30% Bonus)",
	}
	unrelated := []string{
		"Hyatt Announces 12 New Hotels Opening in 2027",
		"Delta SkyMiles Adds New Award Chart Region",
	}

	var arts []Article
	for i, title := range append(append([]string{}, sameStory...), unrelated...) {
		arts = append(arts, Article{ID: int64(i + 1), Title: title, SourceID: int64(i + 1)})
	}

	clusters := Cluster(arts, DefaultThreshold)

	biggest := 0
	for _, c := range clusters {
		if len(c.Articles) > biggest {
			biggest = len(c.Articles)
		}
	}
	if biggest != len(sameStory) {
		t.Errorf("largest cluster holds %d articles, want all %d covering one story; "+
			"got %d clusters total", biggest, len(sameStory), len(clusters))
	}
	// The unrelated stories must not be swept in by a looser threshold.
	if len(clusters) != 1+len(unrelated) {
		t.Errorf("got %d clusters, want %d (one story + %d unrelated)",
			len(clusters), 1+len(unrelated), len(unrelated))
	}
}
