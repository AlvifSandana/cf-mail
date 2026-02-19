package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

const (
	defaultOTPHistoryLimit = 50
	maxOTPQueryLen         = 120
	maxAliasEmailLen       = 320
	sidebarWidth           = 42
)

type Panel int

const (
	PanelStatus Panel = iota
	PanelMailAccount
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
}

type otpManager interface {
	ListOTPEvents(ctx context.Context, filter app.OTPListFilter) ([]domain.OTPEvent, error)
}

type rulesManager interface {
	ListRoutingRules(ctx context.Context) ([]ports.RoutingRule, error)
	CreateRoutingRuleDirect(ctx context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error)
	UpdateRoutingRule(ctx context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error)
	DeleteRoutingRuleByID(ctx context.Context, ruleID string) error
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

	// Full-screen root — no extra padding, we control every pixel
	root := lipgloss.NewStyle().
		Background(th.bg).
		Foreground(th.fg)

	totalH := m.Height
	if totalH <= 0 {
		totalH = 24
	}
	totalW := m.Width
	if totalW <= 0 {
		totalW = 100
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
	if bodyH < 4 {
		bodyH = 4
	}

	// ── Sidebar (right): Health + Mail Account ──────────────────────────
	sbW := sidebarWidth
	mainW := totalW - sbW - 1 // 1 col for border separator
	if mainW < 20 {
		// Narrow terminal: shrink sidebar to give main content room
		sbW = totalW - 20 - 1
		if sbW < 20 {
			sbW = 20
		}
		mainW = totalW - sbW - 1
		if mainW < 10 {
			mainW = 10
		}
	}

	sidebar := m.renderSidebar(th, sbW, bodyH)

	// ── Main content (left): OTP + Logs ─────────────────────────────────
	mainContent := m.renderMainContent(th, mainW, bodyH)

	// ── Body row ─────────────────────────────────────────────────────────
	body := lipgloss.JoinHorizontal(lipgloss.Top, mainContent, sidebar)

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
		rendered = strings.Join(lines, "\n")
	}

	return rendered
}

// ── Top bar: logo + clock + tab bar ──────────────────────────────────────────

func (m Model) renderTopBar(th theme, totalW int) string {
	// Logo (single line, compact)
	logo := th.accentStyle.Render("⚡ TUIOTP") +
		th.mutedStyle.Render("  OTP Dashboard · Cloudflare Email · IMAP")

	now := time.Now().UTC().Format("15:04:05 UTC")
	clock := lipgloss.NewStyle().
		Foreground(th.accent).
		Render(now)

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
	pad := ""
	if totalW > tabW {
		pad = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).
			Render(strings.Repeat(" ", totalW-tabW))
	}

	return tabRow + pad
}

// ── Sidebar (right): Health card stacked on Mail Account card ────────────────

func (m Model) renderSidebar(th theme, w, totalH int) string {
	innerW := w - 2 // account for left border char + gap

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
	hint := th.mutedStyle.Render("  n new  e toggle  d del  r refresh  ↑↓ nav")

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

// ── Main content (left): OTP panel on top, Logs below ────────────────────────

func (m Model) renderMainContent(th theme, w, totalH int) string {
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
		{"o", "otp"},
		{"l", "logs"},
		{"/", "search"},
		{"c", "copy"},
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
		col("/", "search OTP events"),
		col("G", "jump to bottom (logs)"),
		col("c", "copy selected OTP"),
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

	lines = append(lines, sep)
	lines = append(lines, th.mutedStyle.Render("  ↑↓/jk nav  ·  c copy  ·  / search  ·  esc clear"))

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

		rule, err := m.rulesManager.CreateRoutingRuleDirect(ctx, ports.CreateRoutingRuleInput{
			AliasEmail: strings.TrimSpace(aliasEmail),
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

		hint := th.mutedStyle.Render("  enter submit  esc cancel")

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
	if len(m.cfRules) == 0 {
		return lipgloss.NewStyle().
			Foreground(th.muted).
			Italic(true).
			Render("  no mail accounts  ·  press n")
	}

	// ── Mail Account list ────────────────────────────────────────────────────
	lines := make([]string, 0, len(m.cfRules)+1)

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

// truncate clips a string to maxW runes, appending "…" if it was cut.
func truncate(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	return string(r[:maxW-1]) + "…"
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
