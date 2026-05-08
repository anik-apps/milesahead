package db

import "time"

// Article status constants.
const (
	StatusDraft             = "draft"
	StatusPending           = "pending"
	StatusApproved          = "approved"
	StatusRejected          = "rejected"
	StatusRevisionRequested = "revision_requested"
	StatusUnpublished       = "unpublished"
)

type Source struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	URL            string    `json:"url"`
	RSSURL         *string   `json:"rss_url"`
	ScrapeSelector *string   `json:"scrape_selector"`
	ScanInterval   *int      `json:"scan_interval"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type RawArticle struct {
	ID          int64     `json:"id"`
	SourceID    int64     `json:"source_id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Content     string    `json:"content"`
	PublishedAt time.Time `json:"published_at"`
	Hash        string    `json:"hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type GeneratedArticle struct {
	ID             int64      `json:"id"`
	Title          string     `json:"title"`
	Slug           string     `json:"slug"`
	Content        string     `json:"content"`
	Type           string     `json:"type"`
	Status         string     `json:"status"`
	SourcesJSON    string     `json:"sources_json"`
	RevisionNumber int        `json:"revision_number"`
	Featured       bool       `json:"featured"`
	TLDR           string     `json:"tldr"`
	KeyDetails     string     `json:"key_details"`
	Links          string     `json:"links"`
	ImageURL         string     `json:"image_url"`
	ImageThumbnail   string     `json:"image_thumbnail"`
	ImageAttribution string     `json:"image_attribution"`
	ScoutsTake       string     `json:"scouts_take"`
	RelatedCardsJSON   string     `json:"related_cards_json"`
	TagsJSON           string     `json:"tags_json"`
	LinkedCardsHeading string     `json:"linked_cards_heading"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	PublishedAt      *time.Time `json:"published_at"`
}

type ArticleSource struct {
	GeneratedArticleID int64 `json:"generated_article_id"`
	RawArticleID       int64 `json:"raw_article_id"`
}

type VerificationResult struct {
	ID                 int64     `json:"id"`
	GeneratedArticleID int64     `json:"generated_article_id"`
	RevisionNumber     int       `json:"revision_number"`
	Score              int       `json:"score"`
	IssuesJSON         string    `json:"issues_json"`
	FactualCheck       string    `json:"factual_check"`
	AttributionCheck   string    `json:"attribution_check"`
	CompletenessCheck  string    `json:"completeness_check"`
	HallucinationCheck string    `json:"hallucination_check"`
	VerifiedAt         time.Time `json:"verified_at"`
}

type AdminFeedback struct {
	ID                 int64     `json:"id"`
	GeneratedArticleID int64     `json:"generated_article_id"`
	FeedbackText       string    `json:"feedback_text"`
	Source             string    `json:"source"`
	CreatedAt          time.Time `json:"created_at"`
}

type ArticleRevision struct {
	ID                 int64     `json:"id"`
	GeneratedArticleID int64     `json:"generated_article_id"`
	RevisionNumber     int       `json:"revision_number"`
	PreviousContent    string    `json:"previous_content"`
	NewContent         string    `json:"new_content"`
	FeedbackID         int64     `json:"feedback_id"`
	CreatedAt          time.Time `json:"created_at"`
}

type ArticleVote struct {
	ID        int64     `json:"id"`
	ArticleID int64     `json:"article_id"`
	Vote      int       `json:"vote"`
	IPHash    string    `json:"ip_hash"`
	CreatedAt time.Time `json:"created_at"`
}

type Newsletter struct {
	ID                  int64      `json:"id"`
	Date                string     `json:"date"`
	Subject             string     `json:"subject"`
	HTMLContent         string     `json:"html_content"`
	Status              string     `json:"status"`
	ArticleCount        int        `json:"article_count"`
	CardOfWeekID        *int64     `json:"card_of_week_id"`
	ScoutsPickArticleID *int64     `json:"scouts_pick_article_id"`
	SentAt              *time.Time `json:"sent_at"`
}

type NewsletterCandidate struct {
	ID           int64     `json:"id"`
	NewsletterID int64     `json:"newsletter_id"`
	ArticleID    int64     `json:"article_id"`
	Rank         int       `json:"rank"`
	Featured     bool      `json:"featured"`
	CreatedAt    time.Time `json:"created_at"`
}

type Subscriber struct {
	ID           int64      `json:"id"`
	Email        string     `json:"email"`
	Confirmed    bool       `json:"confirmed"`
	Token        string     `json:"token"`
	SubscribedAt time.Time  `json:"subscribed_at"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
}

type AdminSession struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// ReferenceSection represents a single section within a reference page.
type ReferenceSection struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// RecentChange records when a new article affects a reference page.
type RecentChange struct {
	Date        string `json:"date"`
	Summary     string `json:"summary"`
	ArticleID   int64  `json:"article_id"`
	ArticleSlug string `json:"article_slug"`
}

// PendingUpdate holds an AI-drafted section update awaiting admin approval.
type PendingUpdate struct {
	ID              int    `json:"id"`
	ArticleID       int64  `json:"article_id"`
	ArticleTitle    string `json:"article_title"`
	SectionKey      string `json:"section_key"`
	CurrentContent  string `json:"current_content"`
	ProposedContent string `json:"proposed_content"`
	Summary         string `json:"summary"`
	CreatedAt       string `json:"created_at"`
}

// ReferencePage represents a curated reference page (e.g., a credit card).
type ReferencePage struct {
	ID                 int64     `json:"id"`
	Slug               string    `json:"slug"`
	Title              string    `json:"title"`
	Category           string    `json:"category"`
	Issuer             string    `json:"issuer"`
	SectionsJSON       string    `json:"sections_json"`
	RecentChangesJSON  string    `json:"recent_changes_json"`
	PendingUpdatesJSON string    `json:"pending_updates_json"`
	ScoutsVerdict      string    `json:"scouts_verdict"`
	KeywordsJSON       string    `json:"keywords_json"`
	ImageURL           string    `json:"image_url"`
	ApplyURL               string     `json:"apply_url"`
	AnnualFeeAmount        int        `json:"annual_fee_amount"`
	AnnualFeeNote          string     `json:"annual_fee_note"`
	WelcomeBonusText       string     `json:"welcome_bonus_text"`
	WelcomeBonusVerifiedAt *time.Time `json:"welcome_bonus_verified_at"`
	EarningRatesJSON       string     `json:"earning_rates_json"`
	CreditsJSON            string     `json:"credits_json"`
	BenefitsJSON           string     `json:"benefits_json"`
	ProtectionsJSON        string     `json:"protections_json"`
	FlaggedFieldsJSON      string     `json:"flagged_fields_json"`
	Status                 string     `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}
