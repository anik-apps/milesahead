package config

import (
	"fmt"
	netmail "net/mail"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	// The zone database is built in, so newsletter.timezone is read the
	// same way by -check-config in CI and by the service on the server,
	// whatever zone files either machine has.
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	Port   int    `yaml:"port"`
	Domain string `yaml:"domain"`
}

type AdminConfig struct {
	PasswordHash string `yaml:"password_hash"`
}

type DBConfig struct {
	Path string `yaml:"path"`
}

type ScannerConfig struct {
	DefaultInterval int `yaml:"default_interval"`
	// ClusterThreshold is the minimum title similarity for two scanned articles
	// to be treated as the same story (0-1). 0 uses the built-in default.
	ClusterThreshold float64 `yaml:"cluster_threshold"`
	UserAgent        string  `yaml:"user_agent"`
	// StoryDedup is what the story-level duplicate check does with a cluster
	// it judges to be a story already written in the last 7 days: "shadow"
	// (the default) logs and counts the decision but still generates, "block"
	// skips generation, "off" does not run the check. See config.example.yaml.
	StoryDedup string `yaml:"story_dedup"`
}

// Story-level duplicate check modes; see ScannerConfig.StoryDedup.
const (
	StoryDedupOff    = "off"
	StoryDedupShadow = "shadow"
	StoryDedupBlock  = "block"
)

type AIConfig struct {
	GeneratorModel         string `yaml:"generator_model"`
	FallbackGeneratorModel string `yaml:"fallback_generator_model"`
	// CriticModel is one model or a comma-separated chain, tried in order when
	// a critic is overloaded or gone.
	CriticModel string `yaml:"critic_model"`
	// CriticDisableThinking sends chat_template_kwargs {"enable_thinking":
	// false} on critic requests (critic_model and critic2_model) and nothing
	// else. Off by default; see config.example.yaml for why production sets it.
	CriticDisableThinking bool   `yaml:"critic_disable_thinking"`
	VisionModel           string `yaml:"vision_model"`
	RequestTimeout        int    `yaml:"request_timeout"` // seconds; 0 = client default
	// EnrichmentModel is used for Scout's Take, key details, tags and image
	// queries. Deliberately separate from critic_model: those calls used to
	// reuse it, so a degraded critic backend took the enrichment down with it.
	// Comma-separated; tried in order.
	EnrichmentModel string `yaml:"enrichment_model"`
	// EnrichmentInterval is how often the reconciler runs, in minutes.
	EnrichmentInterval int `yaml:"enrichment_interval"`
	// EnrichmentMaxAttempts bounds passes per article before it is left alone.
	EnrichmentMaxAttempts int `yaml:"enrichment_max_attempts"`
	// EnrichmentMaxDuration bounds a single pass, in minutes.
	EnrichmentMaxDuration int    `yaml:"enrichment_max_duration"`
	Critic2Model          string `yaml:"critic2_model"`
	HFAPIToken            string `yaml:"hf_api_token"`
	HFBaseURL             string `yaml:"hf_base_url"`
	CriticThreshold       int    `yaml:"critic_threshold"`
	MaxRetries            int    `yaml:"max_retries"`
	CacheTTL              int    `yaml:"cache_ttl"`
	UnsplashKey           string `yaml:"unsplash_key"`
	PexelsKey             string `yaml:"pexels_key"`
}

type SMTPConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
}

type IMAPConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	User         string `yaml:"user"`
	Password     string `yaml:"password"`
	PollInterval int    `yaml:"poll_interval"`
}

type MailConfig struct {
	SMTP SMTPConfig `yaml:"smtp"`
	IMAP IMAPConfig `yaml:"imap"`
}

type NewsletterConfig struct {
	// SendDay, SendTime and Timezone are when the weekly issue goes out:
	// the day of the week ("saturday" by default; a full English name or
	// its first three letters, any case), the time of day as 24-hour
	// "HH:MM" ("08:00" by default) and the IANA zone both are read in
	// ("America/New_York" by default). The issue is built at 20:00 the day
	// before. Timezone also dates the issues and decides which day counts
	// as "today" on the admin newsletter page. Load fills in the defaults
	// and rejects a value it cannot read.
	SendDay  string `yaml:"send_day"`
	SendTime string `yaml:"send_time"`
	Timezone string `yaml:"timezone"`
	// SignupEnabled opens newsletter sign-up: the header, footer, home and
	// article placements, the /subscribe form and the confirmation email.
	// Off by default, and treated as off at startup when the SMTP settings
	// look unset, so readers never sign up for email that cannot be sent.
	SignupEnabled bool `yaml:"signup_enabled"`
	// UnsubscribeMailbox, when set, is added to every newsletter's
	// List-Unsubscribe header as a mailto. Set it only to a mailbox someone
	// reads and acts on; empty (the default), the header has the one-click
	// https link alone, which is what Gmail and Yahoo require.
	UnsubscribeMailbox string `yaml:"unsubscribe_mailbox"`
}

type VotesConfig struct {
	AlertThreshold int     `yaml:"alert_threshold"`
	AlertRatio     float64 `yaml:"alert_ratio"`
	AlertWindow    int     `yaml:"alert_window"`
	// AlertEmail, when set, is where vote alerts are also emailed. Every
	// alert is listed on the admin Published page either way; empty (the
	// default), none is emailed.
	AlertEmail string `yaml:"alert_email"`
}

type SiteConfig struct {
	PersonaName string `yaml:"persona_name"`
}

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Admin      AdminConfig      `yaml:"admin"`
	DB         DBConfig         `yaml:"db"`
	Scanner    ScannerConfig    `yaml:"scanner"`
	AI         AIConfig         `yaml:"ai"`
	Mail       MailConfig       `yaml:"mail"`
	Newsletter NewsletterConfig `yaml:"newsletter"`
	Votes      VotesConfig      `yaml:"votes"`
	Site       SiteConfig       `yaml:"site"`
}

// envVarPattern is a string value that is exactly ${NAME}.
var envVarPattern = regexp.MustCompile(`^\$\{(\w+)\}$`)

// Load reads a YAML config file from the given path and returns the parsed
// Config. A string value that is exactly ${NAME} is replaced by that
// environment variable, when it is set.
//
// The replacement happens after parsing, on whole values only. It used to be
// a text substitution before parsing, so a secret holding a quote broke the
// YAML, and a value written into the file (by the deploy workflow, from a
// secret) that happened to contain ${NAME} was rewritten. A value can still
// not be the literal text ${NAME} on its own; and numbers cannot come from
// the environment this way.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	expandEnv(reflect.ValueOf(&cfg).Elem())

	// Unset means shadow: the check starts out observing, and blocking is an
	// explicit decision. An unknown value is an error rather than a silent
	// default, so a typo cannot quietly turn the check off or on.
	switch mode := strings.ToLower(strings.TrimSpace(cfg.Scanner.StoryDedup)); mode {
	case "":
		cfg.Scanner.StoryDedup = StoryDedupShadow
	case StoryDedupOff, StoryDedupShadow, StoryDedupBlock:
		cfg.Scanner.StoryDedup = mode
	default:
		return nil, fmt.Errorf("scanner.story_dedup: %q is not one of %q, %q, %q",
			cfg.Scanner.StoryDedup, StoryDedupShadow, StoryDedupBlock, StoryDedupOff)
	}

	if err := cfg.Newsletter.normalize(); err != nil {
		return nil, err
	}

	// A typo would send every alert nowhere, so it is a load error.
	cfg.Votes.AlertEmail = strings.TrimSpace(cfg.Votes.AlertEmail)
	if cfg.Votes.AlertEmail != "" {
		if a, err := netmail.ParseAddress(cfg.Votes.AlertEmail); err != nil || a.Address != cfg.Votes.AlertEmail {
			return nil, fmt.Errorf("votes.alert_email: %q is not a plain email address", cfg.Votes.AlertEmail)
		}
	}

	if cfg.Site.PersonaName == "" {
		cfg.Site.PersonaName = "Scout"
	}

	return &cfg, nil
}

// expandEnv replaces every string in v that is exactly ${NAME} with the
// environment variable NAME, if it is set. The value is used as it is: it
// is not parsed as YAML, and ${...} inside it is left alone.
func expandEnv(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			expandEnv(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				expandEnv(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			expandEnv(v.Index(i))
		}
	case reflect.String:
		if m := envVarPattern.FindStringSubmatch(v.String()); m != nil {
			if val, ok := os.LookupEnv(m[1]); ok {
				v.SetString(val)
			}
		}
	}
}

// The weekly newsletter's schedule when newsletter.send_day, send_time or
// timezone is unset: Saturday at 08:00 New York time.
const (
	DefaultSendDay  = "saturday"
	DefaultSendTime = "08:00"
	DefaultTimezone = "America/New_York"
)

// normalize fills in the schedule defaults and checks that each value can
// be read, so a typo fails the load (and the deploy's -check-config)
// rather than moving or stopping the send.
func (n *NewsletterConfig) normalize() error {
	if strings.TrimSpace(n.SendDay) == "" {
		n.SendDay = DefaultSendDay
	}
	if strings.TrimSpace(n.SendTime) == "" {
		n.SendTime = DefaultSendTime
	}
	if strings.TrimSpace(n.Timezone) == "" {
		n.Timezone = DefaultTimezone
	}
	if _, err := ParseWeekday(n.SendDay); err != nil {
		return fmt.Errorf("newsletter.send_day: %w", err)
	}
	if _, _, err := ParseClock(n.SendTime); err != nil {
		return fmt.Errorf("newsletter.send_time: %w", err)
	}
	if _, err := time.LoadLocation(strings.TrimSpace(n.Timezone)); err != nil {
		return fmt.Errorf("newsletter.timezone: %q is not an IANA time zone name such as %q", n.Timezone, DefaultTimezone)
	}
	n.Timezone = strings.TrimSpace(n.Timezone)
	return nil
}

// ParseWeekday reads a day of the week: its English name or the first
// three letters of it, in any case ("saturday", "Sat").
func ParseWeekday(s string) (time.Weekday, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	for d := time.Sunday; d <= time.Saturday; d++ {
		name := strings.ToLower(d.String())
		if v == name || v == name[:3] {
			return d, nil
		}
	}
	return 0, fmt.Errorf("%q is not a day of the week (e.g. %q)", s, DefaultSendDay)
}

// ParseClock reads a 24-hour time of day, "HH:MM" ("08:00", "8:00",
// "20:30").
func ParseClock(s string) (hour, minute int, err error) {
	bad := fmt.Errorf("%q is not a 24-hour time such as %q", s, DefaultSendTime)
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || len(m) != 2 || len(h) < 1 || len(h) > 2 || !allDigits(h+m) {
		return 0, 0, bad
	}
	hour, err1 := strconv.Atoi(h)
	minute, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, bad
	}
	return hour, minute, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
