package parser

import (
	"strings"
	"testing"
	"time"
)

func TestNewRenderer_DefaultTemplate(t *testing.T) {
	r, err := NewRenderer("")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	out, err := r.Render(ParsedOTP{
		Platform:   "SHOPEE",
		OTPCode:    "123456",
		AliasEmail: "alias@example.com",
		ReceivedAt: time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if out != "SHOPEE | 123456 | 2026-02-18T10:00:00Z | alias@example.com" {
		t.Fatalf("unexpected default render output: %q", out)
	}
}

func TestNewRenderer_InvalidTemplateRejected(t *testing.T) {
	_, err := NewRenderer("{{.Platform")
	if err == nil {
		t.Fatalf("expected template parse error")
	}
}

func TestRenderer_Render_CustomTemplate(t *testing.T) {
	r, err := NewRenderer("{{.Platform}}::{{.OTPCode}}::{{.Alias}}::{{.MessageID}}")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	out, err := r.Render(ParsedOTP{
		Platform:   "TELEGRAM",
		OTPCode:    "77889",
		AliasEmail: "tg@example.com",
		MessageID:  "msg-99",
		ReceivedAt: time.Date(2026, 2, 18, 12, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if out != "TELEGRAM::77889::tg@example.com::msg-99" {
		t.Fatalf("unexpected custom render output: %q", out)
	}
}

func TestRenderer_Render_MissingKeyError(t *testing.T) {
	r, err := NewRenderer("{{.NotExist}}")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	_, err = r.Render(ParsedOTP{Platform: "CUSTOM", OTPCode: "1234", AliasEmail: "x@example.com"})
	if err == nil {
		t.Fatalf("expected missing key render error")
	}
	if !strings.Contains(err.Error(), "render output template") {
		t.Fatalf("expected wrapped render error, got %v", err)
	}
}

func TestRenderer_Render_TrimOutputAndDeterministicZeroReceivedAt(t *testing.T) {
	r, err := NewRenderer("\n  {{.Platform}} | {{.OTP}} | {{.ReceivedAt}}  \n")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	out, err := r.Render(ParsedOTP{Platform: "CUSTOM", OTPCode: "9876", AliasEmail: "x@example.com"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	if out != "CUSTOM | 9876 |" {
		t.Fatalf("unexpected rendered output: %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("expected trimmed single-line output, got %q", out)
	}
}

func TestRenderer_Render_NormalizesReceivedAtToUTC(t *testing.T) {
	r, err := NewRenderer("{{.ReceivedAt}}")
	if err != nil {
		t.Fatalf("NewRenderer() error = %v", err)
	}

	out, err := r.Render(ParsedOTP{
		Platform:   "CUSTOM",
		OTPCode:    "111111",
		AliasEmail: "a@example.com",
		ReceivedAt: time.Date(2026, 2, 18, 19, 0, 0, 0, time.FixedZone("UTC+7", 7*3600)),
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if out != "2026-02-18T12:00:00Z" {
		t.Fatalf("unexpected utc received_at output: %q", out)
	}
}
