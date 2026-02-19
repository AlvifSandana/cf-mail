package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
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
	if m.cfOpTimeout != 30*time.Second {
		t.Fatalf("expected default cf op timeout 30s, got %v", m.cfOpTimeout)
	}
	if m.opContext() == nil {
		t.Fatalf("expected non-nil op context")
	}
}

func TestNewModel_CustomCFOpTimeout(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{CFOpTimeout: 60 * time.Second})
	if m.cfOpTimeout != 60*time.Second {
		t.Fatalf("expected custom cf op timeout 60s, got %v", m.cfOpTimeout)
	}
	if m.opTimeout != 5*time.Second {
		t.Fatalf("expected default op timeout 5s unchanged, got %v", m.opTimeout)
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
	if !contains(m.View(), "refresh aliases failed") {
		t.Fatalf("expected error message in view")
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

	if !contains(view, "Latest OTP") {
		t.Fatalf("expected active panel rendered in view")
	}
	if !contains(view, "Keyboard Shortcuts") || !contains(view, "quit application") {
		t.Fatalf("expected help section in view")
	}
	if strings.Count(view, "Aliases") < 1 {
		t.Fatalf("expected aliases section rendered")
	}
}

func TestModel_View_NarrowLayoutDoesNotForceWideSplit(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 70
	m.Height = 40
	m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}

	v := m.View()
	if !contains(v, "◈ Selected Detail") {
		t.Fatalf("expected detail section in narrow layout")
	}
	if !contains(v, "Latest OTP") {
		t.Fatalf("expected otp section in narrow layout")
	}
}

func TestModel_View_StatusPanelShowsStatusCard(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelStatus
	v := m.View()
	if !contains(v, "System Health") {
		t.Fatalf("expected system status card rendered")
	}
}

func TestModel_View_OTPShowsSearchInputWhenSearchMode(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelLatestOTP
	m.otpSearchMode = true
	m.otpSearchInput = "tokoped"

	v := m.View()
	if !contains(v, "tokoped") {
		t.Fatalf("expected search input visible in view")
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

	v := m.otpTimelineView(80)
	if !contains(v, "SHOP") {
		t.Fatalf("expected SHOP platform in timeline view")
	}
	if !contains(v, "123456") {
		t.Fatalf("expected OTP code 123456 in timeline view")
	}
	if !contains(v, "TOKOPED") {
		t.Fatalf("expected TOKOPED platform in history row")
	}
	if !contains(v, "999999") {
		t.Fatalf("expected OTP code 999999 in history row")
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

func TestModel_Update_RuntimeWatcherEvent_RefreshesLiveMailboxHealth(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{Health: HealthStatus{Mailbox: "configured"}})

	updated, _ := m.Update(app.RuntimeEvent{
		Type: app.RuntimeEventWatcherUpdate,
		Watch: &ports.WatchUpdate{
			Mode:      "idle",
			Timestamp: time.Date(2026, 2, 18, 14, 0, 0, 0, time.UTC),
		},
	})
	m = updated.(Model)

	if m.health.Mailbox == "configured" {
		t.Fatalf("expected mailbox health to update from runtime watcher event")
	}
	if !contains(strings.ToLower(m.LastAction), "mailbox") {
		t.Fatalf("expected last action to mention mailbox runtime update, got %q", m.LastAction)
	}
}

func TestModel_Update_RuntimeErrorEvent_ReflectsRuntimeFailureInStatus(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{Health: HealthStatus{Mailbox: "ready"}})

	updated, _ := m.Update(app.RuntimeEvent{Type: app.RuntimeEventRuntimeError, Err: "runtime watch failed"})
	m = updated.(Model)

	if m.health.Mailbox == "ready" {
		t.Fatalf("expected mailbox health to transition on runtime error event")
	}
}

func TestModel_View_FitsWithinTerminalHeight(t *testing.T) {
	// The rendered View must never exceed the terminal height (m.Height),
	// otherwise the top bar gets pushed off-screen in AltScreen mode.
	sizes := []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{200, 60},
		{100, 30},
		{40, 12}, // narrow + short
	}

	makeEvents := func() []domain.OTPEvent {
		now := time.Now()
		return []domain.OTPEvent{
			{Platform: "SHOP", OTPCode: "123456", AliasEmail: "shop@example.com", ReceivedAt: now},
			{Platform: "BANK", OTPCode: "654321", AliasEmail: "bank@example.com", ReceivedAt: now},
			{Platform: "SOCIAL", OTPCode: "111111", AliasEmail: "social@example.com", ReceivedAt: now},
		}
	}
	makeAliases := func() []domain.Alias {
		return []domain.Alias{
			{Platform: "SHOP", AliasEmail: "shop@example.com", Enabled: true},
			{Platform: "BANK", AliasEmail: "bank@example.com", Enabled: false},
		}
	}

	for _, sz := range sizes {
		// Case 1: empty state
		m := NewModel()
		m.Width = sz.w
		m.Height = sz.h
		output := m.View()
		renderedH := strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("View (empty) at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}

		// Case 2: with OTP events and aliases
		m.otpEvents = makeEvents()
		m.aliases = makeAliases()
		output = m.View()
		renderedH = strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("View (with data) at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}

		// Case 3: with help overlay open
		m.ShowHelp = true
		output = m.View()
		renderedH = strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("View (help open) at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// ── fakeRulesManager ─────────────────────────────────────────────────────────

type fakeRulesManager struct {
	listResult   []ports.RoutingRule
	listErr      error
	listCalls    int
	updateResult ports.RoutingRule
	updateErr    error
	updateCalls  int
	lastUpdate   ports.UpdateRoutingRuleInput
	deleteErr    error
	deleteCalls  int
	lastDeleteID string
}

func (f *fakeRulesManager) ListRoutingRules(_ context.Context) ([]ports.RoutingRule, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ports.RoutingRule, len(f.listResult))
	copy(out, f.listResult)
	return out, nil
}

func (f *fakeRulesManager) UpdateRoutingRule(_ context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error) {
	f.updateCalls++
	f.lastUpdate = in
	if f.updateErr != nil {
		return ports.RoutingRule{}, f.updateErr
	}
	return f.updateResult, nil
}

func (f *fakeRulesManager) DeleteRoutingRuleByID(_ context.Context, ruleID string) error {
	f.deleteCalls++
	f.lastDeleteID = ruleID
	return f.deleteErr
}

// ── CF Rules tests ───────────────────────────────────────────────────────────

func TestModel_Init_LoadsCFRulesWhenManagerConfigured(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{
		{ID: "r1", Name: "tuiotp:SHOP:shop", Enabled: true, AliasEmail: "shop@example.com", Destination: []string{"dest@example.com"}},
	}}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("expected init command when rules manager is configured")
	}

	msg := cmd()
	loaded, ok := msg.(cfRulesLoadedMsg)
	if !ok {
		t.Fatalf("expected cfRulesLoadedMsg, got %T", msg)
	}
	if loaded.err != nil || len(loaded.rules) != 1 {
		t.Fatalf("unexpected loaded cf rules result: %+v", loaded)
	}
	if fakeRules.listCalls != 1 {
		t.Fatalf("expected one list call, got %d", fakeRules.listCalls)
	}
}

func TestModel_CFRulesLoaded_StoresRulesAndClampsSelection(t *testing.T) {
	m := NewModel()
	m.cfSelected = 5

	rules := []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
		{ID: "r2", Name: "rule2", Enabled: false, AliasEmail: "b@example.com"},
	}

	updated, _ := m.Update(cfRulesLoadedMsg{rules: rules})
	m = updated.(Model)

	if len(m.cfRules) != 2 {
		t.Fatalf("expected 2 cf rules, got %d", len(m.cfRules))
	}
	if m.cfSelected != 1 {
		t.Fatalf("expected cfSelected clamped to 1, got %d", m.cfSelected)
	}
	if !contains(m.LastAction, "cf rules refreshed") {
		t.Fatalf("expected cf rules refreshed action, got %q", m.LastAction)
	}
}

func TestModel_CFRulesLoaded_Error(t *testing.T) {
	m := NewModel()

	updated, _ := m.Update(cfRulesLoadedMsg{err: errors.New("cf api down")})
	m = updated.(Model)

	if !contains(m.ErrorMsg, "refresh cf rules failed") {
		t.Fatalf("expected cf rules error msg, got %q", m.ErrorMsg)
	}
	if m.LastAction != "cf rules refresh failed" {
		t.Fatalf("expected cf rules refresh failed action, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_ToggleWithSKey(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}}
	m := NewModelWithConfig(ModelConfig{AliasManager: &fakeAliasManager{}, RulesManager: fakeRules})
	m.ActivePanel = PanelAliases

	// Press s to enter CF rules view
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if !m.showCFRules {
		t.Fatalf("expected showCFRules=true after pressing s")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command when entering cf rules view")
	}
	if m.LastAction != "cf rules view" {
		t.Fatalf("expected cf rules view action, got %q", m.LastAction)
	}

	// Press s again to go back
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.showCFRules {
		t.Fatalf("expected showCFRules=false after pressing s again")
	}
	if cmd != nil {
		t.Fatalf("expected no command when exiting cf rules view")
	}
	if m.LastAction != "aliases view" {
		t.Fatalf("expected aliases view action, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_EscGoesBack(t *testing.T) {
	fakeRules := &fakeRulesManager{}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.showCFRules {
		t.Fatalf("expected showCFRules=false after esc")
	}
}

func TestModel_CFRulesView_NavigationUpDown(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
		{ID: "r2", Name: "rule2", Enabled: false, AliasEmail: "b@example.com"},
		{ID: "r3", Name: "rule3", Enabled: true, AliasEmail: "c@example.com"},
	}

	// Move down
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.cfSelected != 1 {
		t.Fatalf("expected cfSelected=1 after j, got %d", m.cfSelected)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cfSelected != 2 {
		t.Fatalf("expected cfSelected=2 after down, got %d", m.cfSelected)
	}

	// Can't go past end
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.cfSelected != 2 {
		t.Fatalf("expected cfSelected=2 (clamped), got %d", m.cfSelected)
	}

	// Move up
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	if m.cfSelected != 1 {
		t.Fatalf("expected cfSelected=1 after k, got %d", m.cfSelected)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cfSelected != 0 {
		t.Fatalf("expected cfSelected=0 after up, got %d", m.cfSelected)
	}

	// Can't go before start
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.cfSelected != 0 {
		t.Fatalf("expected cfSelected=0 (clamped), got %d", m.cfSelected)
	}
}

func TestModel_CFRulesView_ToggleEnableDisable(t *testing.T) {
	fakeRules := &fakeRulesManager{
		updateResult: ports.RoutingRule{ID: "r1", Name: "rule1", Enabled: false, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}},
		listResult: []ports.RoutingRule{
			{ID: "r1", Name: "rule1", Enabled: false, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}},
		},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}, Priority: 10},
	}

	// Press e to toggle
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected toggle command")
	}
	if !contains(m.LastAction, "disabling") {
		t.Fatalf("expected disabling action, got %q", m.LastAction)
	}

	// Execute toggle command
	msg := cmd()
	toggleResult, ok := msg.(cfRuleUpdatedMsg)
	if !ok {
		t.Fatalf("expected cfRuleUpdatedMsg, got %T", msg)
	}
	if toggleResult.err != nil {
		t.Fatalf("unexpected toggle error: %v", toggleResult.err)
	}
	if fakeRules.updateCalls != 1 {
		t.Fatalf("expected one update call, got %d", fakeRules.updateCalls)
	}
	// The update should toggle enabled from true to false
	if fakeRules.lastUpdate.Enabled != false {
		t.Fatalf("expected toggle to disable (enabled=false), got enabled=%v", fakeRules.lastUpdate.Enabled)
	}
	if fakeRules.lastUpdate.ID != "r1" {
		t.Fatalf("expected rule ID r1, got %q", fakeRules.lastUpdate.ID)
	}

	// Process toggle result
	updated, cmd = m.Update(msg)
	m = updated.(Model)
	if m.LastAction != "cf rule disabled" {
		t.Fatalf("expected cf rule disabled action, got %q", m.LastAction)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after toggle")
	}
}

func TestModel_CFRulesView_ToggleError(t *testing.T) {
	fakeRules := &fakeRulesManager{updateErr: errors.New("toggle failed")}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	msg := cmd()

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !contains(m.ErrorMsg, "toggle cf rule failed") {
		t.Fatalf("expected toggle error message, got %q", m.ErrorMsg)
	}
}

func TestModel_CFRulesView_DeleteFlow(t *testing.T) {
	fakeRules := &fakeRulesManager{
		listResult: []ports.RoutingRule{
			{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
		},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}

	// Press d to enter delete confirm
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)
	if !m.cfDeleteConfirm || cmd != nil {
		t.Fatalf("expected cf delete confirmation mode")
	}

	// Press y to confirm
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected delete command on confirm")
	}

	// Execute delete command
	msg := cmd()
	deleted, ok := msg.(cfRuleDeletedMsg)
	if !ok {
		t.Fatalf("expected cfRuleDeletedMsg, got %T", msg)
	}
	if deleted.err != nil {
		t.Fatalf("unexpected delete error: %v", deleted.err)
	}
	if fakeRules.deleteCalls != 1 || fakeRules.lastDeleteID != "r1" {
		t.Fatalf("unexpected delete call: calls=%d id=%q", fakeRules.deleteCalls, fakeRules.lastDeleteID)
	}

	// Process delete result
	updated, cmd = m.Update(msg)
	m = updated.(Model)
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after success")
	}
	if m.LastAction != "cf rule deleted" {
		t.Fatalf("expected cf rule deleted action, got %q", m.LastAction)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after delete")
	}
}

func TestModel_CFRulesView_DeleteCancelWithN(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfDeleteConfirm = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after n")
	}
	if m.LastAction != "delete cf rule cancelled" {
		t.Fatalf("expected cancelled action, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_DeleteCancelWithEsc(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfDeleteConfirm = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after esc")
	}
}

func TestModel_CFRulesView_DeleteError(t *testing.T) {
	fakeRules := &fakeRulesManager{deleteErr: errors.New("cf api error")}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", AliasEmail: "a@example.com"},
	}

	// Enter delete confirm and confirm
	m.cfDeleteConfirm = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	msg := cmd()

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if !contains(m.ErrorMsg, "delete cf rule failed") {
		t.Fatalf("expected delete error message, got %q", m.ErrorMsg)
	}
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after error")
	}
}

func TestModel_CFRulesView_DeleteInvalidSelection(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfDeleteConfirm = true
	m.cfSelected = 9
	m.cfRules = []ports.RoutingRule{{ID: "r1"}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected no command on invalid selection")
	}
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared")
	}
	if !contains(m.ErrorMsg, "invalid cf rule selection") {
		t.Fatalf("expected invalid selection error, got %q", m.ErrorMsg)
	}
}

func TestModel_CFRulesView_ToggleNoRules(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = nil

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if m.LastAction != "no cf rule to toggle" {
		t.Fatalf("expected no cf rule to toggle, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_DeleteNoRules(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = nil

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)
	if m.LastAction != "no cf rule to delete" {
		t.Fatalf("expected no cf rule to delete, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_CtrlCQuits(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelAliases
	m.showCFRules = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in cf rules view")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in cf rules view")
	}
}

func TestModel_CFRulesView_RenderShowsCFRules(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "shop@example.com"},
		{ID: "r2", Name: "rule2", Enabled: false, AliasEmail: "bank@example.com"},
	}
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "CF Rules") {
		t.Fatalf("expected CF Rules title in view")
	}
	if !contains(v, "shop@example.com") {
		t.Fatalf("expected shop@example.com in cf rules view")
	}
	if !contains(v, "bank@example.com") {
		t.Fatalf("expected bank@example.com in cf rules view")
	}
}

func TestModel_CFRulesView_RenderShowsDeleteConfirm(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfDeleteConfirm = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", AliasEmail: "shop@example.com"},
	}
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "Delete CF Rule?") {
		t.Fatalf("expected delete confirm in view")
	}
	if !contains(v, "shop@example.com") {
		t.Fatalf("expected alias email in delete confirm view")
	}
}

func TestModel_CFRulesView_RenderEmptyState(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelAliases
	m.showCFRules = true
	m.cfRules = nil
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "no cf routing rules found") {
		t.Fatalf("expected empty state message in view")
	}
}

func TestModel_CFRulesView_SKeyUnavailableWithoutManager(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{AliasManager: &fakeAliasManager{}})
	m.ActivePanel = PanelAliases

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.showCFRules {
		t.Fatalf("expected showCFRules=false when rules manager is nil")
	}
	if !contains(m.ErrorMsg, "rules manager unavailable") {
		t.Fatalf("expected rules manager unavailable error, got %q", m.ErrorMsg)
	}
}

func TestModel_CFRulesView_RefreshWithRKey(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelAliases
	m.showCFRules = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected refresh command on r key")
	}
	if !contains(m.LastAction, "refreshing cf rules") {
		t.Fatalf("expected refreshing action, got %q", m.LastAction)
	}
}

func TestModel_CFRulesView_FitsWithinTerminalHeight(t *testing.T) {
	rules := make([]ports.RoutingRule, 20)
	for i := range rules {
		rules[i] = ports.RoutingRule{
			ID:         fmt.Sprintf("r%d", i),
			Name:       fmt.Sprintf("rule%d", i),
			Enabled:    i%2 == 0,
			AliasEmail: fmt.Sprintf("alias%d@example.com", i),
		}
	}

	sizes := []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{40, 12},
	}

	for _, sz := range sizes {
		m := NewModel()
		m.Width = sz.w
		m.Height = sz.h
		m.ActivePanel = PanelAliases
		m.showCFRules = true
		m.cfRules = rules

		output := m.View()
		renderedH := strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("CF rules view at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}
	}
}

func TestModel_CFRuleUpdated_EnabledStatus(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{}}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})

	updated, cmd := m.Update(cfRuleUpdatedMsg{rule: ports.RoutingRule{ID: "r1", Enabled: true}})
	m = updated.(Model)
	if m.LastAction != "cf rule enabled" {
		t.Fatalf("expected cf rule enabled action, got %q", m.LastAction)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after toggle")
	}
}
