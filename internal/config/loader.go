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
	if cfg.Cloudflare.APIToken == "" && cfg.Cloudflare.APITokenEnv != "" {
		if token, ok := os.LookupEnv(cfg.Cloudflare.APITokenEnv); ok {
			cfg.Cloudflare.APIToken = token
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
