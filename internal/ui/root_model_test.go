package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

type fakeOTPManager struct {
	rows      []domain.OTPEvent
	err       error
	lastQuery string
	lastLimit int
	calls     int

	clearByIDCalls     int
	clearByIDLastID    int64
	clearByIDRows      int64
	clearByIDErr       error
	clearByFilterCalls int
	clearByFilterLast  app.OTPDeleteFilter
	clearByFilterRows  int64
	clearByFilterErr   error
}

type fakeClipboard struct {
	err      error
	lastText string
	calls    int
}

type fakeSettingsManager struct {
	loadState SettingsState
	loadErr   error
	loadCalls int

	saveState      SettingsState
	saveClipboard  clipboardCopier
	saveErr        error
	saveCalls      int
	lastSavedState SettingsState
}

func (f *fakeClipboard) Copy(_ context.Context, text string) error {
	f.calls++
	f.lastText = text
	return f.err
}

func (f *fakeSettingsManager) Load(_ context.Context) (SettingsState, error) {
	f.loadCalls++
	if f.loadErr != nil {
		return SettingsState{}, f.loadErr
	}
	return f.loadState, nil
}

func (f *fakeSettingsManager) SaveAndApply(_ context.Context, state SettingsState) (SettingsState, clipboardCopier, error) {
	f.saveCalls++
	f.lastSavedState = state
	if f.saveErr != nil {
		return SettingsState{}, nil, f.saveErr
	}
	if f.saveState.ClipboardMethod == "" {
		f.saveState = state
	}
	return f.saveState, f.saveClipboard, nil
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

func (f *fakeOTPManager) ClearOTPEventByID(_ context.Context, id int64) (int64, error) {
	f.clearByIDCalls++
	f.clearByIDLastID = id
	if f.clearByIDErr != nil {
		return 0, f.clearByIDErr
	}
	return f.clearByIDRows, nil
}

func (f *fakeOTPManager) ClearOTPEvents(_ context.Context, filter app.OTPDeleteFilter) (int64, error) {
	f.clearByFilterCalls++
	f.clearByFilterLast = filter
	if f.clearByFilterErr != nil {
		return 0, f.clearByFilterErr
	}
	return f.clearByFilterRows, nil
}

func TestNewModel_DefaultState(t *testing.T) {
	m := NewModel()

	if m.ActivePanel != PanelStatus {
		t.Fatalf("expected default active panel status, got %v", m.ActivePanel)
	}
	if m.ShowHelp {
		t.Fatalf("expected help hidden by default")
	}
	if m.toast != nil {
		t.Fatalf("expected no initial toast, got %+v", m.toast)
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
	if m2.toast == nil || m2.toast.Message != "refreshing..." {
		t.Fatalf("expected toast 'refreshing...', got %+v", m2.toast)
	}
	if cmd == nil {
		t.Fatalf("expected non-nil command for refresh (toast batch)")
	}

	updated, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyTab})
	m3 := updated.(Model)
	if m3.ActivePanel != PanelMailAccount {
		t.Fatalf("expected panel switch to mail account, got %v", m3.ActivePanel)
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

func TestModel_Update_TabCyclesPanels(t *testing.T) {
	m := NewModel()

	for i := 0; i < int(panelCount); i++ {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(Model)
	}

	if m.ActivePanel != PanelStatus {
		t.Fatalf("expected tab to cycle back to status, got %v", m.ActivePanel)
	}
}

func TestModel_SKey_SwitchesToSettingsPanel(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelStatus

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)

	if m.ActivePanel != PanelSettings {
		t.Fatalf("expected PanelSettings after s key, got %v", m.ActivePanel)
	}
	if cmd != nil {
		t.Fatalf("expected nil command when no settings manager")
	}
}

func TestModel_View_HelpAndPanelHighlight(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelLatestOTP
	m.ShowHelp = true
	m.Width = 120
	m.Height = 40
	view := m.View()

	if !contains(view, "Latest OTP") {
		t.Fatalf("expected active panel rendered in view")
	}
	if !contains(view, "Keyboard Shortcuts") || !contains(view, "quit application") {
		t.Fatalf("expected help section in view")
	}
	if strings.Count(view, "Mail Account") < 1 {
		t.Fatalf("expected mail account section rendered")
	}
}

func TestModel_View_NarrowLayoutDoesNotForceWideSplit(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 70
	m.Height = 40
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}

	v := m.View()
	if !contains(v, "◈ Selected Detail") {
		t.Fatalf("expected detail section in narrow layout")
	}
	if !contains(v, "⚡ OTP") {
		t.Fatalf("expected otp tab visible in narrow layout")
	}
	if !contains(v, "Latest OTP") {
		t.Fatalf("expected otp section in narrow layout")
	}
	if contains(v, "◈ System Health") || contains(v, "☁  Mail Account  live") {
		t.Fatalf("expected single-panel layout on small width")
	}
}

func TestModel_View_SmallWindow_UsesSingleActiveLogsPanel(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 72
	m.Height = 28
	m.ActivePanel = PanelLogs
	m.logLines = []string{"line 1", "line 2"}

	v := m.View()
	if !contains(v, "≡ Logs") {
		t.Fatalf("expected logs tab visible in single layout")
	}
	if !contains(v, "≡ Logs") {
		t.Fatalf("expected logs panel in single layout")
	}
	if contains(v, "⚡ Latest OTP") || contains(v, "◈ System Health") || contains(v, "☁  Mail Account  live") {
		t.Fatalf("expected only active panel content in single layout")
	}
}

func TestModel_View_SmallWindow_UsesSingleActiveMailPanel(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 72
	m.Height = 28
	m.ActivePanel = PanelMailAccount

	v := m.View()
	if !contains(v, "☁ Mail Account") {
		t.Fatalf("expected mail tab visible in single layout")
	}
	if !contains(v, "Mail Account") {
		t.Fatalf("expected mail account panel in single layout")
	}
	if contains(v, "Latest OTP") || contains(v, "System Health") {
		t.Fatalf("expected single active panel without other sections")
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
	fakeRules := &fakeRulesManager{}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	// Enter create mode
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in mail account create mode")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in mail account create mode")
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

	msg := runBatchExtract(cmd)
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
	updated, _ = m.Update(runBatchExtract(cmd))
	m = updated.(Model)
	if clip.calls != 1 || clip.lastText != "123456" {
		t.Fatalf("expected clipboard copy with otp code, calls=%d text=%q", clip.calls, clip.lastText)
	}
	if m.toast == nil || m.toast.Message != "otp copied" {
		t.Fatalf("expected toast 'otp copied', got %+v", m.toast)
	}
	if m.toast.Level != ToastSuccess {
		t.Fatalf("expected ToastSuccess level, got %v", m.toast.Level)
	}
}

func TestModel_OTPPanel_CopyHotkeyUnavailable(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{OTPCode: "123456", ReceivedAt: time.Now().UTC()}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "clipboard unavailable" {
		t.Fatalf("expected toast 'clipboard unavailable', got %+v", m.toast)
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
	updated, _ = m.Update(runBatchExtract(cmd))
	m = updated.(Model)
	if m.toast == nil || !contains(m.toast.Message, "copy otp failed") {
		t.Fatalf("expected toast containing 'copy otp failed', got %+v", m.toast)
	}
	if m.toast.Level != ToastError {
		t.Fatalf("expected ToastError level, got %v", m.toast.Level)
	}
	if contains(m.toast.Message, "detail") {
		t.Fatalf("expected no raw error detail leakage, got %q", m.toast.Message)
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
	updated, _ = m.Update(runBatchExtract(cmd))
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
}

func TestModel_Update_RuntimeErrorEvent_ReflectsRuntimeFailureInStatus(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{Health: HealthStatus{Mailbox: "ready"}})

	updated, _ := m.Update(app.RuntimeEvent{Type: app.RuntimeEventRuntimeError, Err: "runtime watch failed"})
	m = updated.(Model)

	if m.health.Mailbox == "ready" {
		t.Fatalf("expected mailbox health to transition on runtime error event")
	}
}

func TestModel_Update_RuntimeOTPProcessedStored_RefreshesHistory(t *testing.T) {
	fakeOTP := &fakeOTPManager{rows: []domain.OTPEvent{{ID: 7, Platform: "SHOP", OTPCode: "787878", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})

	updated, cmd := m.Update(app.RuntimeEvent{Type: app.RuntimeEventOTPProcessed, OTPStatus: app.OTPPipelineStatusStored})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected refresh command for stored otp runtime event")
	}

	msg := cmd()
	loaded, ok := msg.(otpHistoryLoadedMsg)
	if !ok {
		t.Fatalf("expected otpHistoryLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected load error: %v", loaded.err)
	}
	if fakeOTP.calls != 1 {
		t.Fatalf("expected one otp history fetch, got %d", fakeOTP.calls)
	}
}

func TestModel_Update_RuntimeOTPProcessedDuplicate_NoRefresh(t *testing.T) {
	fakeOTP := &fakeOTPManager{}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})

	_, cmd := m.Update(app.RuntimeEvent{Type: app.RuntimeEventOTPProcessed, OTPStatus: app.OTPPipelineStatusDuplicate})
	if cmd != nil {
		t.Fatalf("expected nil command for non-stored otp runtime event")
	}
	if fakeOTP.calls != 0 {
		t.Fatalf("expected no otp history fetch, got %d", fakeOTP.calls)
	}
}

func TestModel_OTPPanel_ClearSelectedFlow(t *testing.T) {
	fakeOTP := &fakeOTPManager{clearByIDRows: 1}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{ID: 42, OTPCode: "123456", ReceivedAt: time.Now().UTC()}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected nil command when entering clear confirm")
	}
	if !m.otpDeleteMode || m.otpDeleteScope != "selected" {
		t.Fatalf("expected selected clear confirm mode")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected clear selected command on confirm")
	}
	msg := runBatchExtract(cmd)
	deleted, ok := msg.(otpDeletedMsg)
	if !ok {
		t.Fatalf("expected otpDeletedMsg, got %T", msg)
	}
	if deleted.err != nil {
		t.Fatalf("unexpected clear error: %v", deleted.err)
	}
	if fakeOTP.clearByIDCalls != 1 || fakeOTP.clearByIDLastID != 42 {
		t.Fatalf("expected clear by id called once with id=42, calls=%d id=%d", fakeOTP.clearByIDCalls, fakeOTP.clearByIDLastID)
	}
}

func TestModel_OTPPanel_ClearFilteredRequiresActiveFilter(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelLatestOTP

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "no active filter to clear" {
		t.Fatalf("expected warning toast for missing filter, got %+v", m.toast)
	}
}

func TestModel_OTPPanel_ClearFilteredFlow_UsesCurrentQuery(t *testing.T) {
	fakeOTP := &fakeOTPManager{clearByFilterRows: 2}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP
	m.otpSearchQuery = "tokoped"

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected nil command when entering filtered clear confirm")
	}
	if !m.otpDeleteMode || m.otpDeleteScope != "filtered" {
		t.Fatalf("expected filtered clear confirm mode")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected clear filtered command on confirm")
	}
	msg := runBatchExtract(cmd)
	deleted, ok := msg.(otpDeletedMsg)
	if !ok {
		t.Fatalf("expected otpDeletedMsg, got %T", msg)
	}
	if deleted.err != nil {
		t.Fatalf("unexpected clear filtered error: %v", deleted.err)
	}
	if fakeOTP.clearByFilterCalls != 1 {
		t.Fatalf("expected one clear filtered call, got %d", fakeOTP.clearByFilterCalls)
	}
	if fakeOTP.clearByFilterLast.Query != "tokoped" {
		t.Fatalf("expected query tokoped forwarded, got %q", fakeOTP.clearByFilterLast.Query)
	}
}

func TestModel_OTPPanel_ClearAllFlow(t *testing.T) {
	fakeOTP := &fakeOTPManager{clearByFilterRows: 3}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected nil command when entering clear all confirm")
	}
	if !m.otpDeleteMode || m.otpDeleteScope != "all" {
		t.Fatalf("expected all clear confirm mode")
	}

	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected clear all command on confirm")
	}
	msg := runBatchExtract(cmd)
	deleted, ok := msg.(otpDeletedMsg)
	if !ok {
		t.Fatalf("expected otpDeletedMsg, got %T", msg)
	}
	if deleted.err != nil {
		t.Fatalf("unexpected clear all error: %v", deleted.err)
	}
	if fakeOTP.clearByFilterCalls != 1 {
		t.Fatalf("expected one clear-all call, got %d", fakeOTP.clearByFilterCalls)
	}
	if fakeOTP.clearByFilterLast.Query != "" {
		t.Fatalf("expected empty query for clear-all, got %q", fakeOTP.clearByFilterLast.Query)
	}
	if !fakeOTP.clearByFilterLast.AllowDeleteAll {
		t.Fatalf("expected allow-delete-all flag for clear-all flow")
	}
}

func TestModel_OTPPanel_ClearCancel(t *testing.T) {
	fakeOTP := &fakeOTPManager{}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{ID: 99, OTPCode: "123123", ReceivedAt: time.Now().UTC()}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	if !m.otpDeleteMode {
		t.Fatalf("expected delete confirm mode")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected toast command on cancel")
	}
	if m.otpDeleteMode {
		t.Fatalf("expected delete confirm mode cleared after cancel")
	}
	if m.toast == nil || m.toast.Message != "clear cancelled" {
		t.Fatalf("expected toast 'clear cancelled', got %+v", m.toast)
	}
}

func TestModel_OTPPanel_ClearSelectedError_ShowsErrorToast(t *testing.T) {
	fakeOTP := &fakeOTPManager{clearByIDErr: errors.New("delete failed")}
	m := NewModelWithConfig(ModelConfig{OTPManager: fakeOTP})
	m.ActivePanel = PanelLatestOTP
	m.otpEvents = []domain.OTPEvent{{ID: 12, OTPCode: "121212", ReceivedAt: time.Now().UTC()}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updated.(Model)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected clear command")
	}
	msg := runBatchExtract(cmd)
	updated, _ = m.Update(msg)
	m = updated.(Model)
	if m.toast == nil || !contains(m.toast.Message, "clear otp failed") {
		t.Fatalf("expected clear otp failed toast, got %+v", m.toast)
	}
}

func TestModel_View_FitsWithinTerminalHeight(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{200, 60},
		{100, 30},
		{40, 12},
	}

	makeEvents := func() []domain.OTPEvent {
		now := time.Now()
		return []domain.OTPEvent{
			{Platform: "SHOP", OTPCode: "123456", AliasEmail: "shop@example.com", ReceivedAt: now},
			{Platform: "BANK", OTPCode: "654321", AliasEmail: "bank@example.com", ReceivedAt: now},
			{Platform: "SOCIAL", OTPCode: "111111", AliasEmail: "social@example.com", ReceivedAt: now},
		}
	}
	makeRules := func() []ports.RoutingRule {
		return []ports.RoutingRule{
			{ID: "r1", Name: "rule1", AliasEmail: "shop@example.com", Enabled: true},
			{ID: "r2", Name: "rule2", AliasEmail: "bank@example.com", Enabled: false},
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

		// Case 2: with OTP events and mail accounts
		m.otpEvents = makeEvents()
		m.cfRules = makeRules()
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

func TestTrimLastRune_UnicodeSafe(t *testing.T) {
	if got := trimLastRune("A😊"); got != "A" {
		t.Fatalf("expected rune-safe trim result 'A', got %q", got)
	}
	if got := trimLastRune(""); got != "" {
		t.Fatalf("expected empty string for empty input, got %q", got)
	}
}

func TestTruncate_ANSISafeAndWidthAware(t *testing.T) {
	in := "\x1b[31mHELLOWORLD\x1b[0m"
	got := truncate(in, 5)

	if w := lipgloss.Width(got); w > 5 {
		t.Fatalf("expected width <= 5, got %d (%q)", w, got)
	}

	plain := xansi.Strip(got)
	if plain != "HELL…" {
		t.Fatalf("expected stripped output HELL…, got %q", plain)
	}

	if strings.Contains(got, "\x1b[") && !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("expected ANSI reset to remain intact, got %q", got)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// runBatchExtract executes a cmd; if it returns a tea.BatchMsg, it finds
// and returns the first non-toast async message. Toast timer commands
// (tea.Tick for dismiss/countdown) are identified by their batch shape
// and skipped to avoid blocking tests.
func runBatchExtract(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	return extractNonToastMsg(msg)
}

func extractNonToastMsg(msg tea.Msg) tea.Msg {
	if msg == nil {
		return nil
	}
	if _, ok := msg.(toastDismissMsg); ok {
		return nil
	}
	if _, ok := msg.(toastTickMsg); ok {
		return nil
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return msg
	}
	// Try each sub-command with a per-cmd timeout.
	// Toast timer cmds (tea.Tick) block for 1-5s; real async cmds return immediately.
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func() { ch <- sub() }()
		select {
		case result := <-ch:
			found := extractNonToastMsg(result)
			if found != nil {
				return found
			}
		case <-time.After(50 * time.Millisecond):
			// This sub-cmd is a blocking tea.Tick — skip it.
			continue
		}
	}
	return nil
}

// ── fakeRulesManager ─────────────────────────────────────────────────────────

type fakeRulesManager struct {
	listResult     []ports.RoutingRule
	listErr        error
	listCalls      int
	domains        []string
	activeDomain   string
	setActiveErr   error
	setActiveCalls int
	lastSetActive  string
	createResult   ports.RoutingRule
	createErr      error
	createCalls    int
	lastCreate     ports.CreateRoutingRuleInput
	updateResult   ports.RoutingRule
	updateErr      error
	updateCalls    int
	lastUpdate     ports.UpdateRoutingRuleInput
	deleteErr      error
	deleteCalls    int
	lastDeleteID   string
}

func (f *fakeRulesManager) ListDomains() []string {
	out := make([]string, len(f.domains))
	copy(out, f.domains)
	return out
}

func (f *fakeRulesManager) ActiveDomain() string {
	return f.activeDomain
}

func (f *fakeRulesManager) SetActiveDomain(domain string) error {
	f.setActiveCalls++
	f.lastSetActive = domain
	if strings.TrimSpace(domain) == "" {
		return errors.New("active domain is required")
	}
	if f.setActiveErr != nil {
		return f.setActiveErr
	}
	f.activeDomain = strings.ToLower(strings.TrimSpace(domain))
	return nil
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

func (f *fakeRulesManager) CreateRoutingRuleDirect(_ context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error) {
	f.createCalls++
	f.lastCreate = in
	if f.createErr != nil {
		return ports.RoutingRule{}, f.createErr
	}
	return f.createResult, nil
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

// ── Mail Account (CF Rules) tests ────────────────────────────────────────────

func TestModel_Init_LoadsMailAccountsWhenManagerConfigured(t *testing.T) {
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
		t.Fatalf("unexpected loaded mail accounts result: %+v", loaded)
	}
	if fakeRules.listCalls != 1 {
		t.Fatalf("expected one list call, got %d", fakeRules.listCalls)
	}
}

func TestModel_MailAccountLoaded_StoresRulesAndClampsSelection(t *testing.T) {
	m := NewModel()
	m.cfSelected = 5

	rules := []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
		{ID: "r2", Name: "rule2", Enabled: false, AliasEmail: "b@example.com"},
	}

	updated, _ := m.Update(cfRulesLoadedMsg{rules: rules})
	m = updated.(Model)

	if len(m.cfRules) != 2 {
		t.Fatalf("expected 2 mail accounts, got %d", len(m.cfRules))
	}
	if m.cfSelected != 1 {
		t.Fatalf("expected cfSelected clamped to 1, got %d", m.cfSelected)
	}
	if m.toast == nil || !contains(m.toast.Message, "mail accounts refreshed") {
		t.Fatalf("expected toast containing 'mail accounts refreshed', got %+v", m.toast)
	}
}

func TestModel_MailAccountLoaded_Error(t *testing.T) {
	m := NewModel()

	updated, _ := m.Update(cfRulesLoadedMsg{err: errors.New("cf api down")})
	m = updated.(Model)

	if m.toast == nil || m.toast.Level != ToastError || !contains(m.toast.Message, "refresh mail accounts failed") {
		t.Fatalf("expected error toast containing 'refresh mail accounts failed', got %+v", m.toast)
	}
}

func TestModel_MailAccount_CreateFlow_SubmitAndRefresh(t *testing.T) {
	fakeRules := &fakeRulesManager{
		domains:      []string{"example.com"},
		activeDomain: "example.com",
		createResult: ports.RoutingRule{ID: "r-new", Name: "tuiotp:shop", Enabled: true, AliasEmail: "shop@example.com"},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	// Press n to enter create mode
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	if !m.creating || cmd != nil {
		t.Fatalf("expected entering create mode without command")
	}

	// Type alias email
	for _, r := range []rune("shop@example.com") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	if m.createAliasEmail != "shop@example.com" {
		t.Fatalf("expected createAliasEmail to be 'shop@example.com', got %q", m.createAliasEmail)
	}

	// Submit
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected create mail account command on submit")
	}

	createMsg := runBatchExtract(cmd)
	created, ok := createMsg.(cfRuleCreatedMsg)
	if !ok {
		t.Fatalf("expected cfRuleCreatedMsg, got %T", createMsg)
	}
	if created.err != nil {
		t.Fatalf("unexpected create error: %v", created.err)
	}
	if fakeRules.createCalls != 1 {
		t.Fatalf("expected one create call, got %d", fakeRules.createCalls)
	}
	if fakeRules.lastCreate.AliasEmail != "shop@example.com" {
		t.Fatalf("unexpected create input: %+v", fakeRules.lastCreate)
	}
	if !fakeRules.lastCreate.Enabled {
		t.Fatalf("expected new rule to be enabled by default")
	}

	// Process the creation result
	updated, cmd = m.Update(createMsg)
	m = updated.(Model)
	if m.creating {
		t.Fatalf("expected create mode closed after success")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after create")
	}
	if m.toast == nil || m.toast.Message != "mail account created" {
		t.Fatalf("expected toast 'mail account created', got %+v", m.toast)
	}
}

func TestModel_MailAccount_CreateFlow_LocalPartUsesActiveDomain(t *testing.T) {
	fakeRules := &fakeRulesManager{
		domains:      []string{"example.com", "example.net"},
		activeDomain: "example.net",
		createResult: ports.RoutingRule{ID: "r-new", AliasEmail: "shop@example.net", Enabled: true},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	for _, r := range []rune("shop") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected create command")
	}
	_ = runBatchExtract(cmd)
	if fakeRules.lastCreate.AliasEmail != "shop@example.net" {
		t.Fatalf("expected local-part expanded with active domain, got %q", fakeRules.lastCreate.AliasEmail)
	}
}

func TestModel_MailAccount_DomainSwitchControls(t *testing.T) {
	fakeRules := &fakeRulesManager{domains: []string{"example.com", "example.net"}, activeDomain: "example.com"}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	m = updated.(Model)
	if fakeRules.activeDomain != "example.net" {
		t.Fatalf("expected ] to switch to next domain, got %q", fakeRules.activeDomain)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after switching domain")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	m = updated.(Model)
	if fakeRules.activeDomain != "example.com" {
		t.Fatalf("expected [ to switch to previous domain, got %q", fakeRules.activeDomain)
	}
}

func TestModel_MailAccount_CreateFlow_ShieldsGlobalQuitWhileTyping(t *testing.T) {
	fakeRules := &fakeRulesManager{}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected no quit command while create form is active")
	}
	if !strings.Contains(m.createAliasEmail, "q") {
		t.Fatalf("expected typed rune to go into create form field")
	}
}

func TestModel_MailAccount_CreateFlow_EmptyEmail(t *testing.T) {
	fakeRules := &fakeRulesManager{}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.creating = true

	// Submit with empty email
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected toast cmd for validation error")
	}
	if m.toast == nil || !contains(m.toast.Message, "email is required") {
		t.Fatalf("expected toast containing 'email is required', got %+v", m.toast)
	}
}

func TestModel_MailAccount_CreateFlow_CancelWithEsc(t *testing.T) {
	fakeRules := &fakeRulesManager{}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.creating = true
	m.createAliasEmail = "shop@example.com"

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.creating {
		t.Fatalf("expected create mode closed after esc")
	}
	if m.createAliasEmail != "" {
		t.Fatalf("expected createAliasEmail cleared after esc")
	}
	if m.toast == nil || m.toast.Message != "create cancelled" {
		t.Fatalf("expected toast 'create cancelled', got %+v", m.toast)
	}
}

func TestModel_MailAccount_CreateFlow_Error(t *testing.T) {
	fakeRules := &fakeRulesManager{createErr: errors.New("cf api error")}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.creating = true
	m.createAliasEmail = "shop@example.com"

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected create command")
	}

	msg := runBatchExtract(cmd)
	updated, _ := m.Update(msg)
	m = updated.(Model)
	if m.toast == nil || !contains(m.toast.Message, "create mail account failed") {
		t.Fatalf("expected toast containing 'create mail account failed', got %+v", m.toast)
	}
}

func TestModel_MailAccount_NavigationUpDown(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
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

func TestModel_MailAccount_ToggleEnableDisable(t *testing.T) {
	fakeRules := &fakeRulesManager{
		updateResult: ports.RoutingRule{ID: "r1", Name: "rule1", Enabled: false, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}},
		listResult: []ports.RoutingRule{
			{ID: "r1", Name: "rule1", Enabled: false, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}},
		},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com", Destination: []string{"dest@example.com"}, Priority: 10},
	}

	// Press e to toggle
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected toggle command")
	}
	if m.toast == nil || !contains(m.toast.Message, "disabling") {
		t.Fatalf("expected toast containing 'disabling', got %+v", m.toast)
	}

	// Execute toggle command
	msg := runBatchExtract(cmd)
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
	if fakeRules.lastUpdate.Enabled != false {
		t.Fatalf("expected toggle to disable (enabled=false), got enabled=%v", fakeRules.lastUpdate.Enabled)
	}
	if fakeRules.lastUpdate.ID != "r1" {
		t.Fatalf("expected rule ID r1, got %q", fakeRules.lastUpdate.ID)
	}

	// Process toggle result
	updated, cmd = m.Update(msg)
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "mail account disabled" {
		t.Fatalf("expected toast 'mail account disabled', got %+v", m.toast)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after toggle")
	}
}

func TestModel_MailAccount_ToggleError(t *testing.T) {
	fakeRules := &fakeRulesManager{updateErr: errors.New("toggle failed")}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	msg := runBatchExtract(cmd)

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if m.toast == nil || !contains(m.toast.Message, "toggle mail account failed") {
		t.Fatalf("expected toast containing 'toggle mail account failed', got %+v", m.toast)
	}
}

func TestModel_MailAccount_DeleteFlow(t *testing.T) {
	fakeRules := &fakeRulesManager{
		listResult: []ports.RoutingRule{
			{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
		},
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}

	// Press d to enter delete confirm
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)
	if !m.cfDeleteConfirm || cmd != nil {
		t.Fatalf("expected delete confirmation mode")
	}

	// Press y to confirm
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected delete command on confirm")
	}

	// Execute delete command
	msg := runBatchExtract(cmd)
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
	if m.toast == nil || m.toast.Message != "mail account deleted" {
		t.Fatalf("expected toast 'mail account deleted', got %+v", m.toast)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after delete")
	}
}

func TestModel_MailAccount_DeleteCancelWithN(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
	m.cfDeleteConfirm = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after n")
	}
	if m.toast == nil || m.toast.Message != "delete cancelled" {
		t.Fatalf("expected toast 'delete cancelled', got %+v", m.toast)
	}
}

func TestModel_MailAccount_DeleteCancelWithEsc(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
	m.cfDeleteConfirm = true

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after esc")
	}
}

func TestModel_MailAccount_DeleteError(t *testing.T) {
	fakeRules := &fakeRulesManager{deleteErr: errors.New("cf api error")}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", AliasEmail: "a@example.com"},
	}

	// Enter delete confirm and confirm
	m.cfDeleteConfirm = true
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)
	msg := runBatchExtract(cmd)

	updated, _ = m.Update(msg)
	m = updated.(Model)
	if m.toast == nil || !contains(m.toast.Message, "delete mail account failed") {
		t.Fatalf("expected toast containing 'delete mail account failed', got %+v", m.toast)
	}
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared after error")
	}
}

func TestModel_MailAccount_DeleteInvalidSelection(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
	m.cfDeleteConfirm = true
	m.cfSelected = 9
	m.cfRules = []ports.RoutingRule{{ID: "r1"}}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected toast cmd for invalid selection error")
	}
	if m.cfDeleteConfirm {
		t.Fatalf("expected cfDeleteConfirm cleared")
	}
	if m.toast == nil || !contains(m.toast.Message, "invalid mail account selection") {
		t.Fatalf("expected toast containing 'invalid mail account selection', got %+v", m.toast)
	}
}

func TestModel_MailAccount_ToggleNoRules(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
	m.cfRules = nil

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "no mail account to toggle" {
		t.Fatalf("expected toast 'no mail account to toggle', got %+v", m.toast)
	}
}

func TestModel_MailAccount_DeleteNoRules(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount
	m.cfRules = nil

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "no mail account to delete" {
		t.Fatalf("expected toast 'no mail account to delete', got %+v", m.toast)
	}
}

func TestModel_MailAccount_CtrlCQuits(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelMailAccount

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in mail account view")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in mail account view")
	}
}

func TestModel_MailAccount_RenderShowsMailAccounts(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelMailAccount
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "shop@example.com"},
		{ID: "r2", Name: "rule2", Enabled: false, AliasEmail: "bank@example.com"},
	}
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "Mail Account") {
		t.Fatalf("expected Mail Account title in view")
	}
	if !contains(v, "shop@example.com") {
		t.Fatalf("expected shop@example.com in mail account view")
	}
	if !contains(v, "bank@example.com") {
		t.Fatalf("expected bank@example.com in mail account view")
	}
}

func TestModel_MailAccount_RenderShowsDeleteConfirm(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelMailAccount
	m.cfDeleteConfirm = true
	m.cfRules = []ports.RoutingRule{
		{ID: "r1", Name: "rule1", AliasEmail: "shop@example.com"},
	}
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "Delete Mail Account?") {
		t.Fatalf("expected delete confirm in view")
	}
	if !contains(v, "shop@example.com") {
		t.Fatalf("expected alias email in delete confirm view")
	}
}

func TestModel_MailAccount_RenderEmptyState(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelMailAccount
	m.cfRules = nil
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "no mail accounts") {
		t.Fatalf("expected empty state message in view")
	}
}

func TestModel_MailAccount_NewKeyUnavailableWithoutManager(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.ActivePanel = PanelMailAccount

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)
	if m.creating {
		t.Fatalf("expected creating=false when rules manager is nil")
	}
	if m.toast == nil || !contains(m.toast.Message, "rules manager unavailable") {
		t.Fatalf("expected toast containing 'rules manager unavailable', got %+v", m.toast)
	}
}

func TestModel_MailAccount_RefreshWithRKey(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{
		{ID: "r1", Name: "rule1", Enabled: true, AliasEmail: "a@example.com"},
	}}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})
	m.ActivePanel = PanelMailAccount

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected refresh command on r key")
	}
	if m.toast == nil || !contains(m.toast.Message, "refreshing mail accounts") {
		t.Fatalf("expected toast containing 'refreshing mail accounts', got %+v", m.toast)
	}
}

func TestModel_MailAccount_FitsWithinTerminalHeight(t *testing.T) {
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
		m.ActivePanel = PanelMailAccount
		m.cfRules = rules

		output := m.View()
		renderedH := strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("Mail account view at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}
	}
}

func TestModel_MailAccountUpdated_EnabledStatus(t *testing.T) {
	fakeRules := &fakeRulesManager{listResult: []ports.RoutingRule{}}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})

	updated, cmd := m.Update(cfRuleUpdatedMsg{rule: ports.RoutingRule{ID: "r1", Enabled: true}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "mail account enabled" {
		t.Fatalf("expected toast 'mail account enabled', got %+v", m.toast)
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after toggle")
	}
}

// ── fakeLogBuffer ────────────────────────────────────────────────────────────

type fakeLogBuffer struct {
	lines []string
	seq   uint64
}

func (f *fakeLogBuffer) Lines() []string {
	out := make([]string, len(f.lines))
	copy(out, f.lines)
	return out
}

func (f *fakeLogBuffer) Seq() uint64 {
	return f.seq
}

// ── Logs panel tests ─────────────────────────────────────────────────────────

func TestModel_LogTickMsg_UpdatesLinesOnSeqChange(t *testing.T) {
	buf := &fakeLogBuffer{
		lines: []string{`{"ts":"2026-02-19T00:00:00Z","level":"info","event":"app.start","msg":"started"}`},
		seq:   1,
	}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})

	updated, cmd := m.Update(logTickMsg{})
	m = updated.(Model)

	if len(m.logLines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(m.logLines))
	}
	if m.logSeq != 1 {
		t.Fatalf("expected logSeq=1, got %d", m.logSeq)
	}
	if cmd == nil {
		t.Fatalf("expected tick command to be re-scheduled")
	}
}

func TestModel_LogTickMsg_SkipsWhenSeqUnchanged(t *testing.T) {
	buf := &fakeLogBuffer{
		lines: []string{`{"ts":"2026-02-19T00:00:00Z","level":"info","event":"app.start","msg":"started"}`},
		seq:   5,
	}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})
	m.logSeq = 5 // already up to date
	m.logLines = []string{"old line"}

	updated, cmd := m.Update(logTickMsg{})
	m = updated.(Model)

	// logLines should NOT be updated since seq didn't change
	if len(m.logLines) != 1 || m.logLines[0] != "old line" {
		t.Fatalf("expected logLines unchanged when seq matches, got %v", m.logLines)
	}
	if cmd == nil {
		t.Fatalf("expected tick command to be re-scheduled even when no change")
	}
}

func TestModel_LogTickMsg_NilBufferSafe(t *testing.T) {
	m := NewModel() // no log buffer

	updated, cmd := m.Update(logTickMsg{})
	m = updated.(Model)

	if len(m.logLines) != 0 {
		t.Fatalf("expected no log lines with nil buffer")
	}
	if cmd != nil {
		t.Fatalf("expected nil command with nil buffer")
	}
}

func TestModel_LogsPanel_ScrollUpDown(t *testing.T) {
	buf := &fakeLogBuffer{seq: 1}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})
	m.ActivePanel = PanelLogs
	m.logLines = make([]string, 20)
	for i := range m.logLines {
		m.logLines[i] = fmt.Sprintf("line %d", i)
	}
	m.logAutoScroll = true

	// Scroll up
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	if m.logScroll != 1 {
		t.Fatalf("expected logScroll=1 after k, got %d", m.logScroll)
	}
	if m.logAutoScroll {
		t.Fatalf("expected logAutoScroll=false after scrolling up")
	}

	// Scroll up again with arrow key
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.logScroll != 2 {
		t.Fatalf("expected logScroll=2 after up, got %d", m.logScroll)
	}

	// Scroll down
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.logScroll != 1 {
		t.Fatalf("expected logScroll=1 after j, got %d", m.logScroll)
	}

	// Scroll down again with arrow key
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.logScroll != 0 {
		t.Fatalf("expected logScroll=0 after down, got %d", m.logScroll)
	}
	if !m.logAutoScroll {
		t.Fatalf("expected logAutoScroll=true when scrolled to bottom")
	}
}

func TestModel_LogsPanel_GJumpToBottom(t *testing.T) {
	buf := &fakeLogBuffer{seq: 1}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})
	m.ActivePanel = PanelLogs
	m.logLines = make([]string, 20)
	m.logScroll = 10
	m.logAutoScroll = false

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(Model)

	if m.logScroll != 0 {
		t.Fatalf("expected logScroll=0 after G, got %d", m.logScroll)
	}
	if !m.logAutoScroll {
		t.Fatalf("expected logAutoScroll=true after G")
	}
}

func TestModel_LogsPanel_ScrollClampAtMax(t *testing.T) {
	buf := &fakeLogBuffer{seq: 1}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})
	m.ActivePanel = PanelLogs
	m.logLines = []string{"line1", "line2", "line3"}

	// maxScroll = len(logLines) - 1 = 2; first line always visible
	m.logScroll = 2

	// Try to scroll up past max
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.logScroll != 2 {
		t.Fatalf("expected logScroll clamped at 2, got %d", m.logScroll)
	}

	// Try to scroll down below 0
	m.logScroll = 0
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.logScroll != 0 {
		t.Fatalf("expected logScroll clamped at 0, got %d", m.logScroll)
	}
}

func TestModel_Init_StartsLogTick(t *testing.T) {
	buf := &fakeLogBuffer{}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("expected init command when log buffer configured")
	}
}

func TestModel_LogsPanel_ViewRendersLines(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.logLines = []string{
		`{"ts":"2026-02-19T10:30:00Z","level":"info","event":"app.start","msg":"application started"}`,
		`{"ts":"2026-02-19T10:30:01Z","level":"warn","event":"db.slow","msg":"slow query detected"}`,
		`{"ts":"2026-02-19T10:30:02Z","level":"error","event":"net.fail","msg":"connection refused"}`,
	}
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "INF") {
		t.Fatalf("expected INF level in logs view")
	}
	if !contains(v, "WRN") {
		t.Fatalf("expected WRN level in logs view")
	}
	if !contains(v, "ERR") {
		t.Fatalf("expected ERR level in logs view")
	}
	if !contains(v, "app.start") {
		t.Fatalf("expected app.start event in logs view")
	}
}

func TestModel_LogsPanel_ViewShowsEmptyState(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.logLines = nil
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "waiting for log output") {
		t.Fatalf("expected empty state message in logs view")
	}
}

func TestModel_FormatLogLine_ParsesJSONL(t *testing.T) {
	th := newTheme()

	// Valid JSONL info line
	line := `{"ts":"2026-02-19T10:30:00Z","level":"info","event":"app.start","msg":"started ok"}`
	result := formatLogLine(line, th, 100)
	if !contains(result, "10:30:00") {
		t.Fatalf("expected formatted timestamp in output, got %q", result)
	}
	if !contains(result, "INF") {
		t.Fatalf("expected INF level in output, got %q", result)
	}
	if !contains(result, "app.start") {
		t.Fatalf("expected event in output, got %q", result)
	}
	if !contains(result, "started ok") {
		t.Fatalf("expected message in output, got %q", result)
	}

	// Error level
	errLine := `{"ts":"2026-02-19T10:30:00Z","level":"error","event":"db.fail","msg":"timeout"}`
	errResult := formatLogLine(errLine, th, 100)
	if !contains(errResult, "ERR") {
		t.Fatalf("expected ERR level, got %q", errResult)
	}

	// Warn level
	warnLine := `{"ts":"2026-02-19T10:30:00Z","level":"warn","event":"cache.miss","msg":"stale"}`
	warnResult := formatLogLine(warnLine, th, 100)
	if !contains(warnResult, "WRN") {
		t.Fatalf("expected WRN level, got %q", warnResult)
	}

	// Invalid JSON fallback
	rawLine := "not json at all"
	rawResult := formatLogLine(rawLine, th, 100)
	if !contains(rawResult, "not json at all") {
		t.Fatalf("expected raw line in output for invalid JSON, got %q", rawResult)
	}

	// Zero width
	zeroResult := formatLogLine(line, th, 0)
	if zeroResult != "" {
		t.Fatalf("expected empty string for zero maxW, got %q", zeroResult)
	}
}

func TestModel_LKey_SwitchesToLogsPanel(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelStatus

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	m = updated.(Model)

	if m.ActivePanel != PanelLogs {
		t.Fatalf("expected PanelLogs after l key, got %v", m.ActivePanel)
	}
	if cmd != nil {
		t.Fatalf("expected nil command for l key")
	}
}

func TestModel_LogsPanel_CtrlCQuits(t *testing.T) {
	m := NewModel()
	m.ActivePanel = PanelLogs

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatalf("expected quit command for ctrl+c in logs panel")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("expected tea.QuitMsg from ctrl+c in logs panel")
	}
}

func TestModel_View_FitsWithinTerminalHeight_WithLogLines(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40},
		{80, 24},
		{200, 60},
		{40, 12},
	}

	makeLogLines := func() []string {
		lines := make([]string, 10)
		for i := range lines {
			lines[i] = fmt.Sprintf(`{"ts":"2026-02-19T10:%02d:00Z","level":"info","event":"test.evt%d","msg":"log entry %d"}`, i, i, i)
		}
		return lines
	}

	for _, sz := range sizes {
		m := NewModel()
		m.Width = sz.w
		m.Height = sz.h
		m.logLines = makeLogLines()

		output := m.View()
		renderedH := strings.Count(output, "\n") + 1
		if renderedH > sz.h {
			t.Errorf("View (with log lines) at %dx%d rendered %d lines, exceeds %d",
				sz.w, sz.h, renderedH, sz.h)
		}
	}
}

func TestModel_View_NarrowWidth_UsesStackedResponsiveLayout(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 90
	m.Height = 24
	m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}
	m.cfRules = []ports.RoutingRule{{ID: "r1", AliasEmail: "a@example.com", Enabled: true}}

	v := m.View()
	if !contains(v, "Latest OTP") {
		t.Fatalf("expected latest otp card in narrow layout")
	}
	if !contains(v, "System Health") {
		t.Fatalf("expected health card in narrow layout")
	}
	if !contains(v, "Mail Account") {
		t.Fatalf("expected mail account card in narrow layout")
	}
	renderedH := strings.Count(v, "\n") + 1
	if renderedH > m.Height {
		t.Fatalf("expected rendered height <= terminal height, got %d > %d", renderedH, m.Height)
	}
}

func TestModel_View_TinyTerminal_FallsBackMinimalView(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{})
	m.Width = 30
	m.Height = 8
	m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}

	v := m.View()
	if !contains(v, "Resize terminal") {
		t.Fatalf("expected minimal fallback hint in tiny terminal")
	}
	if !contains(v, "OTP events:") {
		t.Fatalf("expected minimal otp summary")
	}
	renderedH := strings.Count(v, "\n") + 1
	if renderedH > m.Height {
		t.Fatalf("expected rendered height <= terminal height, got %d > %d", renderedH, m.Height)
	}
}

func TestModel_View_FitsWithinTerminalWidthAndHeight(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40},
		{100, 28},
		{90, 24},
		{80, 20},
		{64, 16},
	}

	for _, sz := range sizes {
		m := NewModelWithConfig(ModelConfig{})
		m.Width = sz.w
		m.Height = sz.h
		m.ShowHelp = true
		m.toast = &Toast{Level: ToastInfo, Message: "hello", ShownAt: time.Now().UTC()}
		m.otpEvents = []domain.OTPEvent{{Platform: "SHOP", OTPCode: "123456", AliasEmail: "a@example.com", ReceivedAt: time.Now().UTC()}}
		m.cfRules = []ports.RoutingRule{{ID: "r1", AliasEmail: "a@example.com", Enabled: true}}

		output := m.View()
		lines := strings.Split(output, "\n")
		if len(lines) != sz.h {
			t.Fatalf("expected exact %d lines, got %d", sz.h, len(lines))
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w > sz.w {
				t.Fatalf("line %d width overflow: got %d > %d", i, w, sz.w)
			}
		}
	}
}

func TestModel_LogTickMsg_AutoScrollResetsScrollToZero(t *testing.T) {
	buf := &fakeLogBuffer{
		lines: []string{"line1", "line2", "line3"},
		seq:   3,
	}
	m := NewModelWithConfig(ModelConfig{LogBuffer: buf})
	m.logAutoScroll = true
	m.logScroll = 2 // scrolled up, but auto-scroll is on

	updated, _ := m.Update(logTickMsg{})
	m = updated.(Model)

	if m.logScroll != 0 {
		t.Fatalf("expected logScroll reset to 0 with auto-scroll, got %d", m.logScroll)
	}
}

func TestModel_LogsPanel_HelpShowsLogKeys(t *testing.T) {
	m := NewModel()
	m.ShowHelp = true
	m.Width = 120
	m.Height = 60

	v := m.View()
	if !contains(v, "jump to Logs panel") {
		t.Fatalf("expected 'l' key help entry for logs panel")
	}
	if !contains(v, "jump to bottom") {
		t.Fatalf("expected 'G' key help entry for jump to bottom")
	}
}

func TestModel_LogsPanel_FooterShowsLogKey(t *testing.T) {
	m := NewModel()
	m.Width = 120
	m.Height = 40

	v := m.View()
	if !contains(v, "logs") {
		t.Fatalf("expected 'logs' in footer keys")
	}
}

func TestModel_NewModelWithConfig_LogAutoScrollDefault(t *testing.T) {
	m := NewModel()
	if !m.logAutoScroll {
		t.Fatalf("expected logAutoScroll=true by default")
	}
}

func TestModel_Init_LoadsSettingsWhenManagerConfigured(t *testing.T) {
	fakeSettings := &fakeSettingsManager{loadState: SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto"}}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})

	cmd := m.Init()
	if cmd == nil {
		t.Fatalf("expected init command with settings manager")
	}
	msg := cmd()
	loaded, ok := msg.(settingsLoadedMsg)
	if !ok {
		t.Fatalf("expected settingsLoadedMsg, got %T", msg)
	}
	if loaded.err != nil {
		t.Fatalf("unexpected settings load error: %v", loaded.err)
	}

	updated, _ := m.Update(loaded)
	m = updated.(Model)
	if !m.settingsLoaded {
		t.Fatalf("expected settingsLoaded=true")
	}
	if m.settingsForm.ClipboardMethod != "auto" {
		t.Fatalf("expected clipboard method auto, got %q", m.settingsForm.ClipboardMethod)
	}
	if fakeSettings.loadCalls != 1 {
		t.Fatalf("expected one settings load call, got %d", fakeSettings.loadCalls)
	}
}

func TestModel_SettingsPanel_ToggleAndSaveApply(t *testing.T) {
	clip := &fakeClipboard{}
	fakeSettings := &fakeSettingsManager{
		saveState:     SettingsState{ClipboardEnabled: false, ClipboardMethod: "xclip", Timezone: "Asia/Jakarta"},
		saveClipboard: clip,
	}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", Timezone: "UTC"}
	m.settingsOriginal = m.settingsForm

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	if m.settingsForm.ClipboardEnabled {
		t.Fatalf("expected clipboard enabled toggled to false")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected save command")
	}
	if !m.settingsSaving {
		t.Fatalf("expected settingsSaving=true while saving")
	}

	msg := runBatchExtract(cmd)
	saved, ok := msg.(settingsSavedMsg)
	if !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}
	if fakeSettings.saveCalls != 1 {
		t.Fatalf("expected one save call, got %d", fakeSettings.saveCalls)
	}
	if fakeSettings.lastSavedState.ClipboardEnabled {
		t.Fatalf("expected saved state clipboard enabled=false")
	}

	updated, _ = m.Update(saved)
	m = updated.(Model)
	if m.settingsSaving {
		t.Fatalf("expected settingsSaving=false after save result")
	}
	if m.settingsForm.ClipboardMethod != "xclip" {
		t.Fatalf("expected method xclip after apply, got %q", m.settingsForm.ClipboardMethod)
	}
	if m.settingsForm.Timezone != "Asia/Jakarta" {
		t.Fatalf("expected timezone Asia/Jakarta after apply, got %q", m.settingsForm.Timezone)
	}
	if m.clipboard != clip {
		t.Fatalf("expected clipboard adapter updated from settings apply")
	}
}

func TestModel_SettingsPanel_EditDomainsAndActiveDomainThenSave(t *testing.T) {
	fakeSettings := &fakeSettingsManager{}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", DomainsText: "example.com,z1", ActiveDomain: "example.com"}
	m.settingsOriginal = m.settingsForm

	// edit domains (row 2)
	m.settingsSelected = 2
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	for _, r := range []rune("\nexample.net,z2") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	// edit active_domain (row 3)
	m.settingsSelected = 3
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	for i := 0; i < len("example.com"); i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(Model)
	}
	for _, r := range []rune("example.net") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	if _, ok := msg.(settingsSavedMsg); !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}
	if !strings.Contains(fakeSettings.lastSavedState.DomainsText, "example.net,z2") {
		t.Fatalf("expected domains text saved, got %q", fakeSettings.lastSavedState.DomainsText)
	}
	if fakeSettings.lastSavedState.ActiveDomain != "example.net" {
		t.Fatalf("expected active domain saved, got %q", fakeSettings.lastSavedState.ActiveDomain)
	}
}

func TestModel_SettingsPanel_EditMethodThenSave(t *testing.T) {
	fakeSettings := &fakeSettingsManager{}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto"}
	m.settingsOriginal = m.settingsForm
	m.settingsSelected = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	if !m.settingsEditing {
		t.Fatalf("expected editing mode for method")
	}

	for i := 0; i < 4; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(Model)
	}
	for _, r := range []rune("xsel") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.settingsEditing {
		t.Fatalf("expected editing mode closed by enter")
	}
	if m.settingsForm.ClipboardMethod != "xsel" {
		t.Fatalf("expected method xsel after editing, got %q", m.settingsForm.ClipboardMethod)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	if _, ok := msg.(settingsSavedMsg); !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}
	if fakeSettings.lastSavedState.ClipboardMethod != "xsel" {
		t.Fatalf("expected save state method xsel, got %q", fakeSettings.lastSavedState.ClipboardMethod)
	}
}

func TestModel_SettingsPanel_ResetRestoresOriginal(t *testing.T) {
	m := NewModelWithConfig(ModelConfig{SettingsMgr: &fakeSettingsManager{}})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsOriginal = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", Timezone: "UTC"}
	m.settingsForm = SettingsState{ClipboardEnabled: false, ClipboardMethod: "xclip", Timezone: "Asia/Jakarta"}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if !m.settingsForm.ClipboardEnabled || m.settingsForm.ClipboardMethod != "auto" || m.settingsForm.Timezone != "UTC" {
		t.Fatalf("expected reset to original settings, got %+v", m.settingsForm)
	}
	if m.toast == nil || m.toast.Message != "settings reset" {
		t.Fatalf("expected settings reset toast, got %+v", m.toast)
	}
}

func TestModel_SettingsPanel_EditTimezoneThenSave(t *testing.T) {
	fakeSettings := &fakeSettingsManager{}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", Timezone: "UTC", LogPath: "", IMAPPollInterval: "5s"}
	m.settingsOriginal = m.settingsForm
	m.settingsSelected = 4

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	if !m.settingsEditing {
		t.Fatalf("expected timezone editing mode")
	}

	for i := 0; i < 3; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = updated.(Model)
	}
	for _, r := range []rune("Asia/Jakarta") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.settingsEditing {
		t.Fatalf("expected editing closed after enter")
	}
	if m.settingsForm.Timezone != "Asia/Jakarta" {
		t.Fatalf("expected timezone updated, got %q", m.settingsForm.Timezone)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	if _, ok := msg.(settingsSavedMsg); !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}
	if fakeSettings.lastSavedState.Timezone != "Asia/Jakarta" {
		t.Fatalf("expected saved timezone Asia/Jakarta, got %q", fakeSettings.lastSavedState.Timezone)
	}
}

func TestModel_SettingsPanel_EditLogPathAndPollIntervalThenSave(t *testing.T) {
	fakeSettings := &fakeSettingsManager{}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", Timezone: "UTC", LogPath: "", IMAPPollInterval: "5s"}
	m.settingsOriginal = m.settingsForm

	// Edit log_path (row 5)
	m.settingsSelected = 5
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	for _, r := range []rune("/tmp/tuiotp.log") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	// Edit poll interval (row 6)
	m.settingsSelected = 6
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(Model)
	for _, r := range []rune("10s") {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	if _, ok := msg.(settingsSavedMsg); !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}
	if fakeSettings.lastSavedState.LogPath != "/tmp/tuiotp.log" {
		t.Fatalf("expected saved log path, got %q", fakeSettings.lastSavedState.LogPath)
	}
	if fakeSettings.lastSavedState.IMAPPollInterval != "10s" {
		t.Fatalf("expected saved poll interval 10s, got %q", fakeSettings.lastSavedState.IMAPPollInterval)
	}
}

func TestModel_SettingsSaved_AppliesActiveDomainToRulesManager(t *testing.T) {
	fakeSettings := &fakeSettingsManager{
		saveState: SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"},
	}
	fakeRules := &fakeRulesManager{domains: []string{"example.com", "example.net"}, activeDomain: "example.com"}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings, RulesManager: fakeRules})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"}
	m.settingsOriginal = m.settingsForm

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected save command")
	}
	msg := runBatchExtract(cmd)
	if _, ok := msg.(settingsSavedMsg); !ok {
		t.Fatalf("expected settingsSavedMsg, got %T", msg)
	}

	updated, cmd := m.Update(msg)
	m = updated.(Model)
	if fakeRules.activeDomain != "example.net" {
		t.Fatalf("expected active domain updated to example.net, got %q", fakeRules.activeDomain)
	}
	if fakeRules.setActiveCalls == 0 {
		t.Fatalf("expected SetActiveDomain to be called")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after settings saved")
	}
}

func TestModel_SettingsSaved_ActiveDomainApplyFailureShowsWarning(t *testing.T) {
	fakeSettings := &fakeSettingsManager{
		saveState: SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"},
	}
	fakeRules := &fakeRulesManager{
		domains:      []string{"example.com", "example.net"},
		activeDomain: "example.com",
		setActiveErr: errors.New("switch failed"),
	}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings, RulesManager: fakeRules})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"}
	m.settingsOriginal = m.settingsForm

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if m.toast == nil || m.toast.Level != ToastWarning {
		t.Fatalf("expected warning toast when active domain apply fails, got %+v", m.toast)
	}
	if !contains(m.toast.Message, "active domain apply") {
		t.Fatalf("expected warning toast message about active domain apply, got %+v", m.toast)
	}
}

func TestModel_SettingsSaved_DomainListChanged_ShowsRestartWarning(t *testing.T) {
	fakeSettings := &fakeSettingsManager{
		saveState: SettingsState{
			ClipboardEnabled: true,
			ClipboardMethod:  "auto",
			DomainsText:      "example.com,z1\nexample.net,z2",
			ActiveDomain:     "example.net",
		},
	}
	fakeRules := &fakeRulesManager{domains: []string{"example.com"}, activeDomain: "example.com"}
	m := NewModelWithConfig(ModelConfig{SettingsMgr: fakeSettings, RulesManager: fakeRules})
	m.ActivePanel = PanelSettings
	m.settingsLoaded = true
	m.settingsForm = SettingsState{
		ClipboardEnabled: true,
		ClipboardMethod:  "auto",
		DomainsText:      "example.com,z1",
		ActiveDomain:     "example.com",
	}
	m.settingsOriginal = m.settingsForm

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	msg := runBatchExtract(cmd)
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if m.toast == nil || m.toast.Level != ToastWarning {
		t.Fatalf("expected warning toast for changed domain list, got %+v", m.toast)
	}
	if !contains(m.toast.Message, "restart app") {
		t.Fatalf("expected restart warning toast, got %+v", m.toast)
	}
	if fakeRules.setActiveCalls != 0 {
		t.Fatalf("expected no SetActiveDomain call when domains changed, got %d", fakeRules.setActiveCalls)
	}
}

func TestModel_SettingsLoaded_AppliesActiveDomainAndRefreshesRules(t *testing.T) {
	fakeRules := &fakeRulesManager{domains: []string{"example.com", "example.net"}, activeDomain: "example.com"}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})

	updated, cmd := m.Update(settingsLoadedMsg{state: SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"}})
	m = updated.(Model)
	if fakeRules.activeDomain != "example.net" {
		t.Fatalf("expected active domain updated on settings load, got %q", fakeRules.activeDomain)
	}
	if fakeRules.setActiveCalls == 0 {
		t.Fatalf("expected SetActiveDomain called on settings load")
	}
	if cmd == nil {
		t.Fatalf("expected refresh command after applying active domain on load")
	}
}

func TestModel_SettingsLoaded_ActiveDomainApplyFailureShowsWarning(t *testing.T) {
	fakeRules := &fakeRulesManager{
		domains:      []string{"example.com", "example.net"},
		activeDomain: "example.com",
		setActiveErr: errors.New("switch failed"),
	}
	m := NewModelWithConfig(ModelConfig{RulesManager: fakeRules})

	updated, _ := m.Update(settingsLoadedMsg{state: SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto", ActiveDomain: "example.net"}})
	m = updated.(Model)
	if m.toast == nil || m.toast.Level != ToastWarning {
		t.Fatalf("expected warning toast on settings load active domain apply failure, got %+v", m.toast)
	}
	if !contains(m.toast.Message, "active domain apply") {
		t.Fatalf("expected active domain apply warning message, got %+v", m.toast)
	}
}

// ── Toast system tests ──────────────────────────────────────────────────────

func TestToastDuration_LevelMapping(t *testing.T) {
	if d := toastDuration(ToastSuccess); d != 3*time.Second {
		t.Fatalf("expected 3s for success, got %v", d)
	}
	if d := toastDuration(ToastInfo); d != 3*time.Second {
		t.Fatalf("expected 3s for info, got %v", d)
	}
	if d := toastDuration(ToastWarning); d != 4*time.Second {
		t.Fatalf("expected 4s for warning, got %v", d)
	}
	if d := toastDuration(ToastError); d != 5*time.Second {
		t.Fatalf("expected 5s for error, got %v", d)
	}
}

func TestShowToast_SetsToastAndReturnsCmd(t *testing.T) {
	m := NewModel()
	cmd := showToast(&m, ToastSuccess, "hello")
	if m.toast == nil {
		t.Fatalf("expected toast to be set")
	}
	if m.toast.Message != "hello" {
		t.Fatalf("expected message 'hello', got %q", m.toast.Message)
	}
	if m.toast.Level != ToastSuccess {
		t.Fatalf("expected ToastSuccess level")
	}
	if m.toast.ShownAt.IsZero() {
		t.Fatalf("expected non-zero ShownAt")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil dismiss cmd")
	}
}

func TestToastDismissMsg_ClearsMatchingToast(t *testing.T) {
	m := NewModel()
	showToast(&m, ToastInfo, "test")
	shownAt := m.toast.ShownAt

	updated, _ := m.Update(toastDismissMsg{shownAt: shownAt})
	m = updated.(Model)
	if m.toast != nil {
		t.Fatalf("expected toast cleared by matching dismiss, got %+v", m.toast)
	}
}

func TestToastDismissMsg_IgnoresStaleToast(t *testing.T) {
	m := NewModel()
	showToast(&m, ToastInfo, "new toast")

	// Send a dismiss with a different shownAt (stale)
	staleTime := time.Now().Add(-10 * time.Second)
	updated, _ := m.Update(toastDismissMsg{shownAt: staleTime})
	m = updated.(Model)
	if m.toast == nil || m.toast.Message != "new toast" {
		t.Fatalf("expected stale dismiss to be ignored, toast: %+v", m.toast)
	}
}

func TestToastDismissedOnKeyPress(t *testing.T) {
	m := NewModel()
	showToast(&m, ToastSuccess, "will be dismissed")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(Model)
	// Toast should be cleared by key press
	if m.toast != nil {
		t.Fatalf("expected toast cleared on key press, got %+v", m.toast)
	}
	// But the key should still be processed (help toggled)
	if !m.ShowHelp {
		t.Fatalf("expected help toggled despite toast dismissal")
	}
}

func TestRenderToast_Nil_ReturnsEmpty(t *testing.T) {
	m := NewModel()
	m.toast = nil
	th := newTheme()
	result := m.renderToast(th, 80)
	if result != "" {
		t.Fatalf("expected empty string for nil toast, got %q", result)
	}
}

func TestRenderToast_LevelColors(t *testing.T) {
	th := newTheme()
	levels := []struct {
		level ToastLevel
		icon  string
	}{
		{ToastSuccess, "✔"},
		{ToastError, "✖"},
		{ToastWarning, "⚠"},
		{ToastInfo, "ℹ"},
	}
	for _, tc := range levels {
		m := NewModel()
		m.Width = 80
		showToast(&m, tc.level, "test message")
		result := m.renderToast(th, 80)
		if result == "" {
			t.Fatalf("expected non-empty toast for level %d", tc.level)
		}
		if !contains(result, tc.icon) {
			t.Fatalf("expected icon %q in toast for level %d, got %q", tc.icon, tc.level, result)
		}
		if !contains(result, "test message") {
			t.Fatalf("expected 'test message' in toast output for level %d", tc.level)
		}
	}
}

func TestToastTickMsg_ReschedulesWhenToastActive(t *testing.T) {
	m := NewModel()
	showToast(&m, ToastInfo, "test")

	updated, cmd := m.Update(toastTickMsg{})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("expected tick to be rescheduled while toast is active")
	}
	if m.toast == nil || m.toast.Message != "test" {
		t.Fatalf("expected toast unchanged after tick")
	}
}

func TestToastTickMsg_StopsWhenNoToast(t *testing.T) {
	m := NewModel()
	// No toast set

	updated, cmd := m.Update(toastTickMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd when no toast active")
	}
}

func TestToastTickMsg_StopsWhenExpired(t *testing.T) {
	m := NewModel()
	showToast(&m, ToastInfo, "expired")
	// Backdate ShownAt so toast is already expired
	m.toast.ShownAt = time.Now().Add(-10 * time.Second)

	updated, cmd := m.Update(toastTickMsg{})
	m = updated.(Model)
	if cmd != nil {
		t.Fatalf("expected nil cmd when toast is expired")
	}
}

func TestRenderToast_ShowsCountdown(t *testing.T) {
	m := NewModel()
	m.Width = 80
	showToast(&m, ToastInfo, "hello")
	// ShownAt is ~now, so remaining should be ~3s

	th := newTheme()
	result := m.renderToast(th, 80)
	// Should contain countdown like "(3s)" or "(2s)"
	if !contains(result, "s)") {
		t.Fatalf("expected countdown in toast, got %q", result)
	}
}
