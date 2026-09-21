package images

import "testing"

// TestParseVisionScore covers the shapes a vision model actually returns.
//
// The prose cases are the ones that mattered: meta/llama-3.2-11b-vision-instruct
// narrates before answering, and at the old 200-token ceiling its reply was cut
// off before the JSON was ever reached. Every candidate then took a 0.6 penalty
// for the model's verbosity rather than for being a bad image, which is why
// articles ended up with no image at all.
func TestParseVisionScore(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   float64
		wantOK bool
	}{
		{"clean json", `{"score": 9, "reason": "shows the exact aircraft"}`, 9, true},
		{"json with preamble", `Sure! {"score": 7, "reason": "related"}`, 7, true},
		{"json fenced", "```json\n{\"score\": 8.5, \"reason\": \"close\"}\n```", 8.5, true},

		{"prose score colon", `The image shows a JAL plane. Score: 9`, 9, true},
		{"prose out of ten", `I would rate this image 8 out of 10 for relevance.`, 8, true},
		{"prose slash ten", `Relevance: 7/10 — correct airline, wrong aircraft.`, 7, true},
		{"prose rated a", `I rated it a 6 because the subject is generic.`, 6, true},
		{"prose score of", `This image has a score of 4.`, 4, true},

		// The reason the fallback is anchored rather than "first number wins".
		{"model numbers in prose only", `The image shows a Japan Airlines 787 at Tokyo Haneda.`, 0, false},
		{"year in prose only", `A Citi campaign running through October 2026.`, 0, false},

		{"out of range high", `{"score": 87, "reason": "typo"}`, 0, false},
		{"out of range zero", `{"score": 0, "reason": "irrelevant"}`, 0, false},
		{"empty", ``, 0, false},
		{"truncated before json", `The image shows a Japan Airlines plane flying in the sky, which is rele`, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := parseVisionScore(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (input %q)", ok, tc.wantOK, tc.in)
			}
			if ok && got != tc.want {
				t.Errorf("score = %v, want %v", got, tc.want)
			}
		})
	}
}
