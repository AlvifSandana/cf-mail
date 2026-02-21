package config

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	token := strings.TrimSpace(cfg.Cloudflare.APIToken)
	tokenEnv := strings.TrimSpace(cfg.Cloudflare.APITokenEnv)
	if token == "" {
		if tokenEnv == "" {
			return fmt.Errorf("cloudflare.api_token or cloudflare.api_token_env is required")
		}
		return fmt.Errorf("cloudflare api token is empty (env var %q not set or empty)", tokenEnv)
	}
	if cfg.Destination.RequireVerified && strings.TrimSpace(cfg.Cloudflare.AccountID) == "" {
		return fmt.Errorf("cloudflare.account_id is required when destination.require_verified=true")
	}

	if len(cfg.Cloudflare.Domains) > 0 {
		seenConfigured := make(map[string]struct{}, len(cfg.Cloudflare.Domains))
		for i, d := range cfg.Cloudflare.Domains {
			if strings.TrimSpace(d.ZoneID) == "" {
				return fmt.Errorf("cloudflare.domains[%d].zone_id is required", i)
			}
			domainName := strings.ToLower(strings.TrimSpace(d.Domain))
			if domainName == "" {
				return fmt.Errorf("cloudflare.domains[%d].domain is required", i)
			}
			if _, exists := seenConfigured[domainName]; exists {
				return fmt.Errorf("duplicate cloudflare domain: %q", domainName)
			}
			seenConfigured[domainName] = struct{}{}
		}
	}

	effectiveDomains := cfg.Cloudflare.EffectiveDomains()
	if len(effectiveDomains) == 0 {
		return fmt.Errorf("at least one cloudflare domain is required")
	}
	seenDomains := make(map[string]struct{}, len(effectiveDomains))
	for i, d := range effectiveDomains {
		if strings.TrimSpace(d.ZoneID) == "" {
			return fmt.Errorf("cloudflare.domains[%d].zone_id is required", i)
		}
		domainName := strings.ToLower(strings.TrimSpace(d.Domain))
		if domainName == "" {
			return fmt.Errorf("cloudflare.domains[%d].domain is required", i)
		}
		if _, exists := seenDomains[domainName]; exists {
			return fmt.Errorf("duplicate cloudflare domain: %q", domainName)
		}
		seenDomains[domainName] = struct{}{}
	}
	activeDomain := strings.ToLower(strings.TrimSpace(cfg.Cloudflare.ActiveDomain))
	if activeDomain == "" {
		activeDomain = effectiveDomains[0].Domain
	}
	if _, ok := seenDomains[activeDomain]; !ok {
		return fmt.Errorf("cloudflare.active_domain must be one of configured domains")
	}

	if cfg.Destination.Email == "" {
		return fmt.Errorf("destination.email is required")
	}
	if _, err := mail.ParseAddress(cfg.Destination.Email); err != nil {
		return fmt.Errorf("destination.email is invalid: %w", err)
	}

	switch cfg.Mailbox.Mode {
	case "imap":
		if cfg.Mailbox.IMAP.Host == "" {
			return fmt.Errorf("mailbox.imap.host is required")
		}
		if cfg.Mailbox.IMAP.Port <= 0 {
			return fmt.Errorf("mailbox.imap.port must be > 0")
		}
		if cfg.Mailbox.IMAP.Username == "" {
			return fmt.Errorf("mailbox.imap.username is required")
		}
		password := strings.TrimSpace(cfg.Mailbox.IMAP.Password)
		passwordEnv := strings.TrimSpace(cfg.Mailbox.IMAP.PasswordEnv)
		if password == "" {
			if passwordEnv == "" {
				return fmt.Errorf("mailbox.imap.password or mailbox.imap.password_env is required")
			}
			return fmt.Errorf("imap password is empty or not set")
		}
		if !cfg.Mailbox.IMAP.TLS {
			return fmt.Errorf("mailbox.imap.tls must be true")
		}
		if cfg.Mailbox.IMAP.PollInterval != "" {
			d, err := time.ParseDuration(cfg.Mailbox.IMAP.PollInterval)
			if err != nil {
				return fmt.Errorf("invalid mailbox.imap.poll_interval: %w", err)
			}
			if d <= 0 {
				return fmt.Errorf("mailbox.imap.poll_interval must be > 0")
			}
		}
	default:
		return fmt.Errorf("unsupported mailbox.mode: %q", cfg.Mailbox.Mode)
	}

	if cfg.OTP.DedupeWindow != "" {
		d, err := time.ParseDuration(cfg.OTP.DedupeWindow)
		if err != nil {
			return fmt.Errorf("invalid otp.dedupe_window: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("otp.dedupe_window must be > 0")
		}
	}

	for i, r := range cfg.OTP.Rules {
		if r.Platform == "" {
			return fmt.Errorf("otp.rules[%d].platform is required", i)
		}
		if r.OTPRegex == "" {
			return fmt.Errorf("otp.rules[%d].otp_regex is required", i)
		}
		if _, err := regexp.Compile(r.OTPRegex); err != nil {
			return fmt.Errorf("invalid otp.rules[%d].otp_regex: %w", i, err)
		}
		if r.SubjectRegex != "" {
			if _, err := regexp.Compile(r.SubjectRegex); err != nil {
				return fmt.Errorf("invalid otp.rules[%d].subject_regex: %w", i, err)
			}
		}
	}

	method := strings.ToLower(strings.TrimSpace(cfg.UI.Clipboard.Method))
	if method == "" {
		method = "auto"
	}
	switch method {
	case "auto", "wl-copy", "xclip", "xsel", "pbcopy", "clip":
	default:
		return fmt.Errorf("ui.clipboard.method is unsupported: %q", cfg.UI.Clipboard.Method)
	}

	if tz := strings.TrimSpace(cfg.App.Timezone); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("app.timezone is invalid: %w", err)
		}
	}

	return nil
}
