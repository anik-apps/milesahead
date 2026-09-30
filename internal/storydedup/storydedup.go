// Package storydedup decides whether a new cluster of scanned articles is a
// story the site has already written about.
//
// The raw-ID check in the scan loop only catches a cluster that reuses a
// source article. Measured over 2026-09-21..30, 16 of 98 generated articles
// were same-story duplicates and no pair shared a raw article: one event split
// into two clusters in the same scan, late coverage formed a fresh cluster
// once the original sources aged out of the 48h window, or new coverage
// arrived days later. This check compares the cluster with every article
// generated in the last 7 days instead.
//
// It is two-stage. A TF-IDF prefilter (the same weighting internal/cluster
// uses) picks the few recent articles that share vocabulary with the cluster;
// if none do, the cluster is new and no AI call is made. Otherwise one short
// AI call decides whether the cluster is the same story, and if so whether it
// reports a materially new development worth a follow-up. Lexical similarity
// alone cannot separate the two: a legitimate distinct story scored 0.30
// against its neighbour while a real duplicate scored 0.40.
package storydedup

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/cluster"
)

// Tuning, from the lexical measurement on production 2026-09-21..30 (TF-IDF
// cosine of a cluster's raw titles against each candidate's title plus its
// linked raw titles, 7-day window; 16 duplicates, 79 legitimate):
//
//	>= 0.25   16/16 duplicates caught, 24 legitimate flagged
//	>= 0.35   15/16,  9 flagged
//	>= 0.45    9/16,  4 flagged
//	>= 0.55    7/16,  2 flagged
const (
	// Window is how far back candidates are drawn from.
	Window = 7 * 24 * time.Hour
	// LexicalThreshold admits a candidate to the AI stage. Low on purpose: it
	// only has to keep every real duplicate; the AI call removes the rest.
	LexicalThreshold = 0.25
	// MaxCandidates bounds the prompt.
	MaxCandidates = 5
	// FallbackThreshold decides alone when the AI call fails. High on
	// purpose: without the AI, a false "duplicate" loses a real story.
	FallbackThreshold = 0.55

	// aiMaxTokens keeps the call within ai.ShortCallTimeout (replies are a
	// few dozen tokens, but reasoning models need headroom before the JSON).
	aiMaxTokens = 1024
	// snippetLen bounds each source's content excerpt in the prompt.
	snippetLen = 280
)

// Decisions, also the metric's decision label.
const (
	DecisionNew           = "new"
	DecisionDuplicate     = "duplicate"
	DecisionFollowUp      = "follow_up"
	DecisionErrorFallback = "error_fallback"
)

// Stages at which a decision was reached.
const (
	StageLexical  = "lexical"  // no candidate passed the prefilter
	StageAI       = "ai"       // the AI verdict decided
	StageFallback = "fallback" // the AI call failed; the lexical score decided
)

// Source is one scanned article in the cluster being checked.
type Source struct {
	Title   string
	Content string
}

// Candidate is a recently generated article the cluster may duplicate.
type Candidate struct {
	ID        int64
	Title     string
	TLDR      string
	Status    string
	CreatedAt time.Time
	RawTitles []string
}

// Scored is a candidate's lexical similarity to the cluster.
type Scored struct {
	ID    int64   `json:"id"`
	Score float64 `json:"score"`
}

// Verdict is the AI's answer.
type Verdict struct {
	SameStoryAs   *int64 `json:"same_story_as"`
	MateriallyNew bool   `json:"materially_new_development"`
	Reason        string `json:"reason"`
}

// Decision is the outcome of one check.
type Decision struct {
	Decision string
	Stage    string
	// Duplicate is true when the cluster should not become an article.
	Duplicate bool
	// MatchedID is the article the cluster matched (a duplicate or a
	// follow-up), or 0.
	MatchedID int64
	// Candidates are those that passed the prefilter, best first.
	Candidates []Scored
	// BestScore is the top lexical score, 0 with no candidates.
	BestScore float64
	Verdict   *Verdict
	// InvalidMatchID is set when the AI named an article that was not a
	// candidate; the verdict is then treated as no match.
	InvalidMatchID int64
	// Err is why the AI stage failed, when Stage is StageFallback.
	Err error
}

// Checker runs the check. Models is the short-call chain (enrichment_model),
// tried in order through ai.TryModels.
type Checker struct {
	Client ai.HFClientInterface
	Models []string
}

// Check decides whether sources, a cluster about to be generated, repeats a
// story in candidates.
func (c *Checker) Check(sources []Source, candidates []Candidate) Decision {
	scored := Prefilter(sources, candidates)
	d := Decision{Decision: DecisionNew, Stage: StageLexical, Candidates: scored}
	if len(scored) == 0 {
		return d
	}
	d.BestScore = scored[0].Score

	byID := make(map[int64]Candidate, len(candidates))
	for _, cand := range candidates {
		byID[cand.ID] = cand
	}
	shortlist := make([]Candidate, 0, len(scored))
	for _, s := range scored {
		shortlist = append(shortlist, byID[s.ID])
	}

	v, err := c.ask(sources, shortlist)
	if err != nil {
		d.Decision = DecisionErrorFallback
		d.Stage = StageFallback
		d.Err = err
		if d.BestScore >= FallbackThreshold {
			d.Duplicate = true
			d.MatchedID = scored[0].ID
		}
		return d
	}

	d.Stage = StageAI
	d.Verdict = &v
	if v.SameStoryAs == nil {
		return d
	}
	if !inScored(scored, *v.SameStoryAs) {
		d.InvalidMatchID = *v.SameStoryAs
		return d
	}
	d.MatchedID = *v.SameStoryAs
	if v.MateriallyNew {
		d.Decision = DecisionFollowUp
		return d
	}
	d.Decision = DecisionDuplicate
	d.Duplicate = true
	return d
}

func inScored(scored []Scored, id int64) bool {
	for _, s := range scored {
		if s.ID == id {
			return true
		}
	}
	return false
}

func (c *Checker) ask(sources []Source, shortlist []Candidate) (Verdict, error) {
	if c == nil || c.Client == nil {
		return Verdict{}, fmt.Errorf("no AI client configured")
	}
	reply, err := ai.TryModels(c.Client, c.Models, Prompt(sources, shortlist), aiMaxTokens)
	if err != nil {
		return Verdict{}, err
	}
	return ParseVerdict(reply)
}

// Prefilter scores every candidate against the cluster and returns those at
// or above LexicalThreshold, best first, at most MaxCandidates.
//
// The cluster's document is its raw titles; a candidate's is its title plus
// the titles of the raws it was written from. IDF is taken across the cluster
// and all candidates, so words every recent article uses ("points", "card")
// count for little.
func Prefilter(sources []Source, candidates []Candidate) []Scored {
	all := Scores(sources, candidates)
	var out []Scored
	for _, s := range all {
		if s.Score < LexicalThreshold || len(out) == MaxCandidates {
			break
		}
		out = append(out, s)
	}
	return out
}

// Scores returns every candidate's similarity to the cluster, best first
// (ties: newer ID first).
func Scores(sources []Source, candidates []Candidate) []Scored {
	if len(sources) == 0 || len(candidates) == 0 {
		return nil
	}
	titles := make([]string, len(sources))
	for i, s := range sources {
		titles[i] = s.Title
	}
	docs := make([]string, 0, len(candidates)+1)
	docs = append(docs, strings.Join(titles, " "))
	for _, c := range candidates {
		docs = append(docs, c.Title+" "+strings.Join(c.RawTitles, " "))
	}
	vecs := cluster.Vectorize(docs)

	out := make([]Scored, len(candidates))
	for i, c := range candidates {
		out[i] = Scored{ID: c.ID, Score: cluster.CosineSimilarity(vecs[0], vecs[i+1])}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].ID > out[b].ID
	})
	return out
}

// Prompt is the AI question: is this cluster one of these stories, and if so
// is there anything materially new?
func Prompt(sources []Source, shortlist []Candidate) string {
	var b strings.Builder
	b.WriteString(`You check a points-and-miles news site for duplicate stories.

A new group of source articles has just been scanned. Decide whether it reports the SAME news story as one of the site's existing articles below.

Same story: the same underlying announcement, promotion, launch, change or event, even when worded differently or reported by different outlets. A different promotion, route, card or change from the same airline, hotel or bank is NOT the same story, and neither is unrelated news about the same program.

If it is the same story, also decide whether the new sources report a materially new development that deserves its own follow-up article: for example the offer changed, was extended or ended early, new terms or dates were announced, or something announced earlier has now launched. More coverage, extra details, analysis or opinion about the same news is NOT materially new.

NEW SOURCE ARTICLES:
`)
	for i, s := range sources {
		fmt.Fprintf(&b, "%d. %s\n", i+1, oneLine(s.Title))
		if snip := snippet(s.Content); snip != "" {
			fmt.Fprintf(&b, "   %s\n", snip)
		}
	}
	b.WriteString("\nEXISTING ARTICLES:\n")
	for _, c := range shortlist {
		fmt.Fprintf(&b, "- id %d (written %s): %s\n", c.ID, c.CreatedAt.UTC().Format("2006-01-02"), oneLine(c.Title))
		if tldr := oneLine(c.TLDR); tldr != "" {
			fmt.Fprintf(&b, "  TL;DR: %s\n", truncate(tldr, 400))
		}
	}
	b.WriteString(`
Reply with ONLY this JSON object and nothing else:
{"same_story_as": <id of the existing article reporting the same story, or null>, "materially_new_development": <true or false>, "reason": "<one short sentence>"}`)
	return b.String()
}

// ParseVerdict reads the AI reply. same_story_as may come back as a number,
// a numeric string or null; anything else is an error, so the caller falls
// back rather than acting on a reply it did not understand.
func ParseVerdict(reply string) (Verdict, error) {
	obj := ai.ExtractJSON(reply)
	if obj == "" {
		return Verdict{}, fmt.Errorf("no JSON object in reply %q", truncate(oneLine(reply), 200))
	}
	var raw struct {
		SameStoryAs   json.RawMessage `json:"same_story_as"`
		MateriallyNew *bool           `json:"materially_new_development"`
		Reason        string          `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return Verdict{}, fmt.Errorf("parse reply: %w", err)
	}
	v := Verdict{Reason: raw.Reason}
	s := strings.TrimSpace(string(raw.SameStoryAs))
	switch {
	case s == "" || s == "null":
		// No match; materially_new_development is irrelevant.
	default:
		s = strings.Trim(s, `"`)
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return Verdict{}, fmt.Errorf("same_story_as %s is not an article id", string(raw.SameStoryAs))
		}
		if raw.MateriallyNew == nil {
			return Verdict{}, fmt.Errorf("same_story_as %d without materially_new_development", id)
		}
		v.SameStoryAs = &id
		v.MateriallyNew = *raw.MateriallyNew
	}
	return v, nil
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// snippet is a short plain-text excerpt of a source's content.
func snippet(content string) string {
	text := html.UnescapeString(tagRE.ReplaceAllString(content, " "))
	return truncate(oneLine(text), snippetLen)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "..."
}
