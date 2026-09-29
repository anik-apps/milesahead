package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cardupdate"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/enrich"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/linker"
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

	// Shutdown context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

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
	hfClient.SetRequestTimeout(time.Duration(cfg.AI.RequestTimeout) * time.Second)
	cache := ai.NewCache(time.Duration(cfg.AI.CacheTTL) * time.Second)
	generator := ai.NewGenerator(hfClient, cfg.AI.GeneratorModel, cfg.AI.FallbackGeneratorModel, cache)
	critic := ai.NewCritic(hfClient, cfg.AI.CriticModel)
	// Was hardcoded to meta/llama-3.2-90b-vision-instruct, which now times out
	// on every call (120s+, reproducible) -- so each image scored burned the
	// client's full request timeout before failing. Configurable now, so the
	// next retirement is a config edit rather than a release. Declared here so
	// the preflight below can probe it too.
	visionModel := cfg.AI.VisionModel
	if visionModel == "" {
		visionModel = "meta/llama-3.2-11b-vision-instruct"
	}

	// Title-similarity threshold for treating two scanned articles as the same
	// story. See cluster.DefaultThreshold for why it is not 0.5 any more.
	clusterThreshold := cfg.Scanner.ClusterThreshold
	if clusterThreshold <= 0 {
		clusterThreshold = cluster.DefaultThreshold
	}

	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)
	if cfg.AI.Critic2Model != "" {
		critic2 := ai.NewCritic(hfClient, cfg.AI.Critic2Model)
		pipeline.SetCritic2(critic2)
	}

	// Confirm the configured models still exist before the scheduler starts
	// calling them every three hours. A retired model answers 410 forever, and
	// without this the only symptom is articles quietly never appearing.
	go func() {
		results := ai.PreflightAll(hfClient, cfg.AI.GeneratorModel,
			ai.SplitModels(cfg.AI.FallbackGeneratorModel),
			[]string{cfg.AI.CriticModel, cfg.AI.Critic2Model},
			[]string{visionModel})
		switch {
		case ai.PublishingViable(results):
			slog.Info("ai preflight complete", "summary", ai.PreflightSummary(results))
		case ai.GenerationViable(results):
			slog.Error("ai preflight: no CRITIC is reachable - articles will be "+
				"generated but every one will fail verification and none will publish",
				"summary", ai.PreflightSummary(results))
		default:
			slog.Error("ai preflight: NO generator model is reachable - "+
				"article generation cannot run until config is fixed",
				"summary", ai.PreflightSummary(results))
		}
	}()

	// Image search and scoring
	imageSearcher := images.NewSearcher(cfg.AI.UnsplashKey, cfg.AI.PexelsKey)
	imageScorer := images.NewScorer(hfClient, cfg.AI.CriticModel)
	if cfg.AI.HFBaseURL != "" {
		imageScorer.SetVisionModel(cfg.AI.HFBaseURL, cfg.AI.HFAPIToken, visionModel)
	}

	// Enrichment runs as a reconciliation pass rather than inline in article
	// creation. Inline, each step was a best-effort call that logged a warning
	// and moved on, so one slow response permanently cost the article a field.
	// Reconciled, a miss is picked up on the next pass.
	enrichModels := ai.SplitModels(cfg.AI.EnrichmentModel)
	if len(enrichModels) == 0 {
		// Fall back to the critic chain rather than silently doing nothing.
		enrichModels = append([]string{cfg.AI.CriticModel},
			ai.SplitModels(cfg.AI.FallbackGeneratorModel)...)
	}
	enricher := enrich.New(database, hfClient, enrichModels,
		cfg.Site.PersonaName, imageSearcher, imageScorer)
	if cfg.AI.EnrichmentMaxAttempts > 0 {
		enricher.MaxAttempts = cfg.AI.EnrichmentMaxAttempts
	}
	if cfg.AI.EnrichmentMaxDuration > 0 {
		enricher.MaxDuration = time.Duration(cfg.AI.EnrichmentMaxDuration) * time.Minute
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
	newsletterSvc := newsletter.NewServiceWithAI(database, mailSender, cfg.Newsletter, hfClient, cfg.AI.CriticModel, cfg.Server.Domain)

	// Scheduler
	sched := scheduler.New()
	sched.AddScanJob(time.Duration(cfg.Scanner.DefaultInterval)*time.Minute, func() {
		select {
		case <-ctx.Done():
			slog.Info("scan job skipped, shutting down")
			return
		default:
		}
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

		clusters := cluster.Cluster(clusterArticles, clusterThreshold)
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

			// Skip if we already have articles from these source articles
			exists, _ := database.HasArticleForRawIDs(rawIDs)
			if exists {
				slog.Info("skipping cluster, articles already exist for these sources", "topic", c.Articles[0].Title)
				continue
			}

			result, err := pipeline.Process("auto", sa)
			if err != nil {
				slog.Warn("article generation skipped", "topic", c.Articles[0].Title, "error", err)
				continue
			}

			// Image search: generate queries from the article title

			// Auto-link card mentions: insert inline <a> tags for first mention
			// of each card, cap RelatedCards to top 2 by mention count. Articles
			// with 3+ mentioned cards rely on admin curation via
			// /admin/articles/{id}/cards.
			refPages, _ := database.ListPublishedReferencePages("")
			if len(refPages) > 0 {
				linkedContent, _ := linker.AutoLink(result.Content, refPages)
				result.Content = linkedContent

				detected := linker.DetectMentions(result.Content, refPages)
				const autoLinkCap = 2
				top := linker.TopByMentionCount(detected, autoLinkCap)
				for _, dc := range top {
					result.RelatedCards = append(result.RelatedCards, ai.RelatedCard{
						Slug:  dc.Slug,
						Title: dc.Title,
					})
				}
				if len(detected) > 0 {
					slog.Info("auto-linked card mentions",
						"title", result.Title,
						"detected", len(detected),
						"linked", len(top),
					)
				}
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
	// Friday 20:00 ET — build newsletter
	sched.AddWeeklyJob("newsletter-build",
		"CRON_TZ=America/New_York 0 0 20 * * 5",
		func() {
			if _, err := newsletterSvc.BuildWeekly(); err != nil {
				slog.Error("newsletter build failed", "error", err)
			}
		})

	// Saturday 08:00 ET — send newsletter
	sched.AddWeeklyJob("newsletter-send",
		"CRON_TZ=America/New_York 0 0 8 * * 6",
		func() {
			if err := newsletterSvc.SendWeekly(); err != nil {
				slog.Error("newsletter send failed", "error", err)
			}
		})
	enrichInterval := time.Duration(cfg.AI.EnrichmentInterval) * time.Minute
	if enrichInterval <= 0 {
		enrichInterval = 15 * time.Minute
	}
	sched.AddEnrichmentJob(enrichInterval, enricher.Run)
	slog.Info("enrichment reconciler scheduled",
		"interval", enrichInterval, "models", enrichModels,
		"max_attempts", enricher.MaxAttempts)

	scheduleRevisionReconciler(ctx, sched, articleMgr, revisionReconcileInterval)

	sched.Start()

	// Card update detector
	detector := cardupdate.NewDetector(database, hfClient, cfg.AI.CriticModel)

	// Web server
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc, newsletterSvc, detector, ctx)
	if err != nil {
		slog.Error("failed to create web app", "error", err)
		os.Exit(1)
	}

	// Web server with explicit Server struct for graceful shutdown
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Server.Port),
		Handler: app,
	}

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("server starting", "port", cfg.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for shutdown signal or server error
	select {
	case sig := <-sigCh:
		slog.Info("shutting down...", "signal", sig.String())
	case err := <-serverErr:
		slog.Error("server error", "error", err)
		cancel()
		return // defers will run
	}
	cancel()

	// Shutdown is bounded end to end -- scheduler, then HTTP, then background
	// work -- to shutdownBudget in total, inside systemd's default 90s stop
	// timeout, so a stop ends here rather than in SIGKILL.

	// Stop the scheduler, waiting for running jobs within a bound. A revision
	// pass can be mid-rewrite, and one AI call with its retries can take
	// minutes; the context stops the pass starting anything new, but cannot
	// interrupt the call. Abandoning it is safe: nothing is stored until the
	// rewrite finishes, so the article stays revision_requested and the next
	// process's startup pass redoes it.
	if !waitBounded(sched.Stop, schedulerStopWait) {
		slog.Warn("scheduled jobs still running at shutdown; abandoning them",
			"waited", schedulerStopWait)
	}

	// Shutdown HTTP server with a timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownWait)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	// Requests can leave AI rewrites running in the background (the Revise
	// button, unpublish-and-revise). Give them a bounded wait so they do not run
	// on against the database closed below. One still unfinished is harmless:
	// its article stays revision_requested and the next process's startup
	// revision pass resumes it.
	if !waitBounded(app.WaitBackground, backgroundShutdownWait) {
		slog.Warn("background work still running at shutdown; unfinished revisions resume on next start",
			"waited", backgroundShutdownWait)
	}

	slog.Info("shutdown complete")
}

// revisionReconcileInterval is how often articles waiting for an AI rewrite are
// swept. Short, because an admin is usually waiting on the result; each pass is
// one cheap query when there is nothing to do.
const revisionReconcileInterval = 5 * time.Minute

// scheduleRevisionReconciler registers the pass that rewrites articles left in
// revision_requested, plus one pass shortly after startup.
//
// Rewrites were previously started only by the Revise button's one-shot
// goroutine, so an AI failure or a deploy mid-rewrite left the article
// "Revising" for hours, and email replies and unpublish-and-revise never started
// one at all. This sweep is the backstop for every path; the manager's in-flight
// guard keeps it from rewriting an article a request is already rewriting, and
// its retry budget keeps it from retrying a hopeless one forever.
func scheduleRevisionReconciler(ctx context.Context, sched *scheduler.Scheduler, mgr *article.Manager, interval time.Duration) {
	sched.AddRevisionJob(interval, func() {
		if ctx.Err() != nil {
			slog.Info("revision pass skipped, shutting down")
			return
		}
		mgr.ProcessPendingRevisions(ctx)
	})
	slog.Info("revision reconciler scheduled", "interval", interval,
		"max_failures", article.DefaultRevisionMaxFailures)
}

// Shutdown bounds. Their sum, shutdownBudget, must stay under systemd's
// TimeoutStopSec (the default, 90s, since deploy/milesahead.service does not
// set it), or the stop ends in SIGKILL. An AI rewrite can take minutes, so none
// of these waits for one to finish; the revision reconciler makes abandoning a
// rewrite safe.
//
// The scheduler gets most of the budget. Its other jobs -- a newsletter send,
// an enrichment pass, a scan -- are not safe to cut off the way a rewrite is,
// and before this bound they had up to the full 90s before SIGKILL; 60s keeps
// nearly all of that. HTTP requests finish in well under 10s, and a
// request-started rewrite left running resumes on the next start.
const (
	schedulerStopWait      = 60 * time.Second
	httpShutdownWait       = 10 * time.Second
	backgroundShutdownWait = 15 * time.Second
	shutdownBudget         = schedulerStopWait + httpShutdownWait + backgroundShutdownWait
)

// waitBounded runs wait and reports whether it returned within d.
func waitBounded(wait func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
