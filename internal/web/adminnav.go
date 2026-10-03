package web

import (
	"log/slog"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
)

// adminNav is the admin bar: which tab is current, and the counts on the
// tabs, so work waiting on another page is visible from every page.
type adminNav struct {
	Tab string
	// Decide counts articles stuck until the admin acts: rewrites that gave
	// up, and unpublished articles. Ready counts pending and draft articles.
	Decide, Ready int
	// CardWork counts pending card updates plus flagged card fields.
	CardWork int
	// Newsletter is a short note on the current issue for its tab: "Sat"
	// when it is set to go out, "Draft" or "Hold" when it will not.
	Newsletter      string
	NewsletterAlert bool
	// StaleSources counts active sources with nothing new for days.
	StaleSources int
	// VoteAlerts counts articles whose recent votes crossed the alert
	// threshold and that the admin has not acknowledged (Published page).
	VoteAlerts int
}

// adminTabs maps each admin page template to its tab. Pages that sit under
// another list (Linked cards) set "NavTab" in their data instead.
var adminTabs = map[string]string{
	"dashboard.html":     "review",
	"published.html":     "published",
	"sources.html":       "sources",
	"subscribers.html":   "subscribers",
	"admin-cards.html":   "cards",
	"card-edit.html":     "cards",
	"card-updates.html":  "cards",
	"newsletter.html":    "newsletter",
	"settings.html":      "settings",
	"article-cards.html": "review",
}

// adminNavFor builds the admin bar for page name. Each count is best effort:
// one that cannot be read is left at zero rather than failing the page.
func (app *App) adminNavFor(name string, data map[string]interface{}) *adminNav {
	nav := &adminNav{Tab: adminTabs[name]}
	if tab, ok := data["NavTab"].(string); ok && tab != "" {
		nav.Tab = tab
	}

	if counts, err := app.db.CountArticlesByStatus(); err == nil {
		nav.Ready = counts[db.StatusPending] + counts[db.StatusDraft]
		nav.Decide = counts[db.StatusUnpublished]
	} else {
		slog.Warn("admin nav: article counts", "error", err)
	}
	if app.articleManager != nil {
		if pending, err := app.db.ListPendingRevisions(); err == nil {
			for _, p := range pending {
				if p.Failures > 0 && app.articleManager.RevisionGaveUp(p.Failures) {
					nav.Decide++
				}
			}
		}
	}

	if n, err := app.db.CardWorkCount(); err == nil {
		nav.CardWork = n
	} else {
		slog.Warn("admin nav: card work count", "error", err)
	}

	if nl, err := app.db.UpcomingNewsletter(app.newsletterToday()); err == nil && nl != nil {
		switch nl.Status {
		case "approved", "auto_approved":
			if d, err := time.Parse("2006-01-02", nl.Date); err == nil {
				nav.Newsletter = d.Format("Mon")
			}
		case "draft", "hold":
			nav.Newsletter = statusLabel(nl.Status)
			nav.NewsletterAlert = true
		}
	}

	if n, err := app.db.CountOpenVoteAlerts(); err == nil {
		nav.VoteAlerts = n
	} else {
		slog.Warn("admin nav: vote alerts", "error", err)
	}

	if n, err := app.db.StaleSourceCount(time.Now().Add(-sourceStaleAfter)); err == nil {
		nav.StaleSources = n
	} else {
		slog.Warn("admin nav: stale sources", "error", err)
	}
	return nav
}

// newsletterToday is today's date ("2006-01-02") in the newsletter's time
// zone, the zone its issues are dated in.
func (app *App) newsletterToday() string {
	now := time.Now
	if app.now != nil {
		now = app.now
	}
	return now().In(app.schedule.Location()).Format("2006-01-02")
}
