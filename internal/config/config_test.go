package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	return path
}

func TestLoad_ResolvesEnvAndDefaultRequireVerified(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "token-123")
	t.Setenv("IMAP_APP_PASSWORD", "pass-123")

	path := writeTempConfig(t, `
app:
  timezone: "Asia/Jakarta"
cloudflare:
  api_token_env: "CF_API_TOKEN"
  zone_id: "zone"
  domain: "example.com"
destination:
  email: "dest@example.com"
mailbox:
  mode: "imap"
  imap:
    host: "imap.gmail.com"
    port: 993
    tls: true
    username: "user@example.com"
    password_env: "IMAP_APP_PASSWORD"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Cloudflare.APIToken != "token-123" {
		t.Fatalf("expected API token to be resolved")
	}
	if cfg.Mailbox.IMAP.Password != "pass-123" {
		t.Fatalf("expected IMAP password to be resolved")
	}
	if !cfg.Destination.RequireVerified {
		t.Fatalf("expected destination.require_verified default true")
	}
}

func TestLoad_UnknownFieldFails(t *testing.T) {
	path := writeTempConfig(t, `
app:
  timezone: "Asia/Jakarta"
  unknown_field: "x"
`)

	if _, err := Load(path); err == nil {
		t.Fatalf("expected unknown field parse error")
	}
}

func baseValidConfig() *Config {
	return &Config{
		Cloudflare: CloudflareConfig{
			APITokenEnv: "CF_API_TOKEN",
			APIToken:    "token",
			ZoneID:      "zone",
			Domain:      "example.com",
		},
		Destination: DestinationConfig{
			Email:           "dest@example.com",
			RequireVerified: true,
		},
		Mailbox: MailboxConfig{
			Mode: "imap",
			IMAP: IMAPConfig{
				Host:         "imap.gmail.com",
				Port:         993,
				TLS:          true,
				Username:     "dest@example.com",
				PasswordEnv:  "IMAP_APP_PASSWORD",
				Password:     "pass",
				PollInterval: "5s",
			},
		},
		OTP: OTPConfig{
			DedupeWindow: "2m",
			Rules: []OTPRule{{
				Platform: "CUSTOM",
				OTPRegex: `\b(\d{4,8})\b`,
			}},
		},
	}
}

func TestValidate_Success(t *testing.T) {
	cfg := baseValidConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestValidate_RejectsInsecureOrInvalidValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "unsupported mailbox mode",
			mutate: func(c *Config) {
				c.Mailbox.Mode = "gmail-api"
			},
		},
		{
			name: "imap tls disabled",
			mutate: func(c *Config) {
				c.Mailbox.IMAP.TLS = false
			},
		},
		{
			name: "negative poll interval",
			mutate: func(c *Config) {
				c.Mailbox.IMAP.PollInterval = "-1s"
			},
		},
		{
			name: "zero dedupe window",
			mutate: func(c *Config) {
				c.OTP.DedupeWindow = "0s"
			},
		},
		{
			name: "invalid otp regex",
			mutate: func(c *Config) {
				c.OTP.Rules[0].OTPRegex = "("
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseValidConfig()
			tt.mutate(cfg)
			if err := Validate(cfg); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}
