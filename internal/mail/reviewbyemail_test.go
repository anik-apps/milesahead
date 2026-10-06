package mail

import (
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
)

func TestIMAPLooksUnset(t *testing.T) {
	real := config.IMAPConfig{Host: "imap.gmail.com", Port: 993, User: "review@example.com", Password: "app-password"}
	if IMAPLooksUnset(real) {
		t.Error("a real-looking mailbox counts as set")
	}
	for name, c := range map[string]config.IMAPConfig{
		"deploy placeholders": {Host: "localhost", Port: 993, User: "placeholder", Password: "placeholder"},
		"no host":             {User: "u", Password: "p"},
		"loopback":            {Host: "127.0.0.1", User: "u", Password: "p"},
		"placeholder pw":      {Host: "imap.gmail.com", User: "u", Password: "placeholder"},
		"unexpanded env":      {Host: "imap.gmail.com", User: "${IMAP_USER}", Password: "p"},
		"no password":         {Host: "imap.gmail.com", User: "u"},
	} {
		if !IMAPLooksUnset(c) {
			t.Errorf("%s: should look unset", name)
		}
	}
}

// Review by email is off in every case until it is built: with IMAP unset
// it says so, and with a mailbox configured it still does not run, since a
// reply cannot be matched to an article.
func TestReviewByEmailIsOff(t *testing.T) {
	if on, why := ReviewByEmail(config.IMAPConfig{Host: "localhost", User: "placeholder", Password: "placeholder"}); on || why != "mail.imap not configured" {
		t.Errorf("unset: on %v, %q", on, why)
	}
	if on, why := ReviewByEmail(config.IMAPConfig{Host: "imap.gmail.com", User: "u@example.com", Password: "pw"}); on || why == "" {
		t.Errorf("configured: on %v, %q", on, why)
	}
}
