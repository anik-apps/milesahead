package web

import (
	"fmt"
	"strings"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// Template helpers for the admin pages.
func init() {
	templateFuncs["statusLabel"] = statusLabel
	templateFuncs["adminTime"] = adminTime
	templateFuncs["flagRef"] = flagRef
	templateFuncs["cardRowData"] = cardRowData
	// Replaced per App by loadTemplates with versioned URLs.
	templateFuncs["adminStylesheetURL"] = func() string { return adminStylesheetPath }
	templateFuncs["adminScriptURL"] = func() string { return adminScriptPath }
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

// flagRef is what a flag's Resolve button needs: the card and the field.
func flagRef(cardID int64, f db.FlaggedField) struct {
	ID    int64
	Field string
} {
	return struct {
		ID    int64
		Field string
	}{cardID, f.Field}
}

// cardRowData is a cards-list row with the page's data alongside, for the
// row's CSRF field.
func cardRowData(c adminCardRow, root interface{}) struct {
	adminCardRow
	CSRF interface{}
} {
	return struct {
		adminCardRow
		CSRF interface{}
	}{c, root}
}
