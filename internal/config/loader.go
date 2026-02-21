package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"tuiotp/internal/adapters/cloudflare"

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

// ResolveZones fetches zone information from the Cloudflare API when
// auto_discover is enabled and no domains are explicitly configured.
// This must be called after Load() and before Validate().
func ResolveZones(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if !cfg.Cloudflare.AutoDiscover {
		return nil
	}

	// Skip if domains are already configured explicitly.
	if len(cfg.Cloudflare.Domains) > 0 {
		return nil
	}

	token := strings.TrimSpace(cfg.Cloudflare.APIToken)
	if token == "" {
		return fmt.Errorf("cloudflare.api_token is required for auto_discover")
	}

	client, err := cloudflare.NewDiscoveryClient(cloudflare.ClientConfig{
		APIToken:   token,
		AccountID:  strings.TrimSpace(cfg.Cloudflare.AccountID),
		MaxRetries: 3,
	})
	if err != nil {
		return fmt.Errorf("init cloudflare discovery client: %w", err)
	}

	zones, err := client.ListZones(ctx)
	if err != nil {
		return fmt.Errorf("auto-discover zones: %w", err)
	}
	if len(zones) == 0 {
		return fmt.Errorf("auto_discover found no active zones for this API token")
	}

	domains := make([]CloudflareDomain, 0, len(zones))
	for _, z := range zones {
		domains = append(domains, CloudflareDomain{
			ZoneID: z.ID,
			Domain: z.Name,
		})
	}

	cfg.Cloudflare.Domains = domains
	cfg.Cloudflare.ZoneID = domains[0].ZoneID
	cfg.Cloudflare.Domain = domains[0].Domain
	if cfg.Cloudflare.ActiveDomain == "" {
		cfg.Cloudflare.ActiveDomain = domains[0].Domain
	}

	return nil
}
