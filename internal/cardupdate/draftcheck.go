package cardupdate

import (
	"html"
	"regexp"
	"strings"
)

// Approving a draft replaces the whole section with it, so anything the draft
// leaves out is deleted. These checks look for drafts that lose content: the
// detector does not save one that loses items in bulk, and the admin updates
// page warns on any draft that loses more than one item or the section's end.
// Drafts made before the detector showed the model whole sections (it saw the
// first 1,000 bytes) are the main case: they cannot contain what came after.

var (
	// listItem matches the opening tag of a list item or table row.
	listItem = regexp.MustCompile(`(?i)<(li|tr)[\s>]`)
	// blockEnd matches the closing tag of a list item, table row or paragraph.
	blockEnd = regexp.MustCompile(`(?i)</(li|tr|p)>`)
	anyTag   = regexp.MustCompile(`<[^>]*>`)
)

// endingMinLen is the shortest closing block EndingMissing judges: a shorter
// one ("None", "$0") could be missing for no reason worth a warning.
const endingMinLen = 20

// ItemLoss compares the list items and table rows in a draft with the
// section's. It counts net: a draft that swaps one item for another loses
// none, and a draft that edits an item's wording keeps it.
type ItemLoss struct {
	Current int // list items and table rows in the current section
	Lost    int // how many fewer the draft has; negative when it has more
}

// CompareItems counts the list items and table rows in current and proposed.
func CompareItems(current, proposed string) ItemLoss {
	n := len(listItem.FindAllStringIndex(current, -1))
	return ItemLoss{Current: n, Lost: n - len(listItem.FindAllStringIndex(proposed, -1))}
}

// Notable reports a loss of more than one item. One is a single ended benefit
// and is ordinary; more is worth a warning before anyone approves the draft.
func (l ItemLoss) Notable() bool { return l.Lost > 1 }

// Bulk reports a loss of at least three items that is also over a third of the
// section's. The detector does not save such a draft. A real update that ends
// two benefits is still drafted (and warned about); a draft missing most of a
// list is what a reply that lost the section looks like, and drafts made from
// the cut-off sections lost 3 to 16 items.
func (l ItemLoss) Bulk() bool { return l.Lost >= 3 && l.Lost*3 > l.Current }

// EndingMissing reports whether the visible text of the section's last block
// (list item, table row or paragraph) is absent from the draft. A draft made
// from a cut-off section can never contain it, whether the section is a list
// or prose; a legitimate edit to the last item also trips it, which is why it
// is only ever a warning.
func EndingMissing(current, proposed string) bool {
	blocks := blockEnd.Split(current, -1)
	for i := len(blocks) - 1; i >= 0; i-- {
		last := visibleText(blocks[i])
		if last == "" {
			continue
		}
		if len(last) < endingMinLen {
			return false
		}
		return !strings.Contains(visibleText(proposed), last)
	}
	return false
}

// sameContent reports whether a draft only repeats the section, give or take
// whitespace: the model echoed it instead of answering NO_UPDATE.
func sameContent(current, proposed string) bool {
	return strings.Join(strings.Fields(current), " ") == strings.Join(strings.Fields(proposed), " ")
}

// truncate returns at most n runes of s, for logging.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// visibleText is s without tags or entities, with whitespace collapsed.
func visibleText(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(anyTag.ReplaceAllString(s, " "))), " ")
}
