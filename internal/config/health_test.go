package config

import (
	"strings"
	"testing"
)

func TestLoad_HealthCheckInterval(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want int
	}{
		{"ai:\n  critic_model: a\n", DefaultHealthCheckInterval},
		{"ai:\n  health_check_interval: 0\n", DefaultHealthCheckInterval},
		{"ai:\n  health_check_interval: 15\n", 15},
		{"ai:\n  health_check_interval: -1\n", -1},
	} {
		cfg, err := loadYAML(t, tc.yaml)
		if err != nil {
			t.Fatalf("%q: %v", tc.yaml, err)
		}
		if cfg.AI.HealthCheckInterval != tc.want {
			t.Errorf("%q: interval %d, want %d", tc.yaml, cfg.AI.HealthCheckInterval, tc.want)
		}
	}
}

func TestLoad_AIAlertEmail(t *testing.T) {
	cfg, err := loadYAML(t, "ai:\n  alert_email: \" ops@example.com \"\n")
	if err != nil || cfg.AI.AlertEmail != "ops@example.com" {
		t.Fatalf("set: %q, %v", cfg.AI.AlertEmail, err)
	}
	for _, bad := range []string{"ops", "Ops <ops@example.com>"} {
		if _, err := loadYAML(t, "ai:\n  alert_email: \""+bad+"\"\n"); err == nil || !strings.Contains(err.Error(), "ai.alert_email") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestModelAlertEmail_FallsBackToVotes(t *testing.T) {
	c := &Config{}
	c.Votes.AlertEmail = "votes@example.com"
	if got := c.ModelAlertEmail(); got != "votes@example.com" {
		t.Errorf("fallback = %q", got)
	}
	c.AI.AlertEmail = "ai@example.com"
	if got := c.ModelAlertEmail(); got != "ai@example.com" {
		t.Errorf("own = %q", got)
	}
}
