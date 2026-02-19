package app

import (
	"context"
	"errors"
	"time"

	"tuiotp/internal/adapters/cloudflare"
	"tuiotp/internal/adapters/parser"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
	"tuiotp/internal/storage/sqlite"
)

type aliasCloudflareAdapter struct {
	client *cloudflare.Client
}

func (a aliasCloudflareAdapter) EnsureDestinationVerified(ctx context.Context, email string, requireVerified bool) error {
	return a.client.EnsureDestinationVerified(ctx, email, requireVerified)
}

func (a aliasCloudflareAdapter) CreateRoutingRule(ctx context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error) {
	r, err := a.client.CreateRoutingRule(ctx, cloudflare.CreateRuleInput{
		Name:        in.Name,
		AliasEmail:  in.AliasEmail,
		Destination: in.Destination,
		Enabled:     in.Enabled,
		Priority:    in.Priority,
	})
	if err != nil {
		return ports.RoutingRule{}, err
	}

	return ports.RoutingRule{ID: r.ID, Name: r.Name, Enabled: r.Enabled}, nil
}

func (a aliasCloudflareAdapter) DeleteRoutingRule(ctx context.Context, ruleID string) error {
	return a.client.DeleteRoutingRule(ctx, ruleID)
}

type aliasRepositoryAdapter struct {
	repo *sqlite.AliasRepository
}

func (a aliasRepositoryAdapter) Create(ctx context.Context, in domain.Alias) (domain.Alias, error) {
	row, err := a.repo.Create(ctx, sqlite.Alias{
		ID:         in.ID,
		Platform:   in.Platform,
		AliasEmail: in.AliasEmail,
		RuleID:     in.RuleID,
		RuleName:   in.RuleName,
		Enabled:    in.Enabled,
		CreatedAt:  in.CreatedAt,
		DeletedAt:  in.DeletedAt,
	})
	if err != nil {
		return domain.Alias{}, err
	}
	return domain.Alias(row), nil
}

func (a aliasRepositoryAdapter) ListActive(ctx context.Context) ([]domain.Alias, error) {
	rows, err := a.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Alias, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Alias(row))
	}
	return out, nil
}

func (a aliasRepositoryAdapter) FindActiveByAliasEmail(ctx context.Context, aliasEmail string) (domain.Alias, error) {
	row, err := a.repo.FindActiveByAliasEmail(ctx, aliasEmail)
	if err != nil {
		return domain.Alias{}, err
	}
	return domain.Alias(row), nil
}

func (a aliasRepositoryAdapter) SoftDeleteByAliasEmail(ctx context.Context, aliasEmail string, deletedAt time.Time) error {
	return a.repo.SoftDeleteByAliasEmail(ctx, aliasEmail, deletedAt)
}

type otpParserAdapter struct {
	engine *parser.Engine
}

func (a otpParserAdapter) Parse(in domain.IncomingEmail) (domain.ParsedOTP, error) {
	out, err := a.engine.Parse(parser.IncomingEmail{
		To:         in.To,
		From:       in.From,
		Subject:    in.Subject,
		MessageID:  in.MessageID,
		Snippet:    in.Snippet,
		Body:       in.Body,
		ReceivedAt: in.ReceivedAt,
	})
	if err != nil {
		switch {
		case errors.Is(err, parser.ErrNoRuleMatched):
			return domain.ParsedOTP{}, domain.ErrNoRuleMatched
		case errors.Is(err, parser.ErrNoOTPFound):
			return domain.ParsedOTP{}, domain.ErrNoOTPFound
		case errors.Is(err, parser.ErrAliasRequired):
			return domain.ParsedOTP{}, domain.ErrAliasRequired
		default:
			return domain.ParsedOTP{}, err
		}
	}

	return domain.ParsedOTP(out), nil
}

type otpRendererAdapter struct {
	renderer *parser.Renderer
}

func (a otpRendererAdapter) Render(in domain.ParsedOTP) (string, error) {
	return a.renderer.Render(parser.ParsedOTP(in))
}

type otpRepositoryAdapter struct {
	repo *sqlite.OTPRepository
}

func (a otpRepositoryAdapter) Create(ctx context.Context, in domain.OTPEvent) (domain.OTPEvent, error) {
	if a.repo == nil {
		return domain.OTPEvent{}, domain.WrapValidation("otp repository adapter repo is nil", nil)
	}

	row, err := a.repo.Create(ctx, sqlite.OTPEvent(in))
	if err != nil {
		return domain.OTPEvent{}, err
	}
	return domain.OTPEvent(row), nil
}

func (a otpRepositoryAdapter) List(ctx context.Context, filter ports.OTPListFilter) ([]domain.OTPEvent, error) {
	if a.repo == nil {
		return nil, domain.WrapValidation("otp repository adapter repo is nil", nil)
	}

	rows, err := a.repo.List(ctx, sqlite.OTPListFilter{
		AliasEmail: filter.AliasEmail,
		Platform:   filter.Platform,
		Query:      filter.Query,
		Limit:      filter.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]domain.OTPEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.OTPEvent(row))
	}

	return out, nil
}

type otpDuplicateRepositoryAdapter struct {
	repo *sqlite.OTPRepository
}

func (a otpDuplicateRepositoryAdapter) ExistsDuplicateWithinWindow(ctx context.Context, in ports.OTPDuplicateCheck) (bool, error) {
	if a.repo == nil {
		return false, domain.WrapValidation("otp duplicate repository adapter repo is nil", nil)
	}

	return a.repo.ExistsDuplicateWithinWindow(ctx, sqlite.OTPDuplicateCheck{
		AliasEmail: in.AliasEmail,
		OTPCode:    in.OTPCode,
		MessageID:  in.MessageID,
		Since:      in.Since,
	})
}

type runtimeWatchRunnerAdapter struct {
	runner ports.RuntimeWatchRunner
}

func (a runtimeWatchRunnerAdapter) Run(ctx context.Context, onUpdate func(ports.WatchUpdate)) error {
	if onUpdate == nil {
		onUpdate = func(ports.WatchUpdate) {}
	}
	if a.runner == nil {
		return domain.WrapValidation("runtime watch runner adapter runner is nil", nil)
	}
	return a.runner.Run(ctx, onUpdate)
}
