package ui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/domain"
)

func TestSmoke_TUIKeyTransitions(t *testing.T) {
	fixedNow := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	fakeOTP := &fakeOTPManager{rows: []domain.OTPEvent{
		{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: fixedNow},
	}}
	clip := &fakeClipboard{}

	m := NewModelWithConfig(ModelConfig{
		OTPManager: fakeOTP,
		Clipboard:  clip,
		ParentCtx:  context.Background(),
	})

	if cmd := m.Init(); cmd == nil {
		t.Fatalf("expected init command")
	} else {
		updated, _ := m.Update(cmd())
		m = updated.(Model)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.ActivePanel != PanelAliases {
		t.Fatalf("expected aliases panel, got %v", m.ActivePanel)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = updated.(Model)
	if m.ActivePanel != PanelLatestOTP {
		t.Fatalf("expected latest otp panel, got %v", m.ActivePanel)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	if !m.otpSearchMode {
		t.Fatalf("expected otp search mode enabled")
	}

	for _, r := range []rune("shop") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected otp refresh command after search submit")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.otpSearchQuery != "shop" {
		t.Fatalf("expected otp query applied, got %q", m.otpSearchQuery)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected copy command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if clip.calls != 1 || clip.lastText != "123456" {
		t.Fatalf("expected otp copied through clipboard, calls=%d text=%q", clip.calls, clip.lastText)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.otpSearchQuery != "" {
		t.Fatalf("expected otp search cleared with esc, got %q", m.otpSearchQuery)
	}

	_, quitCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if quitCmd == nil {
		t.Fatalf("expected quit command")
	}
	if _, ok := quitCmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from quit command")
	}
}
