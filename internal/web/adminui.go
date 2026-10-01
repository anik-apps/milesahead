package web

import (
	"fmt"
	"strings"
	"time"
)

// Template helpers for the admin pages.
func init() {
	templateFuncs["statusLabel"] = statusLabel
	templateFuncs["adminTime"] = adminTime
}

// statusLabel turns a stored status such as "auto_approved" into what the
// admin pages show: "Auto-approved".
func statusLabel(status string) string {
	if status == "" {
		return ""
	}
	s := strings.ReplaceAll(status, "_", "-")
	return strings.ToUpper(s[:1]) + s[1:]
}

// adminTime formats a stored RFC 3339 timestamp (card flags and updates keep
// them as strings) the way the rest of the admin shows times, or returns it
// unchanged if it is not one.
func adminTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format(adminTimeFormat)
}

// ago says how long before now t was, for the admin's status lines: "just
// now", "5 min ago", "3 h ago", "2 days ago". Zero t is "never".
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
