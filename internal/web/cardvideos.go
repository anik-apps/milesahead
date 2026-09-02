package web

// cardVideo describes a YouTube explainer video for a reference card page.
//
// Heading and Duration live here rather than in the template so that adding a
// second card's video cannot silently render the first card's copy.
type cardVideo struct {
	ID       string // YouTube video ID
	Heading  string
	Duration string
	// Captions controls whether the page advertises captions and asks the player
	// to turn them on by default. Currently false for every card: reviewed EN and
	// HI tracks exist on YouTube, but viewers pick their own language there, and
	// the page should not claim a caption state it does not control. Set true per
	// card only if we decide to surface and force one.
	Captions bool
}

// cardVideos maps a reference-page slug to its video. Adding a card is a
// three-line edit plus a deploy; move this to a reference_pages column if it
// ever needs to be editable from the admin UI.
var cardVideos = map[string]cardVideo{
	"amex-platinum": {
		ID:       "jmcufDcHZwE",
		Heading:  "Watch: is the $895 fee worth it?",
		Duration: "3:30",
		Captions: false,
	},
	"chase-sapphire-reserve": {
		ID:       "qPnU9E9av-k",
		Heading:  "Watch: is the $795 fee worth it?",
		Duration: "3:00",
		Captions: false,
	},
	// Coverage framing, per the convention: this video also lands negative on
	// credits alone (-$80/yr), earning its keep on 4x dining and groceries instead.
	"amex-gold": {
		ID:       "kR901bUzB3s",
		Heading:  "Watch: do the credits cover the $325 fee?",
		Duration: "2:40",
		Captions: false,
	},
	// Coverage framing again: credits total $600 face / $350 real against a $495
	// fee. Unlike the others the credits were never the case for this card -- the
	// argument is points on rent, which the video treats as the real question.
	"bilt-palladium": {
		ID:       "vNZd7pPXAHc",
		Heading:  "Watch: do the credits cover the $495 fee?",
		Duration: "2:50",
		Captions: false,
	},
	// Framed as a question about coverage rather than worth: this video concludes
	// the credits do not cover the fee (-$85/yr), so "is it worth it?" would set
	// up an answer the video does not give.
	"citi-strata-elite": {
		ID:       "lp6-qsI28eo",
		Heading:  "Watch: do the credits cover the $595 fee?",
		Duration: "2:20",
		Captions: false,
	},
}
