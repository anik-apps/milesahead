package web

// cardVideo describes a YouTube explainer video for a reference card page.
//
// Heading and Duration live here rather than in the template so that adding a
// second card's video cannot silently render the first card's copy.
type cardVideo struct {
	ID       string // YouTube video ID
	Heading  string
	Duration string
}

// cardVideos maps a reference-page slug to its video. Adding a card is a
// three-line edit plus a deploy; move this to a reference_pages column if it
// ever needs to be editable from the admin UI.
var cardVideos = map[string]cardVideo{
	"amex-platinum": {
		ID:       "jmcufDcHZwE",
		Heading:  "Watch: is the $895 fee worth it?",
		Duration: "3:30",
	},
}
