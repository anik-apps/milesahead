package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	ScansTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_scans_total",
		Help: "Total scans completed",
	}, []string{"source", "status"})

	ArticlesGeneratedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_articles_generated_total",
		Help: "Total articles generated",
	}, []string{"type", "status"})

	ArticlesApprovedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_articles_approved_total",
		Help: "Total articles approved",
	})

	ArticlesRejectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_articles_rejected_total",
		Help: "Total articles rejected",
	})

	RevisionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_revisions_total",
		Help: "Total revisions processed",
	}, []string{"status"}) // improved, unchanged, failed

	AIAPIDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_ai_api_duration_seconds",
		Help:    "AI API call duration including retries",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300},
	})

	AIAPIErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "milesahead_ai_api_errors_total",
		Help: "Total AI API final failures",
	})

	CriticScore = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "milesahead_critic_score",
		Help:    "Critic verification scores",
		Buckets: []float64{25, 50, 75, 85, 95},
	})

	VotesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_votes_total",
		Help: "Total votes cast",
	}, []string{"direction"}) // up, down

	NewslettersSentTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_newsletters_sent_total",
		Help: "Total newsletters sent",
	}, []string{"status"}) // success, error

	NewsletterRecipientsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_newsletter_recipients_total",
		Help: "Total newsletter recipients",
	}, []string{"status"}) // success, error

	MailPollsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_mail_polls_total",
		Help: "Total IMAP mail polls",
	}, []string{"status"}) // success, error

	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "milesahead_http_requests_total",
		Help: "Total HTTP requests",
	}, []string{"method", "path", "status"})

	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "milesahead_http_duration_seconds",
		Help:    "HTTP request duration",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})
)

func Init() {
	prometheus.MustRegister(
		ScansTotal, ArticlesGeneratedTotal, ArticlesApprovedTotal,
		ArticlesRejectedTotal, RevisionsTotal, AIAPIDuration,
		AIAPIErrorsTotal, CriticScore, VotesTotal,
		NewslettersSentTotal, NewsletterRecipientsTotal,
		MailPollsTotal, HTTPRequestsTotal, HTTPDuration,
	)
}

// RegisterSubscribersGauge registers a gauge that polls the DB for subscriber count.
func RegisterSubscribersGauge(countFn func() float64) {
	prometheus.MustRegister(prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Name: "milesahead_subscribers_total",
			Help: "Current confirmed subscriber count",
		},
		countFn,
	))
}
