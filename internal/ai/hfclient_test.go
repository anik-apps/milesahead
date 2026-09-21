package ai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func makeChatResponse(content string) chatResponse {
	return chatResponse{
		Choices: []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}{
			{Message: struct {
				Content string `json:"content"`
			}{Content: content}},
		},
	}
}

func TestHFClient_Generate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth header")
		}
		// Verify it posts to /chat/completions
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := makeChatResponse("This is a generated article.")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL

	result, err := client.Generate("test-model", "Write an article", 500)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if result != "This is a generated article." {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestHFClient_RateLimit(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(429)
			w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		resp := makeChatResponse("Success after retry.")
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := NewHFClient("test-token", 3)
	client.BaseURL = ts.URL
	client.SetBackoff(time.Millisecond) // keep the suite fast; see TestHFClient_BackoffOutlastsAProviderBlip

	result, err := client.Generate("test-model", "prompt", 500)
	if err != nil {
		t.Fatalf("expected retry success, got: %v", err)
	}
	if result != "Success after retry." {
		t.Errorf("unexpected: %s", result)
	}
}

// TestHFClient_BackoffOutlastsAProviderBlip pins the production retry budget.
//
// The original schedule was 100ms/200ms/400ms: 700ms of total patience. On
// 2026-09-21 a provider-side degradation lasting tens of seconds blew through
// all three attempts for four separate enrichment calls, and the article was
// published missing its key details, Scout's Take and image. The identical
// prompts succeeded on the same model minutes later, so the model was fine --
// the client simply gave up almost immediately.
func TestHFClient_BackoffOutlastsAProviderBlip(t *testing.T) {
	const wantMinimumPatience = 10 * time.Second

	c := NewHFClient("token", 3)
	total, d := time.Duration(0), c.backoff
	for i := 0; i < 3; i++ {
		total += d
		d *= 2
	}
	if total < wantMinimumPatience {
		t.Errorf("3 retries wait %v in total; a provider blip needs at least %v "+
			"of patience to ride out", total, wantMinimumPatience)
	}
}

// TestHFClient_RetriesTransportErrors is the regression test for the defect that
// silently degraded articles for two and a half months.
//
// From 2026-07-01 the provider's latency crossed the client timeout on roughly a
// quarter of enrichment calls. Every one failed as "context deadline exceeded"
// and was NEVER retried: the loop only ever retried HTTP status codes, so a
// transport error returned on the first attempt. The article was then published
// missing its Scout's Take, key details or image, at WARN level, with nothing
// recording which articles were affected.
func TestHFClient_RetriesTransportErrors(t *testing.T) {
	attempts := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			// Hijack and drop the connection: a transport-level failure, which
			// surfaces to the client as an error from Do rather than a status.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("test server does not support hijacking")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			conn.Close()
			return
		}
		json.NewEncoder(w).Encode(makeChatResponse("Recovered after transport failures."))
	}))
	defer ts.Close()

	client := NewHFClient("token", 3)
	client.BaseURL = ts.URL
	client.SetBackoff(time.Millisecond)

	result, err := client.Generate("test-model", "prompt", 256)
	if err != nil {
		t.Fatalf("transport errors must be retried, got: %v", err)
	}
	if result != "Recovered after transport failures." {
		t.Errorf("unexpected result: %q", result)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts (2 dropped + 1 success), got %d", attempts)
	}
}

// TestTimeoutFor_ClassifiesEveryRealCallSite pins each call site in the
// codebase to the timeout class it should get.
//
// The threshold used to be 512, which is exactly what Scout's Take, tags and
// image queries request -- so they were capped only by an <= comparison, and
// the genuinely expensive short calls (key details 1024, image scoring 2048,
// card-update detection 1024) silently inherited the 900s GENERATION timeout.
// A pass could then spend an hour per field on a degraded model before the
// fallback chain was tried at all.
//
// Listing the real token values here means changing any of them fails this
// test rather than silently changing a timeout by 30x.
func TestTimeoutFor_ClassifiesEveryRealCallSite(t *testing.T) {
	const generationTimeout = 15 * time.Minute
	c := NewHFClient("token", 3)
	c.SetRequestTimeout(generationTimeout)

	cases := []struct {
		site      string
		maxTokens int
		wantShort bool
	}{
		{"Scout's Take (ai/enrichment.go)", ScoutsTakeTokens, true},
		{"tags and image queries (ai/enrichment.go)", TagsTokens, true},
		{"key details (ai/enrichment.go)", KeyDetailTokens, true},
		{"preflight probe (ai/preflight.go)", preflightMaxTokens, true},
		{"image text scoring (images/scorer.go)", 2048, true},
		{"card update detection (cardupdate/detector.go)", 1024, true},
		{"newsletter pick (newsletter/newsletter.go)", 512, true},
		{"critic verify (ai/critic.go)", 4096, false},
		{"article generation (ai/generator.go)", 4096, false},
	}
	for _, tc := range cases {
		got := c.timeoutFor(tc.maxTokens)
		isShort := got == ShortCallTimeout
		if isShort != tc.wantShort {
			t.Errorf("%s asks for %d tokens and got %v; want short=%v",
				tc.site, tc.maxTokens, got, tc.wantShort)
		}
	}

	// A configured timeout below the cap must still win: the cap is a ceiling
	// for short calls, not a floor.
	c.SetRequestTimeout(5 * time.Second)
	if got := c.timeoutFor(512); got != 5*time.Second {
		t.Errorf("got %v, want the configured 5s to take precedence over the cap", got)
	}
}

// TestStripThinkingTags_TerminatesOnMalformedInput is a regression guard for an
// infinite loop, not a formatting test.
//
// The closer used to be searched from the start of the string rather than from
// the opener. A reply containing "</think>" BEFORE "<think>" therefore produced
// end < start, and the reassembly s[:start] + s[end+8:] rebuilt the identical
// string — the loop made no progress and span forever at 100% CPU.
//
// The blast radius is why this has a test: the caller holds the enrichment
// pass guard, so one such reply wedges enrichment permanently (every later pass
// logs "skipped: previous pass still running") and blocks graceful shutdown
// until systemd SIGKILLs the process.
func TestStripThinkingTags_TerminatesOnMalformedInput(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"stray closer before opener", "A</think>B<think>C", "A</think>B"},
		{"only a stray closer", "</think>answer", "</think>answer"},
		{"closer then a complete block", "x</think> <think>r</think> ok", "x</think>  ok"},
		{"normal complete block", "<think>reasoning</think>answer", "answer"},
		{"unclosed opener", "before<think>reasoning...", "before"},
		{"no tags", "plain answer", "plain answer"},
		{"two complete blocks", "<think>a</think>mid<think>b</think>end", "midend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan string, 1)
			go func() { done <- stripThinkingTags(tc.in) }()
			select {
			case got := <-done:
				if got != tc.want {
					t.Errorf("stripThinkingTags(%q) = %q, want %q", tc.in, got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("stripThinkingTags(%q) did not terminate", tc.in)
			}
		})
	}
}
