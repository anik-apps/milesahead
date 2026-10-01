package db_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/seeddata"
)

// Every reader query becomes quoted words joined with AND, so no FTS5
// syntax gets through; words of three letters or more, and the last word,
// match as prefixes; nicknames are OR'd in.
func TestParseSearchQuery_Match(t *testing.T) {
	cases := []struct {
		in, and, or string
	}{
		{"sapph", `"sapph"*`, `"sapph"*`},
		{"Reserve Sapphire", `"reserve"* AND "sapphire"*`, `"reserve"* OR "sapphire"*`},
		{"amex plat", `("amex"* OR "american express") AND ("plat"* OR "platinum")`, ``},
		{"American Express Platinum", `("american express" OR "amex") AND "platinum"*`, ``},
		{"csr", `("csr"* OR "chase sapphire reserve")`, ``},
		{"transfer bonuses", `"transfer"* AND "bonus"*`, ``},
		{"lounges", `"lounge"*`, ``},
		{"venture x", `"venture"* AND "x"`, ``},
		{"the card", `"the"* AND "card"*`, ``}, // only stopwords: keep them
		{"Hilton's Aspire card", `"hilton"* AND "aspire"*`, ``},
		{"hilton’s? aspire", `"hilton"* AND "aspire"*`, ``},
		{"bank of america", `("bank of america" OR "boa")`, ``},
		{"card of the year", `"year"*`, ``},
		{`"quoted" NEAR(a b) OR -x title:y`, `"quoted"* AND "near"* AND "b" AND "x" AND "title"* AND "y"`, ``},
		{"ab", `"ab"*`, ``},
		{"ab cd", `"ab" AND "cd"*`, ``},
		{"!!!", ``, ``},
	}
	for _, c := range cases {
		q := db.ParseSearchQuery(c.in)
		if got := q.Match(false); got != c.and {
			t.Errorf("ParseSearchQuery(%q).Match(false) = %s, want %s", c.in, got, c.and)
		}
		if c.or != "" {
			if got := q.Match(true); got != c.or {
				t.Errorf("ParseSearchQuery(%q).Match(true) = %s, want %s", c.in, got, c.or)
			}
		}
	}
	for in, want := range map[string]string{
		"sapph":                     ``,
		"transfer bonuses":          `"transfer bonus"*`,
		"venture x":                 `"venture x"`,
		"American Express Platinum": `"american express platinum"*`,
		"world of hyatt":            `"world of hyatt"*`,
		"transfer bonuses offer":    `"transfer bonus offer"*`,
		"the hyatt transfers card":  `"hyatt transfer"*`,
	} {
		if got := db.ParseSearchQuery(in).Phrase(); got != want {
			t.Errorf("ParseSearchQuery(%q).Phrase() = %s, want %s", in, got, want)
		}
	}
	if !db.ParseSearchQuery("!!!").Empty() {
		t.Error("a query with no words should be empty")
	}
	if got := db.ParseSearchQuery("a b c d e f g h i j k").Words(); len(got) != 8 {
		t.Errorf("a long query should keep 8 words, got %v", got)
	}
}

// seededDB opens a database with the repository's 52 cards and the made-up
// articles.
func seededDB(t *testing.T) (*db.Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "seeded.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := seeddata.Cards(d); err != nil {
		t.Fatal(err)
	}
	if err := seeddata.SeedArticles(d, time.Now()); err != nil {
		t.Fatal(err)
	}
	return d, path
}

func cardSlugs(rs []db.SearchResult) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Slug)
	}
	return out
}

// Pages whose title holds every word come back in title-match searches, and
// a card's own page ranks above pages that only mention it.
func TestSearchCards_TitleMatchAndRanking(t *testing.T) {
	d, _ := seededDB(t)
	cases := []struct {
		query string
		want  []string // title matches, best first
	}{
		{"sapph", []string{"chase-sapphire-reserve", "chase-sapphire-preferred", "chase-sapphire-reserve-business"}},
		{"amex platinum", []string{"amex-platinum", "amex-business-platinum"}},
		{"reserve sapphire", []string{"chase-sapphire-reserve", "chase-sapphire-reserve-business"}},
		{"amex plat", []string{"amex-platinum", "amex-business-platinum"}},
		{"csr", []string{"chase-sapphire-reserve", "chase-sapphire-reserve-business"}},
		{"csp", []string{"chase-sapphire-preferred"}},
		{"hyatt transfer", nil},
	}
	for _, c := range cases {
		got, err := d.SearchCardTitles(db.ParseSearchQuery(c.query), 10)
		if err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		gotSlugs := cardSlugs(got)
		if len(c.want) == 0 {
			if len(gotSlugs) != 0 {
				t.Errorf("%q: no card title has every word, got %v", c.query, gotSlugs)
			}
			continue
		}
		if !sameSet(gotSlugs, c.want) || gotSlugs[0] != c.want[0] {
			t.Errorf("%q: title matches = %v, want %v (first %s)", c.query, gotSlugs, c.want, c.want[0])
		}
	}

	// Many card pages mention "Amex Platinum"; every one is found.
	all, err := d.SearchCards(db.ParseSearchQuery("amex platinum").Match(false), "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(cardSlugs(all), "amex-platinum") || len(all) < 5 {
		t.Errorf("amex platinum: want the Platinum among many pages, got %v", cardSlugs(all))
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

func articleSlugs(t *testing.T, d *db.Database, query string) []string {
	t.Helper()
	q := db.ParseSearchQuery(query)
	rs, err := d.SearchArticles(q.Match(false), q.Phrase(), time.Now().Format("2006-01-02"), 100, 0)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return cardSlugs(rs)
}

// Word order, plurals and partial words no longer change what is found.
func TestSearchArticles_QueryForms(t *testing.T) {
	d, _ := seededDB(t)
	same := [][2]string{
		{"reserve sapphire", "sapphire reserve"},
		{"lounge", "lounges"},
		{"transfer bonus", "transfer bonuses"},
		{"sapph", "sapphire"},
	}
	for _, p := range same {
		a, b := articleSlugs(t, d, p[0]), articleSlugs(t, d, p[1])
		if len(a) == 0 || !sameSet(a, b) {
			t.Errorf("%q found %v but %q found %v", p[0], a, p[1], b)
		}
	}
	for query, want := range map[string]string{
		"hyatt transfer":            "bilt-hyatt",
		"amex plat":                 "plat-saks",
		"american express platinum": "plat-saks", // the story only says "Amex Platinum"
	} {
		got := articleSlugs(t, d, query)
		if want != "" && !contains(got, want) {
			t.Errorf("%q: want %s among %v", query, want, got)
		}
	}
}

// Pages with the words side by side rank above pages that only have them
// somewhere: nearly every card page says "transfer" and "bonus", but the
// ones that say "transfer bonus" come first.
func TestSearchCards_PhraseFirst(t *testing.T) {
	d, _ := seededDB(t)
	q := db.ParseSearchQuery("transfer bonus")
	all, err := d.SearchCards(q.Match(false), q.Phrase(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := d.SearchCards(q.Phrase(), "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(phrase) == 0 || len(phrase) >= len(all) {
		t.Fatalf("want a few phrase matches among many: %d of %d", len(phrase), len(all))
	}
	if got, want := cardSlugs(all[:len(phrase)]), cardSlugs(phrase); !sameSet(got, want) {
		t.Errorf("first %d results = %v, want the phrase matches %v", len(phrase), got, want)
	}
}

// Within a search, offers that have ended come after live ones.
func TestSearchArticles_EndedOffersLast(t *testing.T) {
	d, _ := seededDB(t)
	today := time.Now().Format("2006-01-02")
	rs, err := d.SearchArticles(db.ParseSearchQuery("transfer bonus").Match(false), "", today, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenEnded := false
	ended := 0
	for _, r := range rs {
		isEnded := r.ExpiresOn != "" && r.ExpiresOn < today
		if isEnded {
			seenEnded, ended = true, ended+1
		} else if seenEnded {
			t.Fatalf("live offer %s listed after an ended one: %v", r.Slug, cardSlugs(rs))
		}
	}
	if ended == 0 || ended == len(rs) {
		t.Fatalf("want a mix of live and ended offers, got %d of %d ended", ended, len(rs))
	}
	n, err := d.CountSearchArticles(db.ParseSearchQuery("transfer bonus").Match(false))
	if err != nil || n != len(rs) {
		t.Errorf("CountSearchArticles = %d, %v; want %d", n, err, len(rs))
	}
}

// The excerpt comes from the field that matched, with the match marked.
func TestSearchArticles_ExcerptFromMatchedField(t *testing.T) {
	d, _ := seededDB(t)
	today := time.Now().Format("2006-01-02")
	rs, err := d.SearchArticles(db.ParseSearchQuery("Centurion food").Match(false), "", today, 10, 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("want one result, got %v, %v", rs, err)
	}
	if !strings.Contains(rs[0].Excerpt, "<mark>food</mark>") {
		t.Errorf("excerpt should come from the body that matched: %q", rs[0].Excerpt)
	}
	rs, err = d.SearchArticles(db.ParseSearchQuery("Kyoto renovation").Match(false), "", today, 10, 0)
	if err != nil || len(rs) != 1 || !strings.Contains(rs[0].Excerpt, "<mark>renovation</mark>") {
		t.Fatalf("Kyoto renovation: %v, %v", rs, err)
	}
	if rs[0].PublishedAt.IsZero() {
		t.Error("results should carry the publish date")
	}
}

// rawMatch runs an FTS5 expression as written.
func rawMatch(t *testing.T, d *db.Database, match string) []string {
	t.Helper()
	rs, err := d.SearchArticles(match, "", "2000-01-01", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return cardSlugs(rs)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// oldFTSSchemas are the indexes as production built them before porter
// stemming.
var oldFTSSchemas = []string{
	`CREATE VIRTUAL TABLE articles_fts USING fts5(title, tldr, content)`,
	`CREATE VIRTUAL TABLE cards_fts USING fts5(title, sections_text, scouts_verdict)`,
}

// downgradeFTS swaps both indexes for the old, unstemmed ones, filled with
// every source row.
func downgradeFTS(t *testing.T, d *db.Database) {
	t.Helper()
	for _, stmt := range []string{
		`DROP TABLE articles_fts`, `DROP TABLE cards_fts`,
		oldFTSSchemas[0], oldFTSSchemas[1],
		`INSERT INTO articles_fts(rowid, title, tldr, content) SELECT id, title, COALESCE(tldr,''), content FROM generated_articles`,
		`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) SELECT id, title, sections_json, scouts_verdict FROM reference_pages`,
	} {
		if _, err := d.DB().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
}

type ftsState struct {
	articleSQL, cardSQL string
	articleRows         []int64
	cardRows            []int64
}

func readFTS(t *testing.T, d *db.Database) ftsState {
	t.Helper()
	var s ftsState
	d.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'articles_fts'`).Scan(&s.articleSQL)
	d.DB().QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'cards_fts'`).Scan(&s.cardSQL)
	for _, q := range []struct {
		sql string
		out *[]int64
	}{
		{`SELECT rowid FROM articles_fts ORDER BY rowid`, &s.articleRows},
		{`SELECT rowid FROM cards_fts ORDER BY rowid`, &s.cardRows},
	} {
		rows, err := d.DB().Query(q.sql)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			*q.out = append(*q.out, id)
		}
		rows.Close()
	}
	return s
}

func sourceIDs(t *testing.T, d *db.Database, table string) []int64 {
	t.Helper()
	rows, err := d.DB().Query(`SELECT id FROM ` + table + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

// A database indexed before stemming is rebuilt once at startup: every row
// is kept, stemming works, and a second start changes nothing. The write
// paths keep the new index current.
func TestOpen_RebuildsOldSearchIndex(t *testing.T) {
	d, path := seededDB(t)
	// An unpublished article is indexed too (search filters by status).
	if _, err := d.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Draft About Lounges", Slug: "draft-lounges", Content: "<p>Draft body</p>", Type: "summary",
		Status: db.StatusDraft, SourcesJSON: "[]", RevisionNumber: 1, KeyDetails: "[]", Links: "[]", RelatedCardsJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	downgradeFTS(t, d)
	if got := rawMatch(t, d, `"renovations"`); len(got) != 0 {
		t.Fatalf("the old index should not stem: %v", got)
	}
	d.Close()

	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	after := readFTS(t, d)
	for _, s := range []string{after.articleSQL, after.cardSQL} {
		if !strings.Contains(s, "porter unicode61 remove_diacritics 2") {
			t.Errorf("index not rebuilt with the new tokenizer: %s", s)
		}
	}
	if want := sourceIDs(t, d, "generated_articles"); !reflect.DeepEqual(after.articleRows, want) {
		t.Errorf("articles_fts rows = %v, want every article %v", after.articleRows, want)
	}
	if want := sourceIDs(t, d, "reference_pages"); !reflect.DeepEqual(after.cardRows, want) {
		t.Errorf("cards_fts rows = %d, want every card (%d)", len(after.cardRows), len(want))
	}
	if got := rawMatch(t, d, `"renovations"`); !reflect.DeepEqual(got, []string{"hyatt-kyoto"}) {
		t.Errorf("after the rebuild \"renovations\" should find \"renovation\": %v", got)
	}

	// The write paths keep the rebuilt index current.
	a, err := d.GetGeneratedArticleBySlug("hyatt-kyoto")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB().Exec(`UPDATE generated_articles SET content = '<p>Now about kangaroos</p>' WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncArticleFTS(a.ID); err != nil {
		t.Fatal(err)
	}
	if got := articleSlugs(t, d, "kangaroo"); !reflect.DeepEqual(got, []string{"hyatt-kyoto"}) {
		t.Errorf("SyncArticleFTS should reindex with stemming: %v", got)
	}
	d.Close()

	// A second start leaves the index alone: a marker row planted in it
	// survives.
	d, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	plantSentinel(t, d)
	d.Close()
	d, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := rawMatch(t, d, `"zzsentinel"`); len(got) != 1 {
		t.Error("a second start should not rebuild the index")
	}
}

// plantSentinel replaces one article's index row with a word its source
// doesn't have, so a rebuild would remove it.
func plantSentinel(t *testing.T, d *db.Database) {
	t.Helper()
	a, err := d.GetGeneratedArticleBySlug("csp-100k")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM articles_fts WHERE rowid = ?`,
		`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, 'zzsentinel', '', '')`,
	} {
		if _, err := d.DB().Exec(stmt, a.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// Raising the index version rebuilds an index that already has the current
// tokenizer.
func TestOpen_RebuildsOnVersionChange(t *testing.T) {
	d, path := seededDB(t)
	plantSentinel(t, d)
	if _, err := d.DB().Exec(`UPDATE search_index_versions SET version = 1 WHERE name = 'articles_fts'`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := rawMatch(t, d, `"zzsentinel"`); len(got) != 0 {
		t.Error("an out-of-date version should rebuild the index")
	}
	if got := articleSlugs(t, d, "100k"); !contains(got, "csp-100k") {
		t.Errorf("the rebuilt index should hold the real row: %v", got)
	}
}

// The index holds plain text: entities are decoded, not indexed as numbers,
// and excerpts are escaped exactly once.
func TestSearch_PlainTextIndex(t *testing.T) {
	d, _ := seededDB(t)
	if _, err := d.CreateGeneratedArticle(&db.GeneratedArticle{
		Title: "Let&#39;s Talk About AT&amp;T and the Caf&eacute; Card", Slug: "entities",
		Content: "<p>Let&#39;s compare: AT&amp;T perks at the Café, x &lt; y.</p>", TLDR: "",
		Type: "summary", Status: db.StatusApproved, SourcesJSON: "[]", RevisionNumber: 1,
		KeyDetails: "[]", Links: "[]", RelatedCardsJSON: "[]",
	}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"39", "amp", "eacute"} {
		if got := rawMatch(t, d, `"`+q+`"`); len(got) != 0 {
			t.Errorf("%q should not be indexed, found %v", q, got)
		}
	}
	rs, err := d.SearchArticles(db.ParseSearchQuery("cafe perks").Match(false), "", "2000-01-01", 10, 0)
	if err != nil || len(rs) != 1 {
		t.Fatalf("accents should fold: %v %v", rs, err)
	}
	ex := rs[0].Excerpt
	for _, want := range []string{"Let&#39;s compare", "AT&amp;T", "<mark>perks</mark>", "<mark>Café</mark>", "x &lt; y"} {
		if !strings.Contains(ex, want) {
			t.Errorf("excerpt should contain %q: %q", want, ex)
		}
	}
	if strings.Contains(ex, "&amp;#39;") || strings.Contains(ex, "&amp;amp;") {
		t.Errorf("excerpt escaped twice: %q", ex)
	}
}

// An index emptied out (or a database seeded straight into its tables) is
// refilled at startup.
func TestOpen_RefillsEmptySearchIndex(t *testing.T) {
	d, path := seededDB(t)
	if _, err := d.DB().Exec(`DELETE FROM cards_fts`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got, want := readFTS(t, d).cardRows, sourceIDs(t, d, "reference_pages"); !reflect.DeepEqual(got, want) {
		t.Errorf("cards_fts has %d rows, want %d", len(got), len(want))
	}
}
