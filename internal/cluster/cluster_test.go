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
