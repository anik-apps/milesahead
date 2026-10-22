package main

import (
	"strings"
	"testing"
	"time"

	"github.com/kumaranik/milesahead/internal/config"
)

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
