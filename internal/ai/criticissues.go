package ai

import (
	"regexp"
	"strings"
)

// The critic is asked for problems only, but it often fills "issues" with
// checks that passed: "Deadline of December 31, 2026 matches both sources.",
// "HTML appears valid: no Markdown, tags properly opened and closed.",
// "No links to source news sites -- all links are to ihg.com -- good."
// Process auto-revises whenever issues are left, so every such list costs a
// generator rewrite and a second critic call for nothing, and the generator is
// told to "fix" things that are fine.
//
// dropPassedChecks removes those items after parsing. It is deliberately
// narrow: an item is dropped only when it carries a pass/confirmation cue and
// no cue at all that something might need changing. Anything ambiguous is
// kept, because dropping a real problem hides it from the revision.

var (
	// Quoted article or source text is not the critic's judgement: a quoted
	// "don't count" or "best deal" says nothing about whether the item is a
	// problem. Single quotes count only when they open after a space or
	// bracket and close before one, so apostrophes ("article's") are left.
	criticQuoteRe = regexp.MustCompile(`"[^"]*"|“[^”]*”|‘[^’]*’|(^|[\s(\[:])'(?:[^']|'\w)*'([\s,.;:)\]!?]|$)`)

	// Negations that confirm a check passed ("no hallucinations", "not
	// hallucinated"). They are turned into a pass marker before the negative
	// cues are looked for, so their "no"/"not" does not count against them.
	criticPassPhraseRe = regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`no (?:major |obvious |significant )?(?:issues?|problems?|errors?|concerns?|hallucinat\w*|fabricat\w*|contradictions?(?: or illogical passages)?)(?: (?:found|detected|identified|noted|here))?`,
		`no (?:source[- ]leakage|markdown(?: artifacts)?)(?: issues?)?`,
		`no links? to (?:any )?source news sites?`,
		`(?:does not|doesn't) (?:mention or )?link to (?:any )?source news sites?`,
		`no (?:link[- ]check |source[- ]leakage )?(?:failures?|violations?)`,
		`not (?:a )?hallucinat\w*`,
		`not (?:to )?(?:a )?source news sites?`,
		`no (?:attribution|references?) to (?:the )?sources? by (?:number|position)(?: or (?:outlet|number|position))?`,
		`no (?:numbered source references?|source numbering)`,
		`nothing to (?:fix|change|flag)`,
	}, "|") + `)\b`)

	// Words that mean the item may describe something to change. Any of them
	// keeps the item.
	criticNegativeRe = regexp.MustCompile(`\b(?:` + strings.Join([]string{
		// contrast and hedging
		`but`, `however`, `though`, `although`, `yet`, `except`, `while`, `whereas`, `instead`, `rather`,
		`vs`, `versus`, `only`, `partial\w*`, `slight\w*`, `minor`, `nit\w*`, `may`, `might`, `could`, `would`,
		// absence and negation
		`no`, `not`, `never`, `none`, `nor`, `without`, `cannot`, `\w+n't`, `\w+n’t`, `missing`, `miss\w*`, `lack\w*`,
		`omit\w*`, `absent`, `unclear`, `unsupported`, `unverified`, `unconfirmed`, `unsourced`, `undefined`,
		// error words
		`incorrect\w*`, `inaccura\w*`, `wrong\w*`, `errors?`, `mistake\w*`, `typo\w*`, `invalid`, `inconsisten\w*`,
		`discrepan\w*`, `differ\w*`, `conflict\w*`, `contradict\w*`, `mismatch\w*`, `fail\w*`, `violat\w*`,
		`issues?`, `problem\w*`, `concern\w*`, `mislead\w*`, `confus\w*`, `vague\w*`, `ambigu\w*`, `speculat\w*`,
		`exaggerat\w*`, `overstat\w*`, `understat\w*`, `hallucinat\w*`, `fabricat\w*`, `invent\w*`, `outdated`,
		`stale`, `broken`, `awkward\w*`, `redundan\w*`, `repetit\w*`, `overly`, `too`, `leak\w*`, `artifact\w*`,
		`truncat\w*`, `assum\w*`, `inferred`, `extrapolat\w*`, `embellish\w*`, `beyond`,
		// requests for a change
		`should`, `must`, `needs?`, `fix\w*`, `chang\w*`, `replac\w*`, `remov\w*`, `add\w*`, `updat\w*`,
		`rewrit\w*`, `rephras\w*`, `reword\w*`, `drop\w*`, `delet\w*`, `clarif\w*`, `consider\w*`,
		`recommend\w*`, `avoid\w*`, `ensure`, `improv\w*`, `expand\w*`, `shorten\w*`, `tighten\w*`, `instead`,
	}, "|") + `)\b`)

	// A last sentence that is exactly a no-change verdict. "Otherwise no
	// issues" or "no other issue" do not match: they imply there is one.
	criticNoChangeVerdictRe = regexp.MustCompile(`(?:^|[.;!]\s+|\s[—–-]+\s+)(?:so |thus |therefore |hence |overall, )?(?:there is |there's )?(?:no (?:issue|error|problem)s?(?: here| there)?|no (?:change|fix|action) (?:is )?(?:needed|required)|nothing to fix)[.!]?\s*$`)

	// Splits an item into sentences and semicolon clauses.
	criticClauseRe = regexp.MustCompile(`;|[.!?]\s+`)

	// Confirmation and praise.
	criticPositiveRe = regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`passmarker`, `pass(?:es|ed)?`, `verified`, `match(?:es|ed|ing)?`, `accurate(?:ly)?`, `correct(?:ly)?`,
		`consistent with`, `properly`, `confirm(?:s|ed)?`, `supported`, `traceable`, `valid`, `clean`,
		`good`, `fine`, `strong`, `solid`, `well`, `natural(?:ly)?`, `engaging`, `appropriate(?:ly)?`,
		`permitted`, `allowed`, `intact`, `ok`, `okay`,
	}, "|") + `)\b`)
)

// isPassedCheck reports whether a critic issue only confirms that something is
// right. See dropPassedChecks for the rules.
func isPassedCheck(issue string) bool {
	t := strings.ToLower(strings.TrimSpace(issue))
	if t == "" {
		return true
	}
	t = criticQuoteRe.ReplaceAllString(t, "${1} ${2}")
	// The critic's own closing verdict that nothing needs to change ("...
	// which is correct per source. No issue.") settles it, whatever the
	// item says on the way there.
	if criticNoChangeVerdictRe.MatchString(t) {
		return true
	}
	t = criticPassPhraseRe.ReplaceAllString(t, " passmarker ")
	if criticNegativeRe.MatchString(t) {
		return false
	}
	// Every sentence or clause has to confirm something on its own, so a
	// confirmation cannot carry a bare statement of fact that may be the real
	// complaint: "The deadline is correct; the key details say September 29."
	for _, clause := range criticClauseRe.Split(t, -1) {
		if strings.Trim(clause, " .!?") != "" && !criticPositiveRe.MatchString(clause) {
			return false
		}
	}
	return true
}

// dropPassedChecks removes issues that only confirm a check passed or praise
// the article, returning the issues to act on and the ones dropped.
//
// It never turns a failing verdict into a clean one: when a check failed and
// every issue would be dropped, the issues are kept as they are, since the
// critic saw a problem even if it described it badly.
func dropPassedChecks(v *VerificationResult) (dropped []string) {
	var kept []string
	for _, issue := range v.Issues {
		if isPassedCheck(issue) {
			dropped = append(dropped, issue)
			continue
		}
		kept = append(kept, issue)
	}
	if len(dropped) == 0 {
		return nil
	}
	allPassed := v.FactualCheck && v.AttributionCheck && v.CompletenessCheck && v.HallucinationCheck
	if len(kept) == 0 && !allPassed {
		return nil
	}
	if kept == nil {
		kept = []string{}
	}
	v.Issues = kept
	return dropped
}
