package parser

import (
	"errors"
	"testing"
	"time"
)

func TestNewEngine_ValidateRules(t *testing.T) {
	t.Run("requires platform", func(t *testing.T) {
		_, err := NewEngine([]Rule{{Platform: "", OTPRegex: `\d+`}})
		if err == nil {
			t.Fatalf("expected platform validation error")
		}
	})

	t.Run("requires otp regex", func(t *testing.T) {
		_, err := NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: ""}})
		if err == nil {
			t.Fatalf("expected otp regex validation error")
		}
	})

	t.Run("invalid regex rejected", func(t *testing.T) {
		_, err := NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: "("}})
		if err == nil {
			t.Fatalf("expected invalid otp regex error")
		}

		_, err = NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: `\d+`, SubjectRegex: "("}})
		if err == nil {
			t.Fatalf("expected invalid subject regex error")
		}
	})
}

func TestEngine_Parse_HappyPath(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "shopee",
		FromContains: []string{"shopee.com", "no-reply"},
		SubjectRegex: `(?i)otp|verifikasi`,
		OTPRegex:     `\b(\d{6})\b`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	ts := time.Date(2026, 2, 18, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	out, err := e.Parse(IncomingEmail{
		To:         []string{"Alias-001@Example.com"},
		From:       "No-Reply <service@Shopee.com>",
		Subject:    "Kode OTP Anda 123456",
		Body:       "Body 123456",
		Snippet:    "snippet",
		MessageID:  "msg-1",
		ReceivedAt: ts,
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if out.Platform != "SHOPEE" {
		t.Fatalf("unexpected platform: %q", out.Platform)
	}
	if out.OTPCode != "123456" {
		t.Fatalf("unexpected otp: %q", out.OTPCode)
	}
	if out.AliasEmail != "alias-001@example.com" {
		t.Fatalf("unexpected alias email: %q", out.AliasEmail)
	}
	if out.MessageID != "msg-1" || out.Subject == "" || out.FromEmail == "" {
		t.Fatalf("unexpected passthrough fields: %+v", out)
	}
	wantTS := ts.UTC()
	if !out.ReceivedAt.Equal(wantTS) {
		t.Fatalf("expected received_at %v, got %v", wantTS, out.ReceivedAt)
	}
}

func TestEngine_Parse_NoRuleMatched(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"tokopedia.com"},
		SubjectRegex: `(?i)otp`,
		OTPRegex:     `\b\d{6}\b`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "service@example.com",
		Subject: "hello",
		Body:    "code 123456",
	})
	if !errors.Is(err, ErrNoRuleMatched) {
		t.Fatalf("expected ErrNoRuleMatched, got %v", err)
	}
}

func TestEngine_Parse_RuleMatchedButNoOTP(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"example.com"},
		SubjectRegex: `(?i)otp`,
		OTPRegex:     `\b\d{6}\b`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "service@example.com",
		Subject: "otp incoming",
		Body:    "no code here",
	})
	if !errors.Is(err, ErrNoOTPFound) {
		t.Fatalf("expected ErrNoOTPFound, got %v", err)
	}
}

func TestEngine_Parse_RuleOrderDeterministic(t *testing.T) {
	e, err := NewEngine([]Rule{
		{Platform: "FIRST", FromContains: []string{"example.com"}, SubjectRegex: `(?i)otp`, OTPRegex: `\b(\d{6})\b`},
		{Platform: "SECOND", FromContains: []string{"example.com"}, SubjectRegex: `(?i)otp`, OTPRegex: `\b(\d{6})\b`},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	out, err := e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "sender@example.com",
		Subject: "otp 654321",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if out.Platform != "FIRST" {
		t.Fatalf("expected first matching rule to win, got %q", out.Platform)
	}
}

func TestEngine_Parse_ExtractFromBodyThenSnippet(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"example.com"},
		OTPRegex:     `code[:\s]+(\d{4,8})`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	out, err := e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "sender@example.com",
		Subject: "no otp",
		Body:    "Here code: 777888",
		Snippet: "code: 123123",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if out.OTPCode != "777888" {
		t.Fatalf("expected body OTP first, got %q", out.OTPCode)
	}
}

func TestEngine_Parse_RequiresAliasRecipient(t *testing.T) {
	e, err := NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: `\d+`}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{To: nil, Subject: "otp 1"})
	if !errors.Is(err, ErrAliasRequired) {
		t.Fatalf("expected ErrAliasRequired, got %v", err)
	}
}

func TestEngine_Parse_UsesFirstNonEmptyAliasRecipient(t *testing.T) {
	e, err := NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: `\d+`}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	out, err := e.Parse(IncomingEmail{To: []string{"", "  ", "Alias@Example.com"}, Subject: "otp 123456"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if out.AliasEmail != "alias@example.com" {
		t.Fatalf("expected alias@example.com, got %q", out.AliasEmail)
	}
}

func TestEngine_Parse_ZeroReceivedAtDeterministic(t *testing.T) {
	e, err := NewEngine([]Rule{{Platform: "CUSTOM", OTPRegex: `\d+`}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	out, err := e.Parse(IncomingEmail{To: []string{"alias@example.com"}, Subject: "otp 123456"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !out.ReceivedAt.IsZero() {
		t.Fatalf("expected zero received_at passthrough, got %v", out.ReceivedAt)
	}
}

func TestEngine_Parse_FromContainsUsesParsedAddress(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"shopee.com"},
		OTPRegex:     `\d+`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "Shopee Service <attacker@evil.com>",
		Subject: "otp 123456",
	})
	if !errors.Is(err, ErrNoRuleMatched) {
		t.Fatalf("expected ErrNoRuleMatched for spoofed display name, got %v", err)
	}
}

func TestEngine_Parse_FromContainsDomainBoundary(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"shopee.com"},
		OTPRegex:     `\d+`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "attacker@notshopee.com",
		Subject: "otp 123456",
	})
	if !errors.Is(err, ErrNoRuleMatched) {
		t.Fatalf("expected ErrNoRuleMatched for lookalike domain, got %v", err)
	}

	out, err := e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "sender@mail.shopee.com",
		Subject: "otp 123456",
	})
	if err != nil {
		t.Fatalf("expected match for valid subdomain sender, got %v", err)
	}
	if out.OTPCode != "123456" {
		t.Fatalf("unexpected otp parse result: %+v", out)
	}
}

func TestEngine_Parse_FromContainsBareLabelMatchesOnlyLocalPart(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"shopee"},
		OTPRegex:     `\d+`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "attacker@shopee.evil",
		Subject: "otp 123456",
	})
	if !errors.Is(err, ErrNoRuleMatched) {
		t.Fatalf("expected ErrNoRuleMatched for bare-label domain bypass, got %v", err)
	}

	out, err := e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "shopee@mail.example.com",
		Subject: "otp 123456",
	})
	if err != nil {
		t.Fatalf("expected match when local-part equals token, got %v", err)
	}
	if out.OTPCode != "123456" {
		t.Fatalf("unexpected otp parse result: %+v", out)
	}
}

func TestEngine_Parse_FromContainsSecureTokenDisablesBareLabelBypass(t *testing.T) {
	e, err := NewEngine([]Rule{{
		Platform:     "CUSTOM",
		FromContains: []string{"shopee.com", "no-reply"},
		OTPRegex:     `\d+`,
	}})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	_, err = e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "no-reply@evil.com",
		Subject: "otp 123456",
	})
	if !errors.Is(err, ErrNoRuleMatched) {
		t.Fatalf("expected ErrNoRuleMatched for no-reply@evil.com bypass, got %v", err)
	}

	out, err := e.Parse(IncomingEmail{
		To:      []string{"alias@example.com"},
		From:    "no-reply@mailer.shopee.com",
		Subject: "otp 123456",
	})
	if err != nil {
		t.Fatalf("expected valid domain sender to match, got %v", err)
	}
	if out.OTPCode != "123456" {
		t.Fatalf("unexpected otp parse result: %+v", out)
	}
}
