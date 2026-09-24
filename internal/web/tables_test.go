package web

import (
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

const scrollOpen = `<div class="table-scroll" role="region" tabindex="0" aria-label="`

func TestScrollTables(t *testing.T) {
	tests := []struct {
		name, label, in, want string
	}{
		{"no table", "X", "<p>No tables here.</p>", "<p>No tables here.</p>"},
		{"one table", "Comparison table", "<p>a</p><table><tr><td>1</td></tr></table><p>b</p>",
			"<p>a</p>" + scrollOpen + `Comparison table"><table><tr><td>1</td></tr></table></div><p>b</p>`},
		{"attributes and case", "T", `<TABLE class="x"><tr><td>1</td></tr></TABLE>`,
			scrollOpen + `T"><TABLE class="x"><tr><td>1</td></tr></TABLE></div>`},
		{"two tables", "T", "<table></table><p>x</p><table></table>",
			scrollOpen + `T"><table></table></div><p>x</p>` + scrollOpen + `T"><table></table></div>`},
		{"nested table stays in its parent's box", "T", "<table><tr><td><table></table></td></tr></table>",
			scrollOpen + `T"><table><tr><td><table></table></td></tr></table></div>`},
		{"not a table tag", "T", "<tablet>x</tablet>", "<tablet>x</tablet>"},
		{"unclosed table is left alone", "T", "<table><tr><td>1</td></tr>", "<table><tr><td>1</td></tr>"},
		{"characters whose lowercase has a different length", "T", "\u212A \u0130<table></table>\u212A",
			"\u212A \u0130" + scrollOpen + `T"><table></table></div>` + "\u212A"},
		{"label is escaped", `Fees & "Costs" table`, "<table></table>",
			scrollOpen + `Fees &amp; &#34;Costs&#34; table"><table></table></div>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scrollTables(tt.label, tt.in); got != tt.want {
				t.Errorf("scrollTables()\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

// Integration: a card section's comparison table renders inside its own
// labelled scroll box, so it cannot widen the page on a phone.
func TestHandleCard_WrapsSectionTables(t *testing.T) {
	app := setupRealTemplateCardApp(t)
	if _, err := app.db.CreateReferencePage(&db.ReferencePage{
		Slug: "wide-table-card", Title: "Wide Table Card", Category: "travel", Issuer: "Chase",
		SectionsJSON: `[{"key":"competitor_comparison","title":"Competitor Comparison","content":` +
			`"<p>How it stacks up.</p><table><tr><th>Card</th><th>Fee</th></tr><tr><td>A</td><td>$95</td></tr></table>"}]`,
		ScoutsVerdict: "Fine.", ImageURL: "/static/img/cards/wide-table-card.png", Status: "published",
	}); err != nil {
		t.Fatal(err)
	}
	body := getCardPage(t, app, "wide-table-card")
	want := scrollOpen + `Competitor Comparison table"><table>`
	if !strings.Contains(body, want) {
		t.Errorf("the section table should be wrapped: want %s", want)
	}
	if !strings.Contains(body, "</table></div>") {
		t.Error("the scroll box should close right after the table")
	}
}
