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

	if cfg.Cloudflare.APITokenEnv == "" {
		return fmt.Errorf("cloudflare.api_token_env is required")
	}
	if cfg.Cloudflare.APIToken == "" {
		return fmt.Errorf("cloudflare api token is empty or not set")
	}
	if strings.TrimSpace(cfg.Cloudflare.ZoneID) == "" {
		return fmt.Errorf("cloudflare.zone_id is required")
	}
	if cfg.Destination.RequireVerified && strings.TrimSpace(cfg.Cloudflare.AccountID) == "" {
		return fmt.Errorf("cloudflare.account_id is required when destination.require_verified=true")
	}
	if strings.TrimSpace(cfg.Cloudflare.Domain) == "" {
		return fmt.Errorf("cloudflare.domain is required")
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
		if cfg.Mailbox.IMAP.PasswordEnv == "" {
			return fmt.Errorf("mailbox.imap.password_env is required")
		}
		if cfg.Mailbox.IMAP.Password == "" {
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

	return nil
}
