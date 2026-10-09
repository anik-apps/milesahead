package cardupdate

import "testing"

func TestCompareItems(t *testing.T) {
	five := "<ul><li>A</li><li>B</li><li>C</li><li>D</li><li>E</li></ul>"
	table := "<table><thead><tr><th>Category</th></tr></thead><tbody><tr><td>Dining</td></tr><tr><td>Hotels</td></tr></tbody></table>"
	for _, tc := range []struct {
		name, current, proposed string
		lost                    int
		notable, bulk           bool
	}{
		{"one added", five, "<ul><li>A</li><li>B</li><li>C</li><li>D</li><li>E</li><li>F</li></ul>", -1, false, false},
		{"one ended", five, "<ul><li>A</li><li>B</li><li>C</li><li>D</li></ul>", 1, false, false},
		{"two ended", five, "<ul><li>A</li><li>B</li><li>C</li></ul>", 2, true, false},
		{"most lost", five, "<ul><li>A</li><li>New</li></ul>", 3, true, true},
		// Three of fifteen is a real overhaul, not a lost section: warned, kept.
		{"three of fifteen", "<ul>" + repeat("<li>x</li>", 15) + "</ul>", "<ul>" + repeat("<li>x</li>", 12) + "</ul>", 3, true, false},
		{"case and attributes count", "<UL><LI>A</LI><li class=\"x\">B</li></UL>", "<ul><li>A</li><li>B</li></ul>", 0, false, false},
		{"link and track are not items", "<ul><li>A</li></ul>", "<ul><li>A</li></ul><link rel=x><track>", 0, false, false},
		{"header row on both sides cancels", table, "<table><thead><tr><th>Category</th></tr></thead><tbody><tr><td>Dining</td></tr></tbody></table>", 1, false, false},
	} {
		got := CompareItems(tc.current, tc.proposed)
		if got.Lost != tc.lost || got.Notable() != tc.notable || got.Bulk() != tc.bulk {
			t.Errorf("%s: lost %d notable %v bulk %v; want %d %v %v",
				tc.name, got.Lost, got.Notable(), got.Bulk(), tc.lost, tc.notable, tc.bulk)
		}
	}
}

func TestEndingMissing(t *testing.T) {
	list := "<ul><li>Lounge access at 1,300+ lounges</li><li>No foreign transaction fees on any purchase</li></ul>"
	prose := "<p>Opening paragraph about the card.</p><p>The closing paragraph the cut-off model never saw.</p>"
	for _, tc := range []struct {
		name, current, proposed string
		want                    bool
	}{
		{"list kept, item added", list, "<ul><li>Lounge access at 1,300+ lounges</li><li>DoorDash credit</li><li>No foreign transaction fees on any purchase</li></ul>", false},
		{"list cut off", list, "<ul><li>Lounge access at 1,300+ lounges</li><li>DoorDash credit</li></ul>", true},
		{"prose cut off", prose, "<p>Opening paragraph about the card, now with a new sentence.</p>", true},
		{"prose kept, reflowed", prose, "<p>Opening paragraph about the card.</p>\n<p>The closing   paragraph the cut-off model never saw.</p>", false},
		{"entities compare as text", "<p>Up to $300 &amp; more in Food &amp; Beverage credits.</p>", "<p>Up to $300 & more in Food & Beverage credits.</p>", false},
		{"table last row cut off", "<table><tr><td>Dining</td><td>5x points on restaurants</td></tr><tr><td>Everything else</td><td>1x points</td></tr></table>", "<table><tr><td>Dining</td><td>5x points on restaurants</td></tr></table>", true},
		// "$0" could vanish for many reasons; too short to warn on.
		{"short ending is not judged", "<p>Annual fee details below.</p><p>$0</p>", "<p>Annual fee details below.</p>", false},
		{"empty section", "", "<p>x</p>", false},
	} {
		if got := EndingMissing(tc.current, tc.proposed); got != tc.want {
			t.Errorf("%s: EndingMissing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSameContent(t *testing.T) {
	if !sameContent("<ul>\n  <li>A</li>\n</ul>", "<ul> <li>A</li> </ul>") {
		t.Error("whitespace-only differences should count as the same section")
	}
	if sameContent("<ul><li>A</li></ul>", "<ul><li>B</li></ul>") {
		t.Error("different text is not the same section")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
