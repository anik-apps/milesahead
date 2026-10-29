package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/images"
)

// storedArticle is a finished article the short calls are measured on.
type storedArticle struct {
	id                              int64
	title, tldr, content, thumbnail string
}

// shortRun is one short call: which model, which task, how long, and what came
// back (or why nothing usable did).
type shortRun struct {
	Model   string        `json:"model"`
	Task    string        `json:"task"`
	Article int64         `json:"article"`
	Latency time.Duration `json:"latency_ns"`
	Err     string        `json:"err,omitempty"`
	Sample  string        `json:"sample,omitempty"`
}

// probeShortCalls measures candidates for the enrichment chain and the vision
// list on the newest stored articles, through the same functions production
// calls, with the same client, token budgets, timeouts and retries.
//
// The models take turns on each article rather than one model running its
// whole batch first: the provider's latency swings by the hour, and turns
// keep a slow spell from landing on one candidate only.
func probeShortCalls(cfg *config.Config, enrich, vision []string, want int, outPath string) {
	articles := loadArticles(cfg.DB.Path, want)
	if len(articles) == 0 {
		fatal("no stored articles with a body, a TL;DR and an image")
	}
	client := newClient(cfg)
	if cfg.AI.EnrichmentDisableThinking {
		client = client.WithThinkingDisabled()
	}
	var results []shortRun
	if len(enrich) > 0 {
		fmt.Printf("enrichment: %d articles, thinking off %v, candidates %s\n\n",
			len(articles), cfg.AI.EnrichmentDisableThinking, strings.Join(enrich, ", "))
		results = append(results, probeEnrichment(client, enrich, articles, cfg.Site.PersonaName)...)
	}
	if len(vision) > 0 {
		if cfg.AI.HFBaseURL == "" {
			fatal("vision needs ai.hf_base_url")
		}
		fmt.Printf("\nvision: %d articles, candidates %s\n\n", len(articles), strings.Join(vision, ", "))
		results = append(results, probeVision(cfg, vision, articles)...)
	}
	summarizeShort(results)
	if outPath != "" {
		b, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(outPath, b, 0o644); err != nil {
			fatal("write %s: %v", outPath, err)
		}
		fmt.Printf("\nwrote %s\n", outPath)
	}
}

// probeEnrichment runs each enrichment task on each article with each model
// alone (no fallback: the model is what is measured). An error includes the
// function's own checks -- a Scout's Take that cites "Source 1", tags off the
// approved list -- so "ok" means production would have stored the result.
func probeEnrichment(client *ai.HFClient, models []string, articles []storedArticle, persona string) []shortRun {
	tasks := []struct {
		name string
		run  func(m string, a storedArticle) (string, error)
	}{
		{"tags", func(m string, a storedArticle) (string, error) {
			tags, err := ai.ExtractTags(client, []string{m}, a.title, a.content)
			return strings.Join(tags, ","), err
		}},
		{"scouts_take", func(m string, a storedArticle) (string, error) {
			return ai.GenerateScoutsTake(client, []string{m}, persona, a.title, a.tldr, a.content)
		}},
		{"key_details", func(m string, a storedArticle) (string, error) {
			details, links, err := ai.ExtractDetails(client, []string{m}, a.title, a.content)
			return fmt.Sprintf("%d details, %d links", len(details), len(links)), err
		}},
		{"image_queries", func(m string, a storedArticle) (string, error) {
			q, err := ai.GenerateImageQueries(client, []string{m}, a.title, a.tldr)
			return strings.Join(q, " | "), err
		}},
	}
	var out []shortRun
	for i, a := range articles {
		fmt.Printf("[%d/%d] #%d %s\n", i+1, len(articles), a.id, trunc(a.title, 70))
		for _, t := range tasks {
			for _, m := range models {
				r := shortRun{Model: m, Task: t.name, Article: a.id}
				start := time.Now()
				sample, err := t.run(m, a)
				r.Latency = time.Since(start)
				if err != nil {
					r.Err = trunc(err.Error(), 160)
				} else {
					r.Sample = trunc(sample, 90)
				}
				fmt.Printf("  %-14s %-42s %6.1fs  %s\n", t.name, m, r.Latency.Seconds(), resultText(r))
				out = append(out, r)
			}
		}
	}
	return out
}

// visionCalls collects the vision calls a Scorer reports.
type visionCalls struct{ calls []ai.ModelCall }

func (v *visionCalls) RecordModelCall(c ai.ModelCall) { v.calls = append(v.calls, c) }

// probeVision has each model score three images per article through the real
// Scorer: the article's own image and the images of two other articles. The
// own image was picked for the article, so a model that judges relevance
// should usually rank it first; that, with the call outcomes and latency, is
// what is compared. The text pre-filter is skipped (no text models), so every
// score is the vision model's own.
func probeVision(cfg *config.Config, models []string, articles []storedArticle) []shortRun {
	n := len(articles)
	if n < 3 {
		fatal("vision needs at least 3 articles, got %d", n)
	}
	var out []shortRun
	for i, a := range articles {
		others := []storedArticle{articles[(i+n/2)%n], articles[(i+n/2+1)%n]}
		if others[1].id == a.id {
			others[1] = articles[(i+1)%n]
		}
		fmt.Printf("[%d/%d] #%d %s (against #%d, #%d)\n", i+1, n, a.id, trunc(a.title, 60), others[0].id, others[1].id)
		for _, m := range models {
			rec := &visionCalls{}
			scorer := images.NewScorer(nil, nil)
			scorer.SetVisionModel(cfg.AI.HFBaseURL, cfg.AI.HFAPIToken, m, nil)
			scorer.SetRecorder(rec)
			cands := []images.ImageCandidate{
				{Thumbnail: a.thumbnail, Attribution: "own"},
				{Thumbnail: others[0].thumbnail, Attribution: "other"},
				{Thumbnail: others[1].thumbnail, Attribution: "other"},
			}
			start := time.Now()
			scored, _ := scorer.Score(a.title, a.tldr, cands)
			elapsed := time.Since(start)

			var own, best float64
			for _, c := range scored {
				if c.Attribution == "own" {
					own = c.Score
				} else if c.Score > best {
					best = c.Score
				}
			}
			r := shortRun{Model: m, Task: "vision", Article: a.id, Latency: elapsed}
			var outcomes []string
			for _, c := range rec.calls {
				outcomes = append(outcomes, fmt.Sprintf("%s %.0fs", c.Outcome, c.Duration.Seconds()))
				if c.Outcome != ai.OutcomeOK && r.Err == "" {
					r.Err = trunc(string(c.Outcome)+": "+c.Detail, 120)
				}
			}
			switch {
			case r.Err != "":
			case len(rec.calls) < len(cands):
				// A thumbnail that could not be downloaded, or a 4xx about the
				// image, is not recorded against the model.
				r.Err = fmt.Sprintf("only %d of %d images were scored (a thumbnail that could not be downloaded?)", len(rec.calls), len(cands))
			default:
				verdict := "own image first"
				if own <= best {
					verdict = "own image NOT first"
				}
				r.Sample = fmt.Sprintf("own=%.0f others<=%.0f, %s", own, best, verdict)
			}
			fmt.Printf("  %-42s %6.1fs  [%s]  %s\n", m, elapsed.Seconds(), strings.Join(outcomes, ", "), resultText(r))
			out = append(out, r)
		}
	}
	return out
}

func resultText(r shortRun) string {
	if r.Err != "" {
		return "FAIL " + r.Err
	}
	return "ok " + r.Sample
}

// summarizeShort prints, per task and model: runs, successes, and the mean
// and worst latency. For vision it adds how often the own image came first.
func summarizeShort(results []shortRun) {
	type agg struct {
		n, ok, ownFirst int
		total, worst    time.Duration
	}
	byKey := map[[2]string]*agg{}
	var keys [][2]string
	for _, r := range results {
		k := [2]string{r.Task, r.Model}
		a, seen := byKey[k]
		if !seen {
			a = &agg{}
			byKey[k] = a
			keys = append(keys, k)
		}
		a.n++
		a.total += r.Latency
		if r.Latency > a.worst {
			a.worst = r.Latency
		}
		if r.Err == "" {
			a.ok++
			if strings.HasSuffix(r.Sample, "own image first") {
				a.ownFirst++
			}
		}
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i][0] < keys[j][0] })
	fmt.Printf("\n%-14s %-42s %5s %5s %8s %8s %s\n", "TASK", "MODEL", "RUNS", "OK", "AVG SEC", "MAX SEC", "OWN IMAGE FIRST")
	for _, k := range keys {
		a := byKey[k]
		own := ""
		if k[0] == "vision" {
			own = fmt.Sprintf("%d/%d", a.ownFirst, a.ok)
		}
		fmt.Printf("%-14s %-42s %5d %5d %8.1f %8.1f %s\n", k[0], k[1], a.n, a.ok,
			(a.total / time.Duration(max(a.n, 1))).Seconds(), a.worst.Seconds(), own)
	}
}

// loadArticles returns the newest stored articles that have a body, a TL;DR
// and a hosted image, in any review status.
func loadArticles(dbPath string, want int) []storedArticle {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		fatal("open db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT id, title, tldr, content, image_thumbnail FROM generated_articles
		WHERE content != '' AND tldr != '' AND image_thumbnail LIKE 'https://%'
		ORDER BY id DESC LIMIT ?`, want)
	if err != nil {
		fatal("query generated_articles: %v", err)
	}
	defer rows.Close()
	var out []storedArticle
	for rows.Next() {
		var a storedArticle
		if err := rows.Scan(&a.id, &a.title, &a.tldr, &a.content, &a.thumbnail); err != nil {
			fatal("scan: %v", err)
		}
		out = append(out, a)
	}
	return out
}
