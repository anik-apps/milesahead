package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kumaranik/milesahead/internal/config"
	"github.com/kumaranik/milesahead/internal/mail"
	"github.com/kumaranik/milesahead/internal/newsletter"
	"github.com/kumaranik/milesahead/internal/web"
	"gopkg.in/yaml.v3"
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
			open := wireSignup(app, cfg, smtpLooksUnset(tc.smtp), sender)
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

// deployConfig runs the deploy workflow's "Generate application config"
// step, as written, under bash with env as its secrets, and loads the file it
// writes. It needs bash and jq, as the workflow's runner has.
func deployConfig(t *testing.T, env map[string]string) *config.Config {
	t.Helper()
	path, err := runGenerateConfig(t, env)
	if err != nil {
		t.Fatalf("generating the deploy config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the deploy config does not load: %v", err)
	}
	if err := checkConfig(path); err != nil {
		t.Fatalf("-check-config rejects the deploy config: %v", err)
	}
	return cfg
}

// runGenerateConfig runs the step and returns the path it wrote.
func runGenerateConfig(t *testing.T, env map[string]string) (string, error) {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	b, err := os.ReadFile("../../.github/workflows/deploy.yml")
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatal(err)
	}
	script := ""
	for _, st := range wf.Jobs["config"].Steps {
		if st.Name == "Generate application config" {
			script = st.Run
			if st.Env["CONFIG_OUT"] != "/tmp/config.yaml" {
				t.Errorf("the step should write /tmp/config.yaml, which the check and copy steps read; CONFIG_OUT = %q", st.Env["CONFIG_OUT"])
			}
		}
	}
	if script == "" {
		t.Fatal(`deploy.yml has no "Generate application config" step in the config job`)
	}
	out := filepath.Join(t.TempDir(), "config.yaml")
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "CONFIG_OUT=" + out}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if outb, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%v: %s", err, outb)
	}
	return out, nil
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
		t.Error("SMTP secrets did not reach the config")
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

// A secret is carried into the config exactly, whatever it holds: quotes,
// backslashes and escapes, '$' and ${...}, '#', command substitutions (not
// run), a newline, and text that is not ASCII.
func TestDeployConfig_SecretsRoundTripExactly(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	password := `a"b\c\nd\te $x ${HOME} #c $(touch ` + marker + `) ` + "`touch " + marker + "`" + ` 'q' é` + "\nline2\n"
	secrets := map[string]string{
		"SMTP_PASSWORD":       password,
		"SMTP_USER":           `user: with "colon" & #hash`,
		"SMTP_HOST":           "smtp.example.com",
		"SMTP_FROM":           `"MilesAhead" <news@milesahead.example>`,
		"ADMIN_PASSWORD_HASH": `$2a$10$abc/def.ghi"jk`,
		"NVIDIA_API_KEY":      `nvapi-$(echo x)`,
	}
	cfg := deployConfig(t, secrets)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a command substitution inside a secret was run")
	}
	for name, got := range map[string]string{
		"SMTP_PASSWORD":       cfg.Mail.SMTP.Password,
		"SMTP_USER":           cfg.Mail.SMTP.User,
		"SMTP_HOST":           cfg.Mail.SMTP.Host,
		"SMTP_FROM":           cfg.Mail.SMTP.From,
		"ADMIN_PASSWORD_HASH": cfg.Admin.PasswordHash,
		"NVIDIA_API_KEY":      cfg.AI.HFAPIToken,
	} {
		if got != secrets[name] {
			t.Errorf("%s did not round-trip:\n got %q\nwant %q", name, got, secrets[name])
		}
	}
}

// A non-numeric SMTP_PORT fails the step, before any config is written.
func TestDeployConfig_RejectsNonNumericPort(t *testing.T) {
	if _, err := runGenerateConfig(t, map[string]string{"SMTP_PORT": "587; rm -rf /"}); err == nil || !strings.Contains(err.Error(), "SMTP_PORT must be numeric") {
		t.Errorf("want the step to fail on a non-numeric SMTP_PORT, got %v", err)
	}
}

// -check-config refuses a config that does not load.
func TestCheckConfig(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("mail:\n  smtp:\n    password: \"a\"b\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkConfig(bad); err == nil {
		t.Error("a config that does not parse should fail the check")
	}
	if err := checkConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing config should fail the check")
	}
}
