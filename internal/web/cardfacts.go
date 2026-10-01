package web

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kumaranik/milesahead/internal/db"
)

// cardOffer is a card's welcome offer, cut down from the stored free text
// (welcome_bonus_text) for the card page's summary and the index tiles. Every
// figure in it is copied from that text; when the text doesn't fit the usual
// "<offer> after <condition>" shape, Headline is the text itself, shortened at
// a word boundary.
type cardOffer struct {
	Headline  string // "As high as 175,000 MR points", "$200 cash back"
	Condition string // "after $12,000 spend in 6 months"
	Short     string // for tiles: "Up to 175K points", "$200 cash back"
	// Available is false when the text says there is no offer to get:
	// "No longer available", "No welcome bonus", "None — closed to new
	// applicants…".
	Available bool
	// Closed is true when the card itself can't be applied for.
	Closed bool
}

const (
	offerHeadlineMax = 90 // a fallback headline is cut to this many characters
	offerShortMax    = 26 // a tile's offer is cut to this many characters
)

var (
	offerAfter  = regexp.MustCompile(`(?i)\s(after|upon)\s`)
	offerStops  = regexp.MustCompile(`\s\(|;|,\s*plus\s|\.\s|\.$`)
	offerPoints = regexp.MustCompile(`(?i)\b(\d{1,3}(?:,\d{3})+)\s+(?:[A-Za-z]+\s+){0,4}?(points|miles|skymiles|avios)\b`)
	offerDollar = regexp.MustCompile(`^\$(\d[\d,]*)\s+(cash back|cash bonus|cash rewards bonus|bonus|in ([A-Z][A-Za-z]*) Cash)\b`)
	offerUpTo   = regexp.MustCompile(`(?i)^(up to|as high as)\s`)
	offerNone   = regexp.MustCompile(`(?i)^(no longer available|none\b|no welcome bonus|no bonus)`)
	offerClosed = regexp.MustCompile(`(?i)closed to new applicants|no longer available|discontinued`)
)

// parseOffer reads welcome_bonus_text. An empty text gives a zero cardOffer,
// which renders as nothing.
func parseOffer(text string) cardOffer {
	text = strings.TrimSpace(text)
	if text == "" {
		return cardOffer{}
	}
	o := cardOffer{Available: true, Closed: offerClosed.MatchString(text)}

	if offerNone.MatchString(text) {
		o.Available = false
		o.Headline = cutAt(text, offerStops)
		o.Short = "No current offer"
		return o
	}

	body := strings.TrimPrefix(text, "Earn ")
	if loc := offerAfter.FindStringIndex(body); loc != nil {
		o.Headline = cutAt(body[:loc[0]], offerStops)
		o.Condition = cutAt(strings.TrimSpace(body[loc[0]:]), offerStops)
	} else {
		o.Headline = cutAt(body, offerStops)
	}
	// A headline without a figure is not a summary of an offer; show the text.
	if !strings.ContainsAny(o.Headline, "0123456789") {
		o.Headline, o.Condition = shorten(text, offerHeadlineMax), ""
	}
	o.Headline = shorten(o.Headline, offerHeadlineMax)
	o.Short = shortOffer(o.Headline)
	return o
}

// cutAt returns s up to the first match of stop, trimmed.
func cutAt(s string, stop *regexp.Regexp) string {
	if loc := stop.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	return strings.TrimSpace(s)
}

// shortOffer squeezes a headline into a tile: "75,000 bonus miles" becomes
// "75K miles", "$200 cash bonus" becomes "$200 bonus". A headline it can't
// read is cut at a word boundary, or before a colon ("Cashback Match: …").
func shortOffer(h string) string {
	upTo := ""
	if offerUpTo.MatchString(h) {
		upTo = "Up to "
	}
	if m := offerDollar.FindStringSubmatch(h); m != nil {
		switch {
		case m[3] != "":
			return upTo + "$" + m[1] + " " + m[3] + " Cash"
		case m[2] == "cash back":
			return upTo + "$" + m[1] + " cash back"
		default:
			return upTo + "$" + m[1] + " bonus"
		}
	}
	if m := offerPoints.FindStringSubmatch(h); m != nil {
		unit := strings.ToLower(m[2])
		if unit == "skymiles" || unit == "avios" {
			unit = "miles"
		}
		return upTo + thousands(m[1]) + " " + unit
	}
	if i := strings.Index(h, ":"); i > 0 {
		h = h[:i]
	}
	return shorten(h, offerShortMax)
}

// thousands writes "175,000" as "175K" and leaves other numbers as they are.
func thousands(n string) string {
	v, err := strconv.Atoi(strings.ReplaceAll(n, ",", ""))
	if err != nil || v < 1000 || v%1000 != 0 {
		return n
	}
	return strconv.Itoa(v/1000) + "K"
}

// shorten cuts s to at most max characters at a word boundary, adding "…".
func shorten(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)[:max]
	cut := string(r)
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}

// feeText is the annual fee as the summary and tiles show it.
func feeText(amount int) string {
	if amount == 0 {
		return "$0"
	}
	return "$" + commaInt(amount)
}

func commaInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// hasCardFacts reports whether a card's structured data has been filled in.
// Without it a $0 fee means "unknown", not "no annual fee".
func hasCardFacts(p *db.ReferencePage) bool {
	return strings.TrimSpace(p.WelcomeBonusText) != "" ||
		(p.EarningRatesJSON != "" && p.EarningRatesJSON != "{}")
}

// cardFacts is the summary under a card page's title: fee, offer and the best
// earning rates, all from the card's structured data.
type cardFacts struct {
	Fee       string // "$895", "$0"
	FeeDetail string // annual_fee_note, or "No annual fee"
	Offer     cardOffer
	Earn      []earnRate // best first, at most 3
}

func buildCardFacts(p *db.ReferencePage) *cardFacts {
	if !hasCardFacts(p) {
		return nil
	}
	f := &cardFacts{
		Fee:       feeText(p.AnnualFeeAmount),
		FeeDetail: shorten(strings.TrimSpace(p.AnnualFeeNote), 70),
		Offer:     parseOffer(p.WelcomeBonusText),
		Earn:      topEarnRates(p.EarningRatesJSON, 3),
	}
	if p.AnnualFeeAmount == 0 && f.FeeDetail == "" {
		f.FeeDetail = "No annual fee"
	}
	return f
}

// EarnRest is the summary's second and third earning lines, with conditions.
func (f *cardFacts) EarnRest() string {
	if len(f.Earn) < 2 {
		return ""
	}
	parts := make([]string, 0, 2)
	for _, e := range f.Earn[1:] {
		parts = append(parts, e.Detail())
	}
	return strings.Join(parts, " · ")
}

// leadSentence is the first sentence of a verdict, for the summary panel. A
// sentence ends at ".", "!" or "?" followed by a space and a capital letter or
// quote, so "$1.5" and "2.5x" don't end one. Long first sentences are cut at a
// word boundary.
var sentenceEnd = regexp.MustCompile(`[.!?]["”']?\s+["“'A-Z]`)

func leadSentence(verdict string, max int) string {
	s := strings.Join(strings.Fields(stripHTML(verdict)), " ")
	if loc := sentenceEnd.FindStringIndex(s); loc != nil {
		end := loc[0] + 1
		if s[end] == '"' || s[end] == '\'' {
			end++
		} else if strings.HasPrefix(s[end:], "”") {
			end += len("”")
		}
		s = s[:end]
	}
	return shorten(s, max)
}

// applyHost is the bare host of an apply URL, for "Opens americanexpress.com
// in a new tab".
func applyHost(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimPrefix(strings.ToLower(s), "www.")
}

// feeBand is a range of annual fees offered as an index filter.
type feeBand struct {
	Key      string
	Label    string
	Min, Max int // inclusive; Max < 0 means no upper bound
}

var feeBands = []feeBand{
	{Key: "none", Label: "No fee", Min: 0, Max: 0},
	{Key: "under-100", Label: "Under $100", Min: 1, Max: 99},
	{Key: "100-400", Label: "$100–$400", Min: 100, Max: 400},
	{Key: "over-400", Label: "Over $400", Min: 401, Max: -1},
}

func feeBandByKey(key string) (feeBand, bool) {
	for _, b := range feeBands {
		if b.Key == key {
			return b, true
		}
	}
	return feeBand{}, false
}

func (b feeBand) contains(fee int) bool {
	return fee >= b.Min && (b.Max < 0 || fee <= b.Max)
}

// issuerKey is an issuer's URL value: "Bank of America" becomes
// "bank-of-america".
func issuerKey(issuer string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(issuer)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// cardCountText is "1 card" or "12 cards".
func cardCountText(n int) string {
	if n == 1 {
		return "1 card"
	}
	return fmt.Sprintf("%d cards", n)
}
