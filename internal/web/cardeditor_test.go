package web

import (
	"encoding/json"
	"html"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

// Integration, real templates: a flag shows on the field it is about, with
// a Resolve button; a flag on a name the editor has no box for is listed on
// its own; sections show their pending updates.
func TestCardEditor_FlagsOnTheirFields(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	updates, _ := json.Marshal([]db.PendingUpdate{{ID: 1, SectionKey: "fees", CurrentContent: "<p>$695.</p>", ProposedContent: "<p>$895.</p>"}})
	page.PendingUpdatesJSON = string(updates)
	database.UpdateReferencePage(page)
	database.AppendFlaggedField(page.ID, "annual_fee_amount", "Renewal fee changes in January")
	database.AppendFlaggedField(page.ID, "lounge_policy", "Old name for a field")

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := html.UnescapeString(doAdminGET(t, app, "/admin/cards/"+itoa(page.ID)+"/edit", session).Body.String())

	fee := body[strings.Index(body, `<label for="annual_fee_amount">`):]
	fee = fee[:strings.Index(fee, "</div>\n")]
	for _, want := range []string{"Renewal fee changes in January", `formaction="/admin/cards/` + itoa(page.ID) + `/resolve-flag"`, `name="field" value="annual_fee_amount"`} {
		if !strings.Contains(fee, want) {
			t.Errorf("annual fee field missing %q:\n%s", want, fee)
		}
	}
	if !strings.Contains(body, `class="field flagged"`) {
		t.Error("flagged field is not marked")
	}
	if !strings.Contains(body, "<code>lounge_policy</code>: Old name for a field") {
		t.Error("flag on an unknown field is not listed")
	}
	if !strings.Contains(body, "1 pending update</a>") {
		t.Error("the fees section does not show its pending update")
	}
	if !strings.Contains(body, `<select id="flag_field" name="field" required>`) || !strings.Contains(body, `<option value="annual_fee_amount">Annual fee</option>`) {
		t.Error("flagging should pick a field from a list, not type its name")
	}
	if strings.Count(body, "data-json") != 4 {
		t.Error("the four JSON boxes are not checked as you type")
	}
}

// Resolve posts the whole editor form to the resolve route, which only
// reads "field".
func TestCardEditor_ResolveFromTheField(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	database.AppendFlaggedField(page.ID, "annual_fee_amount", "check")
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	form := cardFormValues(page)
	form.Set("field", "annual_fee_amount")
	rec := doAdminPOST(t, app, "/admin/cards/"+itoa(page.ID)+"/resolve-flag", session, csrf, form)
	if loc := rec.Header().Get("Location"); loc != "/admin/cards/"+itoa(page.ID)+"/edit?msg=resolved" {
		t.Fatalf("Location %q", loc)
	}
	if p, _ := database.GetReferencePage(page.ID); p.FlaggedFieldsJSON != "[]" {
		t.Errorf("flags left: %s", p.FlaggedFieldsJSON)
	}
}

// Card updates are shown as a word diff, link their published source
// article, and cannot be approved when their section is gone.
func TestCardUpdates_WordDiff(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	src := createPendingArticle(t, database, "Fee Goes Up")
	database.UpdateArticleStatus(src, db.StatusApproved)
	srcArticle, _ := database.GetGeneratedArticle(src)
	updates, _ := json.Marshal([]db.PendingUpdate{
		{ID: 1, ArticleID: src, ArticleTitle: "Fee Goes Up", SectionKey: "fees", CurrentContent: "<p>The fee is $695.</p>", ProposedContent: "<p>The fee is $895.</p>", Summary: "Fee"},
		{ID: 2, ArticleID: src, ArticleTitle: "Fee Goes Up", SectionKey: "gone", CurrentContent: "<p>x</p>", ProposedContent: "<p>y</p>", Summary: "Gone"},
	})
	page.PendingUpdatesJSON = string(updates)
	database.UpdateReferencePage(page)

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/cards/"+itoa(page.ID)+"/updates", session).Body.String()
	for _, want := range []string{"<del>$695. </del><ins>$895. </ins>", "+1 / &minus;1 words", `href="/article/` + srcArticle.Slug + `"`, "No such section on this card"} {
		if !strings.Contains(body, want) {
			t.Errorf("updates page missing %q", want)
		}
	}
	if strings.Count(body, `disabled title="The card has no section with this key"`) != 1 {
		t.Error("only the update whose section is gone should have Approve disabled")
	}
}

// Drafts made before the detector saw whole sections were built from the first
// 1,000 bytes, so approving one deletes everything after. The page warns on a
// draft with more than one fewer list item or table row, and on one missing
// the section's last block, which also catches prose sections.
func TestCardUpdates_WarnsWhenADraftLosesContent(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	list := "<ul><li>Airport lounge access worldwide</li><li>B</li><li>C</li><li>No foreign transaction fees on purchases</li></ul>"
	prose := "<p>The card's opening paragraph.</p><p>A closing paragraph the cut-off model never saw.</p>"
	updates, _ := json.Marshal([]db.PendingUpdate{
		{ID: 1, ArticleTitle: "Cut off", SectionKey: "fees", CurrentContent: list,
			ProposedContent: "<ul><li>Airport lounge access worldwide</li><li>New credit</li></ul>", Summary: "Added a credit"},
		{ID: 2, ArticleTitle: "Prose cut off", SectionKey: "fees", CurrentContent: prose,
			ProposedContent: "<p>The card's opening paragraph, with one new fact.</p>", Summary: "Added a fact"},
		{ID: 3, ArticleTitle: "Fine", SectionKey: "fees", CurrentContent: list,
			ProposedContent: "<ul><li>Airport lounge access worldwide</li><li>B</li><li>C</li><li>New credit</li><li>No foreign transaction fees on purchases</li></ul>", Summary: "Added a credit"},
	})
	page.PendingUpdatesJSON = string(updates)
	if err := database.UpdateReferencePage(page); err != nil {
		t.Fatalf("saving pending updates: %v", err)
	}

	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/cards/"+itoa(page.ID)+"/updates", session).Body.String()
	draft := func(id string) string {
		t.Helper()
		start := strings.Index(body, `id="update-`+id+`"`)
		if start < 0 {
			t.Fatalf("update %s is not on the page", id)
		}
		rest := body[start+1:]
		if end := strings.Index(rest, `<article class="update-card"`); end >= 0 {
			rest = rest[:end]
		}
		return rest
	}
	if d := draft("1"); !strings.Contains(d, "2 fewer items than the current section.") ||
		!strings.Contains(d, "The end of the current section is missing") {
		t.Errorf("draft 1 lost two items and the last one, but the warning says otherwise:\n%s", d)
	}
	if d := draft("2"); !strings.Contains(d, "The end of the current section is missing") || strings.Contains(d, "fewer items") {
		t.Errorf("draft 2 lost the closing paragraph of a prose section; want only the ending warning:\n%s", d)
	}
	if d := draft("3"); strings.Contains(d, `role="note"`) {
		t.Errorf("draft 3 keeps everything and should carry no warning:\n%s", d)
	}
}

func TestAdminCards_NeedsAttentionFirst(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	database.AppendFlaggedField(page.ID, "slug", "x")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := doAdminGET(t, app, "/admin/cards", session).Body.String()
	i := strings.Index(body, "Needs attention")
	if i < 0 || !strings.Contains(body[i:], page.Title) {
		t.Fatal("flagged card is not under Needs attention")
	}
	if !strings.Contains(body, `data-confirm="Delete `+page.Title+` permanently?`) {
		t.Error("delete does not name the card it removes")
	}
}

// Every field the editor marks as flagged shows the flag's note and a
// Resolve button: the status field had the outline but not the note.
func TestCardEditor_StatusFlagShowsNoteAndResolve(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	database.AppendFlaggedField(page.ID, "status", "Card was discontinued in August")
	session, _ := getSessionAndCSRF(loginAndGetSession(t, app))
	body := html.UnescapeString(doAdminGET(t, app, "/admin/cards/"+itoa(page.ID)+"/edit", session).Body.String())

	field := body[strings.Index(body, `<label for="status">`):]
	field = field[:strings.Index(field, "</div>\n")]
	for _, want := range []string{"Card was discontinued in August", `name="field" value="status"`, `formaction="/admin/cards/` + itoa(page.ID) + `/resolve-flag"`} {
		if !strings.Contains(field, want) {
			t.Errorf("status field missing %q:\n%s", want, field)
		}
	}
	if strings.Contains(body, "<code>status</code>:") {
		t.Error("a status flag should show on the field, not in the unknown-field list")
	}
}
