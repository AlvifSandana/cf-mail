package ports

import (
	"context"
	"time"

	"tuiotp/internal/domain"
)

type CreateRoutingRuleInput struct {
	Name        string
	AliasEmail  string
	Destination []string
	Enabled     bool
	Priority    int
}

type RoutingRule struct {
	ID      string
	Name    string
	Enabled bool
}

// AliasCloudflareClient defines Cloudflare operations required by alias usecase.
type AliasCloudflareClient interface {
	EnsureDestinationVerified(ctx context.Context, email string, requireVerified bool) error
	CreateRoutingRule(ctx context.Context, in CreateRoutingRuleInput) (RoutingRule, error)
	DeleteRoutingRule(ctx context.Context, ruleID string) error
}

// AliasRepository defines persistence contract for alias records.
type AliasRepository interface {
	Create(ctx context.Context, in domain.Alias) (domain.Alias, error)
	ListActive(ctx context.Context) ([]domain.Alias, error)
	FindActiveByAliasEmail(ctx context.Context, aliasEmail string) (domain.Alias, error)
	SoftDeleteByAliasEmail(ctx context.Context, aliasEmail string, deletedAt time.Time) error
}

// OTPParser defines parser contract for incoming emails.
type OTPParser interface {
	Parse(in domain.IncomingEmail) (domain.ParsedOTP, error)
}

// OTPRenderer defines rendering contract for parsed OTP payload.
type OTPRenderer interface {
	Render(in domain.ParsedOTP) (string, error)
}

// OTPRepository defines persistence/query contract for OTP events.
type OTPRepository interface {
	Create(ctx context.Context, in domain.OTPEvent) (domain.OTPEvent, error)
	List(ctx context.Context, filter OTPListFilter) ([]domain.OTPEvent, error)
}

// OTPDuplicateRepository defines dedupe lookup contract.
type OTPDuplicateRepository interface {
	ExistsDuplicateWithinWindow(ctx context.Context, in OTPDuplicateCheck) (bool, error)
}

type OTPListFilter struct {
	AliasEmail string
	Platform   string
	Query      string
	Limit      int
}

type OTPDuplicateCheck struct {
	AliasEmail string
	OTPCode    string
	MessageID  string
	Since      time.Time
}

// RuntimeWatchRunner defines IMAP watcher execution contract.
type RuntimeWatchRunner interface {
	Run(ctx context.Context, onUpdate func(WatchUpdate)) error
}

type WatchUpdate struct {
	Mode      string
	Timestamp time.Time
}

// Clipboard defines clipboard adapter contract.
type Clipboard interface {
	Copy(ctx context.Context, text string) error
}
