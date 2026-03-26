package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/metrics"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/scanner"
	"github.com/kumaranik/milesahead/internal/scheduler"
	"github.com/kumaranik/milesahead/internal/vote"
	"github.com/kumaranik/milesahead/internal/web"
)

// stubTokenResolver is a placeholder TokenResolver for the mail poller.
// A real implementation would look up review tokens in the database.
type stubTokenResolver struct{}

func (s *stubTokenResolver) Resolve(token string) (int64, string, error) {
	return 0, "", fmt.Errorf("token resolution not yet implemented")
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Metrics
	metrics.Init()

	// Database
	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	metrics.RegisterSubscribersGauge(func() float64 {
		subs, _ := database.ListConfirmedSubscribers()
		return float64(len(subs))
	})

	// AI
	hfClient := ai.NewHFClient(cfg.AI.HFAPIToken, cfg.AI.MaxRetries)
	if cfg.AI.HFBaseURL != "" {
		hfClient.BaseURL = cfg.AI.HFBaseURL
	}
	cache := ai.NewCache(time.Duration(cfg.AI.CacheTTL) * time.Second)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)
	if cfg.AI.Critic2Model != "" {
		critic2 := ai.NewCritic(hfClient, cfg.AI.Critic2Model)
		pipeline.SetCritic2(critic2)
	}

	// Services
	scnr := scanner.NewScanner(database, cfg.Scanner.UserAgent)
	articleMgr := article.NewManager(database, pipeline)
	mailSender := mail.NewSender(cfg.Mail.SMTP)
	mailPoller := mail.NewPoller(cfg.Mail.IMAP, articleMgr, &stubTokenResolver{})
	voteSvc := vote.NewService(database, cfg.Votes, func(articleID int64) {
		a, _ := database.GetGeneratedArticle(articleID)
		if a != nil {
			mailSender.SendVoteAlert(cfg.Mail.SMTP.From, articleID, a.Title)
		}
	})
	newsletterSvc := newsletter.NewService(database, mailSender, cfg.Newsletter)

	// Scheduler
	sched := scheduler.New()
	sched.AddScanJob(time.Duration(cfg.Scanner.DefaultInterval)*time.Minute, func() {
		slog.Info("scheduled job started", "job", "scan")
		results, err := scnr.ScanAll()
		if err != nil {
			slog.Error("scheduled job failed", "job", "scan", "error", err)
			return
		}
		slog.Info("scan complete", "sources", len(results))

		// Cluster and generate articles from multi-source topics
		sources, _ := database.ListActiveSources()
		var clusterArticles []cluster.Article
		for _, s := range sources {
			raws, _ := database.GetRawArticlesBySourceID(s.ID)
			for _, r := range raws {
				if time.Since(r.CreatedAt) < 48*time.Hour {
					clusterArticles = append(clusterArticles, cluster.Article{
						ID: r.ID, Title: r.Title, SourceID: r.SourceID, Content: r.Content,
					})
				}
			}
		}
		if len(clusterArticles) == 0 {
			return
		}

		clusters := cluster.Cluster(clusterArticles, 0.3)
		slog.Info("clustering complete", "articles", len(clusterArticles), "clusters", len(clusters))

		// Filter to multi-source clusters and generate
		for _, c := range clusters {
			sourceIDs := make(map[int64]bool)
			for _, a := range c.Articles {
				sourceIDs[a.SourceID] = true
			}
			if len(sourceIDs) < 2 {
				continue
			}

			var sa []ai.SourceArticle
			var rawIDs []int64
			for _, a := range c.Articles {
				raws, _ := database.GetRawArticlesByIDs([]int64{a.ID})
				if len(raws) == 0 {
					continue
				}
				r := raws[0]
				src, _ := database.GetSource(r.SourceID)
				name := "Unknown"
				if src != nil {
					name = src.Name
				}
				content := r.Content
				if len(content) > 1500 {
					content = content[:1500]
				}
				sa = append(sa, ai.SourceArticle{Name: name, URL: r.URL, Title: r.Title, Content: content})
				rawIDs = append(rawIDs, a.ID)
			}

			result, err := pipeline.Process("auto", sa)
			if err != nil {
				slog.Warn("article generation skipped", "topic", c.Articles[0].Title, "error", err)
				continue
			}

			id, err := articleMgr.CreateFromPipeline(result, rawIDs, result.Type)
			if err != nil {
				slog.Error("article save failed", "error", err)
				continue
			}
			art, _ := database.GetGeneratedArticle(id)
			slog.Info("article created", "id", id, "title", result.Title, "score", result.Verification.Score, "status", art.Status)
		}
	})
	if cfg.Mail.IMAP.Host != "localhost" && cfg.Mail.IMAP.Password != "placeholder" {
		sched.AddMailPollJob(time.Duration(cfg.Mail.IMAP.PollInterval)*time.Second, func() {
			slog.Info("scheduled job started", "job", "mail_poll")
			if err := mailPoller.Poll(); err != nil {
				slog.Error("scheduled job failed", "job", "mail_poll", "error", err)
			}
		})
	} else {
		slog.Info("mail polling disabled", "reason", "IMAP not configured")
	}
	sched.AddNewsletterJob(cfg.Newsletter.SendTime, cfg.Newsletter.Timezone, func() {
		slog.Info("scheduled job started", "job", "newsletter")
		if err := newsletterSvc.SendDaily(); err != nil {
			slog.Error("scheduled job failed", "job", "newsletter", "error", err)
		}
	})
	sched.Start()
	defer sched.Stop()

	// Web server
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc, newsletterSvc)
	if err != nil {
		slog.Error("failed to create web app", "error", err)
		os.Exit(1)
	}

	slog.Info("server starting", "port", cfg.Server.Port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.Server.Port), app); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
