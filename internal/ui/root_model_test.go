package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
)

type fakeAliasManager struct {
	listRows []domain.Alias
	listErr  error

	createErr   error
	deleteErr   error
	createCalls int
	deleteCalls int

	lastCreate app.CreateAliasInput
	lastDelete string
}

type fakeOTPManager struct {
	rows      []domain.OTPEvent
	err       error
	lastQuery string
	lastLimit int
	calls     int
}

type fakeClipboard struct {
	err      error
	lastText string
	calls    int
}

func (f *fakeClipboard) Copy(_ context.Context, text string) error {
	f.calls++
	f.lastText = text
	return f.err
}

func (f *fakeOTPManager) ListOTPEvents(_ context.Context, filter app.OTPListFilter) ([]domain.OTPEvent, error) {
	f.calls++
	f.lastQuery = filter.Query
	f.lastLimit = filter.Limit
	if f.err != nil {
		return nil, f.err
	}
	out := make([]domain.OTPEvent, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeAliasManager) ListAliases(_ context.Context) ([]domain.Alias, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]domain.Alias, len(f.listRows))
	copy(out, f.listRows)
	return out, nil
}

func (f *fakeAliasManager) CreateAlias(_ context.Context, in app.CreateAliasInput) (domain.Alias, error) {
	f.createCalls++
	f.lastCreate = in
	if f.createErr != nil {
		return domain.Alias{}, f.createErr
	}
	return domain.Alias{ID: 1, Platform: in.Platform, AliasEmail: in.AliasEmail, RuleID: "rule-1"}, nil
}

func (f *fakeAliasManager) DeleteAlias(_ context.Context, aliasEmail string) error {
	f.deleteCalls++
	f.lastDelete = aliasEmail
	return f.deleteErr
}

func TestNewModel_DefaultState(t *testing.T) {
	m := NewModel()

	if m.ActivePanel != PanelStatus {
		t.Fatalf("expected default active panel status, got %v", m.ActivePanel)
	}
	if m.ShowHelp {
		t.Fatalf("expected help hidden by default")
	}
	if m.LastAction != "ready" {
		t.Fatalf("unexpected default last action: %q", m.LastAction)
	}
	if m.opTimeout != 5*time.Second {
		t.Fatalf("expected default op timeout 5s, got %v", m.opTimeout)
	}
	if m.opContext() == nil {
		t.Fatalf("expected non-nil op context")
	}
}

func TestModel_Update_GlobalKeymaps(t *testing.T) {
	m := NewModel()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m1 := updated.(Model)
	if !m1.ShowHelp {
		t.Fatalf("expected help toggled on")
	}
	if cmd != nil {
		t.Fatalf("expected nil command for help toggle")
	}

	updated, cmd = m1.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 := updated.(Model)
	if m2.LastAction != "refresh requested" {
		t.Fatalf("expected refresh action label, got %q", m2.LastAction)
	}
	if cmd != nil {
		t.Fatalf("expected nil command for refresh")
	}

	updated, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	m3 := updated.(Model)
	if m3.ActivePanel != PanelAliases {
		t.Fatalf("expected panel switch to aliases, got %v", m3.ActivePanel)
	}
	if cmd != nil {
		t.Fatalf("expected nil command for tab")
	}

	updated, cmd = m3.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("expected quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected quit command to produce tea.QuitMsg")
	}
	_ = updated
}

func TestModel_Init_LoadAliasesWhenManagerConfigured(t *testing.T) {
	fake := &fakeAliasManager{listRows: []domain.Alias{{AliasEmail: "a@example.com"}}}
	m := NewModelWithConfig(ModelConfig{AliasManager: fake})

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("expected init command when alias manager is configured")
	}

	msg := cmd()
	loaded, ok := msg.(aliasesLoadedMsg)
	if !ok {
		t.Fatalf("expected aliasesLoadedMsg, got %T", msg)
	}
	if loaded.err != nil || len(loaded.aliases) != 1 {
		t.Fatalf("unexpected loaded aliases result: %+v", loaded)
	}
}

func TestModel_Update_TabCyclesPanels(t *testing.T) {
	m := NewModel()

	for i := 0; i < 4; i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
	}

	if m.ActivePanel != PanelStatus {
		t.Fatalf("expected tab to cycle back to status, got %v", m.ActivePanel)
	}
}

func TestModel_AliasCreateFlow_SubmitAndRefresh(t *testing.T) {
	fake := &fakeAliasManager{}
	m := NewModelWithConfig(ModelConfig{AliasManager: fake})
	m.ActivePanel = PanelAliases

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	if !m.creating || cmd != nil {
		t.Fatalf("expected entering create mode without command")
	}

	for _, r := range []rune("SHOP") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.createField != 1 {
		t.Fatalf("expected alias email field selected")
	}

	for _, r := range []rune("shop@example.com") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected create alias command on submit")
	}

	createMsg := cmd()
	created, ok := createMsg.(aliasCreatedMsg)
	if !ok {
		t.Fatalf("expected aliasCreatedMsg, got %T", createMsg)
	}
	if created.err != nil {
		t.Fatalf("unexpected create error: %v", created.err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("expected one create call, got %d", fake.createCalls)
	}
	if fake.lastCreate.Platform != "SHOP" || fake.lastCreate.AliasEmail != "shop@example.com" {
		t.Fatalf("unexpected create input: %+v", fake.lastCreate)
	}

	updated, cmd = m.Update(createMsg)
	m = updated.(Model)
	if m.creating {
		t.Fatalf("expected create mode closed after success")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after create")
	}
}

func TestModel_AliasDeleteFlow_ConfirmAndRefresh(t *testing.T) {
	fake := &fakeAliasManager{}
	m := NewModelWithConfig(ModelConfig{AliasManager: fake})
	m.ActivePanel = PanelAliases
	m.aliases = []domain.Alias{{Platform: "SHOP", AliasEmail: "shop@example.com", RuleID: "r1"}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)
	if !m.deleteConfirm || cmd != nil {
		t.Fatalf("expected delete confirmation mode")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected delete command on confirm")
	}

	deleteMsg := cmd()
	deleted, ok := deleteMsg.(aliasDeletedMsg)
	if !ok {
		t.Fatalf("expected aliasDeletedMsg, got %T", deleteMsg)
	}
	if deleted.err != nil {
		t.Fatalf("unexpected delete error: %v", deleted.err)
	}
	if fake.deleteCalls != 1 || fake.lastDelete != "shop@example.com" {
		t.Fatalf("unexpected delete call: calls=%d alias=%q", fake.deleteCalls, fake.lastDelete)
	}

	updated, cmd = m.Update(deleteMsg)
	m = updated.(Model)
	if m.deleteConfirm {
		t.Fatalf("expected delete confirm cleared after success")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after delete")
	}
}

func TestModel_AliasCreateFlow_ShieldsGlobalQuitWhileTyping(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{AliasManager: &fakeAliasManager{}})
	m.ActivePanel = PanelAliases

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected no quit command while create form is active")
	}
	if !strings.Contains(m.createPlatform, "q") {
		t.Fatalf("expected typed rune to go into create form field")
	}
}

func TestModel_AliasRefreshError_ShowsErrorLine(t *testing.T) {
	fake := &fakeAliasManager{listErr: errors.New("list failed")}
	m := NewModelWithConfig(ModelConfig{AliasManager: fake})

	cmd := m.refreshAliasesCmd()
	if cmd == nil {
		t.Fatalf("expected refresh command")
	}

	updated, _ := m.Update(cmd())
	m = updated.(Model)
	if !strings.Contains(m.ErrorMsg, "refresh aliases failed") {
		t.Fatalf("expected list error recorded, got %q", m.ErrorMsg)
	}
	if !contains(m.View(), "Error: refresh aliases failed") {
		t.Fatalf("expected error line in view")
	}
}

func TestModel_AliasDeleteFlow_InvalidSelection_NoPanic(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{AliasManager: &fakeAliasManager{}})
	m.ActivePanel = PanelAliases
	m.aliases = []domain.Alias{{AliasEmail: "a@example.com"}}
	m.selected = 9
	m.deleteConfirm = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected no command on invalid selection")
	}
	if m.deleteConfirm {
		t.Fatalf("expected delete confirm cleared")
	}
	if !contains(m.ErrorMsg, "invalid alias selection") {
		t.Fatalf("expected invalid selection error, got %q", m.ErrorMsg)
	}
}

func TestTrimLastRune_UnicodeSafe(t *testing.T) {
	if got := trimLastRune("A😊"); got != "A" {
		t.Fatalf("expected rune-safe trim result 'A', got %q", got)
	}
	if got := trimLastRune(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
}

func TestModel_View_HelpAndPanelHighlight(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelLatestOTP
	m.ShowHelp = true
	view := m.View()

	if !contains(view, "[Latest OTP]") {
		t.Fatalf("expected active panel marker in view")
	}
	if !contains(view, "Help:") || !contains(view, "- q: quit") {
		t.Fatalf("expected help section in view")
	}
}

func TestModel_Init_LoadsOTPHistoryWhenManagerConfigured(t *testing.T) {
	fakeOTP := &fakeOTPManager{rows: []domain.OTPEvent{{ID: 1, Platform: "SHOP", OTPCode: "111111", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("expected init command")
	}

	msg := cmd()
	if _, ok := msg.(otpHistoryLoadedMsg); !ok {
		t.Fatalf("expected otpHistoryLoadedMsg from init, got %T", msg)
	}

	if fakeOTP.calls != 1 {
		t.Fatalf("expected one otp list call, got %d", fakeOTP.calls)
	}
	if fakeOTP.lastLimit != defaultOTPHistoryLimit {
		t.Fatalf("expected default otp list limit %d, got %d", defaultOTPHistoryLimit, fakeOTP.lastLimit)
	}
}

func TestModel_OTPPanel_SearchAndRefresh(t *testing.T) {
	fakeOTP := &fakeOTPManager{rows: []domain.OTPEvent{{Platform: "TOKOPED", OTPCode: "222222", AliasEmail: "b@example.com", ReceivedAt: time.Now().UTC()}}}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)
	if !m.otpSearchMode || cmd != nil {
		t.Fatalf("expected entering otp search mode")
	}

	for _, r := range []rune("tokoped") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected otp refresh command on search submit")
	}

	msg := cmd()
	loaded, ok := msg.(otpHistoryLoadedMsg)
	if !ok {
		t.Fatalf("expected otpHistoryLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected otp load error: %v", loaded.err)
	}
	if loaded.reqAt.IsZero() {
		t.Fatalf("expected non-zero request timestamp")
	}

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if m.otpSearchQuery != "tokoped" {
		t.Fatalf("expected search query applied, got %q", m.otpSearchQuery)
	}
	if len(m.otpEvents) != 1 {
		t.Fatalf("expected one otp event loaded")
	}
	if fakeOTP.lastQuery != "tokoped" {
		t.Fatalf("expected query forwarded to otp manager, got %q", fakeOTP.lastQuery)
	}
}

func TestModel_OTPPanel_IgnoreStaleHistoryResponse(t *testing.T) {
	now := time.Now().UTC()
	m := NewModelWithConfig(ModelConfig{})
	m.otpLastReqAt = now

	updated, _ := m.Update(otpHistoryLoadedMsg{
		reqAt:  now.Add(-1 * time.Second),
		query:  "old",
		events: []domain.OTPEvent{{Platform: "OLD", OTPCode: "000000", AliasEmail: "old@example.com", ReceivedAt: now.Add(-time.Minute)}},
	})
	m = updated.(Model)
	if len(m.otpEvents) != 0 {
		t.Fatalf("expected stale response ignored")
	}
}

func TestModel_ModalCtrlCQuits(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{AliasManager: &fakeAliasManager{}})
	m.ActivePanel = PanelAliases
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in alias create mode")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in alias create mode")
	}

	m2 := NewModelWithConfig(ModelConfig{})
	m2.ActivePanel = PanelLatestOTP
	m2.otpSearchMode = true
	_, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in otp search mode")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in otp search mode")
	}
}

func TestUserSafeError_HidesRawErrorMessage(t *testing.T) {
	got := userSafeError("refresh otp history", errors.New("dial tcp 10.0.0.1:443: i/o timeout"))
	if contains(got, "10.0.0.1") || contains(got, "dial tcp") {
		t.Fatalf("expected sanitized error message, got %q", got)
	}
}

func TestModel_OTPPanel_ClearFilterByEsc(t *testing.T) {
	fakeOTP := &fakeOTPManager{rows: []domain.OTPEvent{{Platform: "SHOP", OTPCode: "333333", AliasEmail: "c@example.com", ReceivedAt: time.Now().UTC()}}}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP
	m.otpSearchQuery = "shop"

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected refresh command when clearing otp filter")
	}
	if m.otpSearchQuery != "" {
		t.Fatalf("expected query cleared")
	}

	msg := cmd()
	loaded, ok := msg.(otpHistoryLoadedMsg)
	if !ok || loaded.query != "" {
		t.Fatalf("expected cleared-query otpHistoryLoadedMsg, got %#v", msg)
	}
}

func TestModel_OTPPanel_ViewShowsLatestAndHistory(t *testing.T) {
	now := time.Now().UTC()
	m := NewModelWithConfig(ModelConfig{})
	m.otpEvents = []domain.OTPEvent{
		{Platform: "SHOP", OTPCode: "123456", AliasEmail: "x@example.com", ReceivedAt: now},
		{Platform: "TOKOPED", OTPCode: "999999", AliasEmail: "y@example.com", ReceivedAt: now.Add(-time.Minute)},
	}

	v := m.otpPanelView()
	if !contains(v, "latest: SHOP | 123456") {
		t.Fatalf("expected latest otp line in view")
	}
	if !contains(v, "TOKOPED | 999999") {
		t.Fatalf("expected history row in view")
	}
}

func TestModel_OTPPanel_CopyHotkeySuccess(t *testing.T) {
	clip := &fakeClipboard{}
	m := NewModelWithConfig(ModelConfig{Clipboard: clip})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "x@example.com", ReceivedAt: time.Now().UTC()}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected copy command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if clip.calls != 1 || clip.lastText != "123456" {
		t.Fatalf("expected clipboard copy with otp code, calls=%d text=%q", clip.calls, clip.lastText)
	}
	if m.LastAction != "otp copied" {
		t.Fatalf("expected otp copied action, got %q", m.LastAction)
	}
	if m.ErrorMsg != "" {
		t.Fatalf("expected no error after successful copy, got %q", m.ErrorMsg)
	}
}

func TestModel_OTPPanel_CopyHotkeyUnavailable(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{OTPCode: "123456", ReceivedAt: time.Now().UTC()}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if m.LastAction != "clipboard unavailable" {
		t.Fatalf("expected clipboard unavailable action, got %q", m.LastAction)
	}
}

func TestModel_OTPPanel_CopyHotkeyError(t *testing.T) {
	clip := &fakeClipboard{err: errors.New("copy failed detail")}
	m := NewModelWithConfig(ModelConfig{Clipboard: clip})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{OTPCode: "123456", ReceivedAt: time.Now().UTC()}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected copy command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if m.LastAction != "copy otp failed" {
		t.Fatalf("expected copy otp failed action, got %q", m.LastAction)
	}
	if !contains(m.ErrorMsg, "copy otp failed") {
		t.Fatalf("expected sanitized copy error message, got %q", m.ErrorMsg)
	}
	if contains(m.ErrorMsg, "detail") {
		t.Fatalf("expected no raw error detail leakage, got %q", m.ErrorMsg)
	}
}

func TestModel_OTPPanel_CopyHotkeyNilContextSafe(t *testing.T) {
	clip := &fakeClipboard{}
	m := NewModelWithConfig(ModelConfig{Clipboard: clip})
	m.opParentCtx = nil
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{OTPCode: "555555", ReceivedAt: time.Now().UTC()}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected copy command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(Model)
	if clip.calls != 1 || clip.lastText != "555555" {
		t.Fatalf("expected copy called with otp text, calls=%d text=%q", clip.calls, clip.lastText)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
