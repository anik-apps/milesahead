package web

import (
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
