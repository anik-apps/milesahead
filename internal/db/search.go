package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
)

// --- Reader search ---
//
// A reader's query is never handed to FTS5 as written. ParseSearchQuery
// splits it into words and quotes each one, so FTS5 syntax (AND, NEAR, column
// filters, stray quotes) is always treated as text. Each word becomes a term
// that matches:
//
//   - the word as a prefix ("sapph" finds Sapphire), with a plural ending
//     trimmed first so "bonuses" also finds "bonus" (porter stems the two
//     differently);
//   - any nickname for it ("csr" also finds "chase sapphire reserve").
//
// Terms are joined with AND (every word must appear, in any order). The
// handler falls back to OR when AND finds nothing at all.

// maxSearchTerms caps how many words of a query are used.
const maxSearchTerms = 8

// searchStopwords are left out of a query that has other words: they appear
// in nearly every page, so requiring them only loses results.
var searchStopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"for": true, "to": true, "in": true, "on": true, "with": true, "vs": true,
	"card": true, "cards": true,
}

// searchNicknames maps a word readers type to the names pages use. The word
// itself is always searched too.
var searchNicknames = map[string][]string{
	"csr":  {"chase sapphire reserve"},
	"csp":  {"chase sapphire preferred"},
	"cfu":  {"chase freedom unlimited"},
	"cff":  {"chase freedom flex"},
	"cip":  {"ink business preferred"},
	"amex": {"american express"},
	"plat": {"platinum"},
	"vx":   {"venture x"},
	"c1":   {"capital one"},
	"boa":  {"bank of america"},
	"bofa": {"bank of america"},
	"ur":   {"ultimate rewards"},
	"mr":   {"membership rewards"},
	"ty":   {"thankyou"},
	"woh":  {"world of hyatt"},
	"pp":   {"priority pass"},
	"ftf":  {"foreign transaction fee"},
}

// searchPhraseNicknames maps a multi-word name to its short form, so a query
// for the long name also finds pages that only use the short one.
var searchPhraseNicknames = map[string]string{
	"american express": "amex",
	"capital one":      "c1",
	"bank of america":  "boa",
}

// searchTerm is one word (or recognised multi-word name) of a query.
type searchTerm struct {
	word   string   // the reader's word(s), lower-cased
	prefix bool     // match word as a prefix
	alts   []string // nickname phrases, matched exactly
}

// SearchQuery is a reader's query, parsed for FTS5.
type SearchQuery struct {
	terms []searchTerm
}

// ParseSearchQuery parses a reader's query. The last word is always matched
// as a prefix (the reader may still be typing it), and so is every word of
// three letters or more.
func ParseSearchQuery(q string) SearchQuery {
	q = strings.ToLower(q)
	q = strings.NewReplacer("'s ", " ", "’s ", " ").Replace(q + " ")
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	kept := make([]string, 0, len(words))
	for _, w := range words {
		if !searchStopwords[w] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		kept = words
	}

	var terms []searchTerm
	for i := 0; i < len(kept) && len(terms) < maxSearchTerms; i++ {
		// A multi-word name ("american express") is one term.
		if t, n, ok := phraseTerm(kept[i:]); ok {
			terms = append(terms, t)
			i += n - 1
			continue
		}
		w := kept[i]
		last := i == len(kept)-1
		t := searchTerm{word: w, prefix: len([]rune(w)) >= 3 || (last && len([]rune(w)) >= 2)}
		t.alts = searchNicknames[w]
		terms = append(terms, t)
	}
	return SearchQuery{terms: terms}
}

// phraseTerm reports whether words starts with a known multi-word name.
func phraseTerm(words []string) (searchTerm, int, bool) {
	for phrase, short := range searchPhraseNicknames {
		parts := strings.Fields(phrase)
		if len(words) < len(parts) {
			continue
		}
		if strings.Join(words[:len(parts)], " ") == phrase {
			return searchTerm{word: phrase, alts: []string{short}}, len(parts), true
		}
	}
	return searchTerm{}, 0, false
}

// Empty reports whether the query has no searchable words.
func (q SearchQuery) Empty() bool { return len(q.terms) == 0 }

// Words returns the parsed words, for display and tests.
func (q SearchQuery) Words() []string {
	out := make([]string, len(q.terms))
	for i, t := range q.terms {
		out[i] = t.word
	}
	return out
}

// Match returns the FTS5 expression requiring every term (or, with any, at
// least one). It returns "" for an empty query.
func (q SearchQuery) Match(any bool) string {
	parts := make([]string, len(q.terms))
	for i, t := range q.terms {
		parts[i] = t.expr()
	}
	op := " AND "
	if any {
		op = " OR "
	}
	return strings.Join(parts, op)
}

// TitleMatch is Match(false) limited to the title column: pages whose title
// contains every term.
func (q SearchQuery) TitleMatch() string {
	if q.Empty() {
		return ""
	}
	return "title : (" + q.Match(false) + ")"
}

// Phrase returns the query's words as one FTS5 phrase (the last word as a
// prefix when it is one), for ranking pages with the words side by side
// first. It returns "" for a one-word query.
func (q SearchQuery) Phrase() string {
	if len(q.terms) < 2 {
		return ""
	}
	words := make([]string, len(q.terms))
	for i, t := range q.terms {
		words[i] = t.word
	}
	last := q.terms[len(q.terms)-1]
	if last.prefix {
		words[len(words)-1] = singular(last.word)
		return ftsQuote(strings.Join(words, " ")) + "*"
	}
	return ftsQuote(strings.Join(words, " "))
}

// phraseFirst is an ORDER BY term putting rows whose index (table) matches
// phrase first; it is a constant when there is no phrase.
func phraseFirst(table, phrase string) (string, []any) {
	if phrase == "" {
		return "NULL", nil
	}
	return `f.rowid NOT IN (SELECT rowid FROM ` + table + ` WHERE ` + table + ` MATCH ?)`, []any{phrase}
}

// expr renders the term as FTS5: the word (as a prefix when set) OR'd with
// its nicknames.
func (t searchTerm) expr() string {
	var alts []string
	if t.prefix {
		alts = append(alts, ftsQuote(singular(t.word))+"*")
	} else {
		alts = append(alts, ftsQuote(t.word))
	}
	for _, a := range t.alts {
		alts = append(alts, ftsQuote(a))
	}
	if len(alts) == 1 {
		return alts[0]
	}
	return "(" + strings.Join(alts, " OR ") + ")"
}

// ftsQuote makes s an FTS5 string (a phrase when it has spaces).
func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// singular trims an English plural ending before a word is used as a prefix.
// The porter stemmer indexes "bonus" as "bonu" but "bonuses" as "bonus", so
// the prefix "bonuses" would miss pages that say "bonus"; "bonus" finds both.
func singular(w string) string {
	n := len(w)
	switch {
	case n <= 3:
		return w
	case strings.HasSuffix(w, "ies") && n > 4:
		return w[:n-3] + "i"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "uses"),
		strings.HasSuffix(w, "xes"), strings.HasSuffix(w, "zes"),
		strings.HasSuffix(w, "ches"), strings.HasSuffix(w, "shes"):
		return w[:n-2]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") &&
		!strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is"):
		return w[:n-1]
	}
	return w
}

// SearchResult holds a single search result.
type SearchResult struct {
	ID      int64
	Title   string
	Slug    string
	Excerpt string // HTML: escaped text with <mark> around matches
	Type    string // "article" or "card"
	// ArticleType is the article's own type ("deal", "summary", ...); "" for
	// cards.
	ArticleType string
	// ExpiresOn is the article's offer end date ("2006-01-02"), or "" when it
	// has none. Always "" for cards.
	ExpiresOn string
	// PublishedAt is when the article was published (or created, if it has
	// no publish time). Zero for cards.
	PublishedAt time.Time
	// Card fields; empty for articles.
	ImageURL        string
	Issuer          string
	AnnualFeeAmount int
}

// Search ranking. bm25 weights are per column, so a word in the title counts
// ten times a word in the body: a card's own page outranks pages that only
// mention it.
const (
	articleRank = `bm25(articles_fts, 10.0, 4.0, 1.0)`
	cardRank    = `bm25(cards_fts, 10.0, 1.0, 2.0)`
	snippetArgs = `'<mark>', '</mark>', '…', 32`
)

// SearchArticles returns approved articles matching an FTS5 expression (see
// SearchQuery.Match). Offers that ended before today ("2006-01-02") come
// after live ones; within each group, articles with the words side by side
// (phrase, from SearchQuery.Phrase; may be "") come first, then the best
// match.
func (d *Database) SearchArticles(match, phrase, today string, limit, offset int) ([]SearchResult, error) {
	boost, boostArgs := phraseFirst("articles_fts", phrase)
	args := append([]any{match, StatusApproved, today}, boostArgs...)
	args = append(args, limit, offset)
	rows, err := d.db.Query(
		`SELECT a.id, a.title, a.slug, a.type, COALESCE(a.expires_on, ''), a.published_at, a.created_at,
			snippet(articles_fts, 1, `+snippetArgs+`), snippet(articles_fts, 2, `+snippetArgs+`)
		FROM articles_fts f
		JOIN generated_articles a ON a.id = f.rowid
		WHERE articles_fts MATCH ? AND a.status = ?
		ORDER BY (COALESCE(a.expires_on, '') != '' AND a.expires_on < ?), `+boost+`, `+articleRank+`, a.id DESC
		LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var published sql.NullTime
		var created time.Time
		var tldr, body string
		if err := rows.Scan(&r.ID, &r.Title, &r.Slug, &r.ArticleType, &r.ExpiresOn, &published, &created, &tldr, &body); err != nil {
			return nil, err
		}
		r.Type = "article"
		r.PublishedAt = created
		if published.Valid {
			r.PublishedAt = published.Time
		}
		r.Excerpt = pickSnippet(tldr, body)
		results = append(results, r)
	}
	return results, rows.Err()
}

// CountSearchArticles counts the approved articles matching an FTS5 expression.
func (d *Database) CountSearchArticles(match string) (int, error) {
	var n int
	err := d.db.QueryRow(
		`SELECT COUNT(*) FROM articles_fts f
		JOIN generated_articles a ON a.id = f.rowid
		WHERE articles_fts MATCH ? AND a.status = ?`,
		match, StatusApproved,
	).Scan(&n)
	return n, err
}

// SearchCards returns published cards matching an FTS5 expression: those
// with the words side by side (phrase, may be "") first, then the best match.
func (d *Database) SearchCards(match, phrase string, limit, offset int) ([]SearchResult, error) {
	boost, boostArgs := phraseFirst("cards_fts", phrase)
	return d.searchCards(match, boost+`, `+cardRank+`, p.id`, boostArgs, limit, offset)
}

func (d *Database) searchCards(match, order string, orderArgs []any, limit, offset int) ([]SearchResult, error) {
	args := append([]any{match}, orderArgs...)
	args = append(args, limit, offset)
	rows, err := d.db.Query(
		`SELECT p.id, p.title, p.slug, p.image_url, p.issuer, COALESCE(p.annual_fee_amount, 0),
			snippet(cards_fts, 1, `+snippetArgs+`), snippet(cards_fts, 2, `+snippetArgs+`)
		FROM cards_fts f
		JOIN reference_pages p ON p.id = f.rowid
		WHERE cards_fts MATCH ? AND p.status = 'published'
		ORDER BY `+order+`
		LIMIT ? OFFSET ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var sections, verdict string
		if err := rows.Scan(&r.ID, &r.Title, &r.Slug, &r.ImageURL, &r.Issuer, &r.AnnualFeeAmount, &sections, &verdict); err != nil {
			return nil, err
		}
		r.Type = "card"
		r.Excerpt = pickSnippet(sections, verdict)
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchCardTitles returns published cards whose title holds every word of
// q, shortest title first: with every word in each title, the shortest is
// the most exact (the Sapphire Reserve before the Sapphire Reserve for
// Business). bm25 can't order these: FTS5 normalises it by the length of the
// whole page, not the title.
func (d *Database) SearchCardTitles(q SearchQuery, limit int) ([]SearchResult, error) {
	if q.Empty() {
		return nil, nil
	}
	return d.searchCards(q.TitleMatch(), `length(p.title), `+cardRank+`, p.id`, nil, limit, 0)
}

// pickSnippet returns the snippet with the most highlighted matches, so the
// excerpt comes from the field that matched best (the first on a tie). When
// only the title matched, it falls back to the first non-empty snippet: the
// start of that field.
func pickSnippet(snippets ...string) string {
	best, bestMarks := "", 0
	for _, s := range snippets {
		if n := strings.Count(s, "<mark>"); n > bestMarks {
			best, bestMarks = s, n
		}
	}
	if bestMarks > 0 {
		return best
	}
	for _, s := range snippets {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// --- FTS index schema ---

// ftsTokenizer is the tokenizer both search indexes use: porter stemming
// ("lounges" finds "lounge"), Unicode word splitting, and accents folded
// ("cafe" finds "café").
const ftsTokenizer = `porter unicode61 remove_diacritics 2`

// ftsTables describes each search index and how to fill it from its source
// table.
var ftsTables = []struct {
	name   string
	schema string
	source string // table the rowids come from
	fill   func(tx *sql.Tx) (int, error)
}{
	{
		name:   "articles_fts",
		schema: `CREATE VIRTUAL TABLE IF NOT EXISTS articles_fts USING fts5(title, tldr, content, tokenize='` + ftsTokenizer + `')`,
		source: "generated_articles",
		fill:   fillArticlesFTS,
	},
	{
		name:   "cards_fts",
		schema: `CREATE VIRTUAL TABLE IF NOT EXISTS cards_fts USING fts5(title, sections_text, scouts_verdict, tokenize='` + ftsTokenizer + `')`,
		source: "reference_pages",
		fill:   fillCardsFTS,
	},
}

// ensureFTS creates the search indexes, and rebuilds any built with an
// older tokenizer. Each rebuild runs in one transaction: the old index stays
// in place until the new one holds a row for every source row, so a failure
// leaves search working as before (it is logged and retried next start).
func ensureFTS(conn *sql.DB) error {
	for _, t := range ftsTables {
		var existing string
		err := conn.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, t.name).Scan(&existing)
		switch {
		case err == sql.ErrNoRows:
			if _, err := conn.Exec(t.schema); err != nil {
				return fmt.Errorf("creating %s: %w", t.name, err)
			}
		case err != nil:
			return fmt.Errorf("reading %s schema: %w", t.name, err)
		case !strings.Contains(existing, ftsTokenizer):
			start := time.Now()
			n, err := rebuildFTS(conn, t.name, t.schema, t.source, t.fill)
			if err != nil {
				slog.Warn("search index rebuild failed; keeping the old index", "table", t.name, "error", err)
				continue
			}
			slog.Info("rebuilt search index", "table", t.name, "rows", n, "took", time.Since(start).Round(time.Millisecond).String())
		}
	}
	return nil
}

// rebuildFTS drops and recreates one index from its source table in a
// single transaction, and refuses to commit unless every source row was
// indexed.
func rebuildFTS(conn *sql.DB, name, schema, source string, fill func(tx *sql.Tx) (int, error)) (int, error) {
	tx, err := conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DROP TABLE ` + name); err != nil {
		return 0, fmt.Errorf("dropping: %w", err)
	}
	if _, err := tx.Exec(schema); err != nil {
		return 0, fmt.Errorf("creating: %w", err)
	}
	n, err := fill(tx)
	if err != nil {
		return 0, fmt.Errorf("filling: %w", err)
	}
	var want, got int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + source).Scan(&want); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ` + name).Scan(&got); err != nil {
		return 0, err
	}
	if got != want {
		return 0, fmt.Errorf("indexed %d rows, %s has %d", got, source, want)
	}
	return n, tx.Commit()
}

// ftsFillBatch is how many source rows are read at a time while filling an
// index, so a large table is never held in memory at once.
const ftsFillBatch = 500

// fillFTS copies a source table into its index in id order, a batch at a
// time. selectSQL must select the id first and take the last id seen and a
// limit as its parameters; index turns one scanned row into the INSERT args.
func fillFTS(tx *sql.Tx, selectSQL, insertSQL string, ncols int, index func(vals []string) []any) (int, error) {
	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	total := 0
	var lastID int64
	for {
		rows, err := tx.Query(selectSQL, lastID, ftsFillBatch)
		if err != nil {
			return total, err
		}
		type row struct {
			id   int64
			vals []string
		}
		var batch []row
		for rows.Next() {
			r := row{vals: make([]string, ncols)}
			dest := []any{&r.id}
			for i := range r.vals {
				dest = append(dest, &r.vals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				return total, err
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		for _, r := range batch {
			if _, err := stmt.Exec(append([]any{r.id}, index(r.vals)...)...); err != nil {
				return total, err
			}
		}
		total += len(batch)
		if len(batch) < ftsFillBatch {
			return total, nil
		}
		lastID = batch[len(batch)-1].id
	}
}

// fillArticlesFTS indexes every article, whatever its status (search filters
// by status at query time, so publishing needs no reindex).
func fillArticlesFTS(tx *sql.Tx) (int, error) {
	return fillFTS(tx,
		`SELECT id, title, COALESCE(tldr, ''), COALESCE(content, '') FROM generated_articles WHERE id > ? ORDER BY id LIMIT ?`,
		`INSERT INTO articles_fts(rowid, title, tldr, content) VALUES (?, ?, ?, ?)`,
		3, func(v []string) []any {
			return []any{stripHTMLForFTS(v[0]), stripHTMLForFTS(v[1]), stripHTMLForFTS(v[2])}
		})
}

// fillCardsFTS indexes every card page, whatever its status.
func fillCardsFTS(tx *sql.Tx) (int, error) {
	return fillFTS(tx,
		`SELECT id, title, COALESCE(sections_json, ''), COALESCE(scouts_verdict, '') FROM reference_pages WHERE id > ? ORDER BY id LIMIT ?`,
		`INSERT INTO cards_fts(rowid, title, sections_text, scouts_verdict) VALUES (?, ?, ?, ?)`,
		3, func(v []string) []any {
			return []any{stripHTMLForFTS(v[0]), extractSectionsText(v[1]), stripHTMLForFTS(v[2])}
		})
}

// populateFTS fills an index that is empty while its source table is not:
// a new database, or one whose index was lost. It runs on every startup and
// does nothing once both indexes are filled.
func populateFTS(conn *sql.DB) error {
	for _, t := range ftsTables {
		var indexed, rows int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM ` + t.name).Scan(&indexed); err != nil {
			return err
		}
		if indexed > 0 {
			continue
		}
		if err := conn.QueryRow(`SELECT COUNT(*) FROM ` + t.source).Scan(&rows); err != nil {
			return err
		}
		if rows == 0 {
			continue
		}
		tx, err := conn.Begin()
		if err != nil {
			return err
		}
		if _, err := t.fill(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("filling %s: %w", t.name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
