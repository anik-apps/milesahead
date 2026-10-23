package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/article"
	"github.com/kumaranik/milesahead/internal/cardupdate"
	"github.com/kumaranik/milesahead/internal/cluster"
	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/db"
	"github.com/kumaranik/milesahead/internal/enrich"
	"github.com/kumaranik/milesahead/internal/health"
	"github.com/kumaranik/milesahead/internal/images"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/metrics"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/scanner"
	"github.com/kumaranik/milesahead/internal/scheduler"
	"github.com/kumaranik/milesahead/internal/storydedup"
	"github.com/kumaranik/milesahead/internal/vote"
	"github.com/kumaranik/milesahead/internal/web"
)

// newImageScoring builds the image searcher and scorer. With an endpoint for
// vision scoring configured, the searcher warms the thumbnails the scorer is
// about to send: Wikimedia renders a new size before the vision endpoint
// fetches it, so a slow first render does not fail its scoring. textModels is
// the chain for the text pre-filter; see enrichmentModels.
func newImageScoring(cfg *config.Config, client ai.HFClientInterface, visionModel string, textModels []string) (*images.Searcher, *images.Scorer) {
	searcher := images.NewSearcher(cfg.AI.UnsplashKey, cfg.AI.PexelsKey)
	scorer := images.NewScorer(client, textModels)
	if cfg.AI.HFBaseURL != "" {
		scorer.SetVisionModel(cfg.AI.HFBaseURL, cfg.AI.HFAPIToken, visionModel, searcher)
	}
	return searcher, scorer
}

// sourceOutlets reads every row of the sources table, active or not, as the
// outlets articles must not name. It is read on each use, so a source added in
// the admin is covered from the next article on. A read error leaves only the
// article's own sources and the built-in outlets to check, which is logged.
func sourceOutlets(database *db.Database) func() []ai.Outlet {
	return func() []ai.Outlet {
		rows, err := database.ListAllSources()
		if err != nil {
			slog.Warn("could not list sources for the outlet check", "error", err)
		}
		out := make([]ai.Outlet, 0, len(rows))
		for _, r := range rows {
			out = append(out, ai.Outlet{Name: r.Name, URL: r.URL})
		}
		return out
	}
}

// enrichmentModels is the chain for every short AI call; see
// health.EnrichmentModels.
func enrichmentModels(cfg *config.Config) []string { return health.EnrichmentModels(cfg) }

// newCriticClient returns the client the critics call through. With
// critic_disable_thinking set it is a copy of hfClient that asks for no
// reasoning phase; the generator, enrichment, side jobs and vision scoring keep
// using hfClient itself, so the setting cannot leak into them.
//
// With more than one critic, each retries at most once before the chain moves
// on. A critic whose gateway times out takes ~5 minutes per attempt, so the
// usual retries held an article ~20 minutes before the next critic was tried;
// one retry still absorbs a single transient error.
func newCriticClient(cfg *config.Config, hfClient *ai.HFClient) *ai.HFClient {
	c := hfClient
	if cfg.AI.CriticDisableThinking {
		c = c.WithThinkingDisabled()
	}
	if len(ai.SplitModels(cfg.AI.CriticModel)) > 1 && cfg.AI.MaxRetries > criticChainRetries {
		c = c.WithMaxRetries(criticChainRetries)
	}
	return c
}

// newInfographicClient returns the client the infographic call goes through:
// hfClient bounded at ai.InfographicCallTimeout, with no retries. Its answer
// is long enough to fall outside the short-call class, so through hfClient
// itself it would inherit request_timeout (900s in production) and
// max_retries; the model chain and the next pass are its retries instead.
func newInfographicClient(hfClient *ai.HFClient) *ai.HFClient {
	return hfClient.WithRequestTimeout(ai.InfographicCallTimeout).WithMaxRetries(0)
}

// newCardUpdateClient returns the client card-update detection goes through:
// the shared client bounded at ai.CardUpdateCallTimeout, with one retry. The
// reply is a whole card section, so it falls outside the short-call class, and
// through the shared client it would inherit request_timeout (900s) and
// max_retries. One retry, not none: detection runs once, when the article is
// approved, so a check that fails is not tried again on a later pass. A client
// that is not an *ai.HFClient (a test's) is used as it is.
func newCardUpdateClient(client ai.HFClientInterface) ai.HFClientInterface {
	if hf, ok := client.(*ai.HFClient); ok {
		return hf.WithRequestTimeout(ai.CardUpdateCallTimeout).WithMaxRetries(1)
	}
	return client
}

// infographicModels is the chain charts are drawn with; see
// health.InfographicModels.
func infographicModels(cfg *config.Config, _ []string) []string { return health.InfographicModels(cfg) }

// preflightEnrichmentModels is what the boot preflight checks under the
// enrichment role: the enrichment chain, plus any chart model not already in
// it while charts are drawn, so a retired or mistyped infographic_model is
// reported at boot like any other model rather than on the first chart.
func preflightEnrichmentModels(cfg *config.Config, enrichModels []string) []string {
	out := append([]string(nil), enrichModels...)
	if !cfg.AI.DrawInfographics() {
		return out
	}
	seen := map[string]bool{}
	for _, m := range out {
		seen[m] = true
	}
	for _, m := range infographicModels(cfg, enrichModels) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// criticChainRetries is how often each critic in a multi-critic chain retries.
const criticChainRetries = 1

// criticModels is every critic the preflight should probe: the critic_model
// chain, then critic2_model.
func criticModels(cfg *config.Config) []string {
	return append(ai.SplitModels(cfg.AI.CriticModel), ai.SplitModels(cfg.AI.Critic2Model)...)
}

// aiSideJobs are the components whose AI calls are short side jobs rather than
// article generation or verification.
type aiSideJobs struct {
	imageSearcher *images.Searcher
	imageScorer   *images.Scorer
	newsletter    *newsletter.Service
	cardDetector  *cardupdate.Detector
}

// newAISideJobs wires every side job to the same short-call model chain, so
// none of them depends on the critic.
func newAISideJobs(cfg *config.Config, database *db.Database, client ai.HFClientInterface,
	sender newsletter.MailSender, visionModel string, models []string) aiSideJobs {
	searcher, scorer := newImageScoring(cfg, client, visionModel, models)
	return aiSideJobs{
		imageSearcher: searcher,
		imageScorer:   scorer,
		newsletter:    newsletter.NewServiceWithAI(database, sender, cfg.Newsletter, client, models, cfg.Server.Domain),
		cardDetector:  cardupdate.NewDetector(database, newCardUpdateClient(client), models),
	}
}

// warnIfSMTPUnset logs, once at startup, when the SMTP settings look unset
// (smtpLooksUnset, passed in as unset): no email can be sent, so the weekly
// newsletter is skipped and sign-up stays closed. Only the host is logged,
// never the credentials.
func warnIfSMTPUnset(c config.SMTPConfig, unset bool) {
	if unset {
		slog.Warn("mail: SMTP is not configured; no newsletter or confirmation email will be sent",
			"host", c.Host, "from_set", c.From != "")
	}
}

// logReviewByEmail says once, at startup, that review by email is off and
// why. Only whether IMAP looks set is logged, never its settings.
func logReviewByEmail(c config.IMAPConfig) {
	if on, why := mail.ReviewByEmail(c); !on {
		slog.Info("review by email is off (" + why + ")")
	}
}

// smtpLooksUnset reports whether c cannot be a working SMTP setup: no host
// or From, credentials still holding a placeholder ("placeholder", or an
// unexpanded "${...}"), or a relay on this machine that nothing answers.
// Empty credentials are allowed, for a relay that needs none.
func smtpLooksUnset(c config.SMTPConfig) bool {
	if c.Host == "" || c.From == "" {
		return true
	}
	for _, v := range []string{c.User, c.Password} {
		if v == "placeholder" || strings.HasPrefix(v, "${") {
			return true
		}
	}
	switch c.Host {
	case "localhost", "127.0.0.1", "::1":
		return !smtpReachable(net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	}
	return false
}

// smtpReachable reports whether something accepts connections at addr.
// Tests replace it.
var smtpReachable = func(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// signupMailerSetter is the part of web.App wireSignup uses.
type signupMailerSetter interface {
	SetConfirmationMailer(web.ConfirmationMailer)
}

// wireSignup opens newsletter sign-up (placements, the /subscribe form and
// confirmation emails, all sent by m) when newsletter.signup_enabled is on
// and SMTP looks configured (smtpUnset false; see smtpLooksUnset), and
// reports whether it did. On with SMTP unset is an ERROR, and sign-up stays
// closed: readers would sign up and never get the email.
func wireSignup(app signupMailerSetter, cfg *config.Config, smtpUnset bool, m web.ConfirmationMailer) bool {
	if !cfg.Newsletter.SignupEnabled {
		slog.Info("newsletter sign-up is closed (newsletter.signup_enabled is off)")
		return false
	}
	if smtpUnset {
		slog.Error("newsletter sign-up stays closed: newsletter.signup_enabled is on but SMTP is not configured",
			"host", cfg.Mail.SMTP.Host, "port", cfg.Mail.SMTP.Port, "from_set", cfg.Mail.SMTP.From != "")
		return false
	}
	app.SetConfirmationMailer(m)
	slog.Info("newsletter sign-up is open", "smtp_host", cfg.Mail.SMTP.Host)
	return true
}

// checkConfig loads the config at path as startup would, without starting
// anything. The deploy workflow runs it on the config it generated before
// copying it to the server, so a bad value (a secret that breaks the YAML,
// a non-numeric port) fails the deploy instead of the service.
func checkConfig(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Server.Port <= 0 {
		return fmt.Errorf("server.port must be set")
	}
	if cfg.DB.Path == "" {
		return fmt.Errorf("db.path must be set")
	}
	return nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	configPath := flag.String("config", "config.yaml", "path to config file")
	checkOnly := flag.Bool("check-config", false, "load the config file, report whether it is valid, and exit (non-zero if not)")
	flag.Parse()

	if *checkOnly {
		if err := checkConfig(*configPath); err != nil {
			fmt.Fprintln(os.Stderr, "config invalid:", err)
			os.Exit(1)
		}
		fmt.Println("config ok:", *configPath)
		return
	}

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
	// critic_model is a chain; see ai.Critic.
	criticClient := newCriticClient(cfg, hfClient)
	// The short-call chain; see enrichmentModels.
	enrichModels := enrichmentModels(cfg)
	// Was hardcoded to meta/llama-3.2-90b-vision-instruct, which now times out
	// on every call (120s+, reproducible) -- so each image scored burned the
	// client's full request timeout before failing. Configurable now, so the
	// next retirement is a config edit rather than a release. The default
	// lives in health.DefaultVisionModel, so the health check probes the same
	// model the scorer calls.
	visionModels := health.VisionModels(cfg)
	visionModel := strings.Join(visionModels, ",")
	// Model health: every call made through a labelled client below, and a
	// periodic check of every list, judged per step; see internal/health.
	// hfClient and criticClient stay unlabelled: they are the check's probes.
	// The worker starts after its alerting is set, below.
	worker := health.New(health.Options{
		DB:    database,
		Steps: health.Steps(cfg),
		Check: func() []ai.PreflightResult {
			results := ai.PreflightAllWithCriticClient(hfClient, criticClient, cfg.AI.GeneratorModel,
				ai.SplitModels(cfg.AI.FallbackGeneratorModel), criticModels(cfg),
				visionModels, preflightEnrichmentModels(cfg, enrichModels))
			logPreflight(results)
			return results
		},
		AdminURL: "https://" + cfg.Server.Domain + "/admin/models",
		Interval: healthInterval(cfg),
	})
	generator := ai.NewGenerator(hfClient.WithStep(health.StepGenerator, worker), cfg.AI.GeneratorModel, cfg.AI.FallbackGeneratorModel, cache)
	// The critic's client confirms each success once the reply parses as a
	// verdict; an unparseable one is a bad_reply, not an ok.
	criticLabelled := criticClient.WithStep(health.StepCritic, worker).WithConfirmedOK()
	critic := ai.NewCritic(criticLabelled, cfg.AI.CriticModel)

	// Title-similarity threshold for treating two scanned articles as the same
	// story. See cluster.DefaultThreshold for why it is not 0.5 any more.
	clusterThreshold := cfg.Scanner.ClusterThreshold
	if clusterThreshold <= 0 {
		clusterThreshold = cluster.DefaultThreshold
	}

	pipeline := ai.NewPipeline(generator, critic, cfg.AI.CriticThreshold)
	pipeline.SetDetailModels(enrichModels)
	// Key details run on enrichment_model through the critic's request shape.
	// Built from the unconfirmed critic client: confirming is the Critic's job,
	// and a confirmed copy here would never record a success.
	pipeline.SetDetailClient(criticClient.WithStep(health.StepEnrichment, worker))
	// Articles must not name or link to the sites they are written from.
	outlets := sourceOutlets(database)
	pipeline.SetOutlets(outlets)
	if cfg.AI.Critic2Model != "" {
		critic2 := ai.NewCritic(criticLabelled, cfg.AI.Critic2Model)
		pipeline.SetCritic2(critic2)
	}

	// Image scoring, the newsletter pick and card-update detection, all on the
	// short-call chain.
	mailSender := mail.NewSender(cfg.Mail.SMTP)
	// When the weekly goes out, from newsletter.send_day, send_time and
	// timezone (checked by config.Load): the cron lines below and what
	// readers are told both come from it.
	weekly, err := newsletter.ScheduleFor(cfg.Newsletter)
	if err != nil {
		slog.Error("newsletter schedule", "error", err)
		os.Exit(1)
	}
	mailSender.SetNewsletterSchedule(weekly.When())
	if err := mailSender.SetUnsubscribeMailbox(cfg.Newsletter.UnsubscribeMailbox); err != nil {
		slog.Warn("mail: List-Unsubscribe will have no mailto", "error", err)
	}
	// Checked once: it can dial a local relay.
	smtpUnset := smtpLooksUnset(cfg.Mail.SMTP)
	warnIfSMTPUnset(cfg.Mail.SMTP, smtpUnset)
	alertTo, notEmailed := modelAlertRecipient(cfg, smtpUnset)
	if notEmailed != "" {
		slog.Warn("model health alerts are shown on the admin Models tab and not emailed: " + notEmailed)
		worker.SetAlerting(nil, alertTo, notEmailed)
	} else {
		worker.SetAlerting(mailSender, alertTo, "")
	}
	// Started only now: an evaluation that ran before alerting was set would
	// take the not-emailed path and save its alerts as sent. Calls recorded
	// before this are queued and applied once it runs.
	go worker.Run(ctx)
	// Every short call (image scoring, the newsletter pick, card-update
	// detection, enrichment, clustering) goes through one labelled client.
	enrichClient := hfClient.WithStep(health.StepEnrichment, worker)
	side := newAISideJobs(cfg, database, enrichClient, mailSender, visionModel, enrichModels)
	side.imageScorer.SetRecorder(worker)

	// Enrichment runs as a reconciliation pass rather than inline in article
	// creation. Inline, each step was a best-effort call that logged a warning
	// and moved on, so one slow response permanently cost the article a field.
	// Reconciled, a miss is picked up on the next pass.
	enricher := enrich.New(database, enrichClient, enrichModels,
		cfg.Site.PersonaName, side.imageSearcher, side.imageScorer)
	enricher.Outlets = outlets
	if cfg.AI.DrawInfographics() {
		enricher.Infographics = true
		enricher.InfographicClient = newInfographicClient(hfClient).WithStep(health.StepInfographic, worker)
		enricher.InfographicModels = infographicModels(cfg, enrichModels)
		slog.Info("article infographics are drawn", "mode", cfg.AI.EnrichmentInfographics,
			"models", enricher.InfographicModels)
	}
	if cfg.AI.EnrichmentMaxAttempts > 0 {
		enricher.MaxAttempts = cfg.AI.EnrichmentMaxAttempts
	}
	if cfg.AI.EnrichmentMaxDuration > 0 {
		enricher.MaxDuration = time.Duration(cfg.AI.EnrichmentMaxDuration) * time.Minute
	}

	// Services
	scnr := scanner.NewScanner(database, cfg.Scanner.UserAgent)
	articleMgr := article.NewManager(database, pipeline)
	// Vote alerts are recorded for the admin Published page by the vote
	// service; this emails them too when votes.alert_email is set.
	var onVoteAlert func(vote.Alert)
	if notify := voteAlertMailer(cfg, database, mailSender, smtpUnset); notify != nil {
		onVoteAlert = func(a vote.Alert) { go notify(a) }
	}
	voteSvc := vote.NewService(database, cfg.Votes, onVoteAlert)
	newsletterSvc := side.newsletter
	// With no SMTP, the weekly send is skipped (a warning, not a failure)
	// and the admin's Send says why, rather than dialling a relay that is
	// not there and firing the send-failed alert every Saturday.
	newsletterSvc.SetMailUnavailable(smtpUnset)

	// Clustering and generation, with both duplicate checks; see
	// clusterGenerator.
	clusterGen := newClusterGenerator(cfg, database, pipeline, articleMgr, enrichClient, enrichModels, clusterThreshold)
	slog.Info("story dedup configured", "mode", cfg.Scanner.StoryDedup,
		"window", storydedup.Window, "lexical_threshold", storydedup.LexicalThreshold,
		"fallback_threshold", storydedup.FallbackThreshold, "models", enrichModels)

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

		// Cluster and generate articles from multi-source topics.
		clusterGen.run()
	})
	// Review by email (replying APPROVE, REJECT or feedback to a review
	// email) is not built yet, so the IMAP poller never starts; see
	// mail.ReviewByEmail and ROADMAP.md.
	logReviewByEmail(cfg.Mail.IMAP)
	// Built at 20:00 the evening before the send (Friday, by default),
	// sent on the send day (Saturday 08:00 New York time, by default).
	sched.AddWeeklyJob("newsletter-build",
		weekly.BuildCron(),
		func() {
			if _, err := newsletterSvc.BuildWeekly(); err != nil {
				slog.Error("newsletter build failed", "error", err)
			}
		})

	sched.AddWeeklyJob("newsletter-send",
		weekly.SendCron(),
		func() {
			if err := newsletterSvc.SendWeekly(); err != nil {
				slog.Error("newsletter send failed", "error", err)
			}
		})
	slog.Info("weekly newsletter scheduled", "send", weekly.When(),
		"send_cron", weekly.SendCron(), "build_cron", weekly.BuildCron())
	enrichInterval := time.Duration(cfg.AI.EnrichmentInterval) * time.Minute
	if enrichInterval <= 0 {
		enrichInterval = 15 * time.Minute
	}
	sched.AddEnrichmentJob(enrichInterval, enricher.Run)
	slog.Info("enrichment reconciler scheduled",
		"interval", enrichInterval, "models", enrichModels,
		"max_attempts", enricher.MaxAttempts)

	scheduleRevisionReconciler(ctx, sched, articleMgr, revisionReconcileInterval)

	// Confirm the configured models still exist, and keep confirming. A retired
	// model answers 410 forever, and without this the only symptom is articles
	// quietly never appearing.
	if every := healthInterval(cfg); every > 0 {
		sched.AddIntervalJob("model-health", every, 30*time.Second, worker.RunCheck)
	} else {
		// The periodic check is off; still check once at boot, as the preflight
		// always did. Through the scheduler, so shutdown joins it (bounded) like
		// any other job rather than closing the database under it.
		sched.AddStartupJob("model-health", 30*time.Second, worker.RunCheck)
	}

	sched.Start()

	// Card update detector
	detector := side.cardDetector

	// Web server
	app, err := web.NewApp(cfg, database, articleMgr, voteSvc, newsletterSvc, detector, ctx)
	if err != nil {
		slog.Error("failed to create web app", "error", err)
		os.Exit(1)
	}

	// Newsletter sign-up, with confirmation emails from the same SMTP sender
	// as the newsletter, only when newsletter.signup_enabled is on and SMTP
	// is configured.
	wireSignup(app, cfg, smtpUnset, mailSender)
	app.SetModelHealth(worker)

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
