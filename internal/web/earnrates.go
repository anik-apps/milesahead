package web

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// earnRate is one line of a card's earning summary, e.g. "5x flights" or
// "8x travel via Chase Travel". Several categories that earn the same rate
// share a line ("4x dining & groceries").
type earnRate struct {
	Rate  string  // "5x", "2%"
	Value float64 // 5, 2
	Label string  // "flights", "dining & groceries", "everything"
	// Portal names the travel site the rate needs ("Amex Travel"); empty when
	// the rate applies however the purchase is made.
	Portal string
	// Qualifier is a condition worth stating next to the rate: "capped",
	// "top category only", "rotating categories" or "choose one".
	Qualifier string
	Base      bool // the card's rate on everything else
}

// String is the short form for tiles: "5x flights", "5x hotels via Amex Travel".
func (e earnRate) String() string {
	s := e.Rate + " " + e.Label
	if e.Portal != "" {
		s += " via " + e.Portal
	}
	return s
}

// Rest is String without the rate, for layouts that set the rate in bold:
// "flights", "hotels via Amex Travel".
func (e earnRate) Rest() string {
	return strings.TrimPrefix(e.String(), e.Rate+" ")
}

// Detail is String plus the qualifier, for places with room to say it:
// "4x dining & groceries (capped)".
func (e earnRate) Detail() string {
	if e.Qualifier == "" {
		return e.String()
	}
	return e.String() + " (" + e.Qualifier + ")"
}

// earnCategory is a key in earning_rates_json and how it reads in a summary.
type earnCategory struct {
	Key    string
	Label  string
	Pair   bool // stored as Key_direct and Key_portal
	Travel bool
}

// earnSummaryLabels is how each earning category reads in a sentence ("5x
// flights"), where the compare table says "Airlines".
var earnSummaryLabels = map[string]string{
	"airlines":     "flights",
	"car_rental":   "car rentals",
	"other_travel": "other travel",
}

// earnCategories lists the summarised categories in tie-break order. The keys,
// their order and the direct/portal split come from the compare table's
// earningRateCategories and directPortalPairs, so the two can't drift apart.
// "base" is handled separately: it is the floor every bonus is measured
// against.
var earnCategories = func() []earnCategory {
	var out []earnCategory
	for _, c := range earningRateCategories {
		if c.Key == "base" {
			continue
		}
		label := earnSummaryLabels[c.Key]
		if label == "" {
			label = strings.ToLower(c.Label)
		}
		out = append(out, earnCategory{Key: c.Key, Label: label, Pair: directPortalPairs[c.Key], Travel: directPortalPairs[c.Key]})
	}
	return out
}()

// A rate is a number followed by "x" or "%" at the start of the stored value:
// "5x", "1.5%", "3x United miles (car rentals count as travel)".
var rateToken = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([xX%])`)

// portalName finds the travel site a portal rate needs: "Via Chase Travel" or
// "Prepaid via Amex Travel" give "Chase Travel" and "Amex Travel".
var portalName = regexp.MustCompile(`\b((?:Capital One(?: Business)?|[A-Z][A-Za-z]*) Travel)\b`)

// portalSite finds a booking site named by its domain: "Hotels via
// cititravel.com" gives "cititravel.com".
var portalSite = regexp.MustCompile(`\b([a-z0-9-]+(?:\.[a-z0-9-]+)*\.com)\b`)

// defaultPortal stands in when no note on the card names its portal.
const defaultPortal = "the issuer's portal"

// Words that never name a portal even when capitalised before "Travel".
var notPortal = map[string]bool{"Via": true, "Through": true, "Booked": true, "Prepaid": true, "Select": true, "Other": true}

// capWords mark a spending cap; capAmount finds the dollar figure it applies to.
var (
	capWords  = regexp.MustCompile(`(?i)\bup to \$|\bcap\b|\bcapped\b|\bfirst \$|\bthen \d`)
	noCap     = regexp.MustCompile(`(?i)\bno (?:earnings )?cap\b|\buncapped\b`)
	capAmount = regexp.MustCompile(`\$\s?([\d,]+(?:\.\d+)?)\s*([kKmM])?\b`)
)

// practicalCap is the smallest annual cap worth flagging: "up to $500K/yr"
// on flights binds almost nobody, "up to $25,000/yr" on groceries does.
const practicalCap = 100000

// parsedRate is one stored rate before ranking.
type parsedRate struct {
	Rate  string
	Value float64
	Note  string
}

// parseRate reads a stored rate, either {"rate": "5x", "note": "..."} or a
// bare string such as "4x United miles on United purchases". Returns false
// for "N/A", blanks and anything without a leading rate.
func parseRate(raw json.RawMessage) (parsedRate, bool) {
	var entry rateEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return parsedRate{}, false
		}
		entry.Rate = s
	}
	m := rateToken.FindStringSubmatch(entry.Rate)
	if m == nil {
		return parsedRate{}, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || v <= 0 {
		return parsedRate{}, false
	}
	note := entry.Note
	if rest := strings.TrimSpace(entry.Rate[len(m[0]):]); rest != "" {
		note = strings.TrimSpace(rest + " " + note)
	}
	return parsedRate{Rate: m[1] + strings.ToLower(m[2]), Value: v, Note: note}, true
}

// qualifierFor names the condition a bonus rate's note describes, if any.
func qualifierFor(note string) string {
	n := strings.ToLower(note)
	switch {
	case strings.Contains(n, "top category"), strings.Contains(n, "top eligible"), strings.Contains(n, "if top"):
		return "top category only"
	case strings.Contains(n, "choose"):
		return "choose one"
	case strings.Contains(n, "rotating"), strings.Contains(n, "activation"):
		return "rotating categories"
	case isCapped(note):
		return "capped"
	}
	return ""
}

// isCapped reports whether a note describes a spending cap low enough to
// matter. A cap with no readable amount counts.
func isCapped(note string) bool {
	if !capWords.MatchString(note) || noCap.MatchString(note) {
		return false
	}
	amounts := capAmount.FindAllStringSubmatch(note, -1)
	if len(amounts) == 0 {
		return true
	}
	for _, a := range amounts {
		v, err := strconv.ParseFloat(strings.ReplaceAll(a[1], ",", ""), 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(a[2]) {
		case "k":
			v *= 1000
		case "m":
			v *= 1000000
		}
		if v < practicalCap {
			return true
		}
	}
	return false
}

// portalFor names the portal a note mentions, or "" if it names none.
func portalFor(note string) string {
	for _, m := range portalName.FindAllStringSubmatch(note, -1) {
		if first := strings.Fields(m[1])[0]; !notPortal[first] {
			return m[1]
		}
	}
	if m := portalSite.FindStringSubmatch(note); m != nil {
		return m[1]
	}
	return ""
}

// rankedRate is a candidate line before categories sharing a rate merge.
type rankedRate struct {
	earnRate
	order  int
	travel bool
}

// rankEarnRates summarises a card's earning_rates_json, best first:
//
//  1. bonus rates that apply however you book, by rate (an unconditional rate
//     before a capped or conditional one of the same size, then category order);
//  2. if there are none, the flat rate, as "on everything";
//  3. rates that need the issuer's travel portal, where they beat both the
//     direct rate and the flat rate;
//  4. the flat rate, as "everything else", after any bonuses.
//
// Direct rates come before portal ones because they need nothing extra:
// Venture X reads "2x everything · 10x hotels & car rentals via Capital One
// Travel", not the other way round. Categories with the same rate, portal and
// qualifier share a line.
func rankEarnRates(earningRatesJSON string) []earnRate {
	if earningRatesJSON == "" || earningRatesJSON == "{}" {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(earningRatesJSON), &raw); err != nil {
		return nil
	}
	get := func(key string) (parsedRate, bool) {
		v, ok := raw[key]
		if !ok {
			return parsedRate{}, false
		}
		return parseRate(v)
	}

	base, hasBase := get("base")
	var direct, portal []rankedRate
	for i, cat := range earnCategories {
		if !cat.Pair {
			if r, ok := get(cat.Key); ok && r.Value > base.Value {
				direct = append(direct, rankedRate{earnRate{Rate: r.Rate, Value: r.Value, Label: cat.Label, Qualifier: qualifierFor(r.Note)}, i, cat.Travel})
			}
			continue
		}
		d, okD := get(cat.Key + "_direct")
		if !okD {
			d, okD = get(cat.Key) // older rows stored travel rates unsplit
		}
		if okD && d.Value > base.Value {
			direct = append(direct, rankedRate{earnRate{Rate: d.Rate, Value: d.Value, Label: cat.Label, Qualifier: qualifierFor(d.Note)}, i, cat.Travel})
		}
		if p, ok := get(cat.Key + "_portal"); ok && p.Value > base.Value && (!okD || p.Value > d.Value) {
			portal = append(portal, rankedRate{earnRate{Rate: p.Rate, Value: p.Value, Label: cat.Label, Portal: portalFor(p.Note), Qualifier: qualifierFor(p.Note)}, i, cat.Travel})
		}
	}

	// A portal rate whose note names no portal takes the one the card's other
	// portal notes name: Amex Gold's "2x other travel" is Amex Travel too.
	cardPortal := ""
	for _, p := range portal {
		if p.Portal != "" {
			cardPortal = p.Portal
			break
		}
	}
	if cardPortal == "" {
		cardPortal = defaultPortal
	}
	for i := range portal {
		if portal[i].Portal == "" {
			portal[i].Portal = cardPortal
		}
	}

	var out []earnRate
	out = append(out, mergeRates(direct)...)
	if len(direct) == 0 && hasBase {
		out = append(out, earnRate{Rate: base.Rate, Value: base.Value, Label: "everything", Base: true})
	}
	out = append(out, mergeRates(portal)...)
	if len(direct) > 0 && hasBase {
		out = append(out, earnRate{Rate: base.Rate, Value: base.Value, Label: "everything else", Base: true})
	}
	return out
}

// mergeRates sorts candidates and joins neighbours with the same rate, portal
// and qualifier into one line.
func mergeRates(rs []rankedRate) []earnRate {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Value != rs[j].Value {
			return rs[i].Value > rs[j].Value
		}
		if (rs[i].Qualifier == "") != (rs[j].Qualifier == "") {
			return rs[i].Qualifier == ""
		}
		return rs[i].order < rs[j].order
	})
	var out []earnRate
	for i := 0; i < len(rs); {
		j := i + 1
		for j < len(rs) && rs[j].Rate == rs[i].Rate && rs[j].Portal == rs[i].Portal && rs[j].Qualifier == rs[i].Qualifier {
			j++
		}
		group := rs[i:j]
		e := group[0].earnRate
		labels := make([]string, len(group))
		allTravel := true
		for k, g := range group {
			labels[k] = g.Label
			allTravel = allTravel && g.travel
		}
		e.Label = joinLabels(labels, allTravel)
		out = append(out, e)
		i = j
	}
	return out
}

// joinLabels writes "dining & groceries", "flights, hotels & car rentals".
// Three or more travel categories read as "travel"; more than three others
// read as "dining, groceries & more".
func joinLabels(labels []string, allTravel bool) string {
	switch {
	case len(labels) == 1:
		return labels[0]
	case allTravel && len(labels) >= 3:
		return "travel"
	case len(labels) == 2:
		return labels[0] + " & " + labels[1]
	case len(labels) == 3:
		return labels[0] + ", " + labels[1] + " & " + labels[2]
	default:
		return labels[0] + ", " + labels[1] + " & more"
	}
}

// topEarnRates returns the first n lines of rankEarnRates.
func topEarnRates(earningRatesJSON string, n int) []earnRate {
	rates := rankEarnRates(earningRatesJSON)
	if len(rates) > n {
		rates = rates[:n]
	}
	return rates
}
