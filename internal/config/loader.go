package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := Config{
		Destination: DestinationConfig{
			RequireVerified: true,
		},
	}

	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	cfg.Cloudflare.APIToken = strings.TrimSpace(cfg.Cloudflare.APIToken)
	cfg.Cloudflare.APITokenEnv = strings.TrimSpace(cfg.Cloudflare.APITokenEnv)
	cfg.Cloudflare.ZoneID = strings.TrimSpace(cfg.Cloudflare.ZoneID)
	cfg.Cloudflare.Domain = strings.ToLower(strings.TrimSpace(cfg.Cloudflare.Domain))
	cfg.Cloudflare.ActiveDomain = strings.ToLower(strings.TrimSpace(cfg.Cloudflare.ActiveDomain))
	if len(cfg.Cloudflare.Domains) > 0 {
		normalized := make([]CloudflareDomain, 0, len(cfg.Cloudflare.Domains))
		for _, d := range cfg.Cloudflare.Domains {
			normalized = append(normalized, CloudflareDomain{
				ZoneID: strings.TrimSpace(d.ZoneID),
				Domain: strings.ToLower(strings.TrimSpace(d.Domain)),
			})
		}
		cfg.Cloudflare.Domains = normalized
	}
	if cfg.Cloudflare.APIToken == "" && cfg.Cloudflare.APITokenEnv != "" {
		if token, ok := os.LookupEnv(cfg.Cloudflare.APITokenEnv); ok {
			cfg.Cloudflare.APIToken = token
		}
	}

	effective := cfg.Cloudflare.EffectiveDomains()
	if len(effective) > 0 {
		cfg.Cloudflare.Domains = effective
		cfg.Cloudflare.ZoneID = effective[0].ZoneID
		cfg.Cloudflare.Domain = effective[0].Domain
		if cfg.Cloudflare.ActiveDomain == "" {
			cfg.Cloudflare.ActiveDomain = effective[0].Domain
		}
	}

	cfg.Mailbox.IMAP.Password = strings.TrimSpace(cfg.Mailbox.IMAP.Password)
	cfg.Mailbox.IMAP.PasswordEnv = strings.TrimSpace(cfg.Mailbox.IMAP.PasswordEnv)
	if cfg.Mailbox.IMAP.Password == "" && cfg.Mailbox.IMAP.PasswordEnv != "" {
		if password, ok := os.LookupEnv(cfg.Mailbox.IMAP.PasswordEnv); ok {
			cfg.Mailbox.IMAP.Password = password
		}
	}

	return &cfg, nil
}
