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
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	PublishedAt    *time.Time `json:"published_at"`
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
	ID           int64      `json:"id"`
	Date         string     `json:"date"`
	Subject      string     `json:"subject"`
	HTMLContent  string     `json:"html_content"`
	Status       string     `json:"status"`
	ArticleCount int        `json:"article_count"`
	SentAt       *time.Time `json:"sent_at"`
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
