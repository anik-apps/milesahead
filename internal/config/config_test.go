package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yaml := `
server:
  port: 8080
  domain: milesahead.kumaranik.com

admin:
  password_hash: "$2a$12$examplehash"

db:
  path: /opt/milesahead/data/milesahead.db

scanner:
  default_interval: 180
  user_agent: "MilesAhead/1.0"

ai:
  generator_model: "mistralai/Mistral-7B-Instruct-v0.3"
  critic_model: "meta-llama/Meta-Llama-3-8B-Instruct"
  hf_api_token: "test-token"
  critic_threshold: 70
  max_retries: 3
  cache_ttl: 3600

mail:
  smtp:
    host: smtp.example.com
    port: 587
    user: "smtpuser"
    password: "smtppass"
    from: "noreply@example.com"
  imap:
    host: imap.example.com
    port: 993
    user: "imapuser"
    password: "imappass"
    poll_interval: 120

newsletter:
  send_time: "08:00"
  timezone: "America/New_York"

votes:
  alert_threshold: 10
  alert_ratio: 0.4
  alert_window: 24
`

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	// Server
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Domain != "milesahead.kumaranik.com" {
		t.Errorf("Server.Domain = %q, want %q", cfg.Server.Domain, "milesahead.kumaranik.com")
	}

	// Admin
	if cfg.Admin.PasswordHash != "$2a$12$examplehash" {
		t.Errorf("Admin.PasswordHash = %q, want %q", cfg.Admin.PasswordHash, "$2a$12$examplehash")
	}

	// DB
	if cfg.DB.Path != "/opt/milesahead/data/milesahead.db" {
		t.Errorf("DB.Path = %q, want %q", cfg.DB.Path, "/opt/milesahead/data/milesahead.db")
	}

	// Scanner
	if cfg.Scanner.DefaultInterval != 180 {
		t.Errorf("Scanner.DefaultInterval = %d, want 180", cfg.Scanner.DefaultInterval)
	}
	if cfg.Scanner.UserAgent != "MilesAhead/1.0" {
		t.Errorf("Scanner.UserAgent = %q, want %q", cfg.Scanner.UserAgent, "MilesAhead/1.0")
	}

	// AI
	if cfg.AI.GeneratorModel != "mistralai/Mistral-7B-Instruct-v0.3" {
		t.Errorf("AI.GeneratorModel = %q", cfg.AI.GeneratorModel)
	}
	if cfg.AI.CriticModel != "meta-llama/Meta-Llama-3-8B-Instruct" {
		t.Errorf("AI.CriticModel = %q", cfg.AI.CriticModel)
	}
	if cfg.AI.HFAPIToken != "test-token" {
		t.Errorf("AI.HFAPIToken = %q, want %q", cfg.AI.HFAPIToken, "test-token")
	}
	if cfg.AI.CriticThreshold != 70 {
		t.Errorf("AI.CriticThreshold = %d, want 70", cfg.AI.CriticThreshold)
	}
	if cfg.AI.MaxRetries != 3 {
		t.Errorf("AI.MaxRetries = %d, want 3", cfg.AI.MaxRetries)
	}
	if cfg.AI.CacheTTL != 3600 {
		t.Errorf("AI.CacheTTL = %d, want 3600", cfg.AI.CacheTTL)
	}

	// Mail SMTP
	if cfg.Mail.SMTP.Host != "smtp.example.com" {
		t.Errorf("Mail.SMTP.Host = %q", cfg.Mail.SMTP.Host)
	}
	if cfg.Mail.SMTP.Port != 587 {
		t.Errorf("Mail.SMTP.Port = %d, want 587", cfg.Mail.SMTP.Port)
	}
	if cfg.Mail.SMTP.User != "smtpuser" {
		t.Errorf("Mail.SMTP.User = %q", cfg.Mail.SMTP.User)
	}
	if cfg.Mail.SMTP.Password != "smtppass" {
		t.Errorf("Mail.SMTP.Password = %q", cfg.Mail.SMTP.Password)
	}
	if cfg.Mail.SMTP.From != "noreply@example.com" {
		t.Errorf("Mail.SMTP.From = %q", cfg.Mail.SMTP.From)
	}

	// Mail IMAP
	if cfg.Mail.IMAP.Host != "imap.example.com" {
		t.Errorf("Mail.IMAP.Host = %q", cfg.Mail.IMAP.Host)
	}
	if cfg.Mail.IMAP.Port != 993 {
		t.Errorf("Mail.IMAP.Port = %d, want 993", cfg.Mail.IMAP.Port)
	}
	if cfg.Mail.IMAP.PollInterval != 120 {
		t.Errorf("Mail.IMAP.PollInterval = %d, want 120", cfg.Mail.IMAP.PollInterval)
	}

	// Newsletter
	if cfg.Newsletter.SendTime != "08:00" {
		t.Errorf("Newsletter.SendTime = %q, want %q", cfg.Newsletter.SendTime, "08:00")
	}
	if cfg.Newsletter.Timezone != "America/New_York" {
		t.Errorf("Newsletter.Timezone = %q", cfg.Newsletter.Timezone)
	}

	// Votes
	if cfg.Votes.AlertThreshold != 10 {
		t.Errorf("Votes.AlertThreshold = %d, want 10", cfg.Votes.AlertThreshold)
	}
	if cfg.Votes.AlertRatio != 0.4 {
		t.Errorf("Votes.AlertRatio = %f, want 0.4", cfg.Votes.AlertRatio)
	}
	if cfg.Votes.AlertWindow != 24 {
		t.Errorf("Votes.AlertWindow = %d, want 24", cfg.Votes.AlertWindow)
	}
}

func TestLoadConfig_EnvOverride(t *testing.T) {
	yaml := `
ai:
  hf_api_token: "${HF_API_TOKEN}"
mail:
  smtp:
    user: "${SMTP_USER}"
    password: "${SMTP_PASSWORD}"
  imap:
    user: "${IMAP_USER}"
    password: "${IMAP_APP_PASSWORD}"
`

	t.Setenv("HF_API_TOKEN", "my-secret-token")
	t.Setenv("SMTP_USER", "smtp-user-from-env")
	t.Setenv("SMTP_PASSWORD", "smtp-pass-from-env")
	t.Setenv("IMAP_USER", "imap-user-from-env")
	t.Setenv("IMAP_APP_PASSWORD", "imap-pass-from-env")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.AI.HFAPIToken != "my-secret-token" {
		t.Errorf("AI.HFAPIToken = %q, want %q", cfg.AI.HFAPIToken, "my-secret-token")
	}
	if cfg.Mail.SMTP.User != "smtp-user-from-env" {
		t.Errorf("Mail.SMTP.User = %q, want %q", cfg.Mail.SMTP.User, "smtp-user-from-env")
	}
	if cfg.Mail.SMTP.Password != "smtp-pass-from-env" {
		t.Errorf("Mail.SMTP.Password = %q, want %q", cfg.Mail.SMTP.Password, "smtp-pass-from-env")
	}
	if cfg.Mail.IMAP.User != "imap-user-from-env" {
		t.Errorf("Mail.IMAP.User = %q, want %q", cfg.Mail.IMAP.User, "imap-user-from-env")
	}
	if cfg.Mail.IMAP.Password != "imap-pass-from-env" {
		t.Errorf("Mail.IMAP.Password = %q, want %q", cfg.Mail.IMAP.Password, "imap-pass-from-env")
	}
}

// critic_disable_thinking is opt-in: a config that predates it must keep
// sending critic requests exactly as before.
func TestLoadConfig_CriticDisableThinking(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		want       bool
	}{
		{"absent", "", false},
		{"false", "  critic_disable_thinking: false\n", false},
		{"true", "  critic_disable_thinking: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			yaml := "ai:\n  critic_model: \"a, b\"\n" + tc.line
			if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.AI.CriticDisableThinking != tc.want {
				t.Errorf("CriticDisableThinking = %v, want %v", cfg.AI.CriticDisableThinking, tc.want)
			}
			// The chain is kept verbatim; ai.SplitModels parses it.
			if cfg.AI.CriticModel != "a, b" {
				t.Errorf("CriticModel = %q", cfg.AI.CriticModel)
			}
		})
	}
}
