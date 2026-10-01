package web

import "strings"

// Template helpers for the admin pages.
func init() {
	templateFuncs["statusLabel"] = statusLabel
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
