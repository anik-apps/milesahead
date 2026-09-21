// Command ab-models A/B-tests candidate generator models against the real
// pipeline: real clustering, real prompts, real critic, real source material.
//
// The original model choice was made this way -- 80 generations across four
// candidates, scored by the critic -- and the primary it picked has since been
// retired upstream. Rerunning the same measurement is how we pick a replacement
// on evidence rather than on a vendor's model card.
//
// It never writes to the database; it opens it read-only.
//
// Usage:
//
//	ab-models -config /opt/milesahead/config.yaml -topics 5 \
//	  -generators "a,b,c" -critic nvidia/...
//
// Generation runs through ai.Generator, which fixes max_tokens at 4096 exactly
// as production does -- that budget is part of what is being measured, since a
// reasoning model can spend it on thinking and return no article at all.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
)

type run struct {
	Model   string
	Topic   string
	Score   int
	Latency time.Duration
	Chars   int
	Err     string
	Issues  []string
	Checks  [4]bool
}

func main() {
	cfgPath := flag.String("config", "/opt/milesahead/config.yaml", "path to config.yaml")
	generators := flag.String("generators", "", "comma-separated candidate generator models")
	criticModel := flag.String("critic", "", "critic model to score with (must be working)")
	topicCount := flag.Int("topics", 5, "number of source clusters to generate for")
	days := flag.Int("days", 3, "look back this many days for source articles")
	outPath := flag.String("out", "", "optional path to write JSON results")
	criticCands := flag.String("critic-candidates", "", "comma-separated critics to probe for JSON-contract compliance, then exit")
	full := flag.Bool("full", false, "run the complete Pipeline (generate+verify+revise) and dump whole articles to -out")
	dumpPrompt := flag.String("dump-prompt", "", "write the exact generation prompt for the first topic here and exit")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal("load config: %v", err)
	}
	cands := split(*generators)
	if len(cands) == 0 {
		fatal("no -generators given")
	}
	critic := *criticModel
	if critic == "" && *criticCands == "" {
		fatal("no -critic given (the configured one is retired)")
	}

	topics := loadTopics(cfg.DB.Path, *days, *topicCount)
	if len(topics) == 0 {
		fatal("no source clusters found in the last %d days", *days)
	}
	fmt.Printf("%d topics, %d candidates, critic=%s, threshold=%d\n\n",
		len(topics), len(cands), critic, cfg.AI.CriticThreshold)

	client := ai.NewHFClient(cfg.AI.HFAPIToken, cfg.AI.MaxRetries)
	if cfg.AI.HFBaseURL != "" {
		client.BaseURL = cfg.AI.HFBaseURL
	}

	if *criticCands != "" {
		probeCritics(client, cands[0], split(*criticCands), topics[0])
		return
	}

	criticSvc := ai.NewCritic(client, critic)

	if *dumpPrompt != "" {
		// The prompt the generator actually sends, verbatim, so a failing model
		// can be bisected against the real input rather than a paraphrase.
		if err := os.WriteFile(*dumpPrompt, []byte(ai.DealAlertPrompt(topics[0].sources)), 0o644); err != nil {
			fatal("write prompt: %v", err)
		}
		fmt.Printf("wrote %s for topic %q\n", *dumpPrompt, topics[0].name)
		return
	}

	if *full {
		dumpFullArticles(client, cands[0], criticSvc, cfg.AI.CriticThreshold, topics, *outPath)
		return
	}

	var results []run
	for _, model := range cands {
		// No fallback and no cache: we are measuring this model, not the chain.
		gen := ai.NewGenerator(client, model, "", nil)
		for _, t := range topics {
			r := run{Model: model, Topic: t.name}
			start := time.Now()
			article, genErr := gen.Generate("deal", t.sources)
			r.Latency = time.Since(start)
			switch {
			case genErr != nil:
				r.Err = trunc(genErr.Error(), 140)
			case strings.TrimSpace(article) == "":
				// Reasoning models can spend the whole token budget on thinking
				// and return an empty content field, which is not an API error.
				r.Err = "empty content (model returned no article text)"
			default:
				r.Chars = len(article)
				v, vErr := criticSvc.Verify(article, t.sources)
				if vErr != nil {
					r.Err = "critic: " + trunc(vErr.Error(), 120)
				} else {
					r.Score = v.Score
					r.Issues = v.Issues
					r.Checks = [4]bool{v.FactualCheck, v.AttributionCheck,
						v.CompletenessCheck, v.HallucinationCheck}
				}
			}
			status := fmt.Sprintf("score=%d", r.Score)
			if r.Err != "" {
				status = "ERR " + r.Err
			}
			fmt.Printf("  %-38s %-44s %6.1fs  %s\n",
				model, trunc(t.name, 42), r.Latency.Seconds(), status)
			results = append(results, r)
		}
		fmt.Println()
	}

	summarize(results, cfg.AI.CriticThreshold)
	if *outPath != "" {
		b, _ := json.MarshalIndent(results, "", " ")
		os.WriteFile(*outPath, b, 0o644)
		fmt.Printf("\nwrote %s\n", *outPath)
	}
}

func summarize(results []run, threshold int) {
	type agg struct {
		n, ok, pass, sum, lat int
	}
	byModel := map[string]*agg{}
	var order []string
	for _, r := range results {
		a, seen := byModel[r.Model]
		if !seen {
			a = &agg{}
			byModel[r.Model] = a
			order = append(order, r.Model)
		}
		a.n++
		a.lat += int(r.Latency.Seconds())
		if r.Err == "" {
			a.ok++
			a.sum += r.Score
			if r.Score >= threshold {
				a.pass++
			}
		}
	}
	fmt.Printf("\n%-38s %6s %8s %10s %12s %9s\n",
		"MODEL", "RUNS", "NO-ERROR", "AVG SCORE", "PUBLISHABLE", "AVG SEC")
	sort.Slice(order, func(i, j int) bool {
		a, b := byModel[order[i]], byModel[order[j]]
		return float64(a.pass)/float64(max(a.n, 1)) > float64(b.pass)/float64(max(b.n, 1))
	})
	for _, m := range order {
		a := byModel[m]
		avg := 0
		if a.ok > 0 {
			avg = a.sum / a.ok
		}
		fmt.Printf("%-38s %6d %8d %10d %9d/%-2d %9d\n",
			m, a.n, a.ok, avg, a.pass, a.n, a.lat/max(a.n, 1))
	}
	fmt.Printf("\nPUBLISHABLE = critic score >= %d (the configured threshold).\n", threshold)
}

// probeCritics checks which candidate critics actually honour the JSON contract.
// A critic that scores well but returns prose is useless: Critic.Verify runs the
// reply through extractJSON and fails the whole verification if no object is
// found, which is how a reasoning-style model silently disqualifies itself.
func probeCritics(client *ai.HFClient, genModel string, critics []string, t topic) {
	fmt.Printf("generating one article with %s to score...\n", genModel)
	article, err := ai.NewGenerator(client, genModel, "", nil).Generate("deal", t.sources)
	if err != nil || strings.TrimSpace(article) == "" {
		fatal("could not produce an article to score: %v (empty=%v)", err, strings.TrimSpace(article) == "")
	}
	fmt.Printf("article: %d chars, topic %q\n\n", len(article), trunc(t.name, 50))
	fmt.Printf("%-44s %8s %7s  %s\n", "CRITIC", "SECONDS", "SCORE", "RESULT")
	for _, m := range critics {
		start := time.Now()
		v, vErr := ai.NewCritic(client, m).Verify(article, t.sources)
		secs := time.Since(start).Seconds()
		if vErr != nil {
			fmt.Printf("%-44s %8.1f %7s  FAIL %s\n", m, secs, "-", trunc(vErr.Error(), 70))
			continue
		}
		fmt.Printf("%-44s %8.1f %7d  ok (issues=%d, checks=%v)\n",
			m, secs, v.Score, len(v.Issues),
			[]bool{v.FactualCheck, v.AttributionCheck, v.CompletenessCheck, v.HallucinationCheck})
	}
}

// dumpFullArticles runs the real Pipeline -- generate, verify, auto-revise --
// so the output is the article the site would actually publish, not a prompt
// response. Written out whole for human review.
func dumpFullArticles(client *ai.HFClient, genModel string, critic *ai.Critic,
	threshold int, topics []topic, outPath string) {

	pipeline := ai.NewPipeline(ai.NewGenerator(client, genModel, "", nil), critic, threshold)

	type dumped struct {
		Topic      string             `json:"topic"`
		Model      string             `json:"model"`
		Seconds    float64            `json:"seconds"`
		Title      string             `json:"title"`
		TLDR       string             `json:"tldr"`
		Content    string             `json:"content"`
		KeyDetails []ai.KeyDetail     `json:"key_details"`
		Links      []ai.ArticleLink   `json:"links"`
		Score      int                `json:"score"`
		Passed     bool               `json:"passed"`
		Issues     []string           `json:"issues"`
		Sources    []ai.SourceArticle `json:"sources"`
		Err        string             `json:"err,omitempty"`
	}

	var out []dumped
	for _, t := range topics {
		start := time.Now()
		res, err := pipeline.Process("auto", t.sources)
		d := dumped{Topic: t.name, Model: genModel, Seconds: time.Since(start).Seconds(),
			Sources: t.sources}
		if err != nil {
			d.Err = trunc(err.Error(), 200)
			fmt.Printf("  FAIL %-46s %s\n", trunc(t.name, 44), d.Err)
		} else {
			d.Title, d.TLDR, d.Content = res.Title, res.TLDR, res.Content
			d.KeyDetails, d.Links = res.KeyDetails, res.Links
			d.Passed = res.PassedThreshold
			if res.Verification != nil {
				d.Score, d.Issues = res.Verification.Score, res.Verification.Issues
			}
			fmt.Printf("  OK   %-46s score=%-3d passed=%-5v %.0fs %q\n",
				trunc(t.name, 44), d.Score, d.Passed, d.Seconds, trunc(d.Title, 48))
		}
		out = append(out, d)
	}
	if outPath != "" {
		b, _ := json.MarshalIndent(out, "", " ")
		if err := os.WriteFile(outPath, b, 0o644); err != nil {
			fatal("write %s: %v", outPath, err)
		}
		fmt.Printf("\nwrote %s (%d articles)\n", outPath, len(out))
	}
}

type topic struct {
	name    string
	sources []ai.SourceArticle
}

// loadTopics reconstructs what the pipeline itself would have tried to write
// about: recent raw articles, clustered by title similarity at the same 0.5
// threshold main.go uses, keeping only multi-source clusters.
func loadTopics(dbPath string, days, want int) []topic {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		fatal("open db: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT r.id, r.title, r.url, COALESCE(r.content,''), COALESCE(s.name,'Source'), r.source_id
		FROM raw_articles r LEFT JOIN sources s ON s.id = r.source_id
		WHERE r.created_at >= datetime('now', ?) AND r.title != ''
		ORDER BY r.created_at DESC`, fmt.Sprintf("-%d days", days))
	if err != nil {
		fatal("query raw_articles: %v", err)
	}
	defer rows.Close()

	type raw struct {
		id, sourceID                int64
		title, url, content, source string
	}
	var all []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.id, &r.title, &r.url, &r.content, &r.source, &r.sourceID); err != nil {
			fatal("scan: %v", err)
		}
		all = append(all, r)
	}

	ca := make([]cluster.Article, len(all))
	for i, r := range all {
		ca[i] = cluster.Article{ID: r.id, Title: r.title, SourceID: r.sourceID, Content: r.content}
	}
	byID := map[int64]raw{}
	for _, r := range all {
		byID[r.id] = r
	}

	var out []topic
	for _, c := range cluster.Cluster(ca, 0.5) {
		if len(c.Articles) < 2 {
			continue // single-source clusters are not what the pipeline writes from
		}
		distinct := map[string]bool{}
		for _, a := range c.Articles {
			distinct[byID[a.ID].source] = true
		}
		if len(distinct) < 2 {
			continue // Pipeline.Process requires 2+ different sites (hallucination guard)
		}
		t := topic{name: c.Articles[0].Title}
		for _, a := range c.Articles {
			r := byID[a.ID]
			content := r.content
			if len(content) > 1500 {
				content = content[:1500] // same truncation as main.go
			}
			t.sources = append(t.sources, ai.SourceArticle{
				Name: r.source, URL: r.url, Title: r.title, Content: content})
		}
		out = append(out, t)
		if len(out) == want {
			break
		}
	}
	return out
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ab-models: "+format+"\n", args...)
	os.Exit(1)
}
