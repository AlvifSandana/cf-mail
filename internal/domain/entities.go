package domain

import "time"

// Alias is the canonical alias entity for app/domain flows.
type Alias struct {
	ID         int64
	Platform   string
	AliasEmail string
	RuleID     string
	RuleName   string
	Enabled    bool
	CreatedAt  time.Time
	DeletedAt  *time.Time
}

// OTPEvent is the canonical OTP event entity for app/domain flows.
type OTPEvent struct {
	ID         int64
	AliasEmail string
	Platform   string
	OTPCode    string
	ReceivedAt time.Time
	FromEmail  string
	Subject    string
	MessageID  string
	RawSnippet string
}

// IncomingEmail is the canonical normalized mailbox event.
type IncomingEmail struct {
	To         []string
	From       string
	Subject    string
	MessageID  string
	Snippet    string
	Body       string
	ReceivedAt time.Time
}

// ParsedOTP is the canonical parser output entity.
type ParsedOTP struct {
	Platform   string
	OTPCode    string
	AliasEmail string
	MessageID  string
	Subject    string
	FromEmail  string
	Snippet    string
	ReceivedAt time.Time
}
