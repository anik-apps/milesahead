package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/mail"
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
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Database
	database, err := db.Open(cfg.DB.Path)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	// AI
	hfClient := ai.NewHFClient(cfg.AI.HFAPIToken, cfg.AI.MaxRetries)
	cache := ai.NewCache(time.Duration(cfg.AI.CacheTTL) * time.Second)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)

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
		results, err := scnr.ScanAll()
		if err != nil {
			log.Printf("Scan error: %v", err)
			return
		}
		log.Printf("Scan complete: %d sources", len(results))
	})
	sched.AddMailPollJob(time.Duration(cfg.Mail.IMAP.PollInterval)*time.Second, func() {
		if err := mailPoller.Poll(); err != nil {
			log.Printf("Mail poll error: %v", err)
		}
	})
	sched.AddNewsletterJob(cfg.Newsletter.SendTime, cfg.Newsletter.Timezone, func() {
		if err := newsletterSvc.SendDaily(); err != nil {
			log.Printf("Newsletter send error: %v", err)
		}
	})
	sched.Start()
	defer sched.Stop()

	// Web server
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc)
	if err != nil {
		log.Fatalf("failed to create web app: %v", err)
	}

	log.Printf("Miles Ahead running on :%d", cfg.Server.Port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", cfg.Server.Port), app))
}
