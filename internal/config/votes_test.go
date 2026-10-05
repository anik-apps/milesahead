package config

import (
	"strings"
	"testing"
)

func TestLoad_VotesAlertEmail(t *testing.T) {
	cfg, err := loadYAML(t, "votes:\n  alert_email: \"\"\n")
	if err != nil || cfg.Votes.AlertEmail != "" {
		t.Fatalf("empty: %q, %v", cfg.Votes.AlertEmail, err)
	}
	cfg, err = loadYAML(t, "votes:\n  alert_email: \" owner@example.com \"\n")
	if err != nil || cfg.Votes.AlertEmail != "owner@example.com" {
		t.Fatalf("set: %q, %v", cfg.Votes.AlertEmail, err)
	}
	for _, bad := range []string{"owner", "Owner <owner@example.com>", "a@b@c"} {
		if _, err := loadYAML(t, "votes:\n  alert_email: \""+bad+"\"\n"); err == nil || !strings.Contains(err.Error(), "votes.alert_email") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
	// From the environment, like the deploy's secret.
	t.Setenv("ALERT_EMAIL_TEST", "env@example.com")
	cfg, err = loadYAML(t, "votes:\n  alert_email: \"${ALERT_EMAIL_TEST}\"\n")
	if err != nil || cfg.Votes.AlertEmail != "env@example.com" {
		t.Errorf("from env: %q, %v", cfg.Votes.AlertEmail, err)
	}
}
