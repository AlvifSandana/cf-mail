package imap

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeIncomingEmail_Basic(t *testing.T) {
	in := IncomingEmail{
		To:         []string{" User One <User+tag@Example.com> ", "user+tag@example.com"},
		From:       " Service <No-Reply@Example.COM> ",
		Subject:    "  Your\tOTP \r\n Code  ",
		MessageID:  " <ABC-123@EXAMPLE.COM> ",
		Body:       "\r\n  Code:\t123456  \r\n\r\nThanks\u0007",
		ReceivedAt: time.Date(2026, 2, 1, 10, 0, 0, 0, time.FixedZone("WIB", 7*3600)),
	}

	out, err := NormalizeIncomingEmail(in)
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}

	if len(out.To) != 1 || out.To[0] != "user+tag@example.com" {
		t.Fatalf("unexpected normalized to: %#v", out.To)
	}
	if out.From != "no-reply@example.com" {
		t.Fatalf("unexpected normalized from: %q", out.From)
	}
	if out.Subject != "Your OTP\nCode" {
		t.Fatalf("unexpected normalized subject: %q", out.Subject)
	}
	if out.MessageID != "abc-123@example.com" {
		t.Fatalf("unexpected normalized message id: %q", out.MessageID)
	}
	if out.Body != "Code: 123456\n\nThanks" {
		t.Fatalf("unexpected normalized body: %q", out.Body)
	}
	if out.Snippet != "Code: 123456" {
		t.Fatalf("unexpected derived snippet: %q", out.Snippet)
	}
	if out.ReceivedAt.Location() != time.UTC {
		t.Fatalf("expected received_at UTC, got %v", out.ReceivedAt.Location())
	}
}

func TestNormalizeIncomingEmail_MessageIDStripsWhitespaceAndControls(t *testing.T) {
	out, err := NormalizeIncomingEmail(IncomingEmail{
		To:        []string{"to@example.com"},
		MessageID: " <A\nB\tC@EXAMPLE.COM> ",
	})
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}
	if out.MessageID != "abc@example.com" {
		t.Fatalf("unexpected normalized message id: %q", out.MessageID)
	}
}

func TestNormalizeIncomingEmail_RejectTooManyToAddresses(t *testing.T) {
	to := make([]string, 0, maxToEntries+1)
	for i := 0; i < maxToEntries+1; i++ {
		to = append(to, "u"+strings.Repeat("a", i%3)+"@example.com")
	}

	_, err := NormalizeIncomingEmail(IncomingEmail{To: to})
	if err == nil {
		t.Fatalf("expected error for too many to addresses")
	}
}

func TestNormalizeIncomingEmail_TruncateBodyAndSnippetInput(t *testing.T) {
	body := strings.Repeat("b", maxBodyBytes+1024)
	snippet := strings.Repeat("s", maxSnippetBytes+500)

	out, err := NormalizeIncomingEmail(IncomingEmail{
		To:      []string{"to@example.com"},
		Body:    body,
		Snippet: snippet,
	})
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}

	if len(out.Body) > maxBodyBytes {
		t.Fatalf("expected body truncated to <= %d bytes, got %d", maxBodyBytes, len(out.Body))
	}
	if len(out.Snippet) > defaultSnippetMax {
		t.Fatalf("expected normalized snippet <= %d runes, got %d", defaultSnippetMax, len([]rune(out.Snippet)))
	}
}

func TestNormalizeIncomingEmail_RequiresTo(t *testing.T) {
	_, err := NormalizeIncomingEmail(IncomingEmail{To: []string{"  "}})
	if err == nil {
		t.Fatalf("expected error for empty to")
	}
}

func TestNormalizeIncomingEmail_InvalidAddress(t *testing.T) {
	_, err := NormalizeIncomingEmail(IncomingEmail{To: []string{"not an email"}})
	if err == nil {
		t.Fatalf("expected invalid to address error")
	}

	_, err = NormalizeIncomingEmail(IncomingEmail{To: []string{"ok@example.com"}, From: "broken@@example"})
	if err == nil {
		t.Fatalf("expected invalid from address error")
	}
}

func TestNormalizeIncomingEmail_SnippetFallbackAndClamp(t *testing.T) {
	long := strings.Repeat("a", 400)
	out, err := NormalizeIncomingEmail(IncomingEmail{
		To:      []string{"to@example.com"},
		Body:    long,
		Snippet: "",
	})
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}
	if len([]rune(out.Snippet)) != defaultSnippetMax {
		t.Fatalf("expected snippet length %d, got %d", defaultSnippetMax, len([]rune(out.Snippet)))
	}

	out2, err := NormalizeIncomingEmail(IncomingEmail{
		To:      []string{"to@example.com"},
		Snippet: long,
	})
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}
	if len([]rune(out2.Snippet)) != defaultSnippetMax {
		t.Fatalf("expected explicit snippet clamped to %d, got %d", defaultSnippetMax, len([]rune(out2.Snippet)))
	}
}

func TestNormalizeIncomingEmail_ReceivedAtDefaultNow(t *testing.T) {
	out, err := NormalizeIncomingEmail(IncomingEmail{To: []string{"to@example.com"}})
	if err != nil {
		t.Fatalf("NormalizeIncomingEmail() error = %v", err)
	}
	if out.ReceivedAt.IsZero() {
		t.Fatalf("expected non-zero received_at")
	}
	if out.ReceivedAt.Location() != time.UTC {
		t.Fatalf("expected utc received_at")
	}
}
