package main

import (
	"log/slog"
	"time"

	"github.com/kumaranik/milesahead/internal/ai"
	"github.com/kumaranik/milesahead/internal/config"
)

// logPreflight logs one check round the way the boot preflight always has.
func logPreflight(results []ai.PreflightResult) {
	switch {
	case ai.PublishingViable(results):
		slog.Info("ai preflight complete", "summary", ai.PreflightSummary(results))
	case ai.GenerationViable(results):
		slog.Error("ai preflight: no CRITIC is reachable - articles will be "+
			"generated but every one will fail verification and none will publish",
			"summary", ai.PreflightSummary(results))
	default:
		slog.Error("ai preflight: NO generator model is reachable - "+
			"article generation cannot run until config is fixed", "summary", ai.PreflightSummary(results))
	}
}

// modelAlertRecipient is where model-health alerts go and, when they cannot
// be emailed, why (shown on the Models tab).
func modelAlertRecipient(cfg *config.Config, smtpUnset bool) (to, notEmailed string) {
	to = cfg.ModelAlertEmail()
	switch {
	case to == "":
		return "", "no address: set the ALERT_EMAIL repository secret (ai.alert_email or votes.alert_email)"
	case smtpUnset:
		return to, "SMTP is not configured"
	}
	return to, ""
}

// healthInterval is the time between model health checks; 0 when off.
func healthInterval(cfg *config.Config) time.Duration {
	if cfg.AI.HealthCheckInterval <= 0 {
		return 0
	}
	return time.Duration(cfg.AI.HealthCheckInterval) * time.Minute
}
