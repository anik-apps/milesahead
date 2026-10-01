package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/web"
)

// main hands the one SMTP sender to the web app for confirmation emails and
// to the newsletter; it must satisfy both.
var (
	_ web.ConfirmationMailer = (*mail.Sender)(nil)
	_ newsletter.MailSender  = (*mail.Sender)(nil)
	_ signupMailerSetter     = (*web.App)(nil)
)

// workingSMTP is a complete relay setup.
func workingSMTP() config.SMTPConfig {
	return config.SMTPConfig{Host: "smtp.email.example.com", Port: 587, User: "u", Password: "p", From: "noreply@milesahead.example"}
}

// stubReachable makes smtpReachable answer up for the test.
func stubReachable(t *testing.T, up bool) {
	t.Helper()
	prev := smtpReachable
	smtpReachable = func(string) bool { return up }
	t.Cleanup(func() { smtpReachable = prev })
}

func TestSMTPLooksUnset(t *testing.T) {
	stubReachable(t, false)
	if smtpLooksUnset(workingSMTP()) {
		t.Error("a complete SMTP setup should not be reported as unset")
	}
	noCreds := workingSMTP()
	noCreds.User, noCreds.Password = "", ""
	if smtpLooksUnset(noCreds) {
		t.Error("a relay without credentials is a valid setup")
	}
	for name, mod := range map[string]func(*config.SMTPConfig){
		"no host":                  func(c *config.SMTPConfig) { c.Host = "" },
		"no from":                  func(c *config.SMTPConfig) { c.From = "" },
		"placeholder password":     func(c *config.SMTPConfig) { c.Password = "placeholder" },
		"placeholder user":         func(c *config.SMTPConfig) { c.User = "placeholder" },
		"unexpanded user":          func(c *config.SMTPConfig) { c.User = "${SMTP_USER}" },
		"localhost with no answer": func(c *config.SMTPConfig) { c.Host = "localhost" },
	} {
		c := workingSMTP()
		mod(&c)
		if !smtpLooksUnset(c) {
			t.Errorf("%s: should be reported as unset", name)
		}
	}

	// A relay on this machine that answers is a real setup.
	stubReachable(t, true)
	local := workingSMTP()
	local.Host = "localhost"
	if smtpLooksUnset(local) {
		t.Error("a local relay that answers should not be reported as unset")
	}
}

// recordingApp records whether sign-up was opened.
type recordingApp struct{ mailer web.ConfirmationMailer }

func (r *recordingApp) SetConfirmationMailer(m web.ConfirmationMailer) { r.mailer = m }

// captureLog sends the default logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// Sign-up opens only with newsletter.signup_enabled on and SMTP set up. On
// with SMTP unset logs an ERROR and keeps it closed, so readers never sign up
// for an email that cannot be sent.
func TestWireSignup(t *testing.T) {
	stubReachable(t, false)
	sender := mail.NewSender(workingSMTP())
	for _, tc := range []struct {
		name      string
		enabled   bool
		smtp      config.SMTPConfig
		wantOpen  bool
		wantError bool
	}{
		{"off (the default)", false, workingSMTP(), false, false},
		{"on, SMTP set up", true, workingSMTP(), true, false},
		{"on, SMTP placeholders", true, config.SMTPConfig{Host: "localhost", Port: 587, User: "placeholder", Password: "placeholder", From: "noreply@milesahead.example"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			app := &recordingApp{}
			cfg := &config.Config{Mail: config.MailConfig{SMTP: tc.smtp}, Newsletter: config.NewsletterConfig{SignupEnabled: tc.enabled}}
			open := wireSignup(app, cfg, sender)
			if open != tc.wantOpen || (app.mailer != nil) != tc.wantOpen {
				t.Errorf("wireSignup = %v (mailer set %v), want open %v", open, app.mailer != nil, tc.wantOpen)
			}
			if got := strings.Contains(logs.String(), "level=ERROR"); got != tc.wantError {
				t.Errorf("ERROR logged = %v, want %v:\n%s", got, tc.wantError, logs.String())
			}
			if strings.Contains(logs.String(), "placeholder") {
				t.Errorf("the log should not carry credentials:\n%s", logs.String())
			}
		})
	}
}

// deployConfig is the config.yaml the deploy workflow writes, with the
// heredoc's ${VAR} and ${VAR:-default} expanded from env as the shell would.
func deployConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	b, err := os.ReadFile("../../.github/workflows/deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	start := strings.Index(wf, "cat > /tmp/config.yaml << CONFIGEOF\n")
	if start < 0 {
		t.Fatal("deploy.yml no longer writes the config through the CONFIGEOF heredoc")
	}
	body := wf[start+len("cat > /tmp/config.yaml << CONFIGEOF\n"):]
	body = body[:strings.Index(body, "\n          CONFIGEOF")]
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		lines = append(lines, strings.TrimPrefix(l, "          "))
	}
	expanded := regexp.MustCompile(`\$\{(\w+)(:-([^}]*))?\}`).ReplaceAllStringFunc(strings.Join(lines, "\n"), func(m string) string {
		sm := regexp.MustCompile(`\$\{(\w+)(:-([^}]*))?\}`).FindStringSubmatch(m)
		if v := env[sm[1]]; v != "" {
			return v
		}
		return sm[3]
	})
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(expanded), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the deploy config does not load: %v", err)
	}
	return cfg
}

// Production keeps sign-up closed, and without the SMTP secrets keeps the
// placeholders the app recognises as unset; with them, they reach the config.
func TestDeployConfig_SignupClosedAndSMTPFromSecrets(t *testing.T) {
	stubReachable(t, false)
	cfg := deployConfig(t, nil)
	if cfg.Newsletter.SignupEnabled {
		t.Error("the deploy config should set newsletter.signup_enabled: false")
	}
	if !smtpLooksUnset(cfg.Mail.SMTP) || cfg.Mail.SMTP.Port != 587 {
		t.Errorf("without SMTP secrets the deploy config should keep unset placeholders on port 587, got host %q port %d", cfg.Mail.SMTP.Host, cfg.Mail.SMTP.Port)
	}

	cfg = deployConfig(t, map[string]string{
		"SMTP_HOST": "smtp.email.us-ashburn-1.oci.oraclecloud.com", "SMTP_PORT": "2587",
		"SMTP_USER": "ocid1.user.test", "SMTP_PASSWORD": "secret", "SMTP_FROM": "news@milesahead.example",
	})
	s := cfg.Mail.SMTP
	if s.Host != "smtp.email.us-ashburn-1.oci.oraclecloud.com" || s.Port != 2587 || s.User != "ocid1.user.test" || s.Password != "secret" || s.From != "news@milesahead.example" {
		t.Errorf("SMTP secrets did not reach the config: %+v", s)
	}
	if smtpLooksUnset(s) {
		t.Error("with the SMTP secrets set, SMTP should count as configured")
	}
}

// config.example.yaml documents the switch, off.
func TestConfigExample_SignupOff(t *testing.T) {
	cfg, err := config.Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Newsletter.SignupEnabled {
		t.Error("config.example.yaml should have newsletter.signup_enabled: false")
	}
	b, _ := os.ReadFile("../../config.example.yaml")
	if !strings.Contains(string(b), "signup_enabled: false") {
		t.Error("config.example.yaml should show signup_enabled explicitly")
	}
}
