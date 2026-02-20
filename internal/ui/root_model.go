package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

const (
	defaultOTPHistoryLimit = 50
	maxOTPQueryLen         = 120
	maxAliasEmailLen       = 320
	maxClipboardMethodLen  = 24
	maxActiveDomainLen     = 253
	maxTimezoneLen         = 64
	maxLogPathLen          = 260
	maxPollIntervalLen     = 24
	maxDomainsTextLen      = 4096
	sidebarWidth           = 42
)

var supportedClipboardMethods = []string{"auto", "wl-copy", "xclip", "xsel", "pbcopy", "clip"}

type Panel int

const (
	PanelStatus Panel = iota
	PanelMailAccount
	PanelSettings
	PanelLatestOTP
	PanelLogs
	panelCount
)

// ToastLevel represents the severity of a toast notification.
type ToastLevel int

const (
	ToastSuccess ToastLevel = iota
	ToastError
	ToastWarning
	ToastInfo
)

type Model struct {
	ActivePanel Panel
	ShowHelp    bool
	Width       int
	Height      int

	// Toast notification (nil = no toast visible)
	toast  *Toast
	health HealthStatus

	otpManager   otpManager
	rulesManager rulesManager
	settingsMgr  settingsManager
	clipboard    clipboardCopier
	opParentCtx  context.Context
	opTimeout    time.Duration
	cfOpTimeout  time.Duration

	// Mail Account (CF routing rules) management
	cfRules         []ports.RoutingRule
	cfSelected      int
	cfDeleteConfirm bool

	creating         bool
	createAliasEmail string

	otpEvents      []domain.OTPEvent
	otpSelected    int
	otpDeleteMode  bool
	otpDeleteScope string
	otpSearchMode  bool
	otpSearchInput string
	otpSearchQuery string
	otpLastReqAt   time.Time

	// Logs panel
	logBuffer     logBufferReader
	logLines      []string
	logSeq        uint64
	logScroll     int  // lines scrolled up from bottom (0 = bottom)
	logAutoScroll bool // true = auto-follow new lines

	// Settings panel
	settingsLoaded       bool
	settingsLoading      bool
	settingsSaving       bool
	settingsSelected     int
	settingsEditing      bool
	settingsMethodInput  string
	settingsDomainsInput string
	settingsActiveInput  string
	settingsTZInput      string
	settingsLogPathInput string
	settingsPollInput    string
	settingsForm         SettingsState
	settingsOriginal     SettingsState
}

type otpManager interface {
	ListOTPEvents(ctx context.Context, filter app.OTPListFilter) ([]domain.OTPEvent, error)
	ClearOTPEventByID(ctx context.Context, id int64) (int64, error)
	ClearOTPEvents(ctx context.Context, filter app.OTPDeleteFilter) (int64, error)
}

type rulesManager interface {
	ListRoutingRules(ctx context.Context) ([]ports.RoutingRule, error)
	CreateRoutingRuleDirect(ctx context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error)
	UpdateRoutingRule(ctx context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error)
	DeleteRoutingRuleByID(ctx context.Context, ruleID string) error
	ListDomains() []string
	ActiveDomain() string
	SetActiveDomain(domain string) error
}

type SettingsState struct {
	ClipboardEnabled bool
	ClipboardMethod  string
	DomainsText      string
	ActiveDomain     string
	Timezone         string
	LogPath          string
	IMAPPollInterval string
}

type settingsManager interface {
	Load(ctx context.Context) (SettingsState, error)
	SaveAndApply(ctx context.Context, state SettingsState) (SettingsState, clipboardCopier, error)
}

type clipboardCopier = ports.Clipboard

// logBufferReader is the interface used by the TUI to poll log lines
// from the ring buffer. Decoupled from concrete *observability.RingBuffer
// for testability.
type logBufferReader interface {
	Lines() []string
	Seq() uint64
}

// Toast represents a transient notification message.
type Toast struct {
	Message string
	Level   ToastLevel
	ShownAt time.Time
}

// toastDuration returns the auto-dismiss duration for a toast level.
func toastDuration(level ToastLevel) time.Duration {
	switch level {
	case ToastError:
		return 5 * time.Second
	case ToastWarning:
		return 4 * time.Second
	default: // ToastSuccess, ToastInfo
		return 3 * time.Second
	}
}

// showToast sets a toast on the model and returns the auto-dismiss command
// along with a 1-second tick for the live countdown display.
func showToast(m *Model, level ToastLevel, message string) tea.Cmd {
	now := time.Now().UTC()
	m.toast = &Toast{
		Message: message,
		Level:   level,
		ShownAt: now,
	}
	shownAt := now
	dur := toastDuration(level)
	dismissCmd := tea.Tick(dur, func(_ time.Time) tea.Msg {
		return toastDismissMsg{shownAt: shownAt}
	})
	tickCmd := tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return toastTickMsg{}
	})
	return tea.Batch(dismissCmd, tickCmd)
}

type ModelConfig struct {
	OTPManager   otpManager
	RulesManager rulesManager
	SettingsMgr  settingsManager
	Clipboard    clipboardCopier
	LogBuffer    logBufferReader
	Health       HealthStatus
	ParentCtx    context.Context
	OpTimeout    time.Duration
	CFOpTimeout  time.Duration // timeout for CF API operations (paginated); defaults to 30s
}

type HealthStatus struct {
	Cloudflare  string
	Destination string
	Mailbox     string
	Parser      string
}

func NewModel() Model {
	return NewModelWithConfig(ModelConfig{})
}

func NewModelWithConfig(cfg ModelConfig) Model {
	if cfg.OpTimeout <= 0 {
		cfg.OpTimeout = 5 * time.Second
	}
	if cfg.CFOpTimeout <= 0 {
		cfg.CFOpTimeout = 30 * time.Second
	}

	return Model{
		ActivePanel:   PanelStatus,
		ShowHelp:      false,
		health:        normalizeHealthStatus(cfg.Health),
		otpManager:    cfg.OTPManager,
		rulesManager:  cfg.RulesManager,
		settingsMgr:   cfg.SettingsMgr,
		clipboard:     cfg.Clipboard,
		opParentCtx:   cfg.ParentCtx,
		opTimeout:     cfg.OpTimeout,
		cfOpTimeout:   cfg.CFOpTimeout,
		logBuffer:     cfg.LogBuffer,
		logAutoScroll: true,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 3)
	if m.otpManager != nil {
		cmds = append(cmds, m.refreshOTPHistoryCmd(m.otpSearchQuery))
	}
	if m.rulesManager != nil {
		cmds = append(cmds, m.refreshCFRulesCmd())
	}
	if m.logBuffer != nil {
		cmds = append(cmds, m.logTickCmd())
	}
	if m.settingsMgr != nil {
		m.settingsLoading = true
		cmds = append(cmds, m.loadSettingsCmd())
	}

	if len(cmds) == 0 {
		return nil
	}
	if len(cmds) == 1 {
		return cmds[0]
	}

	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case toastTickMsg:
		if m.toast == nil {
			return m, nil
		}
		remaining := toastDuration(m.toast.Level) - time.Since(m.toast.ShownAt)
		if remaining <= 0 {
			return m, nil
		}
		return m, tea.Tick(time.Second, func(_ time.Time) tea.Msg {
			return toastTickMsg{}
		})

	case toastDismissMsg:
		// Only dismiss if this msg matches the current toast (stale guard)
		if m.toast != nil && m.toast.ShownAt.Equal(msg.shownAt) {
			m.toast = nil
		}
		return m, nil

	case otpHistoryLoadedMsg:
		if !m.otpLastReqAt.IsZero() && msg.reqAt.Before(m.otpLastReqAt) {
			return m, nil
		}

		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("refresh otp history", msg.err))
			return m, toastCmd
		}

		m.otpEvents = msg.events
		if len(m.otpEvents) == 0 {
			m.otpSelected = 0
		} else if m.otpSelected >= len(m.otpEvents) {
			m.otpSelected = len(m.otpEvents) - 1
		}
		m.otpSearchQuery = msg.query
		toastCmd := showToast(&m, ToastInfo, fmt.Sprintf("otp refreshed (%d)", len(m.otpEvents)))
		return m, toastCmd

	case otpCopiedMsg:
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("copy otp", msg.err))
			return m, toastCmd
		}
		toastCmd := showToast(&m, ToastSuccess, "otp copied")
		return m, toastCmd

	case otpDeletedMsg:
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("clear otp", msg.err))
			return m, toastCmd
		}
		reqAt := time.Now().UTC()
		m.otpLastReqAt = reqAt
		label := "otp cleared"
		switch msg.scope {
		case "selected":
			label = "selected otp cleared"
		case "filtered":
			label = "filtered otp cleared"
		case "all":
			label = "all otp cleared"
		}
		toastCmd := showToast(&m, ToastSuccess, fmt.Sprintf("%s (%d)", label, msg.rows))
		return m, tea.Batch(toastCmd, m.refreshOTPHistoryCmdAt(m.otpSearchQuery, reqAt))

	case cfRulesLoadedMsg:
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("refresh mail accounts", msg.err))
			return m, toastCmd
		}
		m.cfRules = msg.rules
		if len(m.cfRules) == 0 {
			m.cfSelected = 0
		} else if m.cfSelected >= len(m.cfRules) {
			m.cfSelected = len(m.cfRules) - 1
		}
		toastCmd := showToast(&m, ToastInfo, fmt.Sprintf("mail accounts refreshed (%d)", len(m.cfRules)))
		return m, toastCmd

	case cfRuleCreatedMsg:
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("create mail account", msg.err))
			return m, toastCmd
		}

		m.creating = false
		m.createAliasEmail = ""
		toastCmd := showToast(&m, ToastSuccess, "mail account created")
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd())

	case cfRuleUpdatedMsg:
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("toggle mail account", msg.err))
			return m, toastCmd
		}
		status := "disabled"
		if msg.rule.Enabled {
			status = "enabled"
		}
		toastCmd := showToast(&m, ToastSuccess, fmt.Sprintf("mail account %s", status))
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd())

	case cfRuleDeletedMsg:
		if msg.err != nil {
			m.cfDeleteConfirm = false
			toastCmd := showToast(&m, ToastError, userSafeError("delete mail account", msg.err))
			return m, toastCmd
		}
		m.cfDeleteConfirm = false
		toastCmd := showToast(&m, ToastSuccess, "mail account deleted")
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd())

	case settingsLoadedMsg:
		m.settingsLoading = false
		if msg.err != nil {
			m.settingsLoaded = false
			toastCmd := showToast(&m, ToastError, userSafeError("load settings", msg.err))
			return m, toastCmd
		}
		m.settingsLoaded = true
		m.settingsForm = normalizeSettingsState(msg.state)
		m.settingsOriginal = m.settingsForm
		m.settingsMethodInput = m.settingsForm.ClipboardMethod
		m.settingsDomainsInput = m.settingsForm.DomainsText
		m.settingsActiveInput = m.settingsForm.ActiveDomain
		m.settingsTZInput = m.settingsForm.Timezone
		m.settingsLogPathInput = m.settingsForm.LogPath
		m.settingsPollInput = m.settingsForm.IMAPPollInterval
		m.settingsEditing = false
		if m.rulesManager != nil {
			if active := strings.TrimSpace(m.settingsForm.ActiveDomain); active != "" {
				if err := m.rulesManager.SetActiveDomain(active); err != nil {
					toastCmd := showToast(&m, ToastWarning, "settings loaded; active domain apply requires refresh/restart")
					return m, toastCmd
				}
				return m, m.refreshCFRulesCmd()
			}
		}
		return m, nil

	case settingsSavedMsg:
		m.settingsSaving = false
		if msg.err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("save settings", msg.err))
			return m, toastCmd
		}
		prevDomains := canonicalDomainsText(m.settingsOriginal.DomainsText)
		nextDomains := canonicalDomainsText(msg.state.DomainsText)
		domainsChanged := prevDomains != nextDomains
		m.settingsLoaded = true
		m.settingsForm = normalizeSettingsState(msg.state)
		m.settingsOriginal = m.settingsForm
		m.settingsMethodInput = m.settingsForm.ClipboardMethod
		m.settingsDomainsInput = m.settingsForm.DomainsText
		m.settingsActiveInput = m.settingsForm.ActiveDomain
		m.settingsTZInput = m.settingsForm.Timezone
		m.settingsLogPathInput = m.settingsForm.LogPath
		m.settingsPollInput = m.settingsForm.IMAPPollInterval
		m.settingsEditing = false
		m.clipboard = msg.clipboard
		if domainsChanged {
			toastCmd := showToast(&m, ToastWarning, "settings saved; domain list changed, restart app to apply new domains")
			if m.rulesManager != nil {
				return m, tea.Batch(toastCmd, m.refreshCFRulesCmd())
			}
			return m, toastCmd
		}
		if m.rulesManager != nil {
			if active := strings.TrimSpace(m.settingsForm.ActiveDomain); active != "" {
				if err := m.rulesManager.SetActiveDomain(active); err != nil {
					toastCmd := showToast(&m, ToastWarning, "settings saved; active domain apply requires restart")
					return m, toastCmd
				}
			}
		}
		toastCmd := showToast(&m, ToastSuccess, "settings saved and applied")
		if m.rulesManager != nil {
			return m, tea.Batch(toastCmd, m.refreshCFRulesCmd())
		}
		return m, toastCmd

	case app.RuntimeEvent:
		switch msg.Type {
		case app.RuntimeEventWatcherUpdate:
			mode := "watching"
			if msg.Watch != nil && strings.TrimSpace(msg.Watch.Mode) != "" {
				mode = "watching(" + sanitizeMailboxMode(msg.Watch.Mode) + ")"
			}
			m.health.Mailbox = mode
			// No toast — high-frequency background event
			return m, nil
		case app.RuntimeEventRuntimeError:
			m.health.Mailbox = "error"
			errMsg := "mailbox runtime error"
			if strings.TrimSpace(msg.Err) != "" {
				errMsg = userSafeError("mailbox runtime", errors.New(msg.Err))
			}
			toastCmd := showToast(&m, ToastError, errMsg)
			return m, toastCmd
		case app.RuntimeEventOTPProcessed:
			if msg.OTPStatus == app.OTPPipelineStatusStored {
				reqAt := time.Now().UTC()
				m.otpLastReqAt = reqAt
				return m, m.refreshOTPHistoryCmdAt(m.otpSearchQuery, reqAt)
			}
			return m, nil
		}

	case logTickMsg:
		if m.logBuffer == nil {
			return m, nil
		}
		newSeq := m.logBuffer.Seq()
		if newSeq != m.logSeq {
			m.logSeq = newSeq
			m.logLines = m.logBuffer.Lines()
			if m.logAutoScroll {
				m.logScroll = 0
			}
		}
		return m, m.logTickCmd()

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Dismiss toast on any key press (but don't consume the key —
		// let it be processed normally by the panel handlers below).
		if m.toast != nil {
			m.toast = nil
		}

		if m.ActivePanel == PanelLogs {
			updated, cmd, handled := m.updateLogsPanel(msg)
			if handled {
				return updated, cmd
			}
		}
		if m.ActivePanel == PanelMailAccount {
			updated, cmd, handled := m.updateMailAccountPanel(msg)
			if handled {
				return updated, cmd
			}
		}
		if m.ActivePanel == PanelSettings {
			updated, cmd, handled := m.updateSettingsPanel(msg)
			if handled {
				return updated, cmd
			}
		}
		if m.ActivePanel == PanelLatestOTP {
			updated, cmd, handled := m.updateOTPPanel(msg)
			if handled {
				return updated, cmd
			}
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "?":
			m.ShowHelp = !m.ShowHelp
			return m, nil
		case "r":
			toastCmd := showToast(&m, ToastInfo, "refreshing...")
			updated, refreshCmd := m.nextRefreshAllCmd()
			return updated, tea.Batch(toastCmd, refreshCmd)
		case "tab":
			m.ActivePanel = (m.ActivePanel + 1) % panelCount
			return m, nil
		case "o":
			m.ActivePanel = PanelLatestOTP
			return m, nil
		case "l":
			m.ActivePanel = PanelLogs
			return m, nil
		case "s":
			m.ActivePanel = PanelSettings
			if m.settingsMgr != nil && !m.settingsLoaded && !m.settingsLoading {
				m.settingsLoading = true
				return m, m.loadSettingsCmd()
			}
			return m, nil
		}
	}

	return m, nil
}

// theme holds all colors and styles used across the TUI.
type theme struct {
	// palette
	bg      lipgloss.Color
	bgAlt   lipgloss.Color
	fg      lipgloss.Color
	muted   lipgloss.Color
	accent  lipgloss.Color
	success lipgloss.Color
	warning lipgloss.Color
	danger  lipgloss.Color
	purple  lipgloss.Color

	// derived styles
	base         lipgloss.Style
	bold         lipgloss.Style
	mutedStyle   lipgloss.Style
	accentStyle  lipgloss.Style
	successStyle lipgloss.Style
	warnStyle    lipgloss.Style
	errStyle     lipgloss.Style
	purpleStyle  lipgloss.Style
}

func newTheme() theme {
	t := theme{
		bg:      lipgloss.Color("234"),
		bgAlt:   lipgloss.Color("236"),
		fg:      lipgloss.Color("253"),
		muted:   lipgloss.Color("242"),
		accent:  lipgloss.Color("39"),
		success: lipgloss.Color("78"),
		warning: lipgloss.Color("214"),
		danger:  lipgloss.Color("204"),
		purple:  lipgloss.Color("135"),
	}

	t.base = lipgloss.NewStyle().Foreground(t.fg)
	t.bold = lipgloss.NewStyle().Bold(true).Foreground(t.fg)
	t.mutedStyle = lipgloss.NewStyle().Foreground(t.muted)
	t.accentStyle = lipgloss.NewStyle().Bold(true).Foreground(t.accent)
	t.successStyle = lipgloss.NewStyle().Bold(true).Foreground(t.success)
	t.warnStyle = lipgloss.NewStyle().Bold(true).Foreground(t.warning)
	t.errStyle = lipgloss.NewStyle().Bold(true).Foreground(t.danger)
	t.purpleStyle = lipgloss.NewStyle().Foreground(t.purple)

	return t
}

func (m Model) View() string {
	th := newTheme()

	totalH := m.Height
	if totalH <= 0 {
		totalH = 24
	}
	totalW := m.Width
	if totalW <= 0 {
		totalW = 100
	}

	// Full-screen root — paint full terminal background (non-transparent)
	root := lipgloss.NewStyle().
		Background(th.bg).
		Foreground(th.fg)
	if totalW < 36 || totalH < 10 {
		return m.renderMinimalView(th, totalW, totalH)
	}

	// ── Top bar (header + tab bar) ────────────────────────────────────────
	topBar := m.renderTopBar(th, totalW)
	footer := m.renderFooter(th, totalW)

	topBarH := lipgloss.Height(topBar)
	footerH := lipgloss.Height(footer)

	// When help overlay is shown, pre-render it so we can subtract its
	// height from the body budget — prevents overflow in AltScreen.
	var helpBlock string
	helpH := 0
	if m.ShowHelp {
		helpBlock = m.renderHelp(th)
		helpH = lipgloss.Height(helpBlock)
	}

	// Pre-render toast to account for its height in the body budget.
	var toastBlock string
	toastH := 0
	if m.toast != nil {
		toastBlock = m.renderToast(th, totalW)
		toastH = lipgloss.Height(toastBlock)
	}

	// Reserve rows: topBar + footer + help (if open) + toast (if visible)
	bodyH := totalH - topBarH - footerH - helpH - toastH
	if bodyH < 10 {
		return m.renderMinimalView(th, totalW, totalH)
	}

	// ── Body layout responsive: horizontal / stacked / single (small) ────
	const minMainW = 36
	const minSidebarW = 24
	const singleModeMaxW = 80
	const horizontalModeMinW = 100
	singleMode := totalW <= singleModeMaxW || bodyH < 16
	horizontal := totalW >= horizontalModeMinW

	var body string
	if singleMode {
		body = m.renderSinglePanelBody(th, totalW, bodyH)
	} else if horizontal {
		sbW := sidebarWidth
		mainW := totalW - sbW - 1 // 1 col for separator
		if mainW < minMainW {
			sbW = totalW - minMainW - 1
			if sbW < minSidebarW {
				sbW = minSidebarW
			}
			mainW = totalW - sbW - 1
		}

		sidebar := m.renderSidebar(th, sbW, bodyH, true)
		mainContent := m.renderMainContent(th, mainW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, mainContent, sidebar)
	} else {
		mainH := bodyH * 65 / 100
		if mainH < 6 {
			mainH = 6
		}
		sidebarH := bodyH - mainH
		if sidebarH < 5 {
			sidebarH = 5
			mainH = bodyH - sidebarH
			if mainH < 4 {
				mainH = 4
			}
		}

		mainContent := m.renderMainContent(th, totalW, mainH)
		sidebar := m.renderSidebar(th, totalW, sidebarH, false)
		body = lipgloss.JoinVertical(lipgloss.Left, mainContent, sidebar)
	}
	body = fitBlock(body, totalW, bodyH, th.bg)

	// ── Assemble final output ────────────────────────────────────────────
	parts := []string{topBar, body}
	if m.ShowHelp {
		parts = append(parts, helpBlock)
	}
	if toastH > 0 {
		parts = append(parts, toastBlock)
	}
	parts = append(parts, footer)

	rendered := root.Render(strings.Join(parts, "\n"))

	// Clamp output to terminal height — lipgloss Height() is a minimum,
	// not a maximum, so inner content can exceed the card Height.
	// Truncating here guarantees AltScreen never overflows.
	lines := strings.Split(rendered, "\n")
	if len(lines) > totalH {
		lines = lines[:totalH]
	}
	padBg := lipgloss.NewStyle().Background(th.bg)
	for i := range lines {
		if lipgloss.Width(lines[i]) > totalW {
			lines[i] = xansi.Truncate(lines[i], totalW, "")
		}
		w := lipgloss.Width(lines[i])
		if w < totalW {
			lines[i] += padBg.Render(strings.Repeat(" ", totalW-w))
		}
	}
	if len(lines) < totalH {
		blank := lipgloss.NewStyle().Background(th.bg).Width(totalW).Render("")
		for len(lines) < totalH {
			lines = append(lines, blank)
		}
	}
	rendered = strings.Join(lines, "\n")

	return rendered
}

func (m Model) renderSinglePanelBody(th theme, w, h int) string {
	if h < 6 {
		h = 6
	}

	switch m.ActivePanel {
	case PanelLatestOTP:
		return m.renderOTPCard(th, w, h)
	case PanelLogs:
		return m.renderLogsCard(th, w, h)
	case PanelMailAccount:
		return m.renderMailAccountCard(th, w, h)
	case PanelSettings:
		return m.renderSettingsCard(th, w, h)
	default:
		return m.renderHealthCard(th, w)
	}
}

func (m Model) renderMinimalView(th theme, totalW, totalH int) string {
	if totalW <= 0 {
		totalW = 36
	}
	if totalH <= 0 {
		totalH = 10
	}

	lines := []string{
		truncate("⚡ TUIOTP", totalW),
		truncate("Resize terminal for full dashboard", totalW),
		truncate(fmt.Sprintf("OTP events: %d", len(m.otpEvents)), totalW),
		truncate("keys: q quit  r refresh  o otp", totalW),
	}
	out := strings.Join(lines, "\n")
	rendered := lipgloss.NewStyle().Background(th.bg).Foreground(th.fg).Width(totalW).Render(out)
	s := strings.Split(rendered, "\n")
	if len(s) > totalH {
		s = s[:totalH]
	}
	if len(s) < totalH {
		blank := lipgloss.NewStyle().Background(th.bg).Width(totalW).Render("")
		for len(s) < totalH {
			s = append(s, blank)
		}
	}
	return strings.Join(s, "\n")
}

// ── Top bar: logo + clock + tab bar ──────────────────────────────────────────

func (m Model) renderTopBar(th theme, totalW int) string {
	// Logo (single line, compact)
	brand := "⚡ TUIOTP"
	tagline := "OTP Dashboard · Cloudflare Email · IMAP"

	now := time.Now().UTC().Format("15:04:05 UTC")
	clock := lipgloss.NewStyle().
		Foreground(th.accent).
		Render(now)
	if totalW < 24 {
		return lipgloss.NewStyle().
			Background(th.bgAlt).
			Width(totalW).
			Render(now)
	}

	logoText := brand + "  " + tagline
	maxLogoW := totalW - lipgloss.Width(clock) - 3
	if maxLogoW < 8 {
		maxLogoW = 8
	}
	if lipgloss.Width(logoText) > maxLogoW {
		logoText = truncate(logoText, maxLogoW)
	}
	logo := th.accentStyle.Render(brand)
	if strings.HasPrefix(logoText, brand) {
		rest := strings.TrimPrefix(logoText, brand)
		logo += th.mutedStyle.Render(rest)
	} else {
		logo = th.mutedStyle.Render(logoText)
	}

	// Push clock to right
	logoW := lipgloss.Width(logo)
	clockW := lipgloss.Width(clock)
	gap := totalW - logoW - clockW - 2
	if gap < 1 {
		gap = 1
	}
	topLine := lipgloss.NewStyle().
		Background(th.bgAlt).
		Width(totalW).
		Render(logo + strings.Repeat(" ", gap) + clock)

	// Tab bar
	tabBar := m.renderTabBar(th, totalW)

	return strings.Join([]string{topLine, tabBar}, "\n")
}

func (m Model) renderTabBar(th theme, totalW int) string {
	type tabDef struct {
		panel Panel
		label string
		icon  string
	}
	tabs := []tabDef{
		{PanelLatestOTP, "OTP", "⚡"},
		{PanelLogs, "Logs", "≡"},
		{PanelMailAccount, "Mail Account", "☁"},
		{PanelSettings, "Settings", "⚙"},
		{PanelStatus, "Health", "◈"},
	}

	activeTabSt := lipgloss.NewStyle().
		Bold(true).
		Foreground(th.bg).
		Background(th.accent).
		Padding(0, 2)

	inactiveTabSt := lipgloss.NewStyle().
		Foreground(th.muted).
		Background(lipgloss.Color("235")).
		Padding(0, 2)

	sepSt := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Background(lipgloss.Color("235"))

	parts := make([]string, 0, len(tabs)*2)
	for i, t := range tabs {
		if i > 0 {
			parts = append(parts, sepSt.Render("│"))
		}
		label := t.icon + " " + t.label
		if m.ActivePanel == t.panel {
			parts = append(parts, activeTabSt.Render(label))
		} else {
			parts = append(parts, inactiveTabSt.Render(label))
		}
	}

	tabRow := strings.Join(parts, "")
	tabW := lipgloss.Width(tabRow)
	if tabW > totalW {
		compact := make([]string, 0, len(tabs))
		for _, t := range tabs {
			label := t.icon
			if m.ActivePanel == t.panel {
				compact = append(compact, activeTabSt.Render(label))
			} else {
				compact = append(compact, inactiveTabSt.Render(label))
			}
		}
		tabRow = strings.Join(compact, " ")
		tabW = lipgloss.Width(tabRow)
		if tabW > totalW {
			shortParts := make([]string, 0, len(tabs))
			for _, t := range tabs {
				if m.ActivePanel == t.panel {
					shortParts = append(shortParts, "["+t.icon+"]")
				} else {
					shortParts = append(shortParts, t.icon)
				}
			}
			plain := truncate(strings.Join(shortParts, " "), totalW)
			return lipgloss.NewStyle().
				Background(lipgloss.Color("235")).
				Width(totalW).
				Render(plain)
		}
	}
	pad := ""
	if totalW > tabW {
		pad = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Render(strings.Repeat(" ", totalW-tabW))
	}

	return tabRow + pad
}

// ── Sidebar (right): Health card stacked on Mail Account card ────────────────

func (m Model) renderSidebar(th theme, w, totalH int, withLeftSeparator bool) string {
	innerW := w
	if withLeftSeparator {
		innerW = w - 2 // account for left border char + gap
	}
	if innerW < 20 {
		innerW = 20
	}

	// Health card (fixed height ~9, includes its own border)
	healthCard := m.renderHealthCard(th, innerW)
	healthH := lipgloss.Height(healthCard)

	// Mail Account card takes remaining height.
	// Card uses RoundedBorder (+2 rows outside content Height),
	// so we subtract 2 to keep the total sidebar within totalH.
	const cardBorderRows = 2
	mailContentH := totalH - healthH - cardBorderRows
	if mailContentH < 4 {
		mailContentH = 4
	}
	mailCard := m.renderMailAccountCard(th, innerW, mailContentH)

	sidebar := lipgloss.JoinVertical(lipgloss.Left, healthCard, mailCard)

	if !withLeftSeparator {
		return sidebar
	}

	// Left border line to visually separate from main content
	borderSt := lipgloss.NewStyle().
		Border(lipgloss.Border{Left: "│"}, false, false, false, true).
		BorderForeground(lipgloss.Color("237")).
		PaddingLeft(0)

	return borderSt.Render(sidebar)
}

func (m Model) renderHealthCard(th theme, w int) string {
	health := normalizeHealthStatus(m.health)

	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w).
		Padding(0, 1)
	title := titleSt.Render("◈ System Health")

	type pill struct{ name, val string }
	pills := []pill{
		{"☁  cloudflare", health.Cloudflare},
		{"📬 destination", health.Destination},
		{"📥 mailbox", health.Mailbox},
		{"⚙  parser", health.Parser},
	}

	rows := make([]string, 0, len(pills))
	for _, p := range pills {
		dot, label := m.healthDotLabel(p.val, th)
		nameStr := th.mutedStyle.Render(fmt.Sprintf("  %-15s", p.name))
		rows = append(rows, nameStr+dot+" "+label)
	}

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w)

	if m.ActivePanel == PanelStatus {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	inner := strings.Join(append([]string{title, ""}, rows...), "\n")
	return cardSt.Render(inner)
}

// healthDotLabel returns a colored dot + status text for a health value.
func (m Model) healthDotLabel(v string, th theme) (dot string, label string) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.Contains(v, "error") || strings.Contains(v, "failed"):
		return th.errStyle.Render("●"), th.errStyle.Render(v)
	case v == "unknown" || strings.Contains(v, "reconnect"):
		return th.warnStyle.Render("●"), th.warnStyle.Render(v)
	default:
		return th.successStyle.Render("●"), th.successStyle.Copy().Bold(false).Render(v)
	}
}

func (m Model) renderMailAccountCard(th theme, w, h int) string {
	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w).
		Padding(0, 1)

	title := titleSt.Render("☁  Mail Account  " + th.mutedStyle.Render("live"))
	body := m.mailAccountPanelView(w)
	hint := th.mutedStyle.Render("  [/] domain  n new  e toggle  d del  r refresh  ↑↓ nav")

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelMailAccount {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	sep := th.mutedStyle.Render(strings.Repeat("─", w-2))

	inner := strings.Join([]string{title, sep, "", body, "", hint}, "\n")
	inner = clampLines(inner, h)
	return cardSt.Render(inner)
}

func (m Model) renderSettingsCard(th theme, w, h int) string {
	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w-2).
		Padding(0, 1)

	title := titleSt.Render("⚙ Settings  " + th.mutedStyle.Render("clipboard"))
	sepW := w - 4
	if sepW < 1 {
		sepW = 1
	}
	sep := th.mutedStyle.Render(strings.Repeat("─", sepW))
	body := m.settingsPanelView(w - 4)
	hint := th.mutedStyle.Render("  ↑↓ nav  space toggle  enter save+apply  r reset")

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelSettings {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	inner := strings.Join([]string{title, sep, "", body, "", hint}, "\n")
	inner = clampLines(inner, h)
	return cardSt.Render(inner)
}

func (m Model) settingsPanelView(w int) string {
	th := newTheme()
	if m.settingsMgr == nil {
		return th.mutedStyle.Render("  settings manager unavailable")
	}
	if m.settingsLoading {
		return th.mutedStyle.Render("  loading settings...")
	}
	if !m.settingsLoaded {
		return th.warnStyle.Render("  settings not loaded  ·  press r")
	}
	if m.settingsSaving {
		return th.mutedStyle.Render("  saving and applying settings...")
	}

	method := strings.TrimSpace(m.settingsForm.ClipboardMethod)
	if m.settingsEditing {
		if m.settingsSelected == 1 {
			method = m.settingsMethodInput
		}
	}
	if strings.TrimSpace(method) == "" {
		method = "auto"
	}
	domainsText := strings.TrimSpace(m.settingsForm.DomainsText)
	if m.settingsEditing && m.settingsSelected == 2 {
		domainsText = m.settingsDomainsInput
	}
	if domainsText == "" {
		domainsText = "example.com,zone_id"
	}
	domainsInline := strings.ReplaceAll(domainsText, "\n", " | ")
	activeDomain := strings.TrimSpace(m.settingsForm.ActiveDomain)
	if m.settingsEditing && m.settingsSelected == 3 {
		activeDomain = m.settingsActiveInput
	}
	if activeDomain == "" {
		activeDomain = "(auto:first)"
	}
	tz := strings.TrimSpace(m.settingsForm.Timezone)
	if m.settingsEditing && m.settingsSelected == 4 {
		tz = m.settingsTZInput
	}
	if tz == "" {
		tz = "local"
	}
	logPath := strings.TrimSpace(m.settingsForm.LogPath)
	if m.settingsEditing && m.settingsSelected == 5 {
		logPath = m.settingsLogPathInput
	}
	if logPath == "" {
		logPath = "stderr"
	}
	poll := strings.TrimSpace(m.settingsForm.IMAPPollInterval)
	if m.settingsEditing && m.settingsSelected == 6 {
		poll = m.settingsPollInput
	}
	if poll == "" {
		poll = "5s"
	}
	enabledLabel := "off"
	if m.settingsForm.ClipboardEnabled {
		enabledLabel = "on"
	}

	row := func(active bool, label, value string) string {
		cursor := "  "
		labelSt := th.mutedStyle
		valueSt := th.base
		if active {
			cursor = th.accentStyle.Render("▶ ")
			labelSt = th.accentStyle.Copy().Bold(false)
			valueSt = th.bold
		}
		return cursor + labelSt.Render(fmt.Sprintf("%-18s", label)) + valueSt.Render(value)
	}

	methodValue := method
	if m.settingsEditing && m.settingsSelected == 1 {
		methodValue += "▌"
	}

	lines := []string{
		th.bold.Render("  Clipboard"),
		"",
		row(m.settingsSelected == 0, "enabled", enabledLabel),
		row(m.settingsSelected == 1, "method", methodValue),
		row(m.settingsSelected == 2, "domains", func() string {
			if m.settingsEditing && m.settingsSelected == 2 {
				return domainsInline + "▌"
			}
			return domainsInline
		}()),
		row(m.settingsSelected == 3, "active_domain", func() string {
			if m.settingsEditing && m.settingsSelected == 3 {
				return activeDomain + "▌"
			}
			return activeDomain
		}()),
		row(m.settingsSelected == 4, "timezone", func() string {
			if m.settingsEditing && m.settingsSelected == 4 {
				return tz + "▌"
			}
			return tz
		}()),
		row(m.settingsSelected == 5, "log_path", func() string {
			if m.settingsEditing && m.settingsSelected == 5 {
				return logPath + "▌"
			}
			return logPath
		}()),
		row(m.settingsSelected == 6, "imap.poll_interval", func() string {
			if m.settingsEditing && m.settingsSelected == 6 {
				return poll + "▌"
			}
			return poll
		}()),
		"",
		th.mutedStyle.Render("  supported: " + strings.Join(supportedClipboardMethods, ", ")),
	}
	if m.settingsEditing {
		lines = append(lines, th.mutedStyle.Render("  editing method: enter done  esc cancel  backspace delete"))
	}
	return strings.Join(lines, "\n")
}

// ── Main content (left): OTP panel on top, Logs below ────────────────────────

func (m Model) renderMainContent(th theme, w, totalH int) string {
	if m.ActivePanel == PanelSettings {
		return m.renderSettingsCard(th, w, totalH)
	}

	// Each card has RoundedBorder which adds 2 rows (top+bottom) outside
	// the content Height. We have 2 cards, so 4 border rows total.
	const borderRows = 2 // per card
	contentH := totalH - borderRows*2
	if contentH < 6 {
		contentH = 6
	}

	// Split content area: OTP gets ~65%, Logs ~35%
	otpH := contentH * 65 / 100
	logsH := contentH - otpH
	if logsH < 3 {
		logsH = 3
		otpH = contentH - logsH
	}

	otpCard := m.renderOTPCard(th, w, otpH)
	logsCard := m.renderLogsCard(th, w, logsH)

	return lipgloss.JoinVertical(lipgloss.Left, otpCard, logsCard)
}

func (m Model) renderOTPCard(th theme, w, h int) string {
	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w-2).
		Padding(0, 1)
	title := titleSt.Render("⚡ Latest OTP  " + th.mutedStyle.Render("incoming timeline"))

	sep := th.mutedStyle.Render(strings.Repeat("─", w-4))

	timeline := m.otpTimelineView(w - 4)
	detailSep := th.mutedStyle.Render(strings.Repeat("─", w-4))
	detailTitle := th.purpleStyle.Copy().Bold(true).Render("◈ Selected Detail")
	detail := m.otpDetailView()

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelLatestOTP {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	inner := strings.Join([]string{
		title, sep, "",
		timeline,
		"", detailSep, detailTitle,
		detail,
	}, "\n")

	// Clamp inner content to the allotted height so the card
	// doesn't overflow (lipgloss Height is a minimum, not a max).
	inner = clampLines(inner, h)

	return cardSt.Render(inner)
}

func (m Model) renderLogsCard(th theme, w, h int) string {
	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w-2).
		Padding(0, 1)

	scrollIndicator := th.mutedStyle.Render("auto ▼")
	if m.logScroll > 0 {
		scrollIndicator = th.mutedStyle.Render(fmt.Sprintf("↑%d", m.logScroll))
	}
	title := titleSt.Render("≡ Logs  " + th.mutedStyle.Render("runtime log stream") + "  " + scrollIndicator)

	sep := th.mutedStyle.Render(strings.Repeat("─", w-4))

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelLogs {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	// Calculate visible lines
	// h accounts for title + sep + padding, so usable lines for log content
	usableH := h - 2 // title + separator
	if usableH < 1 {
		usableH = 1
	}

	if len(m.logLines) == 0 {
		placeholder := lipgloss.NewStyle().
			Foreground(th.muted).
			Italic(true).
			Render("  waiting for log output…")
		inner := strings.Join([]string{title, sep, "", placeholder}, "\n")
		inner = clampLines(inner, h)
		return cardSt.Render(inner)
	}

	// Window into log lines based on scroll position
	lineW := w - 6 // account for borders + padding
	if lineW < 10 {
		lineW = 10
	}

	total := len(m.logLines)
	end := total - m.logScroll
	if end < 1 {
		end = 1
	}
	if end > total {
		end = total
	}
	start := end - usableH
	if start < 0 {
		start = 0
	}

	visible := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		visible = append(visible, formatLogLine(m.logLines[i], th, lineW))
	}

	inner := strings.Join(append([]string{title, sep}, visible...), "\n")
	inner = clampLines(inner, h)
	return cardSt.Render(inner)
}

// ── Footer ────────────────────────────────────────────────────────────────────

func (m Model) renderFooter(th theme, totalW int) string {
	type key struct{ k, desc string }
	keys := []key{
		{"q", "quit"},
		{"?", "help"},
		{"r", "refresh"},
		{"tab", "panel"},
		{"s", "settings"},
		{"o", "otp"},
		{"l", "logs"},
		{"/", "search"},
		{"c", "copy"},
		{"x/X/C", "clear"},
	}

	kSt := lipgloss.NewStyle().
		Bold(true).
		Foreground(th.bg).
		Background(th.muted).
		Padding(0, 1)
	dSt := th.mutedStyle

	parts := make([]string, 0, len(keys))
	for _, kv := range keys {
		parts = append(parts, kSt.Render(kv.k)+" "+dSt.Render(kv.desc))
	}

	inner := strings.Join(parts, "  ")
	return lipgloss.NewStyle().
		Background(th.bgAlt).
		Padding(0, 1).
		Width(totalW).
		Render(inner)
}

// renderToast renders the toast notification bar.
func (m Model) renderToast(th theme, totalW int) string {
	if m.toast == nil {
		return ""
	}

	var icon string
	var style lipgloss.Style

	switch m.toast.Level {
	case ToastSuccess:
		icon = "✔"
		style = lipgloss.NewStyle().
			Bold(true).
			Foreground(th.success).
			Background(lipgloss.Color("22")) // dark green bg
	case ToastError:
		icon = "✖"
		style = lipgloss.NewStyle().
			Bold(true).
			Foreground(th.danger).
			Background(lipgloss.Color("52")) // dark red bg
	case ToastWarning:
		icon = "⚠"
		style = lipgloss.NewStyle().
			Bold(true).
			Foreground(th.warning).
			Background(lipgloss.Color("58")) // dark yellow bg
	case ToastInfo:
		icon = "ℹ"
		style = lipgloss.NewStyle().
			Bold(true).
			Foreground(th.accent).
			Background(lipgloss.Color("17")) // dark blue bg
	}

	msg := truncate(m.toast.Message, totalW-10)
	left := fmt.Sprintf(" %s  %s", icon, msg)

	remaining := toastDuration(m.toast.Level) - time.Since(m.toast.ShownAt)
	secs := int(remaining.Seconds())
	if secs < 0 {
		secs = 0
	}
	countdown := fmt.Sprintf("(%ds)", secs)

	gap := totalW - lipgloss.Width(left) - lipgloss.Width(countdown) - 1
	if gap < 1 {
		gap = 1
	}
	content := left + strings.Repeat(" ", gap) + countdown

	return style.
		Width(totalW).
		Padding(0, 0).
		Render(content)
}

// ── Help overlay ──────────────────────────────────────────────────────────────

func (m Model) renderHelp(th theme) string {
	helpSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(th.purple).
		Padding(1, 3)

	kSt := th.accentStyle
	dSt := th.mutedStyle

	col := func(k, d string) string {
		return kSt.Render(fmt.Sprintf("%-14s", k)) + dSt.Render(d)
	}

	rows := []string{
		th.bold.Render("Keyboard Shortcuts"),
		"",
		col("q / ctrl+c", "quit application"),
		col("?", "toggle this help"),
		col("r", "refresh all data"),
		col("tab", "cycle panels"),
		col("o", "jump to OTP panel"),
		col("l", "jump to Logs panel"),
		col("s", "jump to Settings panel"),
		col("/", "search OTP events"),
		col("G", "jump to bottom (logs)"),
		col("space", "toggle setting value"),
		col("enter", "save+apply settings"),
		col("c", "copy selected OTP"),
		col("x", "clear selected OTP"),
		col("X", "clear OTP by current filter"),
		col("C", "clear all OTP"),
		col("n", "new mail account (routing rule)"),
		col("d", "delete mail account"),
		col("e", "toggle mail account enable/disable"),
		col("↑↓ / j k", "navigate items"),
		col("esc", "cancel / clear filter / back"),
	}

	return helpSt.Render(strings.Join(rows, "\n"))
}

// otpTimelineView renders the OTP event timeline table within the given width.
func (m Model) otpTimelineView(w int) string {
	th := newTheme()

	// Column widths that adapt to available w
	// cursor(3) + num(3) + plat(12) + otp(8) + alias(?) + time(8) + gaps(~10)
	aliasW := w - 3 - 3 - 12 - 8 - 8 - 10
	if aliasW < 10 {
		aliasW = 10
	}
	sepW := w - 2
	if sepW < 10 {
		sepW = 10
	}

	if m.otpSearchMode {
		searchBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(th.accent).
			Padding(0, 1).
			Render(fmt.Sprintf("🔍 %s▌", m.otpSearchInput))
		return strings.Join([]string{
			searchBox,
			th.mutedStyle.Render("  enter apply  ·  esc cancel  ·  backspace edit"),
		}, "\n")
	}

	if len(m.otpEvents) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(th.muted).
			Italic(true).
			Render("  no OTP events found")
		if m.otpSearchQuery != "" {
			return strings.Join([]string{
				empty,
				th.mutedStyle.Render(fmt.Sprintf("  filter: %q", m.otpSearchQuery)),
			}, "\n")
		}
		return empty
	}

	hdrSt := lipgloss.NewStyle().Bold(true).Foreground(th.muted)
	header := hdrSt.Render(fmt.Sprintf("   %-3s  %-12s  %-8s  %-*s  %s",
		"#", "PLATFORM", "OTP", aliasW, "ALIAS", "TIME",
	))
	sep := th.mutedStyle.Render("  " + strings.Repeat("─", sepW))

	lines := make([]string, 0, len(m.otpEvents)+4)
	if m.otpSearchQuery != "" {
		lines = append(lines, th.warnStyle.Render(fmt.Sprintf("  filter: %q", m.otpSearchQuery)))
	}
	lines = append(lines, header, sep)

	for i, evt := range m.otpEvents {
		numSt := th.mutedStyle
		platSt := th.accentStyle.Copy().Bold(false)
		otpSt := lipgloss.NewStyle().Bold(true).Foreground(th.success)
		aliasSt := th.mutedStyle
		timeSt := th.purpleStyle
		cursor := "   "

		if i == m.otpSelected {
			cursor = th.accentStyle.Render(" ▶ ")
			otpSt = lipgloss.NewStyle().Bold(true).Foreground(th.warning)
		}

		line := fmt.Sprintf("%s%s  %s  %s  %s  %s",
			cursor,
			numSt.Render(fmt.Sprintf("%-3d", i+1)),
			platSt.Render(truncate(evt.Platform, 12)),
			otpSt.Render(fmt.Sprintf("%-8s", evt.OTPCode)),
			aliasSt.Render(truncate(fmt.Sprintf("%-*s", aliasW, evt.AliasEmail), aliasW)),
			timeSt.Render(evt.ReceivedAt.UTC().Format("15:04:05")),
		)
		lines = append(lines, line)
	}

	if m.otpDeleteMode {
		scope := "selected"
		switch m.otpDeleteScope {
		case "filtered":
			scope = "filtered"
		case "all":
			scope = "all"
		}
		lines = append(lines, th.warnStyle.Render(fmt.Sprintf("  clear %s OTP?  y confirm  n/esc cancel", scope)))
	}

	lines = append(lines, sep)
	lines = append(lines, th.mutedStyle.Render("  ↑↓/jk nav  ·  c copy  ·  x/X/C clear  ·  / search  ·  esc clear"))

	return strings.Join(lines, "\n")
}

func (m Model) otpDetailView() string {
	th := newTheme()

	if len(m.otpEvents) == 0 {
		return lipgloss.NewStyle().Foreground(th.muted).Italic(true).Render("  no OTP selected")
	}

	idx := m.otpSelected
	if idx < 0 || idx >= len(m.otpEvents) {
		idx = 0
	}
	e := m.otpEvents[idx]

	labelStyle := th.mutedStyle
	valueStyle := lipgloss.NewStyle().Foreground(th.fg)
	otpBig := lipgloss.NewStyle().
		Bold(true).
		Foreground(th.warning).
		Background(lipgloss.Color("236")).
		Padding(0, 2).
		Render(e.OTPCode)

	rows := []string{
		lipgloss.JoinHorizontal(lipgloss.Center,
			labelStyle.Render("  OTP Code   "),
			otpBig,
		),
		"",
		labelStyle.Render("  platform  ") + valueStyle.Render(e.Platform),
		labelStyle.Render("  alias     ") + valueStyle.Render(e.AliasEmail),
		labelStyle.Render("  from      ") + valueStyle.Render(e.FromEmail),
		labelStyle.Render("  received  ") + th.purpleStyle.Render(e.ReceivedAt.UTC().Format(time.RFC3339)),
		"",
		th.mutedStyle.Render("  press ") +
			th.accentStyle.Render("c") +
			th.mutedStyle.Render(" to copy OTP to clipboard"),
	}

	return strings.Join(rows, "\n")
}

func panelLabel(p Panel) string {
	switch p {
	case PanelStatus:
		return "status"
	case PanelMailAccount:
		return "mail_account"
	case PanelSettings:
		return "settings"
	case PanelLatestOTP:
		return "latest_otp"
	case PanelLogs:
		return "logs"
	default:
		return "unknown"
	}
}

type otpHistoryLoadedMsg struct {
	events []domain.OTPEvent
	query  string
	reqAt  time.Time
	err    error
}

type otpCopiedMsg struct {
	err error
}

type otpDeletedMsg struct {
	rows  int64
	scope string
	err   error
}

type cfRulesLoadedMsg struct {
	rules []ports.RoutingRule
	err   error
}

type cfRuleCreatedMsg struct {
	rule ports.RoutingRule
	err  error
}

type cfRuleUpdatedMsg struct {
	rule ports.RoutingRule
	err  error
}

type cfRuleDeletedMsg struct {
	err error
}

type settingsLoadedMsg struct {
	state SettingsState
	err   error
}

type settingsSavedMsg struct {
	state     SettingsState
	clipboard clipboardCopier
	err       error
}

type logTickMsg struct{}

type toastTickMsg struct{}

type toastDismissMsg struct {
	shownAt time.Time // identifies which toast to dismiss (stale guard)
}

func (m Model) logTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return logTickMsg{}
	})
}

func defaultSettingsState() SettingsState {
	return SettingsState{ClipboardEnabled: true, ClipboardMethod: "auto"}
}

func normalizeSettingsState(in SettingsState) SettingsState {
	out := in
	out.ClipboardMethod = strings.ToLower(strings.TrimSpace(out.ClipboardMethod))
	if out.ClipboardMethod == "" || !isSupportedClipboardMethod(out.ClipboardMethod) {
		out.ClipboardMethod = "auto"
	}
	out.DomainsText = strings.TrimSpace(out.DomainsText)
	if utf8.RuneCountInString(out.DomainsText) > maxDomainsTextLen {
		r := []rune(out.DomainsText)
		out.DomainsText = string(r[:maxDomainsTextLen])
	}
	out.ActiveDomain = strings.ToLower(strings.TrimSpace(out.ActiveDomain))
	if utf8.RuneCountInString(out.ActiveDomain) > maxActiveDomainLen {
		r := []rune(out.ActiveDomain)
		out.ActiveDomain = string(r[:maxActiveDomainLen])
	}
	out.Timezone = strings.TrimSpace(out.Timezone)
	if utf8.RuneCountInString(out.Timezone) > maxTimezoneLen {
		r := []rune(out.Timezone)
		out.Timezone = string(r[:maxTimezoneLen])
	}
	out.LogPath = strings.TrimSpace(out.LogPath)
	if utf8.RuneCountInString(out.LogPath) > maxLogPathLen {
		r := []rune(out.LogPath)
		out.LogPath = string(r[:maxLogPathLen])
	}
	out.IMAPPollInterval = strings.TrimSpace(out.IMAPPollInterval)
	if utf8.RuneCountInString(out.IMAPPollInterval) > maxPollIntervalLen {
		r := []rune(out.IMAPPollInterval)
		out.IMAPPollInterval = string(r[:maxPollIntervalLen])
	}
	return out
}

func isSupportedClipboardMethod(method string) bool {
	method = strings.ToLower(strings.TrimSpace(method))
	for _, allowed := range supportedClipboardMethods {
		if method == allowed {
			return true
		}
	}
	return false
}

func (m Model) currentSettingsState() SettingsState {
	state := m.settingsForm
	if m.settingsEditing {
		switch m.settingsSelected {
		case 1:
			state.ClipboardMethod = m.settingsMethodInput
		case 2:
			state.DomainsText = m.settingsDomainsInput
		case 3:
			state.ActiveDomain = m.settingsActiveInput
		case 4:
			state.Timezone = m.settingsTZInput
		case 5:
			state.LogPath = m.settingsLogPathInput
		case 6:
			state.IMAPPollInterval = m.settingsPollInput
		}
	}
	return normalizeSettingsState(state)
}

func (m Model) loadSettingsCmd() tea.Cmd {
	if m.settingsMgr == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()
		state, err := m.settingsMgr.Load(ctx)
		return settingsLoadedMsg{state: normalizeSettingsState(state), err: err}
	}
}

func (m Model) saveSettingsCmd() tea.Cmd {
	if m.settingsMgr == nil {
		return nil
	}
	state := m.currentSettingsState()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()
		next, clip, err := m.settingsMgr.SaveAndApply(ctx, state)
		return settingsSavedMsg{state: normalizeSettingsState(next), clipboard: clip, err: err}
	}
}

func (m Model) refreshOTPHistoryCmd(query string) tea.Cmd {
	return m.refreshOTPHistoryCmdAt(query, time.Now().UTC())
}

func (m Model) copyOTPCmd(otpCode string) tea.Cmd {
	if m.clipboard == nil {
		return nil
	}
	otpCode = strings.TrimSpace(otpCode)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()

		err := m.clipboard.Copy(ctx, otpCode)
		return otpCopiedMsg{err: err}
	}
}

func (m Model) clearOTPByIDCmd(id int64) tea.Cmd {
	if m.otpManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()

		rows, err := m.otpManager.ClearOTPEventByID(ctx, id)
		return otpDeletedMsg{rows: rows, scope: "selected", err: err}
	}
}

func (m Model) clearOTPByScopeCmd(scope string) tea.Cmd {
	if m.otpManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()

		filter := app.OTPDeleteFilter{}
		if scope == "filtered" {
			filter.Query = strings.TrimSpace(m.otpSearchQuery)
		} else if scope == "all" {
			filter.AllowDeleteAll = true
		}
		rows, err := m.otpManager.ClearOTPEvents(ctx, filter)
		return otpDeletedMsg{rows: rows, scope: scope, err: err}
	}
}

func (m Model) refreshCFRulesCmd() tea.Cmd {
	if m.rulesManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()

		rules, err := m.rulesManager.ListRoutingRules(ctx)
		if err != nil {
			return cfRulesLoadedMsg{err: err}
		}
		return cfRulesLoadedMsg{rules: rules}
	}
}

func (m Model) createCFRuleCmd(aliasEmail string) tea.Cmd {
	if m.rulesManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()
		aliasEmail = strings.TrimSpace(aliasEmail)
		if aliasEmail != "" && !strings.Contains(aliasEmail, "@") {
			activeDomain := strings.TrimSpace(m.rulesManager.ActiveDomain())
			if activeDomain != "" {
				aliasEmail = aliasEmail + "@" + activeDomain
			}
		}

		rule, err := m.rulesManager.CreateRoutingRuleDirect(ctx, ports.CreateRoutingRuleInput{
			AliasEmail: aliasEmail,
			Enabled:    true,
		})
		if err != nil {
			return cfRuleCreatedMsg{err: err}
		}
		return cfRuleCreatedMsg{rule: rule}
	}
}

func (m Model) toggleCFRuleCmd(rule ports.RoutingRule) tea.Cmd {
	if m.rulesManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()

		updated, err := m.rulesManager.UpdateRoutingRule(ctx, ports.UpdateRoutingRuleInput{
			ID:          rule.ID,
			Name:        rule.Name,
			AliasEmail:  rule.AliasEmail,
			Destination: rule.Destination,
			Enabled:     !rule.Enabled,
			Priority:    rule.Priority,
		})
		if err != nil {
			return cfRuleUpdatedMsg{err: err}
		}
		return cfRuleUpdatedMsg{rule: updated}
	}
}

func (m Model) deleteCFRuleCmd(ruleID string) tea.Cmd {
	if m.rulesManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()

		err := m.rulesManager.DeleteRoutingRuleByID(ctx, strings.TrimSpace(ruleID))
		return cfRuleDeletedMsg{err: err}
	}
}

func (m Model) refreshOTPHistoryCmdAt(query string, reqAt time.Time) tea.Cmd {
	if m.otpManager == nil {
		return nil
	}

	query = strings.TrimSpace(query)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()

		events, err := m.otpManager.ListOTPEvents(ctx, app.OTPListFilter{
			Query: query,
			Limit: defaultOTPHistoryLimit,
		})
		if err != nil {
			return otpHistoryLoadedMsg{query: query, reqAt: reqAt, err: err}
		}

		return otpHistoryLoadedMsg{events: events, query: query, reqAt: reqAt}
	}
}

func (m Model) nextRefreshAllCmd() (Model, tea.Cmd) {
	reqAt := time.Now().UTC()
	m.otpLastReqAt = reqAt

	cmds := make([]tea.Cmd, 0, 2)
	if cmd := m.refreshOTPHistoryCmdAt(m.otpSearchQuery, reqAt); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.refreshCFRulesCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}

	if len(cmds) == 0 {
		return m, nil
	}
	if len(cmds) == 1 {
		return m, cmds[0]
	}

	return m, tea.Batch(cmds...)
}

func (m Model) updateMailAccountPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}

	if m.cfDeleteConfirm {
		switch key {
		case "y", "enter":
			if len(m.cfRules) == 0 || m.cfSelected < 0 || m.cfSelected >= len(m.cfRules) {
				m.cfDeleteConfirm = false
				toastCmd := showToast(&m, ToastError, "invalid mail account selection")
				return m, toastCmd, true
			}
			rule := m.cfRules[m.cfSelected]
			toastCmd := showToast(&m, ToastInfo, "deleting mail account...")
			return m, tea.Batch(toastCmd, m.deleteCFRuleCmd(rule.ID)), true
		case "n", "esc":
			m.cfDeleteConfirm = false
			toastCmd := showToast(&m, ToastInfo, "delete cancelled")
			return m, toastCmd, true
		default:
			return m, nil, true
		}
	}

	if m.creating {
		switch key {
		case "esc":
			m.creating = false
			m.createAliasEmail = ""
			toastCmd := showToast(&m, ToastInfo, "create cancelled")
			return m, toastCmd, true
		case "enter":
			if strings.TrimSpace(m.createAliasEmail) == "" {
				toastCmd := showToast(&m, ToastError, "email is required")
				return m, toastCmd, true
			}
			toastCmd := showToast(&m, ToastInfo, "creating mail account...")
			return m, tea.Batch(toastCmd, m.createCFRuleCmd(m.createAliasEmail)), true
		case "backspace":
			m.createAliasEmail = trimLastRune(m.createAliasEmail)
			return m, nil, true
		default:
			if len(msg.Runes) > 0 {
				r := msg.Runes[0]
				if r >= 32 && r != 127 {
					if utf8.RuneCountInString(m.createAliasEmail) < maxAliasEmailLen {
						m.createAliasEmail += string(r)
					}
				}
			}
			return m, nil, true
		}
	}

	switch key {
	case "[":
		if m.rulesManager == nil {
			return m, nil, true
		}
		domains := m.rulesManager.ListDomains()
		if len(domains) == 0 {
			return m, nil, true
		}
		active := strings.ToLower(strings.TrimSpace(m.rulesManager.ActiveDomain()))
		idx := 0
		for i, d := range domains {
			if strings.ToLower(strings.TrimSpace(d)) == active {
				idx = i
				break
			}
		}
		next := idx - 1
		if next < 0 {
			next = len(domains) - 1
		}
		if err := m.rulesManager.SetActiveDomain(domains[next]); err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("switch active domain", err))
			return m, toastCmd, true
		}
		toastCmd := showToast(&m, ToastInfo, "active domain: "+domains[next])
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd()), true
	case "]":
		if m.rulesManager == nil {
			return m, nil, true
		}
		domains := m.rulesManager.ListDomains()
		if len(domains) == 0 {
			return m, nil, true
		}
		active := strings.ToLower(strings.TrimSpace(m.rulesManager.ActiveDomain()))
		idx := 0
		for i, d := range domains {
			if strings.ToLower(strings.TrimSpace(d)) == active {
				idx = i
				break
			}
		}
		next := (idx + 1) % len(domains)
		if err := m.rulesManager.SetActiveDomain(domains[next]); err != nil {
			toastCmd := showToast(&m, ToastError, userSafeError("switch active domain", err))
			return m, toastCmd, true
		}
		toastCmd := showToast(&m, ToastInfo, "active domain: "+domains[next])
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd()), true
	case "n":
		if m.rulesManager == nil {
			toastCmd := showToast(&m, ToastError, "rules manager unavailable")
			return m, toastCmd, true
		}
		m.creating = true
		m.createAliasEmail = ""
		// No toast — create form is visually obvious
		return m, nil, true
	case "e":
		if len(m.cfRules) == 0 {
			toastCmd := showToast(&m, ToastWarning, "no mail account to toggle")
			return m, toastCmd, true
		}
		if m.rulesManager == nil {
			toastCmd := showToast(&m, ToastError, "rules manager unavailable")
			return m, toastCmd, true
		}
		idx := m.cfSelected
		if idx < 0 || idx >= len(m.cfRules) {
			return m, nil, true
		}
		rule := m.cfRules[idx]
		status := "disabling"
		if !rule.Enabled {
			status = "enabling"
		}
		toastCmd := showToast(&m, ToastInfo, fmt.Sprintf("%s mail account...", status))
		return m, tea.Batch(toastCmd, m.toggleCFRuleCmd(rule)), true
	case "d":
		if len(m.cfRules) == 0 {
			toastCmd := showToast(&m, ToastWarning, "no mail account to delete")
			return m, toastCmd, true
		}
		if m.rulesManager == nil {
			toastCmd := showToast(&m, ToastError, "rules manager unavailable")
			return m, toastCmd, true
		}
		m.cfDeleteConfirm = true
		// No toast — delete confirm dialog is visually obvious
		return m, nil, true
	case "r":
		toastCmd := showToast(&m, ToastInfo, "refreshing mail accounts...")
		return m, tea.Batch(toastCmd, m.refreshCFRulesCmd()), true
	case "up", "k":
		if len(m.cfRules) > 0 && m.cfSelected > 0 {
			m.cfSelected--
		}
		return m, nil, true
	case "down", "j":
		if len(m.cfRules) > 0 && m.cfSelected < len(m.cfRules)-1 {
			m.cfSelected++
		}
		return m, nil, true
	}

	return m, nil, false
}

func (m Model) updateSettingsPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}
	if m.settingsMgr == nil {
		if key == "r" {
			toastCmd := showToast(&m, ToastWarning, "settings manager unavailable")
			return m, toastCmd, true
		}
		return m, nil, false
	}
	if m.settingsSaving {
		return m, nil, true
	}

	if m.settingsEditing {
		switch key {
		case "esc":
			m.settingsEditing = false
			m.settingsMethodInput = m.settingsForm.ClipboardMethod
			m.settingsDomainsInput = m.settingsForm.DomainsText
			m.settingsActiveInput = m.settingsForm.ActiveDomain
			m.settingsTZInput = m.settingsForm.Timezone
			m.settingsLogPathInput = m.settingsForm.LogPath
			m.settingsPollInput = m.settingsForm.IMAPPollInterval
			return m, nil, true
		case "enter":
			m.settingsEditing = false
			switch m.settingsSelected {
			case 1:
				m.settingsForm.ClipboardMethod = strings.TrimSpace(m.settingsMethodInput)
				if m.settingsForm.ClipboardMethod == "" || !isSupportedClipboardMethod(m.settingsForm.ClipboardMethod) {
					m.settingsForm.ClipboardMethod = "auto"
				}
			case 2:
				m.settingsForm.DomainsText = strings.TrimSpace(m.settingsDomainsInput)
				if utf8.RuneCountInString(m.settingsForm.DomainsText) > maxDomainsTextLen {
					r := []rune(m.settingsForm.DomainsText)
					m.settingsForm.DomainsText = string(r[:maxDomainsTextLen])
				}
			case 3:
				m.settingsForm.ActiveDomain = strings.ToLower(strings.TrimSpace(m.settingsActiveInput))
				if utf8.RuneCountInString(m.settingsForm.ActiveDomain) > maxActiveDomainLen {
					r := []rune(m.settingsForm.ActiveDomain)
					m.settingsForm.ActiveDomain = string(r[:maxActiveDomainLen])
				}
			case 4:
				m.settingsForm.Timezone = strings.TrimSpace(m.settingsTZInput)
				if utf8.RuneCountInString(m.settingsForm.Timezone) > maxTimezoneLen {
					r := []rune(m.settingsForm.Timezone)
					m.settingsForm.Timezone = string(r[:maxTimezoneLen])
				}
			case 5:
				m.settingsForm.LogPath = strings.TrimSpace(m.settingsLogPathInput)
				if utf8.RuneCountInString(m.settingsForm.LogPath) > maxLogPathLen {
					r := []rune(m.settingsForm.LogPath)
					m.settingsForm.LogPath = string(r[:maxLogPathLen])
				}
			case 6:
				m.settingsForm.IMAPPollInterval = strings.TrimSpace(m.settingsPollInput)
				if utf8.RuneCountInString(m.settingsForm.IMAPPollInterval) > maxPollIntervalLen {
					r := []rune(m.settingsForm.IMAPPollInterval)
					m.settingsForm.IMAPPollInterval = string(r[:maxPollIntervalLen])
				}
			}
			return m, nil, true
		case "backspace":
			if m.settingsSelected == 2 {
				m.settingsDomainsInput = trimLastRune(m.settingsDomainsInput)
			} else if m.settingsSelected == 3 {
				m.settingsActiveInput = trimLastRune(m.settingsActiveInput)
			} else if m.settingsSelected == 4 {
				m.settingsTZInput = trimLastRune(m.settingsTZInput)
			} else if m.settingsSelected == 5 {
				m.settingsLogPathInput = trimLastRune(m.settingsLogPathInput)
			} else if m.settingsSelected == 6 {
				m.settingsPollInput = trimLastRune(m.settingsPollInput)
			} else {
				m.settingsMethodInput = trimLastRune(m.settingsMethodInput)
			}
			return m, nil, true
		default:
			if len(msg.Runes) > 0 {
				r := msg.Runes[0]
				if r >= 32 && r != 127 {
					if m.settingsSelected == 2 {
						if utf8.RuneCountInString(m.settingsDomainsInput) < maxDomainsTextLen {
							m.settingsDomainsInput += string(r)
						}
					} else if m.settingsSelected == 3 {
						if utf8.RuneCountInString(m.settingsActiveInput) < maxActiveDomainLen {
							m.settingsActiveInput += string(r)
						}
					} else if m.settingsSelected == 4 {
						if utf8.RuneCountInString(m.settingsTZInput) < maxTimezoneLen {
							m.settingsTZInput += string(r)
						}
					} else if m.settingsSelected == 5 {
						if utf8.RuneCountInString(m.settingsLogPathInput) < maxLogPathLen {
							m.settingsLogPathInput += string(r)
						}
					} else if m.settingsSelected == 6 {
						if utf8.RuneCountInString(m.settingsPollInput) < maxPollIntervalLen {
							m.settingsPollInput += string(r)
						}
					} else if utf8.RuneCountInString(m.settingsMethodInput) < maxClipboardMethodLen {
						m.settingsMethodInput += string(r)
					}
				}
			}
			return m, nil, true
		}
	}

	switch key {
	case "up", "k":
		if m.settingsSelected > 0 {
			m.settingsSelected--
		}
		return m, nil, true
	case "down", "j":
		if m.settingsSelected < 6 {
			m.settingsSelected++
		}
		return m, nil, true
	case " ":
		if m.settingsSelected == 0 {
			m.settingsForm.ClipboardEnabled = !m.settingsForm.ClipboardEnabled
			return m, nil, true
		}
		m.settingsEditing = true
		if m.settingsSelected == 2 {
			m.settingsDomainsInput = m.settingsForm.DomainsText
		} else if m.settingsSelected == 3 {
			m.settingsActiveInput = m.settingsForm.ActiveDomain
		} else if m.settingsSelected == 4 {
			m.settingsTZInput = m.settingsForm.Timezone
		} else if m.settingsSelected == 5 {
			m.settingsLogPathInput = m.settingsForm.LogPath
		} else if m.settingsSelected == 6 {
			m.settingsPollInput = m.settingsForm.IMAPPollInterval
		} else {
			m.settingsMethodInput = m.settingsForm.ClipboardMethod
		}
		return m, nil, true
	case "e":
		if m.settingsSelected == 0 {
			m.settingsForm.ClipboardEnabled = !m.settingsForm.ClipboardEnabled
			return m, nil, true
		}
		m.settingsEditing = true
		if m.settingsSelected == 2 {
			m.settingsDomainsInput = m.settingsForm.DomainsText
		} else if m.settingsSelected == 3 {
			m.settingsActiveInput = m.settingsForm.ActiveDomain
		} else if m.settingsSelected == 4 {
			m.settingsTZInput = m.settingsForm.Timezone
		} else if m.settingsSelected == 5 {
			m.settingsLogPathInput = m.settingsForm.LogPath
		} else if m.settingsSelected == 6 {
			m.settingsPollInput = m.settingsForm.IMAPPollInterval
		} else {
			m.settingsMethodInput = m.settingsForm.ClipboardMethod
		}
		return m, nil, true
	case "enter":
		if !m.settingsLoaded || m.settingsLoading {
			toastCmd := showToast(&m, ToastWarning, "settings not loaded yet")
			return m, toastCmd, true
		}
		m.settingsSaving = true
		toastCmd := showToast(&m, ToastInfo, "saving settings...")
		return m, tea.Batch(toastCmd, m.saveSettingsCmd()), true
	case "S":
		if !m.settingsLoaded || m.settingsLoading {
			toastCmd := showToast(&m, ToastWarning, "settings not loaded yet")
			return m, toastCmd, true
		}
		m.settingsSaving = true
		toastCmd := showToast(&m, ToastInfo, "saving settings...")
		return m, tea.Batch(toastCmd, m.saveSettingsCmd()), true
	case "r":
		if !m.settingsLoaded {
			m.settingsLoading = true
			toastCmd := showToast(&m, ToastInfo, "loading settings...")
			return m, tea.Batch(toastCmd, m.loadSettingsCmd()), true
		}
		m.settingsForm = m.settingsOriginal
		m.settingsMethodInput = m.settingsForm.ClipboardMethod
		m.settingsDomainsInput = m.settingsForm.DomainsText
		m.settingsActiveInput = m.settingsForm.ActiveDomain
		m.settingsTZInput = m.settingsForm.Timezone
		m.settingsLogPathInput = m.settingsForm.LogPath
		m.settingsPollInput = m.settingsForm.IMAPPollInterval
		m.settingsEditing = false
		toastCmd := showToast(&m, ToastInfo, "settings reset")
		return m, toastCmd, true
	}

	return m, nil, false
}

func (m Model) mailAccountPanelView(w int) string {
	th := newTheme()

	inner := w - 4
	if inner < 10 {
		inner = 10
	}

	// ── Create form ──────────────────────────────────────────────────────────
	if m.creating {
		activeFieldSt := lipgloss.NewStyle().
			Bold(true).Foreground(th.accent).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(th.accent).
			Width(inner - 14)

		emailLabel := th.mutedStyle.Render("  email     ")
		emailValue := m.createAliasEmail + "▌"
		emailField := activeFieldSt.Render(emailValue)

		hint := th.mutedStyle.Render("  local-part ok (auto @active)  enter submit  esc cancel")

		return strings.Join([]string{
			th.accentStyle.Render("  ✚ New Mail Account"),
			"",
			emailLabel + emailField,
			"",
			hint,
		}, "\n")
	}

	// ── Delete confirm ───────────────────────────────────────────────────────
	if m.cfDeleteConfirm {
		ruleLabel := ""
		if len(m.cfRules) > 0 && m.cfSelected >= 0 && m.cfSelected < len(m.cfRules) {
			rule := m.cfRules[m.cfSelected]
			ruleLabel = truncate(rule.AliasEmail, inner-2)
			if ruleLabel == "" {
				ruleLabel = truncate(rule.Name, inner-2)
			}
		}
		return strings.Join([]string{
			th.errStyle.Render("  ⚠  Delete Mail Account?"),
			"",
			"  " + lipgloss.NewStyle().Foreground(th.fg).Render(ruleLabel),
			"",
			th.successStyle.Render("  y") + th.mutedStyle.Render(" confirm  ") +
				th.errStyle.Render("n") + th.mutedStyle.Render(" cancel"),
		}, "\n")
	}

	// ── Empty state ──────────────────────────────────────────────────────────
	activeDomain := ""
	lines := make([]string, 0, len(m.cfRules)+3)
	if m.rulesManager != nil {
		activeDomain = strings.TrimSpace(m.rulesManager.ActiveDomain())
	}
	if activeDomain != "" {
		lines = append(lines, th.mutedStyle.Render("  active domain: "+activeDomain))
		lines = append(lines, "")
	}

	if len(m.cfRules) == 0 {
		return lipgloss.NewStyle().
			Foreground(th.muted).
			Italic(true).
			Render("  no mail accounts  ·  press n")
	}

	// ── Mail Account list ────────────────────────────────────────────────────
	for i, rule := range m.cfRules {
		cursor := "  "
		emailSt := lipgloss.NewStyle().Foreground(th.muted)

		if i == m.cfSelected {
			cursor = th.accentStyle.Render("▶ ")
			emailSt = lipgloss.NewStyle().Foreground(th.fg)
		}

		dot := th.successStyle.Render("●")
		if !rule.Enabled {
			dot = th.mutedStyle.Render("○")
		}

		label := rule.AliasEmail
		if label == "" {
			label = rule.Name
		}

		line := cursor + dot + " " +
			emailSt.Render(truncate(label, inner-4))
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (m Model) updateOTPPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}

	if m.otpDeleteMode {
		switch key {
		case "y", "enter":
			scope := m.otpDeleteScope
			m.otpDeleteMode = false
			m.otpDeleteScope = ""
			switch scope {
			case "selected":
				if len(m.otpEvents) == 0 {
					toastCmd := showToast(&m, ToastWarning, "no otp to clear")
					return m, toastCmd, true
				}
				idx := m.otpSelected
				if idx < 0 || idx >= len(m.otpEvents) {
					idx = 0
				}
				toastCmd := showToast(&m, ToastInfo, "clearing selected otp...")
				return m, tea.Batch(toastCmd, m.clearOTPByIDCmd(m.otpEvents[idx].ID)), true
			case "filtered":
				toastCmd := showToast(&m, ToastInfo, "clearing filtered otp...")
				return m, tea.Batch(toastCmd, m.clearOTPByScopeCmd("filtered")), true
			case "all":
				toastCmd := showToast(&m, ToastInfo, "clearing all otp...")
				return m, tea.Batch(toastCmd, m.clearOTPByScopeCmd("all")), true
			default:
				return m, nil, true
			}
		case "n", "esc":
			m.otpDeleteMode = false
			m.otpDeleteScope = ""
			toastCmd := showToast(&m, ToastInfo, "clear cancelled")
			return m, toastCmd, true
		default:
			return m, nil, true
		}
	}

	if m.otpSearchMode {
		switch key {
		case "esc":
			m.otpSearchMode = false
			m.otpSearchInput = ""
			// No toast — search cancel is obvious from the UI
			return m, nil, true
		case "enter":
			m.otpSearchMode = false
			m.otpSearchQuery = strings.TrimSpace(m.otpSearchInput)
			m.otpSearchInput = ""
			reqAt := time.Now().UTC()
			m.otpLastReqAt = reqAt
			return m, m.refreshOTPHistoryCmdAt(m.otpSearchQuery, reqAt), true
		case "backspace":
			m.otpSearchInput = trimLastRune(m.otpSearchInput)
			return m, nil, true
		default:
			if len(msg.Runes) > 0 {
				r := msg.Runes[0]
				if r >= 32 && r != 127 && utf8.RuneCountInString(m.otpSearchInput) < maxOTPQueryLen {
					m.otpSearchInput += string(r)
				}
			}
			return m, nil, true
		}
	}

	switch key {
	case "c":
		if len(m.otpEvents) == 0 {
			toastCmd := showToast(&m, ToastWarning, "no otp to copy")
			return m, toastCmd, true
		}
		if m.clipboard == nil {
			toastCmd := showToast(&m, ToastWarning, "clipboard unavailable")
			return m, toastCmd, true
		}
		idx := m.otpSelected
		if idx < 0 || idx >= len(m.otpEvents) {
			idx = 0
		}
		evt := m.otpEvents[idx]
		toastCmd := showToast(&m, ToastInfo, "copying otp...")
		return m, tea.Batch(toastCmd, m.copyOTPCmd(evt.OTPCode)), true
	case "x":
		if len(m.otpEvents) == 0 {
			toastCmd := showToast(&m, ToastWarning, "no otp to clear")
			return m, toastCmd, true
		}
		if m.otpManager == nil {
			toastCmd := showToast(&m, ToastWarning, "otp manager unavailable")
			return m, toastCmd, true
		}
		m.otpDeleteMode = true
		m.otpDeleteScope = "selected"
		return m, nil, true
	case "X":
		if strings.TrimSpace(m.otpSearchQuery) == "" {
			toastCmd := showToast(&m, ToastWarning, "no active filter to clear")
			return m, toastCmd, true
		}
		if m.otpManager == nil {
			toastCmd := showToast(&m, ToastWarning, "otp manager unavailable")
			return m, toastCmd, true
		}
		m.otpDeleteMode = true
		m.otpDeleteScope = "filtered"
		return m, nil, true
	case "C":
		if m.otpManager == nil {
			toastCmd := showToast(&m, ToastWarning, "otp manager unavailable")
			return m, toastCmd, true
		}
		m.otpDeleteMode = true
		m.otpDeleteScope = "all"
		return m, nil, true
	case "/":
		m.otpSearchMode = true
		m.otpSearchInput = m.otpSearchQuery
		// No toast — search mode is obvious from the input cursor
		return m, nil, true
	case "esc":
		if m.otpSearchQuery == "" {
			return m, nil, true
		}
		m.otpSearchQuery = ""
		toastCmd := showToast(&m, ToastInfo, "filter cleared")
		reqAt := time.Now().UTC()
		m.otpLastReqAt = reqAt
		return m, tea.Batch(toastCmd, m.refreshOTPHistoryCmdAt("", reqAt)), true
	case "up", "k":
		if len(m.otpEvents) > 0 && m.otpSelected > 0 {
			m.otpSelected--
		}
		return m, nil, true
	case "down", "j":
		if len(m.otpEvents) > 0 && m.otpSelected < len(m.otpEvents)-1 {
			m.otpSelected++
		}
		return m, nil, true
	}

	return m, nil, false
}

func (m Model) updateLogsPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}

	switch key {
	case "up", "k":
		maxScroll := len(m.logLines) - 1
		if maxScroll < 0 {
			maxScroll = 0
		}
		if m.logScroll < maxScroll {
			m.logScroll++
			m.logAutoScroll = false
		}
		return m, nil, true
	case "down", "j":
		if m.logScroll > 0 {
			m.logScroll--
			if m.logScroll == 0 {
				m.logAutoScroll = true
			}
		}
		return m, nil, true
	case "G":
		m.logScroll = 0
		m.logAutoScroll = true
		return m, nil, true
	}

	return m, nil, false
}

func (m Model) opContext() context.Context {
	if m.opParentCtx != nil {
		return m.opParentCtx
	}

	return context.Background()
}

// truncate clips a string to max display width (terminal cells),
// appending "…" when truncated.
func truncate(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	return xansi.Truncate(s, maxW, "…")
}

// clampLines truncates s to at most maxLines lines.
func clampLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	return strings.Join(lines[:maxLines], "\n")
}

func canonicalDomainsText(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := strings.FieldsFunc(v, func(r rune) bool {
		switch r {
		case '\n', ';', '|':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

// fitBlock ensures a rendered block fits exactly inside width x height.
func fitBlock(s string, maxW, maxH int, bg lipgloss.Color) string {
	if maxW <= 0 || maxH <= 0 {
		return ""
	}

	lines := strings.Split(s, "\n")
	if len(lines) > maxH {
		lines = lines[:maxH]
	}

	padBg := lipgloss.NewStyle().Background(bg)
	for i := range lines {
		if lipgloss.Width(lines[i]) > maxW {
			lines[i] = xansi.Truncate(lines[i], maxW, "")
		}
		w := lipgloss.Width(lines[i])
		if w < maxW {
			lines[i] += padBg.Render(strings.Repeat(" ", maxW-w))
		}
	}

	blank := lipgloss.NewStyle().Background(bg).Width(maxW).Render("")
	for len(lines) < maxH {
		lines = append(lines, blank)
	}

	return strings.Join(lines, "\n")
}

func trimLastRune(v string) string {
	r := []rune(v)
	if len(r) == 0 {
		return v
	}

	return string(r[:len(r)-1])
}

func userSafeError(action string, err error) string {
	if strings.TrimSpace(action) == "" {
		action = "operation"
	}
	if err == nil {
		return action + " failed"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return action + " timed out"
	}
	if errors.Is(err, context.Canceled) {
		return action + " cancelled"
	}

	return action + " failed"
}

func normalizeHealthStatus(in HealthStatus) HealthStatus {
	return HealthStatus{
		Cloudflare:  normalizeHealthValue(in.Cloudflare, "unknown"),
		Destination: normalizeHealthValue(in.Destination, "unknown"),
		Mailbox:     normalizeHealthValue(in.Mailbox, "unknown"),
		Parser:      normalizeHealthValue(in.Parser, "ready"),
	}
}

func normalizeHealthValue(v, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return fallback
	}

	return v
}

func sanitizeMailboxMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return "unknown"
	}
	switch m {
	case "idle", "poll", "reconnect", "reconnecting", "connected", "watching":
		return m
	default:
		return "unknown"
	}
}

// formatLogLine parses a JSONL log line and renders it with colored level,
// timestamp, event, and message. Falls back to showing the raw line in muted
// style if the line is not valid JSON.
func formatLogLine(raw string, th theme, maxW int) string {
	if maxW <= 0 {
		return ""
	}

	type logEntry struct {
		Ts     string         `json:"ts"`
		Level  string         `json:"level"`
		Event  string         `json:"event"`
		Msg    string         `json:"msg"`
		Fields map[string]any `json:"fields"`
	}

	var entry logEntry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		// Not valid JSON — show raw line muted
		return "  " + th.mutedStyle.Render(truncate(raw, maxW-2))
	}

	// Parse timestamp — show HH:MM:SS
	ts := entry.Ts
	if t, err := time.Parse(time.RFC3339Nano, entry.Ts); err == nil {
		ts = t.Format("15:04:05")
	}

	// Colorize level
	var levelStr string
	switch strings.ToLower(entry.Level) {
	case "error":
		levelStr = th.errStyle.Render("ERR")
	case "warn":
		levelStr = th.warnStyle.Render("WRN")
	default:
		levelStr = th.successStyle.Render("INF")
	}

	tsStr := th.purpleStyle.Render(ts)
	eventStr := th.accentStyle.Copy().Bold(false).Render(truncate(entry.Event, 30))
	msgW := maxW - 50
	if msgW < 10 {
		msgW = 10
	}
	msgStr := th.base.Render(truncate(entry.Msg, msgW))

	return fmt.Sprintf("  %s %s %s %s", tsStr, levelStr, eventStr, msgStr)
}
