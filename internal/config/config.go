package config

import (
	"fmt"
	"os"
	"regexp"

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
	DefaultInterval int    `yaml:"default_interval"`
	UserAgent       string `yaml:"user_agent"`
}

type AIConfig struct {
	GeneratorModel string `yaml:"generator_model"`
	CriticModel    string `yaml:"critic_model"`
	Critic2Model   string `yaml:"critic2_model"`
	HFAPIToken     string `yaml:"hf_api_token"`
	HFBaseURL      string `yaml:"hf_base_url"`
	CriticThreshold int   `yaml:"critic_threshold"`
	MaxRetries     int    `yaml:"max_retries"`
	CacheTTL       int    `yaml:"cache_ttl"`
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
	SendTime string `yaml:"send_time"`
	Timezone string `yaml:"timezone"`
}

type VotesConfig struct {
	AlertThreshold int     `yaml:"alert_threshold"`
	AlertRatio     float64 `yaml:"alert_ratio"`
	AlertWindow    int     `yaml:"alert_window"`
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
}

var envVarPattern = regexp.MustCompile(`\$\{(\w+)\}`)

// Load reads a YAML config file from the given path, expands any ${ENV_VAR}
// patterns with the corresponding environment variable values, and returns
// the parsed Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	expanded := envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		varName := envVarPattern.FindSubmatch(match)[1]
		if val, ok := os.LookupEnv(string(varName)); ok {
			return []byte(val)
		}
		return match
	})

	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &cfg, nil
}
