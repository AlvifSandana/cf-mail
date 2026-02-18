package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

const defaultOTPDedupeWindow = 2 * time.Minute

type otpParser = ports.OTPParser

type otpRenderer = ports.OTPRenderer

type otpRepository interface {
	Create(ctx context.Context, in domain.OTPEvent) (domain.OTPEvent, error)
}

type otpDeduper interface {
	IsDuplicate(ctx context.Context, evt domain.OTPEvent) (bool, error)
}

type OTPService struct {
	parser   otpParser
	renderer otpRenderer
	repo     otpRepository
	deduper  otpDeduper
	nowFn    func() time.Time
}

type OTPPipelineStatus string

const (
	OTPPipelineStatusStored        OTPPipelineStatus = "stored"
	OTPPipelineStatusDuplicate     OTPPipelineStatus = "duplicate"
	OTPPipelineStatusIgnoredNoRule OTPPipelineStatus = "ignored_no_rule"
	OTPPipelineStatusIgnoredNoOTP  OTPPipelineStatus = "ignored_no_otp"
)

type OTPUIEvent struct {
	PersistedID int64
	AliasEmail  string
	Platform    string
	OTPCode     string
	MessageID   string
	Subject     string
	FromEmail   string
	ReceivedAt  time.Time
	Rendered    string
}

type OTPPipelineResult struct {
	Status OTPPipelineStatus
	Event  *OTPUIEvent
}

func NewOTPService(parser otpParser, renderer otpRenderer, repo otpRepository, deduper otpDeduper) (*OTPService, error) {
	if parser == nil {
		return nil, fmt.Errorf("otp service parser is nil")
	}
	if renderer == nil {
		return nil, fmt.Errorf("otp service renderer is nil")
	}
	if repo == nil {
		return nil, fmt.Errorf("otp service repository is nil")
	}
	if deduper == nil {
		return nil, fmt.Errorf("otp service deduper is nil")
	}

	return &OTPService{
		parser:   parser,
		renderer: renderer,
		repo:     repo,
		deduper:  deduper,
		nowFn:    time.Now,
	}, nil
}

func (s *OTPService) ProcessNormalizedEmail(ctx context.Context, in domain.IncomingEmail) (OTPPipelineResult, error) {
	if s == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service is nil", nil)
	}
	if s.parser == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service parser is nil", nil)
	}
	if s.renderer == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service renderer is nil", nil)
	}
	if s.repo == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service repository is nil", nil)
	}
	if s.deduper == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service deduper is nil", nil)
	}
	if s.nowFn == nil {
		return OTPPipelineResult{}, domain.WrapValidation("otp service nowFn is nil", nil)
	}

	parsed, err := s.parser.Parse(in)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrNoRuleMatched), errors.Is(err, domain.ErrAliasRequired):
			return OTPPipelineResult{Status: OTPPipelineStatusIgnoredNoRule}, nil
		case errors.Is(err, domain.ErrNoOTPFound):
			return OTPPipelineResult{Status: OTPPipelineStatusIgnoredNoOTP}, nil
		default:
			return OTPPipelineResult{}, domain.WrapDependency("parse incoming email", err)
		}
	}

	processedAt := s.nowFn().UTC()

	// Persist/dedupe timestamp is anchored to processing time to prevent
	// attacker-controlled email timestamps (future/backdated) from weakening
	// duplicate suppression behavior.
	receivedAt := processedAt

	event := domain.OTPEvent{
		AliasEmail: parsed.AliasEmail,
		Platform:   parsed.Platform,
		OTPCode:    parsed.OTPCode,
		ReceivedAt: receivedAt,
		FromEmail:  parsed.FromEmail,
		Subject:    parsed.Subject,
		MessageID:  parsed.MessageID,
		RawSnippet: parsed.Snippet,
	}

	isDup, err := s.deduper.IsDuplicate(ctx, event)
	if err != nil {
		return OTPPipelineResult{}, domain.WrapDependency("check duplicate otp event", err)
	}
	if isDup {
		return OTPPipelineResult{Status: OTPPipelineStatusDuplicate}, nil
	}

	stored, err := s.repo.Create(ctx, event)
	if err != nil {
		return OTPPipelineResult{}, domain.WrapDependency("persist otp event", err)
	}

	renderInput := parsed
	renderInput.ReceivedAt = receivedAt

	rendered, err := s.renderer.Render(renderInput)
	if err != nil {
		rendered = fallbackRenderOutput(stored)
	}

	return OTPPipelineResult{
		Status: OTPPipelineStatusStored,
		Event: &OTPUIEvent{
			PersistedID: stored.ID,
			AliasEmail:  stored.AliasEmail,
			Platform:    stored.Platform,
			OTPCode:     stored.OTPCode,
			MessageID:   stored.MessageID,
			Subject:     stored.Subject,
			FromEmail:   stored.FromEmail,
			ReceivedAt:  stored.ReceivedAt,
			Rendered:    rendered,
		},
	}, nil
}

func fallbackRenderOutput(evt domain.OTPEvent) string {
	receivedAt := ""
	if !evt.ReceivedAt.IsZero() {
		receivedAt = evt.ReceivedAt.UTC().Format(time.RFC3339)
	}

	return fmt.Sprintf("%s | %s | %s | %s", evt.Platform, evt.OTPCode, receivedAt, evt.AliasEmail)
}
