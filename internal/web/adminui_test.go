package web

import (
	"strings"
	"testing"
)

func TestStatusLabel(t *testing.T) {
	for in, want := range map[string]string{
		"auto_approved": "Auto-approved", "sent": "Sent", "hold": "Hold", "": "",
	} {
		if got := statusLabel(in); got != want {
			t.Errorf("statusLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// Buttons and badges that only carry the base class must still be visible:
// white text needs a background under it.
func TestAdminCSS_BaseButtonAndBadgeHaveABackground(t *testing.T) {
	css := readStylesheet(t)
	btn := cssRule(t, css, ".btn")
	if !strings.Contains(btn, "background: var(--card-bg)") || !strings.Contains(btn, "color: var(--text)") {
		t.Errorf(".btn must default to a themed background and text colour, got:%s", btn)
	}
	if badge := cssRule(t, css, ".badge"); !strings.Contains(badge, "background:") {
		t.Errorf(".badge has white text and no background:%s", badge)
	}
}

// The ellipsis for long source URLs must not apply to article titles.
func TestAdminCSS_TitlesAreNotTruncated(t *testing.T) {
	css := readStylesheet(t)
	if strings.Contains(css, ".admin-table td a[href]") {
		t.Error("an unscoped `.admin-table td a[href]` rule truncates every admin link, titles included")
	}
}
