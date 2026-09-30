package ai

import (
	"strings"
	"testing"
)

func TestPipeline_Process_AboveThreshold(t *testing.T) {
	genMock := &mockHFClient{response: "Deal Alert: 40% Bonus\nTransfer bonus to ANA [TPG]"}
	criticMock := &mockHFClient{
		response: `{"score": 90, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", "", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus", Content: "40% bonus to ANA"},
		{Name: "OMAAT", URL: "https://omaat.com/1", Title: "ANA Bonus", Content: "40% transfer bonus"},
	}

	result, err := pipeline.Process("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PassedThreshold {
		t.Error("expected PassedThreshold to be true for score 90 >= threshold 80")
	}
	if result.Verification.Score != 90 {
		t.Errorf("expected score 90, got %d", result.Verification.Score)
	}
	if result.Type != "deal" {
		t.Errorf("expected type 'deal', got %q", result.Type)
	}
	if result.Title != "Deal Alert: 40% Bonus" {
		t.Errorf("expected title 'Deal Alert: 40%% Bonus', got %q", result.Title)
	}
	// Content should contain the article body (title stripped by parser)
	if result.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestPipeline_Process_BelowThreshold(t *testing.T) {
	genMock := &mockHFClient{response: "Bad Article Title\nSome content"}
	criticMock := &mockHFClient{
		response: `{"score": 40, "issues": ["Missing citations", "Hallucinated data"], "factual_check": false, "attribution_check": false, "completeness_check": false, "hallucination_check": false}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", "", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "DofC", URL: "https://dofc.com/1", Title: "Deals", Content: "deals info"},
		{Name: "VFTW", URL: "https://vftw.com/1", Title: "Deals", Content: "more deals"},
	}

	result, err := pipeline.Process("deal", sources)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PassedThreshold {
		t.Error("expected PassedThreshold to be false for score 40 < threshold 80")
	}
	// adjustScore: hallucination cap 60 on score 40 → stays 40, then -3*10 = 10
	if result.Verification.Score > 40 {
		t.Errorf("expected score <= 40 for all-failed checks, got %d", result.Verification.Score)
	}
}

func TestPipeline_Process_SingleSourceRejected(t *testing.T) {
	gen := NewGenerator(&mockHFClient{response: "Title\nContent"}, "model", "", nil)
	critic := NewCritic(&mockHFClient{response: `{"score":90,"issues":[],"factual_check":true,"attribution_check":true,"completeness_check":true,"hallucination_check":true}`}, "model")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Deal", Content: "info"},
	}

	_, err := pipeline.Process("deal", sources)
	if err != ErrNotEnoughSources {
		t.Errorf("expected ErrNotEnoughSources, got: %v", err)
	}
}

func TestPipeline_ProcessWithFeedback(t *testing.T) {
	genMock := &mockHFClient{response: "Revised Title\nRevised content [TPG]"}
	criticMock := &mockHFClient{
		response: `{"score": 95, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
	}

	gen := NewGenerator(genMock, "mistral-7b", "", nil)
	critic := NewCritic(criticMock, "llama-3-8b")
	pipeline := NewPipeline(gen, critic, 80)

	sources := []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus", Content: "40% bonus"},
	}

	result, err := pipeline.ProcessWithFeedback(sources, "old article", "fix dates", "missing end date")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.PassedThreshold {
		t.Error("expected PassedThreshold to be true")
	}
	if result.Title != "Revised Title" {
		t.Errorf("expected title 'Revised Title', got %q", result.Title)
	}
}

// --- parseStructuredContent / stripStructuralMarkers tests ---

func TestParseStructuredContent_WellFormed(t *testing.T) {
	raw := `Article Title

TLDR: Short summary here.

KEY_DETAILS_START
- Bonus: 100K points
- Deadline: May 1
KEY_DETAILS_END

LINKS_START
- Buy Points | https://example.com/buy
- Program Page | https://example.com/program
LINKS_END

<p>First paragraph of the body.</p>
<p>Second paragraph.</p>`

	title, tldr, kd, links, body := parseStructuredContent(raw)

	if title != "Article Title" {
		t.Errorf("title: got %q", title)
	}
	if tldr != "Short summary here." {
		t.Errorf("tldr: got %q", tldr)
	}
	if len(kd) != 2 || kd[0].Label != "Bonus" || kd[0].Value != "100K points" {
		t.Errorf("key details: got %+v", kd)
	}
	if len(links) != 2 || links[0].Text != "Buy Points" || links[0].URL != "https://example.com/buy" {
		t.Errorf("links: got %+v", links)
	}
	if !strings.HasPrefix(body, "<p>First paragraph") {
		t.Errorf("body: got %q", body)
	}
	if strings.Contains(body, "KEY_DETAILS") || strings.Contains(body, "LINKS_") || strings.Contains(body, "TLDR:") {
		t.Errorf("body contains leaked markers: %q", body)
	}
}

// TestParseStructuredContent_InlineMarkers reproduces the legacy Nemotron bug
// where the model emitted everything on a single line block, with markers
// inline rather than on their own lines.
func TestParseStructuredContent_InlineMarkers(t *testing.T) {
	raw := `Chase Freedom Unlimited Branch Offer TLDR: Branch-only deal gives 20K points after $500 spend. KEY_DETAILS_START - Bonus: 20,000 UR points - Spend: $500 in 90 days - Annual Fee: $0 KEY_DETAILS_END LINKS_START - Find a Branch | https://www.chase.com/ - Card Overview | https://www.chase.com/freedom LINKS_END <p>The actual body of the article goes here.</p>`

	title, tldr, kd, links, body := parseStructuredContent(raw)

	// Title should NOT contain the leaked TLDR
	if strings.Contains(title, "TLDR") {
		t.Errorf("title leaked TLDR: %q", title)
	}
	if title != "Chase Freedom Unlimited Branch Offer" {
		t.Errorf("title: got %q", title)
	}
	if !strings.HasPrefix(tldr, "Branch-only deal gives 20K") {
		t.Errorf("tldr: got %q", tldr)
	}
	if len(kd) != 3 {
		t.Errorf("expected 3 key details from inline block, got %d: %+v", len(kd), kd)
	}
	if len(kd) >= 1 && (kd[0].Label != "Bonus" || kd[0].Value != "20,000 UR points") {
		t.Errorf("first detail wrong: got %+v", kd[0])
	}
	if len(links) != 2 {
		t.Errorf("expected 2 links from inline block, got %d: %+v", len(links), links)
	}
	if len(links) >= 1 && links[0].URL != "https://www.chase.com/" {
		t.Errorf("first link wrong: got %+v", links[0])
	}
	if !strings.HasPrefix(body, "<p>The actual body") {
		t.Errorf("body should start at LINKS_END, got: %q", body)
	}
	for _, marker := range []string{"KEY_DETAILS_START", "KEY_DETAILS_END", "LINKS_START", "LINKS_END", "TLDR:"} {
		if strings.Contains(body, marker) {
			t.Errorf("body still contains marker %q: %q", marker, body)
		}
	}
}

func TestParseStructuredContent_NoLinksEnd(t *testing.T) {
	// Model didn't emit LINKS_END at all — body falls back to everything after title line.
	raw := `Title Goes Here

<p>Just the body, no markers anywhere.</p>`
	title, _, _, _, body := parseStructuredContent(raw)
	if title != "Title Goes Here" {
		t.Errorf("title: got %q", title)
	}
	if !strings.HasPrefix(body, "<p>Just the body") {
		t.Errorf("body fallback failed: %q", body)
	}
}

func TestStripStructuralMarkers_RemovesInlineBlocks(t *testing.T) {
	in := `<p>Lead paragraph.</p> KEY_DETAILS_START - foo: bar KEY_DETAILS_END <p>Middle paragraph.</p> LINKS_START - Click | https://x.com LINKS_END <p>Trailing paragraph.</p>`
	out := stripStructuralMarkers(in)
	for _, marker := range []string{"KEY_DETAILS_START", "KEY_DETAILS_END", "LINKS_START", "LINKS_END"} {
		if strings.Contains(out, marker) {
			t.Errorf("output still contains %q: %q", marker, out)
		}
	}
	// All three actual paragraphs survived.
	for _, want := range []string{"Lead paragraph", "Middle paragraph", "Trailing paragraph"} {
		if !strings.Contains(out, want) {
			t.Errorf("output dropped %q: %q", want, out)
		}
	}
}

func TestStripStructuralMarkers_RemovesTLDRLine(t *testing.T) {
	in := "TLDR: This should be stripped from body.\n<p>Real body content.</p>"
	out := stripStructuralMarkers(in)
	if strings.Contains(out, "TLDR:") {
		t.Errorf("TLDR line not stripped: %q", out)
	}
	if !strings.Contains(out, "Real body content") {
		t.Errorf("real body lost: %q", out)
	}
}

func TestStripStructuralMarkers_StripsBareResidualMarkers(t *testing.T) {
	// A marker without a matching counterpart (e.g. KEY_DETAILS_START with no END)
	// should still be removed defensively.
	in := "<p>Body.</p>\nKEY_DETAILS_START\nstray content\n"
	out := stripStructuralMarkers(in)
	if strings.Contains(out, "KEY_DETAILS_START") {
		t.Errorf("bare marker not stripped: %q", out)
	}
}

func TestParseDashItems_Newline(t *testing.T) {
	got := parseDashItems("- foo: 1\n- bar: 2\n- baz: 3")
	want := []string{"foo: 1", "bar: 2", "baz: 3"}
	if len(got) != len(want) {
		t.Fatalf("length: got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("item %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseDashItems_Inline(t *testing.T) {
	got := parseDashItems("- foo: 1 - bar: 2 - baz: 3")
	want := []string{"foo: 1", "bar: 2", "baz: 3"}
	if len(got) != len(want) {
		t.Fatalf("length: got %d, want %d (items: %+v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("item %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestPipeline_DetailsUseTheirOwnChain: the inline key-details call asks for
// 1024 tokens, so it runs under the 30s short-call timeout. On the critic model
// (minutes per reply since 2026-09-29) it would time out on every article, so
// it runs on the chain given to SetDetailModels and falls over a retired model.
func TestPipeline_DetailsUseTheirOwnChain(t *testing.T) {
	genMock := &mockHFClient{response: "Deal Alert: 40% Bonus\nTransfer bonus to ANA [TPG]"}
	criticClient := &perModelMockHFClient{
		responses: map[string]string{
			"critic": `{"score": 90, "issues": [], "factual_check": true, "attribution_check": true, "completeness_check": true, "hallucination_check": true}`,
			"live":   `{"key_details": [{"Label": "Bonus", "Value": "40%"}], "links": []}`,
		},
		errs: map[string]error{"gone": &APIError{StatusCode: 410, Model: "gone", Body: "end of life"}},
	}
	pipeline := NewPipeline(NewGenerator(genMock, "gen", "", nil), NewCritic(criticClient, "critic"), 80)
	pipeline.SetDetailModels([]string{"gone", "live"})

	result, err := pipeline.Process("deal", []SourceArticle{
		{Name: "TPG", URL: "https://tpg.com/1", Title: "Bonus", Content: "40% bonus to ANA"},
		{Name: "OMAAT", URL: "https://omaat.com/1", Title: "ANA Bonus", Content: "40% transfer bonus"},
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if got := strings.Join(criticClient.callsOrder, ","); got != "critic,gone,live" {
		t.Errorf("calls = %s; want the critic to verify, then details on gone then live", got)
	}
	if len(result.KeyDetails) != 1 || result.KeyDetails[0].Value != "40%" {
		t.Errorf("key details = %+v; want the fallback model's answer", result.KeyDetails)
	}
}
