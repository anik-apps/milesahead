package web

// cardInfographic is a static, human-designed infographic for a card page.
//
// The page shows a cropped teaser in the card-art slot (sidebar on desktop,
// header card on mobile) that opens the full image in a viewer. It is not placed
// inline: at the article column's width the image renders at under half its
// designed size, which leaves its body text too small to read in place.
//
// Images are committed by slug, like the video posters:
// static/img/infographic-<slug>.jpg is the full 1024x1536 image and
// static/img/infographic-<slug>-teaser.jpg is its top 1024x640 region at 480x300.
type cardInfographic struct {
	// Alt is the full text alternative. The image is almost entirely text, so a
	// short description would hide everything it says from screen readers.
	Alt string
	// Checked is when the image's figures were last verified. It is shown in the
	// caption because, unlike page prose, an image cannot be corrected by a reseed.
	Checked string
	// Facts are figures the image states. TestCardInfographics_FactsMatchCardProse
	// fails when one no longer appears in the card's cards.json text, so changing a
	// fee or credit cannot leave the image silently contradicting the page.
	Facts []string
}

// cardInfographics maps a reference-page slug to its infographic. Adding a card
// is an entry here plus the two committed images.
var cardInfographics = map[string]cardInfographic{
	"amex-platinum": {
		Alt: "Amex Platinum infographic, figures checked September 22, 2026. $895 annual fee. " +
			"Earn: 5X Membership Rewards points on flights booked directly with airlines or through Amex Travel " +
			"(up to $500,000 a calendar year, then 1X), 5X on prepaid hotels booked through Amex Travel, and 1X on " +
			"other eligible spending including dining and groceries. Redeem: transfer to airline and hotel partners, " +
			"book flights through Amex Travel at typically 1 cent a point, or cover eligible card charges at 0.6 cents " +
			"a point. Credits, separate from points: $600 hotel, $400 Resy, $300 digital entertainment, $300 lululemon, " +
			"$200 Uber Cash, $120 Uber One, $200 airline fee and $219 CLEAR+, plus Oura Ring, Equinox and Walmart+ " +
			"credits; enrollment and merchant rules apply. Travel benefits: Centurion and Priority Pass lounges, 10 Delta " +
			"Sky Club visits a benefit year, and Hilton Gold and Marriott Gold Elite status. Unused periodic credits " +
			"generally do not roll over, and fees and interest can outweigh rewards.",
		Checked: "Sep 22, 2026",
		Facts:   []string{"$895", "$219", "$600", "$400", "$500,000", "0.6 cents"},
	},
	"bilt-palladium": {
		Alt: "Bilt Palladium Mastercard infographic, figures checked September 22, 2026. $495 annual fee. " +
			"Earn 2X points on eligible everyday purchases excluding housing, then choose one of two earning modes, " +
			"which do not stack. Flexible Bilt Cash adds 4% Bilt Cash on everyday spending. Housing-only earns points on " +
			"rent or mortgage based on everyday spending as a share of housing payments: 250 points a billing cycle below " +
			"25%, then 0.5X, 0.75X, 1X and 1.25X at 100% or more. More ways to earn: a $300 Bilt Cash welcome offer, $200 " +
			"Bilt Cash each year, $50 Bilt Cash per 25,000 status-qualifying points, and 50,000 points plus Gold status " +
			"after $4,000 of non-housing purchases in the first 90 days. Redeem Bilt Points by transferring to airline and " +
			"hotel partners, booking through Bilt Travel, or toward rent, a home down payment, gift cards or a statement " +
			"credit. Use Bilt Cash to unlock housing points at $30 per 1,000 points, to activate the Point Accelerator " +
			"($200 for an extra 1X on the next $5,000 of eligible purchases), or for partner credits. Bilt Cash cannot be " +
			"withdrawn, transferred to travel partners or used as a statement credit, and at most $100 rolls over at year end.",
		Checked: "Sep 22, 2026",
		Facts:   []string{"$495", "4%", "1.25x", "50,000", "$4,000"},
	},
	"chase-sapphire-reserve": {
		Alt: "Chase Sapphire Reserve infographic, figures checked September 23, 2026. $795 annual fee; authorized " +
			"users $195 a year. Earn Ultimate Rewards: 8X points on Chase Travel purchases, 4X on flights and hotels " +
			"booked directly, 3X on dining worldwide, 1X on other purchases, and 5X on eligible Lyft rides through " +
			"September 30, 2027. Spending covered by the $300 travel, $500 The Edit or $250 select-hotel credits earns no " +
			"points. Redeem: transfer to airline and hotel partners; Chase Travel at a standard 1 cent a point, or more " +
			"with Points Boost on selected flights and hotels; cash back at 1 cent a point. Cardholders who applied " +
			"before June 23, 2025 keep 1.5 cents a point on Chase Travel for points earned before October 26, 2025, until " +
			"October 26, 2027. Credits, separate from points: $300 travel each account anniversary year; $500 The Edit " +
			"each calendar year, up to $250 per prepaid booking of two nights or more; $300 dining at Sapphire Reserve " +
			"Exclusive Tables restaurants, $150 each half-year; $300 StubHub or viagogo, $150 each half-year through 2027; " +
			"$120 Lyft at $10 a month through September 30, 2027; $120 Peloton at $10 a month through 2027; $250 for " +
			"select hotels in 2026 only; and Apple TV+ and Apple Music through June 22, 2027. DoorDash: up to $25 a month " +
			"through September 30, 2026, then up to $35 a month from October 1, 2026. Travel benefits: Sapphire Lounges and " +
			"Priority Pass with up to 2 guests; up to $120 for Global Entry, TSA PreCheck or NEXUS every 4 years; IHG " +
			"Platinum Elite status through 2027; trip protections and primary rental car coverage.",
		Checked: "Sep 23, 2026",
		Facts:   []string{"$795", "$195", "8x", "$500"},
	},
}
