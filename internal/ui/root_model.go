package ui

import (
	"context"
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
	maxAliasPlatformLen    = 32
	maxAliasEmailLen       = 320
	sidebarWidth           = 42
)

type Panel int

const (
	PanelStatus Panel = iota
	PanelAliases
	PanelLatestOTP
	PanelLogs
	panelCount
)

type Model struct {
	ActivePanel Panel
	ShowHelp    bool
	Width       int
	Height      int

	LastAction string
	ErrorMsg   string
	health     HealthStatus

	aliasManager aliasManager
	otpManager   otpManager
	rulesManager rulesManager
	clipboard    clipboardCopier
	opParentCtx  context.Context
	opTimeout    time.Duration
	cfOpTimeout  time.Duration
	aliases      []domain.Alias
	selected     int

	creating         bool
	createPlatform   string
	createAliasEmail string
	createField      int

	deleteConfirm bool

	// CF routing rules management
	showCFRules     bool
	cfRules         []ports.RoutingRule
	cfSelected      int
	cfDeleteConfirm bool

	otpEvents      []domain.OTPEvent
	otpSelected    int
	otpSearchMode  bool
	otpSearchInput string
	otpSearchQuery string
	otpLastReqAt   time.Time
}

type aliasManager interface {
	ListAliases(ctx context.Context) ([]domain.Alias, error)
	CreateAlias(ctx context.Context, in app.CreateAliasInput) (domain.Alias, error)
	DeleteAlias(ctx context.Context, aliasEmail string) error
}

type otpManager interface {
	ListOTPEvents(ctx context.Context, filter app.OTPListFilter) ([]domain.OTPEvent, error)
}

type rulesManager interface {
	ListRoutingRules(ctx context.Context) ([]ports.RoutingRule, error)
	UpdateRoutingRule(ctx context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error)
	DeleteRoutingRuleByID(ctx context.Context, ruleID string) error
}

type clipboardCopier = ports.Clipboard

type ModelConfig struct {
	AliasManager aliasManager
	OTPManager   otpManager
	RulesManager rulesManager
	Clipboard    clipboardCopier
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
		ActivePanel:  PanelStatus,
		ShowHelp:     false,
		LastAction:   "ready",
		health:       normalizeHealthStatus(cfg.Health),
		aliasManager: cfg.AliasManager,
		otpManager:   cfg.OTPManager,
		rulesManager: cfg.RulesManager,
		clipboard:    cfg.Clipboard,
		opParentCtx:  cfg.ParentCtx,
		opTimeout:    cfg.OpTimeout,
		cfOpTimeout:  cfg.CFOpTimeout,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 3)
	if m.aliasManager != nil {
		cmds = append(cmds, m.refreshAliasesCmd())
	}
	if m.otpManager != nil {
		cmds = append(cmds, m.refreshOTPHistoryCmd(m.otpSearchQuery))
	}
	if m.rulesManager != nil {
		cmds = append(cmds, m.refreshCFRulesCmd())
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
	case aliasesLoadedMsg:
		if msg.err != nil {
			m.ErrorMsg = userSafeError("refresh aliases", msg.err)
			m.LastAction = "refresh failed"
			return m, nil
		}

		m.ErrorMsg = ""
		m.aliases = msg.aliases
		if len(m.aliases) == 0 {
			m.selected = 0
		} else if m.selected >= len(m.aliases) {
			m.selected = len(m.aliases) - 1
		}
		m.LastAction = fmt.Sprintf("aliases refreshed (%d)", len(m.aliases))
		return m, nil

	case aliasCreatedMsg:
		if msg.err != nil {
			m.ErrorMsg = userSafeError("create alias", msg.err)
			m.LastAction = "create alias failed"
			return m, nil
		}

		m.creating = false
		m.createField = 0
		m.createPlatform = ""
		m.createAliasEmail = ""
		m.ErrorMsg = ""
		m.LastAction = "alias created"
		return m, m.refreshAliasesCmd()

	case aliasDeletedMsg:
		if msg.err != nil {
			m.deleteConfirm = false
			m.ErrorMsg = userSafeError("delete alias", msg.err)
			m.LastAction = "delete alias failed"
			return m, nil
		}

		m.deleteConfirm = false
		m.ErrorMsg = ""
		m.LastAction = "alias deleted"
		return m, m.refreshAliasesCmd()

	case otpHistoryLoadedMsg:
		if !m.otpLastReqAt.IsZero() && msg.reqAt.Before(m.otpLastReqAt) {
			return m, nil
		}

		if msg.err != nil {
			m.ErrorMsg = userSafeError("refresh otp history", msg.err)
			m.LastAction = "otp refresh failed"
			return m, nil
		}

		m.ErrorMsg = ""
		m.otpEvents = msg.events
		if len(m.otpEvents) == 0 {
			m.otpSelected = 0
		} else if m.otpSelected >= len(m.otpEvents) {
			m.otpSelected = len(m.otpEvents) - 1
		}
		m.otpSearchQuery = msg.query
		m.LastAction = fmt.Sprintf("otp refreshed (%d)", len(m.otpEvents))
		return m, nil

	case otpCopiedMsg:
		if msg.err != nil {
			m.ErrorMsg = userSafeError("copy otp", msg.err)
			m.LastAction = "copy otp failed"
			return m, nil
		}
		m.ErrorMsg = ""
		m.LastAction = "otp copied"
		return m, nil

	case cfRulesLoadedMsg:
		if msg.err != nil {
			m.ErrorMsg = userSafeError("refresh cf rules", msg.err)
			m.LastAction = "cf rules refresh failed"
			return m, nil
		}
		m.ErrorMsg = ""
		m.cfRules = msg.rules
		if len(m.cfRules) == 0 {
			m.cfSelected = 0
		} else if m.cfSelected >= len(m.cfRules) {
			m.cfSelected = len(m.cfRules) - 1
		}
		m.LastAction = fmt.Sprintf("cf rules refreshed (%d)", len(m.cfRules))
		return m, nil

	case cfRuleUpdatedMsg:
		if msg.err != nil {
			m.ErrorMsg = userSafeError("toggle cf rule", msg.err)
			m.LastAction = "toggle cf rule failed"
			return m, nil
		}
		m.ErrorMsg = ""
		status := "disabled"
		if msg.rule.Enabled {
			status = "enabled"
		}
		m.LastAction = fmt.Sprintf("cf rule %s", status)
		return m, m.refreshCFRulesCmd()

	case cfRuleDeletedMsg:
		if msg.err != nil {
			m.cfDeleteConfirm = false
			m.ErrorMsg = userSafeError("delete cf rule", msg.err)
			m.LastAction = "delete cf rule failed"
			return m, nil
		}
		m.cfDeleteConfirm = false
		m.ErrorMsg = ""
		m.LastAction = "cf rule deleted"
		return m, m.refreshCFRulesCmd()

	case app.RuntimeEvent:
		switch msg.Type {
		case app.RuntimeEventWatcherUpdate:
			mode := "watching"
			if msg.Watch != nil && strings.TrimSpace(msg.Watch.Mode) != "" {
				mode = "watching(" + sanitizeMailboxMode(msg.Watch.Mode) + ")"
			}
			m.health.Mailbox = mode
			m.LastAction = "mailbox runtime update"
			return m, nil
		case app.RuntimeEventRuntimeError:
			m.health.Mailbox = "error"
			if strings.TrimSpace(msg.Err) != "" {
				m.ErrorMsg = userSafeError("mailbox runtime", errors.New(msg.Err))
			}
			m.LastAction = "mailbox runtime error"
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.ActivePanel == PanelAliases {
			if m.showCFRules {
				updated, cmd, handled := m.updateCFRulesPanel(msg)
				if handled {
					return updated, cmd
				}
			} else {
				updated, cmd, handled := m.updateAliasesPanel(msg)
				if handled {
					return updated, cmd
				}
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
			m.LastAction = "refresh requested"
			return m.nextRefreshAllCmd()
		case "tab":
			m.ActivePanel = (m.ActivePanel + 1) % panelCount
			return m, nil
		case "o":
			m.ActivePanel = PanelLatestOTP
			m.LastAction = "otp panel"
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

	// Reserve rows: topBar + footer + help (if open)
	bodyH := totalH - topBarH - footerH - helpH
	if bodyH < 4 {
		bodyH = 4
	}

	// ── Sidebar (right): Health + Aliases ────────────────────────────────
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

	// Tab bar (only OTP and Logs — health & aliases are in sidebar)
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
		{PanelAliases, "Aliases", "⊞"},
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

// ── Sidebar (right): Health card stacked on Aliases card ─────────────────────

func (m Model) renderSidebar(th theme, w, totalH int) string {
	innerW := w - 2 // account for left border char + gap

	// Health card (fixed height ~9, includes its own border)
	healthCard := m.renderHealthCard(th, innerW)
	healthH := lipgloss.Height(healthCard)

	// Aliases card takes remaining height.
	// aliasCard uses RoundedBorder (+2 rows outside content Height),
	// so we subtract 2 to keep the total sidebar within totalH.
	const aliasBorderRows = 2
	aliasContentH := totalH - healthH - aliasBorderRows
	if aliasContentH < 4 {
		aliasContentH = 4
	}
	aliasCard := m.renderAliasCard(th, innerW, aliasContentH)

	sidebar := lipgloss.JoinVertical(lipgloss.Left, healthCard, aliasCard)

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

	// Feedback / last-action line
	feedback := " " + m.feedbackBanner(th)

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w)

	if m.ActivePanel == PanelStatus {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	inner := strings.Join(append([]string{title, ""}, append(rows, "", feedback)...), "\n")
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

func (m Model) renderAliasCard(th theme, w, h int) string {
	titleSt := lipgloss.NewStyle().
		Bold(true).Foreground(th.fg).
		Background(th.bgAlt).
		Width(w).
		Padding(0, 1)

	var title, body, hint string

	if m.showCFRules {
		title = titleSt.Render("☁  CF Rules  " + th.mutedStyle.Render("live"))
		body = m.cfRulesPanelView(w)
		hint = th.mutedStyle.Render("  s back  e toggle  d del  r refresh  ↑↓ nav")
	} else {
		title = titleSt.Render("⊞ Aliases")
		body = m.aliasPanelView(w)
		hint = th.mutedStyle.Render("  n new  d del  s cf rules  ↑↓ nav")
	}

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelAliases {
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
	title := titleSt.Render("≡ Logs  " + th.mutedStyle.Render("runtime log stream"))

	sep := th.mutedStyle.Render(strings.Repeat("─", w-4))

	placeholder := lipgloss.NewStyle().
		Foreground(th.muted).
		Italic(true).
		Render("  coming soon — logs will stream here")

	cardSt := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("238")).
		Width(w).
		Height(h)

	if m.ActivePanel == PanelLogs {
		cardSt = cardSt.BorderForeground(th.accent)
	}

	inner := strings.Join([]string{title, sep, "", placeholder}, "\n")
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
		{"/", "search"},
		{"c", "copy"},
		{"s", "cf rules"},
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
		col("/", "search OTP events"),
		col("c", "copy selected OTP"),
		col("n", "create alias (aliases panel)"),
		col("d", "delete alias / cf rule"),
		col("s", "toggle cf rules view"),
		col("e", "toggle cf rule enable/disable"),
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

// feedbackBanner renders the last-action / error feedback row.
func (m Model) feedbackBanner(th theme) string {
	if strings.TrimSpace(m.ErrorMsg) != "" {
		return th.errStyle.
			Copy().
			Background(lipgloss.Color("52")).
			Padding(0, 1).
			Render("✖  " + m.ErrorMsg)
	}
	action := strings.TrimSpace(m.LastAction)
	if action == "" {
		action = "ready"
	}
	if strings.Contains(strings.ToLower(action), "refresh") || strings.Contains(strings.ToLower(action), "loading") {
		return th.warnStyle.Render("↻  " + action)
	}
	if strings.Contains(strings.ToLower(action), "copied") {
		return th.successStyle.
			Copy().
			Background(lipgloss.Color("22")).
			Padding(0, 1).
			Render("✔  " + action)
	}
	return th.successStyle.Render("✔  " + action)
}

func panelLabel(p Panel) string {
	switch p {
	case PanelStatus:
		return "status"
	case PanelAliases:
		return "aliases"
	case PanelLatestOTP:
		return "latest_otp"
	case PanelLogs:
		return "logs"
	default:
		return "unknown"
	}
}

type aliasesLoadedMsg struct {
	aliases []domain.Alias
	err     error
}

type aliasCreatedMsg struct {
	err error
}

type aliasDeletedMsg struct {
	err error
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

type cfRuleUpdatedMsg struct {
	rule ports.RoutingRule
	err  error
}

type cfRuleDeletedMsg struct {
	err error
}

func (m Model) refreshAliasesCmd() tea.Cmd {
	if m.aliasManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
		defer cancel()

		rows, err := m.aliasManager.ListAliases(ctx)
		if err != nil {
			return aliasesLoadedMsg{err: err}
		}
		return aliasesLoadedMsg{aliases: rows}
	}
}

func (m Model) createAliasCmd(platform, aliasEmail string) tea.Cmd {
	if m.aliasManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()

		_, err := m.aliasManager.CreateAlias(ctx, app.CreateAliasInput{
			Platform:   strings.TrimSpace(platform),
			AliasEmail: strings.TrimSpace(aliasEmail),
		})
		return aliasCreatedMsg{err: err}
	}
}

func (m Model) deleteAliasCmd(aliasEmail string) tea.Cmd {
	if m.aliasManager == nil {
		return nil
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.opContext(), m.cfOpTimeout)
		defer cancel()

		err := m.aliasManager.DeleteAlias(ctx, strings.TrimSpace(aliasEmail))
		return aliasDeletedMsg{err: err}
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

	cmds := make([]tea.Cmd, 0, 3)
	if cmd := m.refreshAliasesCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
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

func (m Model) updateAliasesPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}

	if m.deleteConfirm {
		switch key {
		case "y", "enter":
			if len(m.aliases) == 0 || m.selected < 0 || m.selected >= len(m.aliases) {
				m.deleteConfirm = false
				m.ErrorMsg = "invalid alias selection"
				m.LastAction = "delete alias failed"
				return m, nil, true
			}
			alias := m.aliases[m.selected]
			m.LastAction = "deleting alias..."
			return m, m.deleteAliasCmd(alias.AliasEmail), true
		case "n", "esc":
			m.deleteConfirm = false
			m.LastAction = "delete cancelled"
			return m, nil, true
		default:
			return m, nil, true
		}
	}

	if m.creating {
		switch key {
		case "esc":
			m.creating = false
			m.createPlatform = ""
			m.createAliasEmail = ""
			m.createField = 0
			m.LastAction = "create alias cancelled"
			return m, nil, true
		case "tab":
			m.createField = (m.createField + 1) % 2
			return m, nil, true
		case "enter":
			if m.createField == 0 {
				m.createField = 1
				return m, nil, true
			}
			if strings.TrimSpace(m.createPlatform) == "" || strings.TrimSpace(m.createAliasEmail) == "" {
				m.ErrorMsg = "platform and alias email are required"
				return m, nil, true
			}
			m.LastAction = "creating alias..."
			m.ErrorMsg = ""
			return m, m.createAliasCmd(m.createPlatform, m.createAliasEmail), true
		case "backspace":
			if m.createField == 0 {
				m.createPlatform = trimLastRune(m.createPlatform)
			} else {
				m.createAliasEmail = trimLastRune(m.createAliasEmail)
			}
			return m, nil, true
		default:
			if len(msg.Runes) > 0 {
				r := msg.Runes[0]
				if r >= 32 && r != 127 {
					if m.createField == 0 {
						if utf8.RuneCountInString(m.createPlatform) < maxAliasPlatformLen {
							m.createPlatform += string(r)
						}
					} else {
						if utf8.RuneCountInString(m.createAliasEmail) < maxAliasEmailLen {
							m.createAliasEmail += string(r)
						}
					}
				}
			}
			return m, nil, true
		}
	}

	switch key {
	case "n":
		if m.aliasManager == nil {
			m.ErrorMsg = "alias service unavailable"
			return m, nil, true
		}
		m.creating = true
		m.createPlatform = ""
		m.createAliasEmail = ""
		m.createField = 0
		m.ErrorMsg = ""
		m.LastAction = "create alias form"
		return m, nil, true
	case "s":
		if m.rulesManager == nil {
			m.ErrorMsg = "rules manager unavailable"
			return m, nil, true
		}
		m.showCFRules = true
		m.ErrorMsg = ""
		m.LastAction = "cf rules view"
		return m, m.refreshCFRulesCmd(), true
	case "d":
		if len(m.aliases) == 0 {
			m.LastAction = "no alias to delete"
			return m, nil, true
		}
		if m.aliasManager == nil {
			m.ErrorMsg = "alias service unavailable"
			return m, nil, true
		}
		m.deleteConfirm = true
		m.LastAction = "confirm delete alias"
		return m, nil, true
	case "up", "k":
		if len(m.aliases) > 0 && m.selected > 0 {
			m.selected--
		}
		return m, nil, true
	case "down", "j":
		if len(m.aliases) > 0 && m.selected < len(m.aliases)-1 {
			m.selected++
		}
		return m, nil, true
	}

	return m, nil, false
}

func (m Model) aliasPanelView(w int) string {
	th := newTheme()

	// Usable inner width (account for card border + padding)
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
		inactiveFieldSt := lipgloss.NewStyle().
			Foreground(th.fg).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(th.muted).
			Width(inner - 14)

		platLabel := th.mutedStyle.Render("  platform  ")
		emailLabel := th.mutedStyle.Render("  email     ")
		platValue := m.createPlatform + "▌"
		emailValue := m.createAliasEmail + "▌"

		var platField, emailField string
		if m.createField == 0 {
			platField = activeFieldSt.Render(platValue)
			emailField = inactiveFieldSt.Render(emailValue)
		} else {
			platField = inactiveFieldSt.Render(platValue)
			emailField = activeFieldSt.Render(emailValue)
		}

		hint := th.mutedStyle.Render("  enter  tab  esc")

		return strings.Join([]string{
			th.accentStyle.Render("  ✚ New Alias"),
			"",
			platLabel + platField,
			emailLabel + emailField,
			"",
			hint,
		}, "\n")
	}

	// ── Delete confirm ───────────────────────────────────────────────────────
	if m.deleteConfirm {
		alias := ""
		if len(m.aliases) > 0 && m.selected >= 0 && m.selected < len(m.aliases) {
			alias = truncate(m.aliases[m.selected].AliasEmail, inner-2)
		}
		return strings.Join([]string{
			th.errStyle.Render("  ⚠  Delete?"),
			"",
			"  " + lipgloss.NewStyle().Foreground(th.fg).Render(alias),
			"",
			th.successStyle.Render("  y") + th.mutedStyle.Render(" confirm  ") +
				th.errStyle.Render("n") + th.mutedStyle.Render(" cancel"),
		}, "\n")
	}

	// ── Empty state ──────────────────────────────────────────────────────────
	if len(m.aliases) == 0 {
		return lipgloss.NewStyle().
			Foreground(th.muted).
			Italic(true).
			Render("  no aliases  ·  press n")
	}

	// ── Alias list (compact for narrow sidebar) ───────────────────────────────
	lines := make([]string, 0, len(m.aliases)*2+1)

	for i, row := range m.aliases {
		cursor := "  "
		platSt := th.accentStyle.Copy().Bold(false)
		emailSt := lipgloss.NewStyle().Foreground(th.muted)

		if i == m.selected {
			cursor = th.accentStyle.Render("▶ ")
			platSt = th.accentStyle
			emailSt = lipgloss.NewStyle().Foreground(th.fg)
		}

		dot := th.successStyle.Render("●")
		if !row.Enabled {
			dot = th.mutedStyle.Render("○")
		}

		platW := inner / 2
		emailW := inner - platW - 4
		if emailW < 8 {
			emailW = 8
		}

		line := cursor + dot + " " +
			platSt.Render(truncate(row.Platform, platW)) + "  " +
			emailSt.Render(truncate(row.AliasEmail, emailW))
		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

func (m Model) updateCFRulesPanel(msg tea.KeyMsg) (Model, tea.Cmd, bool) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit, true
	}

	if m.cfDeleteConfirm {
		switch key {
		case "y", "enter":
			if len(m.cfRules) == 0 || m.cfSelected < 0 || m.cfSelected >= len(m.cfRules) {
				m.cfDeleteConfirm = false
				m.ErrorMsg = "invalid cf rule selection"
				m.LastAction = "delete cf rule failed"
				return m, nil, true
			}
			rule := m.cfRules[m.cfSelected]
			m.LastAction = "deleting cf rule..."
			return m, m.deleteCFRuleCmd(rule.ID), true
		case "n", "esc":
			m.cfDeleteConfirm = false
			m.LastAction = "delete cf rule cancelled"
			return m, nil, true
		default:
			return m, nil, true
		}
	}

	switch key {
	case "s", "esc":
		m.showCFRules = false
		m.ErrorMsg = ""
		m.LastAction = "aliases view"
		return m, nil, true
	case "e":
		if len(m.cfRules) == 0 {
			m.LastAction = "no cf rule to toggle"
			return m, nil, true
		}
		if m.rulesManager == nil {
			m.ErrorMsg = "rules manager unavailable"
			return m, nil, true
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
		m.LastAction = fmt.Sprintf("%s cf rule...", status)
		return m, m.toggleCFRuleCmd(rule), true
	case "d":
		if len(m.cfRules) == 0 {
			m.LastAction = "no cf rule to delete"
			return m, nil, true
		}
		if m.rulesManager == nil {
			m.ErrorMsg = "rules manager unavailable"
			return m, nil, true
		}
		m.cfDeleteConfirm = true
		m.LastAction = "confirm delete cf rule"
		return m, nil, true
	case "r":
		m.LastAction = "refreshing cf rules..."
		return m, m.refreshCFRulesCmd(), true
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

func (m Model) cfRulesPanelView(w int) string {
	th := newTheme()

	inner := w - 4
	if inner < 10 {
		inner = 10
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
			th.errStyle.Render("  ⚠  Delete CF Rule?"),
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
			Render("  no cf routing rules found")
	}

	// ── CF Rules list ────────────────────────────────────────────────────────
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
			m.LastAction = "otp search cancelled"
			return m, nil, true
		case "enter":
			m.otpSearchMode = false
			m.otpSearchQuery = strings.TrimSpace(m.otpSearchInput)
			m.otpSearchInput = ""
			m.LastAction = "otp search requested"
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
			m.LastAction = "no otp to copy"
			return m, nil, true
		}
		if m.clipboard == nil {
			m.LastAction = "clipboard unavailable"
			return m, nil, true
		}
		idx := m.otpSelected
		if idx < 0 || idx >= len(m.otpEvents) {
			idx = 0
		}
		evt := m.otpEvents[idx]
		m.LastAction = "copying otp..."
		return m, m.copyOTPCmd(evt.OTPCode), true
	case "/":
		m.otpSearchMode = true
		m.otpSearchInput = m.otpSearchQuery
		m.LastAction = "otp search mode"
		return m, nil, true
	case "esc":
		if m.otpSearchQuery == "" {
			return m, nil, true
		}
		m.otpSearchQuery = ""
		m.LastAction = "otp filter cleared"
		reqAt := time.Now().UTC()
		m.otpLastReqAt = reqAt
		return m, m.refreshOTPHistoryCmdAt("", reqAt), true
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
