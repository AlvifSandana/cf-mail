package config

import "strings"

type Config struct {
	App         AppConfig         `yaml:"app"`
	Cloudflare  CloudflareConfig  `yaml:"cloudflare"`
	Destination DestinationConfig `yaml:"destination"`
	Mailbox     MailboxConfig     `yaml:"mailbox"`
	OTP         OTPConfig         `yaml:"otp"`
	UI          UIConfig          `yaml:"ui"`
}

type AppConfig struct {
	Timezone string `yaml:"timezone"`
	DBPath   string `yaml:"db_path"`
	LogPath  string `yaml:"log_path"`
}

type CloudflareConfig struct {
	AutoDiscover     bool               `yaml:"auto_discover"`
	APITokenEnv      string             `yaml:"api_token_env"`
	APIToken         string             `yaml:"api_token"`
	AccountID        string             `yaml:"account_id"`
	ZoneID           string             `yaml:"zone_id"`
	Domain           string             `yaml:"domain"`
	Domains          []CloudflareDomain `yaml:"domains"`
	ActiveDomain     string             `yaml:"active_domain"`
	RuleNamePrefix   string             `yaml:"rule_name_prefix"`
	DefaultPriority  int                `yaml:"default_priority"`
	EnabledByDefault bool               `yaml:"enabled_by_default"`
}

type CloudflareDomain struct {
	ZoneID string `yaml:"zone_id"`
	Domain string `yaml:"domain"`
}

func (c CloudflareConfig) EffectiveDomains() []CloudflareDomain {
	if len(c.Domains) > 0 {
		out := make([]CloudflareDomain, 0, len(c.Domains))
		for _, d := range c.Domains {
			zoneID := strings.TrimSpace(d.ZoneID)
			domain := strings.ToLower(strings.TrimSpace(d.Domain))
			if zoneID == "" || domain == "" {
				continue
			}
			out = append(out, CloudflareDomain{ZoneID: zoneID, Domain: domain})
		}
		if len(out) > 0 {
			return out
		}
	}

	zoneID := strings.TrimSpace(c.ZoneID)
	domain := strings.ToLower(strings.TrimSpace(c.Domain))
	if zoneID == "" || domain == "" {
		return nil
	}

	return []CloudflareDomain{{ZoneID: zoneID, Domain: domain}}
}

func (c CloudflareConfig) EffectiveActiveDomain() string {
	active := strings.ToLower(strings.TrimSpace(c.ActiveDomain))
	domains := c.EffectiveDomains()
	if len(domains) == 0 {
		return ""
	}
	if active == "" {
		return domains[0].Domain
	}
	for _, d := range domains {
		if d.Domain == active {
			return active
		}
	}
	return active
}

type DestinationConfig struct {
	Email           string `yaml:"email"`
	RequireVerified bool   `yaml:"require_verified"`
}

type MailboxConfig struct {
	Mode string     `yaml:"mode"`
	IMAP IMAPConfig `yaml:"imap"`
}

type IMAPConfig struct {
	Host         string `yaml:"host"`
	Port         int    `yaml:"port"`
	TLS          bool   `yaml:"tls"`
	Username     string `yaml:"username"`
	PasswordEnv  string `yaml:"password_env"`
	Password     string `yaml:"password"`
	Mailbox      string `yaml:"mailbox"`
	Idle         bool   `yaml:"idle"`
	PollInterval string `yaml:"poll_interval"`
	FetchBody    string `yaml:"fetch_body"`
}

type OTPConfig struct {
	OutputFormat string    `yaml:"output_format"`
	DedupeWindow string    `yaml:"dedupe_window"`
	Rules        []OTPRule `yaml:"rules"`
}

type OTPRule struct {
	Platform     string   `yaml:"platform"`
	FromContains []string `yaml:"from_contains"`
	SubjectRegex string   `yaml:"subject_regex"`
	OTPRegex     string   `yaml:"otp_regex"`
}

type UIConfig struct {
	Clipboard ClipboardConfig `yaml:"clipboard"`
}

type ClipboardConfig struct {
	Enabled bool   `yaml:"enabled"`
	Method  string `yaml:"method"`
}
