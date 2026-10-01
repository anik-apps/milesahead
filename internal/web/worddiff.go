package web

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

// wordDiff is a word-level comparison of two HTML snippets as a reader sees
// them: tags dropped, paragraphs kept. The card updates page uses it so the
// admin sees the one changed sentence instead of hunting for it.
type wordDiff struct {
	HTML    template.HTML // paragraphs with <ins> and <del>; escaped text only
	Added   int           // words inserted
	Removed int           // words deleted
	TooLong bool          // too large to compare word by word; HTML is empty
	Same    bool          // no difference in the text
}

// maxDiffCells bounds the comparison's table (words before x words after),
// about 30 MB of ints; longer sections fall back to side by side.
const maxDiffCells = 4_000_000

var (
	blockEndRE = regexp.MustCompile(`(?i)</(p|li|h[1-6]|tr|div|blockquote|ul|ol|table)>|<br\s*/?>|<hr[^>]*>`)
	liStartRE  = regexp.MustCompile(`(?i)<li[^>]*>`)
	cellEndRE  = regexp.MustCompile(`(?i)</(td|th)>`)
	tagRE      = regexp.MustCompile(`<[^>]*>`)
	spaceRE    = regexp.MustCompile(`[ \t\r\f\v]+`)
)

// diffTokens turns HTML into words, with "\n" tokens between paragraphs.
func diffTokens(s string) []string {
	s = liStartRE.ReplaceAllString(s, "\n• ")
	s = blockEndRE.ReplaceAllString(s, "\n")
	s = cellEndRE.ReplaceAllString(s, " ")
	// Inline tags go without a space: "<strong>$895</strong>." is one word.
	s = tagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(spaceRE.ReplaceAllString(para, " "))
		if len(words) == 0 {
			continue
		}
		if len(out) > 0 {
			out = append(out, "\n")
		}
		out = append(out, words...)
	}
	return out
}

// diffWords compares before and after word by word (longest common
// subsequence) and renders the result.
func diffWords(before, after string) wordDiff {
	a, b := diffTokens(before), diffTokens(after)
	if len(a)*len(b) > maxDiffCells {
		return wordDiff{TooLong: true}
	}
	// lcs[i][j] is the LCS length of a[i:] and b[j:].
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out strings.Builder
	var d wordDiff
	open := "" // the run being written: "", "ins" or "del"
	setRun := func(kind string) {
		if kind == open {
			return
		}
		if open != "" {
			out.WriteString("</" + open + ">")
		}
		if kind != "" {
			out.WriteString("<" + kind + ">")
		}
		open = kind
	}
	out.WriteString("<p>")
	word := func(kind, w string) {
		if w == "\n" {
			setRun("")
			out.WriteString("</p><p>")
			return
		}
		setRun(kind)
		out.WriteString(html.EscapeString(w))
		out.WriteString(" ")
		switch kind {
		case "ins":
			d.Added++
		case "del":
			d.Removed++
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			word("", a[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			word("del", a[i])
			i++
		default:
			word("ins", b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		word("del", a[i])
	}
	for ; j < len(b); j++ {
		word("ins", b[j])
	}
	setRun("")
	out.WriteString("</p>")
	d.HTML = template.HTML(out.String())
	d.Same = d.Added == 0 && d.Removed == 0
	return d
}
