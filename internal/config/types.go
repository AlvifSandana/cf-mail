package config

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
	APITokenEnv      string `yaml:"api_token_env"`
	AccountID        string `yaml:"account_id"`
	ZoneID           string `yaml:"zone_id"`
	Domain           string `yaml:"domain"`
	RuleNamePrefix   string `yaml:"rule_name_prefix"`
	DefaultPriority  int    `yaml:"default_priority"`
	EnabledByDefault bool   `yaml:"enabled_by_default"`

	APIToken string `yaml:"-"`
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
	Mailbox      string `yaml:"mailbox"`
	Idle         bool   `yaml:"idle"`
	PollInterval string `yaml:"poll_interval"`
	FetchBody    string `yaml:"fetch_body"`

	Password string `yaml:"-"`
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
