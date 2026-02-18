package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

type otpDuplicateChecker = ports.OTPDuplicateRepository

type OTPDeduper struct {
	repo   otpDuplicateChecker
	window time.Duration
	nowFn  func() time.Time
}

func NewOTPDeduper(repo otpDuplicateChecker, window time.Duration) (*OTPDeduper, error) {
	if repo == nil {
		return nil, fmt.Errorf("otp deduper repository is nil")
	}
	if window <= 0 {
		return nil, fmt.Errorf("otp dedupe window must be > 0")
	}

	return &OTPDeduper{
		repo:   repo,
		window: window,
		nowFn:  time.Now,
	}, nil
}

func (d *OTPDeduper) IsDuplicate(ctx context.Context, evt domain.OTPEvent) (bool, error) {
	if d == nil {
		return false, domain.WrapValidation("otp deduper is nil", nil)
	}
	if d.repo == nil {
		return false, domain.WrapValidation("otp deduper repository is nil", nil)
	}
	if d.nowFn == nil {
		return false, domain.WrapValidation("otp deduper nowFn is nil", nil)
	}

	aliasEmail := strings.ToLower(strings.TrimSpace(evt.AliasEmail))
	if aliasEmail == "" {
		return false, domain.WrapValidation("otp event alias_email is required for dedupe", nil)
	}

	otpCode := strings.TrimSpace(evt.OTPCode)
	messageID := strings.TrimSpace(evt.MessageID)
	if otpCode == "" && messageID == "" {
		return false, domain.WrapValidation("otp event otp_code or message_id is required for dedupe", nil)
	}

	// Use processing time as dedupe anchor to avoid attacker-controlled event time bypass.
	refNow := d.nowFn().UTC()
	since := refNow.Add(-d.window)

	isDup, err := d.repo.ExistsDuplicateWithinWindow(ctx, ports.OTPDuplicateCheck{
		AliasEmail: aliasEmail,
		OTPCode:    otpCode,
		MessageID:  messageID,
		Since:      since,
	})
	if err != nil {
		return false, domain.WrapDependency("check otp duplicate", err)
	}

	return isDup, nil
}
