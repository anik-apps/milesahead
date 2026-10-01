package web

import (
	"encoding/json"
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

	if pages, err := app.db.ListReferencePages(""); err == nil {
		for _, p := range pages {
			nav.CardWork += jsonArrayLen(p.PendingUpdatesJSON) + jsonArrayLen(p.FlaggedFieldsJSON)
		}
	}

	if nls, err := app.db.ListNewsletters(); err == nil {
		for _, nl := range nls {
			if nl.Status == "sent" {
				continue
			}
			switch nl.Status {
			case "approved", "auto_approved":
				if d, err := time.Parse("2006-01-02", nl.Date); err == nil {
					nav.Newsletter = d.Format("Mon")
				}
			case "draft", "hold":
				nav.Newsletter = statusLabel(nl.Status)
				nav.NewsletterAlert = true
			}
			break
		}
	}

	if sources, err := app.db.ListAllSources(); err == nil {
		now := time.Now()
		if health, err := app.db.ListSourceHealth(now.Add(-sourceRecentWindow)); err == nil {
			for _, s := range sources {
				h := health[s.ID]
				if s.Active && (h.LastArticleAt.IsZero() || now.Sub(h.LastArticleAt) > sourceStaleAfter) {
					nav.StaleSources++
				}
			}
		}
	}
	return nav
}

// jsonArrayLen is the length of a JSON array, or 0.
func jsonArrayLen(s string) int {
	if s == "" || s == "[]" {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return 0
	}
	return len(items)
}
