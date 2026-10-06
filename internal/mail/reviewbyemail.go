package mail

import (
	"strings"

	"github.com/kumaranik/milesahead/internal/config"
)

// IMAPLooksUnset reports whether c cannot be a real mailbox: no host, a
// host on this machine, or a user or password that is empty, "placeholder"
// or an unexpanded "${...}". The deploy config ships localhost and
// placeholders.
func IMAPLooksUnset(c config.IMAPConfig) bool {
	switch strings.TrimSpace(c.Host) {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	for _, v := range []string{c.User, c.Password} {
		if v == "" || v == "placeholder" || strings.HasPrefix(v, "${") {
			return true
		}
	}
	return false
}

// ReviewByEmail says whether reviewing articles by replying to an email
// runs, and why not. It does not yet, whatever the config: nothing sends
// review emails (SendAdminNotification has no caller) and no token resolver
// matches a reply to its article, so a poller would only mark the
// mailbox's unread mail as read and act on none of it. ROADMAP.md
// ("Review by email") lists what it would take.
func ReviewByEmail(c config.IMAPConfig) (on bool, why string) {
	if IMAPLooksUnset(c) {
		return false, "mail.imap not configured"
	}
	return false, "replies cannot be matched to articles yet; see ROADMAP.md"
}
