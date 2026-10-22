package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/health"
	"github.com/kumaranik/milesahead/internal/images"
)

// The vision scorer records under the same step name health judges.
func TestVisionStepName(t *testing.T) {
	if images.VisionStep != health.StepVision {
		t.Errorf("images.VisionStep %q != health.StepVision %q", images.VisionStep, health.StepVision)
	}
}

func TestModelAlertRecipient(t *testing.T) {
	cfg := &config.Config{}
	if to, why := modelAlertRecipient(cfg, false); to != "" || !strings.Contains(why, "ALERT_EMAIL") {
		t.Errorf("no address: %q %q", to, why)
	}
	cfg.Votes.AlertEmail = "ops@example.com"
	if to, why := modelAlertRecipient(cfg, true); to != "ops@example.com" || !strings.Contains(why, "SMTP") {
		t.Errorf("smtp unset: %q %q", to, why)
	}
	if to, why := modelAlertRecipient(cfg, false); to != "ops@example.com" || why != "" {
		t.Errorf("ok: %q %q", to, why)
	}
}

func TestHealthInterval(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.HealthCheckInterval = 60
	if got := healthInterval(cfg); got != time.Hour {
		t.Errorf("60 -> %v", got)
	}
	cfg.AI.HealthCheckInterval = -1
	if got := healthInterval(cfg); got != 0 {
		t.Errorf("-1 -> %v, want 0 (off)", got)
	}
}
