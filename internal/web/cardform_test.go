package web

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/db"
)

func createEditableCard(t *testing.T, database *db.Database) *db.ReferencePage {
	t.Helper()
	sections, _ := json.Marshal([]db.ReferenceSection{
		{Key: "overview", Title: "Overview", Content: "<p>Old overview.</p>"},
		{Key: "fees", Title: "Fees", Content: "<p>$695.</p>"},
	})
	page := &db.ReferencePage{
		Slug: "test-platinum", Title: "Test Platinum", Category: "travel", Issuer: "Amex",
		SectionsJSON: string(sections), RecentChangesJSON: "[]", PendingUpdatesJSON: "[]",
		FlaggedFieldsJSON: "[]", KeywordsJSON: `["Platinum"]`, Status: "published",
		AnnualFeeAmount: 695, EarningRatesJSON: `{"flights":{"rate":"5x"}}`,
		CreditsJSON: `{"uber":{"amount":"$200/yr"}}`, BenefitsJSON: `{"lounge":{"value":"Centurion"}}`,
		ProtectionsJSON: "{}",
	}
	id, err := database.CreateReferencePage(page)
	if err != nil {
		t.Fatal(err)
	}
	page.ID = id
	return page
}

// cardFormValues is the edit form as the browser would post it for page.
func cardFormValues(page *db.ReferencePage) url.Values {
	return url.Values{
		"title": {page.Title}, "slug": {page.Slug}, "category": {page.Category}, "issuer": {page.Issuer},
		"status": {page.Status}, "keywords": {"Platinum"}, "annual_fee_amount": {strconv.Itoa(page.AnnualFeeAmount)},
		"earning_rates_json": {page.EarningRatesJSON}, "credits_json": {page.CreditsJSON},
		"benefits_json": {page.BenefitsJSON}, "protections_json": {page.ProtectionsJSON},
		"section_key[]": {"overview", "fees"}, "section_title[]": {"Overview", "Fees"},
		"section_content[]": {"<p>Old overview.</p>", "<p>$695.</p>"},
	}
}

// One bad JSON field must not cost the admin the rest of the edit: the form
// comes back with everything as typed, and the error next to the field.
func TestCardEdit_InvalidJSONReshowsTheForm(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	form := cardFormValues(page)
	form.Set("title", "Test Platinum (renamed)")
	form.Set("credits_json", `{"hotel": {"amount": "$600/yr",}}`)
	form.Set("section_content[]", "<p>Rewritten overview.</p>")
	rec := doAdminPOST(t, app, "/admin/cards/"+strconv.FormatInt(page.ID, 10)+"/edit", session, csrf, form)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	body := html.UnescapeString(rec.Body.String())
	for _, want := range []string{
		"Not saved: Credits is not valid JSON",
		`value="Test Platinum (renamed)"`,
		`{"hotel": {"amount": "$600/yr",}}`,
		"<p>Rewritten overview.</p>",
		`<form method="POST" action="/admin/cards/` + strconv.FormatInt(page.ID, 10) + `/edit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("re-shown form is missing %q", want)
		}
	}
	stored, _ := database.GetReferencePage(page.ID)
	if stored.Title != "Test Platinum" || stored.CreditsJSON != page.CreditsJSON {
		t.Errorf("a failed save changed the card: title %q credits %q", stored.Title, stored.CreditsJSON)
	}
}

func TestCardCreate_MissingTitleReshowsTheForm(t *testing.T) {
	app, _ := realTemplateApp(t)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	form := url.Values{"title": {""}, "slug": {"new-card"}, "issuer": {"Chase"}, "keywords": {"Freedom, Flex"}}
	rec := doAdminPOST(t, app, "/admin/cards", session, csrf, form)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Title is required.", `value="new-card"`, `value="Freedom, Flex"`, `action="/admin/cards"`} {
		if !strings.Contains(body, want) {
			t.Errorf("re-shown new-card form is missing %q", want)
		}
	}
}

// Emptying a JSON box clears the data; it used to keep the old value.
func TestCardEdit_ClearingAFieldClearsIt(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))

	form := cardFormValues(page)
	form.Set("credits_json", "")
	form.Set("annual_fee_amount", "")
	rec := doAdminPOST(t, app, "/admin/cards/"+strconv.FormatInt(page.ID, 10)+"/edit", session, csrf, form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/cards/"+strconv.FormatInt(page.ID, 10)+"/edit?msg=saved" {
		t.Errorf("Location %q: a save should return to the card", loc)
	}
	stored, _ := database.GetReferencePage(page.ID)
	if stored.CreditsJSON != "{}" || stored.AnnualFeeAmount != 0 {
		t.Errorf("credits %q fee %d, want {} and 0", stored.CreditsJSON, stored.AnnualFeeAmount)
	}
	if stored.EarningRatesJSON != page.EarningRatesJSON {
		t.Errorf("untouched field changed: %q", stored.EarningRatesJSON)
	}
	if body := doAdminGET(t, app, rec.Header().Get("Location"), session).Body.String(); !strings.Contains(body, "Saved.") {
		t.Error("edit page does not confirm the save")
	}
}

// Approving an update for a section the card no longer has must not throw
// the proposed text away.
func TestCardUpdateApprove_MissingSectionFailsVisibly(t *testing.T) {
	app, database := realTemplateApp(t)
	page := createEditableCard(t, database)
	updates, _ := json.Marshal([]db.PendingUpdate{
		{ID: 1, ArticleID: 9, ArticleTitle: "Fee news", SectionKey: "credits_and_perks",
			CurrentContent: "<p>old</p>", ProposedContent: "<p>New Resy credit.</p>", Summary: "Resy"},
		{ID: 2, ArticleID: 9, ArticleTitle: "Fee news", SectionKey: "fees",
			CurrentContent: "<p>$695.</p>", ProposedContent: "<p>$895.</p>", Summary: "Fee"},
	})
	page.PendingUpdatesJSON = string(updates)
	if err := database.UpdateReferencePage(page); err != nil {
		t.Fatal(err)
	}
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	base := "/admin/cards/" + strconv.FormatInt(page.ID, 10) + "/updates"

	rec := doAdminPOST(t, app, base+"/1/approve", session, csrf, nil)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=missing_section&uid=1") {
		t.Fatalf("approve: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	stored, _ := database.GetReferencePage(page.ID)
	var left []db.PendingUpdate
	json.Unmarshal([]byte(stored.PendingUpdatesJSON), &left)
	if len(left) != 2 {
		t.Fatalf("pending updates = %d, want both kept", len(left))
	}
	body := html.UnescapeString(doAdminGET(t, app, rec.Header().Get("Location"), session).Body.String())
	if !strings.Contains(body, `no longer has a section with the key "credits_and_perks"`) {
		t.Error("updates page does not explain why nothing was applied")
	}

	// An update whose section exists still applies.
	rec = doAdminPOST(t, app, base+"/2/approve", session, csrf, nil)
	if !strings.Contains(rec.Header().Get("Location"), "msg=approved") {
		t.Fatalf("approve existing section: %q", rec.Header().Get("Location"))
	}
	stored, _ = database.GetReferencePage(page.ID)
	if !strings.Contains(stored.SectionsJSON, "$895.") {
		t.Error("fees section not updated")
	}
}

func TestEditSource_ClearsIntervalAndRejectsBadInput(t *testing.T) {
	app, database := setupAdminTestApp(t)
	interval, rss := 30, "https://example.com/feed"
	id, err := database.CreateSource(&db.Source{Name: "Feed", URL: "https://example.com", RSSURL: &rss, ScanInterval: &interval, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	session, csrf := getSessionAndCSRF(loginAndGetSession(t, app))
	path := "/admin/sources/" + strconv.FormatInt(id, 10) + "/edit"

	rec := doAdminPOST(t, app, path, session, csrf, url.Values{"name": {"Feed"}, "url": {"https://example.com"}, "rss_url": {""}, "scan_interval": {""}})
	if loc := rec.Header().Get("Location"); loc != "/admin/sources?msg=saved" {
		t.Fatalf("Location %q", loc)
	}
	src, _ := database.GetSource(id)
	if src.ScanInterval != nil || src.RSSURL != nil {
		t.Errorf("cleared fields kept values: interval %v rss %v", src.ScanInterval, src.RSSURL)
	}

	for form, code := range map[string]string{"": "required", "abc": "interval", "0": "interval"} {
		name := "Feed"
		if form == "" {
			name = ""
		}
		rec := doAdminPOST(t, app, path, session, csrf, url.Values{"name": {name}, "url": {"https://example.com"}, "scan_interval": {form}})
		if loc := rec.Header().Get("Location"); loc != "/admin/sources?error="+code {
			t.Errorf("interval %q name %q: Location %q, want error=%s", form, name, loc, code)
		}
	}
	if src, _ := database.GetSource(id); src.Name != "Feed" {
		t.Errorf("a rejected edit changed the source: %q", src.Name)
	}
}
