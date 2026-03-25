package cluster

import (
	"math"
	"strings"
	"unicode"
)

// Article represents a scanned article from a source.
type Article struct {
	ID       int64
	Title    string
	SourceID int64
	Content  string
}

// ArticleCluster groups articles about the same topic.
type ArticleCluster struct {
	Articles []Article
}

// Cluster groups articles by title similarity using TF-IDF cosine similarity
// and greedy agglomerative clustering. The threshold controls the minimum
// average similarity required to join an existing cluster (0.0-1.0).
func Cluster(articles []Article, threshold float64) []ArticleCluster {
	if len(articles) == 0 {
		return nil
	}

	// Tokenize all titles.
	tokens := make([][]string, len(articles))
	for i, a := range articles {
		tokens[i] = tokenize(a.Title)
	}

	// Build document frequency map.
	df := make(map[string]int)
	for _, toks := range tokens {
		seen := make(map[string]bool)
		for _, t := range toks {
			if !seen[t] {
				df[t]++
				seen[t] = true
			}
		}
	}

	// Compute TF-IDF vectors.
	n := float64(len(articles))
	vectors := make([]map[string]float64, len(articles))
	for i, toks := range tokens {
		tf := termFrequency(toks)
		vec := make(map[string]float64)
		for term, freq := range tf {
			idf := math.Log(1 + n/float64(df[term]))
			vec[term] = freq * idf
		}
		vectors[i] = vec
	}

	// Greedy agglomerative clustering.
	var clusters []ArticleCluster

	for i := range articles {
		bestCluster := -1
		bestSim := -1.0

		for ci, cl := range clusters {
			// Compute average similarity to articles in this cluster.
			var sumSim float64
			for _, a := range cl.Articles {
				// Find original index of this article.
				idx := -1
				for j, orig := range articles {
					if orig.ID == a.ID {
						idx = j
						break
					}
				}
				if idx >= 0 {
					sumSim += cosineSimilarity(vectors[i], vectors[idx])
				}
			}
			avgSim := sumSim / float64(len(cl.Articles))
			if avgSim > bestSim {
				bestSim = avgSim
				bestCluster = ci
			}
		}

		if bestCluster >= 0 && bestSim >= threshold {
			clusters[bestCluster].Articles = append(clusters[bestCluster].Articles, articles[i])
		} else {
			clusters = append(clusters, ArticleCluster{
				Articles: []Article{articles[i]},
			})
		}
	}

	return clusters
}

// Common English stop words to filter from titles.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"but": true, "in": true, "on": true, "at": true, "to": true,
	"for": true, "of": true, "with": true, "by": true, "from": true,
	"is": true, "it": true, "its": true, "are": true, "was": true,
	"be": true, "has": true, "have": true, "had": true, "do": true,
	"does": true, "did": true, "will": true, "would": true, "could": true,
	"should": true, "may": true, "might": true, "shall": true,
	"not": true, "no": true, "nor": true, "so": true, "yet": true,
	"this": true, "that": true, "these": true, "those": true,
	"up": true, "out": true, "about": true, "into": true,
}

// tokenize lowercases the input, splits on non-alphanumeric characters,
// and removes stop words.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	filtered := words[:0]
	for _, w := range words {
		if !stopWords[w] {
			filtered = append(filtered, w)
		}
	}
	return filtered
}

// termFrequency computes normalized term frequency for a list of tokens.
func termFrequency(tokens []string) map[string]float64 {
	counts := make(map[string]int)
	for _, t := range tokens {
		counts[t]++
	}
	total := float64(len(tokens))
	tf := make(map[string]float64, len(counts))
	for term, count := range counts {
		tf[term] = float64(count) / total
	}
	return tf
}

// cosineSimilarity computes the cosine similarity between two sparse vectors.
func cosineSimilarity(a, b map[string]float64) float64 {
	var dot, normA, normB float64
	for k, v := range a {
		normA += v * v
		if bv, ok := b[k]; ok {
			dot += v * bv
		}
	}
	for _, v := range b {
		normB += v * v
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
