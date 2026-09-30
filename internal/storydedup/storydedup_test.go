package storydedup

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
)

// fakeAI answers every call with reply (or err) and records the calls.
type fakeAI struct {
	mu      sync.Mutex
	reply   string
	err     error
	models  []string
	prompts []string
	tokens  []int
}

func (f *fakeAI) Generate(model, prompt string, maxTokens int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = append(f.models, model)
	f.prompts = append(f.prompts, prompt)
	f.tokens = append(f.tokens, maxTokens)
	return f.reply, f.err
}

var (
	alaskaCluster = []Source{
		{Title: "Alaska Airlines adds premium cabins to more routes", Content: "<p>Alaska is adding its <b>premium</b> cabin &amp; more.</p>"},
		{Title: "Alaska expands premium class cabins across its network"},
	}
	day = 24 * time.Hour
	now = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
)

func candidates() []Candidate {
	return []Candidate{
		// Scores ~0.37 against alaskaCluster: past the prefilter, below the
		// fallback threshold, like the real pairs the AI has to separate.
		{ID: 1120, Title: "Alaska Airlines premium cabins arrive on Hawaii routes", TLDR: "Alaska adds premium class.",
			CreatedAt: now.Add(-time.Hour), RawTitles: []string{"Alaska Hawaii flights get new seats"}},
		{ID: 1103, Title: "Hyatt launches fall promotion with bonus points", CreatedAt: now.Add(-2 * day),
			RawTitles: []string{"Hyatt fall promo: bonus points on stays"}},
		{ID: 1099, Title: "Chase Sapphire Reserve refresh details", CreatedAt: now.Add(-3 * day)},
	}
}

func TestPrefilter(t *testing.T) {
	got := Prefilter(alaskaCluster, candidates())
	if len(got) != 1 || got[0].ID != 1120 {
		t.Fatalf("Prefilter = %+v, want only 1120", got)
	}
	if got[0].Score < LexicalThreshold {
		t.Errorf("score %.3f below threshold", got[0].Score)
	}
	if got := Prefilter(alaskaCluster, nil); got != nil {
		t.Errorf("no candidates: %+v", got)
	}
}

func TestPrefilter_KeepsTopFive(t *testing.T) {
	var cands []Candidate
	for i := int64(1); i <= 8; i++ {
		cands = append(cands, Candidate{ID: i, Title: "Alaska premium cabins"})
	}
	got := Prefilter(alaskaCluster, cands)
	if len(got) != MaxCandidates {
		t.Fatalf("got %d, want %d", len(got), MaxCandidates)
	}
	// Equal scores: newest (highest ID) first.
	if got[0].ID != 8 {
		t.Errorf("first = %d, want 8", got[0].ID)
	}
}

func TestCheck_NoCandidatesMakesNoAICall(t *testing.T) {
	f := &fakeAI{reply: `{"same_story_as": 1103, "materially_new_development": false}`}
	c := &Checker{Client: f, Models: []string{"m"}}
	d := c.Check(alaskaCluster, candidates()[1:])
	if d.Decision != DecisionNew || d.Stage != StageLexical || d.Duplicate {
		t.Errorf("decision = %+v, want new at the lexical stage", d)
	}
	if len(f.prompts) != 0 {
		t.Errorf("AI called %d times, want 0", len(f.prompts))
	}
}

func TestCheck_Verdicts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reply         string
		err           error
		wantDecision  string
		wantStage     string
		wantDuplicate bool
		wantMatch     int64
		wantInvalid   int64
	}{
		{"duplicate", `{"same_story_as": 1120, "materially_new_development": false, "reason": "same"}`, nil,
			DecisionDuplicate, StageAI, true, 1120, 0},
		{"follow-up", "Sure.\n```json\n{\"same_story_as\": 1120, \"materially_new_development\": true, \"reason\": \"extended\"}\n```", nil,
			DecisionFollowUp, StageAI, false, 1120, 0},
		{"different story", `{"same_story_as": null, "materially_new_development": false, "reason": "different"}`, nil,
			DecisionNew, StageAI, false, 0, 0},
		{"string id", `{"same_story_as": "1120", "materially_new_development": false}`, nil,
			DecisionDuplicate, StageAI, true, 1120, 0},
		// An ID that was not offered is no match, not a block on some
		// arbitrary article.
		{"invalid id", `{"same_story_as": 1099, "materially_new_development": false}`, nil,
			DecisionNew, StageAI, false, 0, 1099},
		// The AI failing falls back to the lexical score at 0.55. The
		// Alaska pair scores below it, so it generates.
		{"ai error", "", errors.New("503"), DecisionErrorFallback, StageFallback, false, 0, 0},
		{"unparseable", "I think it's the same story.", nil, DecisionErrorFallback, StageFallback, false, 0, 0},
		{"match without materially_new", `{"same_story_as": 1120}`, nil,
			DecisionErrorFallback, StageFallback, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAI{reply: tc.reply, err: tc.err}
			c := &Checker{Client: f, Models: []string{"fast"}}
			d := c.Check(alaskaCluster, candidates())
			if d.Decision != tc.wantDecision || d.Stage != tc.wantStage || d.Duplicate != tc.wantDuplicate ||
				d.MatchedID != tc.wantMatch || d.InvalidMatchID != tc.wantInvalid {
				t.Errorf("got %+v", d)
			}
			if len(f.prompts) != 1 {
				t.Fatalf("AI called %d times, want 1", len(f.prompts))
			}
			if f.tokens[0] > 2048 {
				t.Errorf("max tokens %d is not a short call", f.tokens[0])
			}
			if (tc.wantStage == StageFallback) != (d.Err != nil) {
				t.Errorf("Err = %v", d.Err)
			}
		})
	}
}

func TestCheck_FallbackBlocksAboveThreshold(t *testing.T) {
	cands := []Candidate{{ID: 7, Title: "Alaska Airlines adds premium cabins to more routes",
		RawTitles: []string{"Alaska expands premium class cabins across its network"}}, {ID: 8, Title: "Unrelated Hyatt news"}}
	c := &Checker{Client: &fakeAI{err: errors.New("timeout")}, Models: []string{"m"}}
	d := c.Check(alaskaCluster, cands)
	if d.BestScore < FallbackThreshold {
		t.Fatalf("setup: best score %.3f is below the fallback threshold", d.BestScore)
	}
	if d.Decision != DecisionErrorFallback || !d.Duplicate || d.MatchedID != 7 {
		t.Errorf("got %+v, want error_fallback duplicate of 7", d)
	}

	// No client at all is the same as a failing one.
	var nilChecker *Checker
	if d := nilChecker.Check(alaskaCluster, cands); d.Decision != DecisionErrorFallback || !d.Duplicate {
		t.Errorf("nil checker: %+v", d)
	}
}

func TestCheck_UsesTheModelChain(t *testing.T) {
	// The first model is gone; TryModels moves on to the next.
	client := &chainAI{gone: "retired", reply: `{"same_story_as": null, "materially_new_development": false}`}
	c := &Checker{Client: client, Models: []string{"retired", "live"}}
	d := c.Check(alaskaCluster, candidates())
	if d.Stage != StageAI || d.Decision != DecisionNew {
		t.Errorf("got %+v", d)
	}
	if strings.Join(client.calls, ",") != "retired,live" {
		t.Errorf("calls = %v", client.calls)
	}
}

type chainAI struct {
	gone  string
	reply string
	calls []string
}

func (c *chainAI) Generate(model, _ string, _ int) (string, error) {
	c.calls = append(c.calls, model)
	if model == c.gone {
		return "", &ai.APIError{StatusCode: 410, Model: model, Body: "end of life"}
	}
	return c.reply, nil
}

func TestPrompt(t *testing.T) {
	p := Prompt(alaskaCluster, candidates()[:1])
	for _, want := range []string{
		"Alaska Airlines adds premium cabins to more routes",
		"Alaska is adding its premium cabin & more.", // tags stripped, entities decoded
		"id 1120 (written 2026-09-30): Alaska Airlines premium cabins arrive on Hawaii routes",
		"TL;DR: Alaska adds premium class.",
		`"same_story_as"`, `"materially_new_development"`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	long := Prompt([]Source{{Title: "t", Content: strings.Repeat("word ", 500)}}, nil)
	if len(long) > 2000 {
		t.Errorf("content snippet not bounded: prompt is %d bytes", len(long))
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := ParseVerdict(`{"same_story_as": 12, "materially_new_development": true, "reason": "r"}`)
	if err != nil || v.SameStoryAs == nil || *v.SameStoryAs != 12 || !v.MateriallyNew || v.Reason != "r" {
		t.Errorf("got %+v, %v", v, err)
	}
	v, err = ParseVerdict(`{"same_story_as": null}`)
	if err != nil || v.SameStoryAs != nil {
		t.Errorf("null: %+v, %v", v, err)
	}
	for _, bad := range []string{"", "no json", `{"same_story_as": "abc", "materially_new_development": false}`, `{"same_story_as": 1.5, "materially_new_development": false}`} {
		if _, err := ParseVerdict(bad); err == nil {
			t.Errorf("ParseVerdict(%q) should fail", bad)
		}
	}
}
