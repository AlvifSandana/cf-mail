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

func TestLoad_UsesInlineSecretsWithoutEnv(t *testing.T) {
	path := writeTempConfig(t, `
app:
  timezone: "Asia/Jakarta"
cloudflare:
  api_token: "token-inline"
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
    password: "pass-inline"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Cloudflare.APIToken != "token-inline" {
		t.Fatalf("expected API token from config file")
	}
	if cfg.Mailbox.IMAP.Password != "pass-inline" {
		t.Fatalf("expected IMAP password from config file")
	}
}

func TestLoad_InlineSecretsOverrideEnvFallback(t *testing.T) {
	t.Setenv("CF_API_TOKEN", "token-from-env")
	t.Setenv("IMAP_APP_PASSWORD", "pass-from-env")

	path := writeTempConfig(t, `
cloudflare:
  api_token: "token-inline"
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
    password: "pass-inline"
    password_env: "IMAP_APP_PASSWORD"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Cloudflare.APIToken != "token-inline" {
		t.Fatalf("expected inline API token to override env fallback")
	}
	if cfg.Mailbox.IMAP.Password != "pass-inline" {
		t.Fatalf("expected inline IMAP password to override env fallback")
	}
}

func TestLoad_NormalizesCloudflareDomainsAndDerivesLegacyFields(t *testing.T) {
	path := writeTempConfig(t, `
cloudflare:
  api_token: "token-inline"
  active_domain: "  EXAMPLE.NET  "
  domains:
    - domain: " Example.com "
      zone_id: " zone-1 "
    - domain: "EXAMPLE.NET"
      zone_id: "zone-2"
destination:
  email: "dest@example.com"
mailbox:
  mode: "imap"
  imap:
    host: "imap.gmail.com"
    port: 993
    tls: true
    username: "user@example.com"
    password: "pass-inline"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Cloudflare.Domains) != 2 {
		t.Fatalf("expected 2 cloudflare domains, got %d", len(cfg.Cloudflare.Domains))
	}
	if cfg.Cloudflare.Domains[0].Domain != "example.com" || cfg.Cloudflare.Domains[0].ZoneID != "zone-1" {
		t.Fatalf("unexpected normalized first domain: %+v", cfg.Cloudflare.Domains[0])
	}
	if cfg.Cloudflare.ZoneID != "zone-1" || cfg.Cloudflare.Domain != "example.com" {
		t.Fatalf("expected legacy fields derived from first domain, got zone=%q domain=%q", cfg.Cloudflare.ZoneID, cfg.Cloudflare.Domain)
	}
	if cfg.Cloudflare.ActiveDomain != "example.net" {
		t.Fatalf("expected normalized active domain example.net, got %q", cfg.Cloudflare.ActiveDomain)
	}
}

func TestLoad_LegacyDomainConfigDerivesDomainsAndActive(t *testing.T) {
	path := writeTempConfig(t, `
cloudflare:
  api_token: "token-inline"
  zone_id: "zone-legacy"
  domain: "Legacy.Example.com"
destination:
  email: "dest@example.com"
mailbox:
  mode: "imap"
  imap:
    host: "imap.gmail.com"
    port: 993
    tls: true
    username: "user@example.com"
    password: "pass-inline"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(cfg.Cloudflare.Domains) != 1 {
		t.Fatalf("expected 1 derived cloudflare domain, got %d", len(cfg.Cloudflare.Domains))
	}
	if cfg.Cloudflare.Domains[0].Domain != "legacy.example.com" || cfg.Cloudflare.Domains[0].ZoneID != "zone-legacy" {
		t.Fatalf("unexpected derived domain entry: %+v", cfg.Cloudflare.Domains[0])
	}
	if cfg.Cloudflare.ActiveDomain != "legacy.example.com" {
		t.Fatalf("expected derived active domain legacy.example.com, got %q", cfg.Cloudflare.ActiveDomain)
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
			AccountID:   "account",
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
			name: "missing cloudflare token and env",
			mutate: func(c *Config) {
				c.Cloudflare.APIToken = ""
				c.Cloudflare.APITokenEnv = ""
			},
		},
		{
			name: "missing imap password and env",
			mutate: func(c *Config) {
				c.Mailbox.IMAP.Password = ""
				c.Mailbox.IMAP.PasswordEnv = ""
			},
		},
		{
			name: "unsupported mailbox mode",
			mutate: func(c *Config) {
				c.Mailbox.Mode = "gmail-api"
			},
		},
		{
			name: "missing account id when require verified true",
			mutate: func(c *Config) {
				c.Cloudflare.AccountID = ""
				c.Destination.RequireVerified = true
			},
		},
		{
			name: "whitespace account id when require verified true",
			mutate: func(c *Config) {
				c.Cloudflare.AccountID = "   "
				c.Destination.RequireVerified = true
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
		{
			name: "unsupported clipboard method",
			mutate: func(c *Config) {
				c.UI.Clipboard.Method = "evil-copy"
			},
		},
		{
			name: "invalid timezone",
			mutate: func(c *Config) {
				c.App.Timezone = "Mars/Phobos"
			},
		},
		{
			name: "invalid imap poll interval",
			mutate: func(c *Config) {
				c.Mailbox.IMAP.PollInterval = "abc"
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

func TestValidate_AllowsMissingAccountIDWhenDestinationVerificationDisabled(t *testing.T) {
	cfg := baseValidConfig()
	cfg.Cloudflare.AccountID = ""
	cfg.Destination.RequireVerified = false

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() unexpected error when require_verified=false: %v", err)
	}
}

func TestValidate_CloudflareDomainsRules(t *testing.T) {
	t.Run("duplicate domains rejected case insensitive", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Cloudflare.Domains = []CloudflareDomain{
			{ZoneID: "z1", Domain: "Example.com"},
			{ZoneID: "z2", Domain: "example.COM"},
		}
		cfg.Cloudflare.ActiveDomain = "example.com"
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected duplicate domain validation error")
		}
	})

	t.Run("active domain must be configured", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Cloudflare.Domains = []CloudflareDomain{{ZoneID: "z1", Domain: "example.com"}}
		cfg.Cloudflare.ActiveDomain = "other.com"
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected active domain validation error")
		}
	})

	t.Run("domain entry requires zone and domain", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Cloudflare.Domains = []CloudflareDomain{{ZoneID: "", Domain: "example.com"}}
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected missing zone_id validation error")
		}

		cfg = baseValidConfig()
		cfg.Cloudflare.Domains = []CloudflareDomain{{ZoneID: "z1", Domain: ""}}
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected missing domain validation error")
		}
	})

	t.Run("missing active domain defaults to first effective", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Cloudflare.Domains = []CloudflareDomain{{ZoneID: "z1", Domain: "example.com"}}
		cfg.Cloudflare.ActiveDomain = ""
		if err := Validate(cfg); err != nil {
			t.Fatalf("expected validation success, got %v", err)
		}
	})

	t.Run("legacy fallback still validates", func(t *testing.T) {
		cfg := baseValidConfig()
		cfg.Cloudflare.Domains = nil
		cfg.Cloudflare.ActiveDomain = ""
		if err := Validate(cfg); err != nil {
			t.Fatalf("expected legacy config valid, got %v", err)
		}
	})
}
