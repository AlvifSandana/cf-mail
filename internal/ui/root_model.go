package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/app"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

const (
	defaultOTPHistoryLimit = 50
	maxOTPQueryLen         = 120
	maxAliasPlatformLen    = 32
	maxAliasEmailLen       = 320
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
	clipboard    clipboardCopier
	opParentCtx  context.Context
	opTimeout    time.Duration
	aliases      []domain.Alias
	selected     int

	creating         bool
	createPlatform   string
	createAliasEmail string
	createField      int

	deleteConfirm bool

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

type clipboardCopier = ports.Clipboard

type ModelConfig struct {
	AliasManager aliasManager
	OTPManager   otpManager
	Clipboard    clipboardCopier
	Health       HealthStatus
	ParentCtx    context.Context
	OpTimeout    time.Duration
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

	return Model{
		ActivePanel:  PanelStatus,
		ShowHelp:     false,
		LastAction:   "ready",
		health:       normalizeHealthStatus(cfg.Health),
		aliasManager: cfg.AliasManager,
		otpManager:   cfg.OTPManager,
		clipboard:    cfg.Clipboard,
		opParentCtx:  cfg.ParentCtx,
		opTimeout:    cfg.OpTimeout,
	}
}

func (m Model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2)
	if m.aliasManager != nil {
		cmds = append(cmds, m.refreshAliasesCmd())
	}
	if m.otpManager != nil {
		cmds = append(cmds, m.refreshOTPHistoryCmd(m.otpSearchQuery))
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
			updated, cmd, handled := m.updateAliasesPanel(msg)
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

func (m Model) View() string {
	panels := []string{"Status", "Aliases", "Latest OTP", "Logs"}
	for i := range panels {
		if m.ActivePanel == Panel(i) {
			panels[i] = "[" + panels[i] + "]"
		}
	}

	header := "TUIOTP Dashboard (Skeleton)"
	state := fmt.Sprintf("Panel: %s | Last action: %s", panelLabel(m.ActivePanel), m.LastAction)
	keyHints := "Global: q quit | ? help | r refresh | tab switch panel"

	body := strings.Join([]string{
		header,
		state,
		m.errorLine(),
		"",
		"Panels: " + strings.Join(panels, " | "),
		"",
		m.healthLine(),
		"Aliases:",
		m.aliasPanelView(),
		"Latest OTP:",
		m.otpPanelView(),
		"Logs: (coming soon)",
		"",
		keyHints,
	}, "\n")

	if !m.ShowHelp {
		return body
	}

	help := strings.Join([]string{
		"",
		"Help:",
		"- q: quit",
		"- ?: toggle help",
		"- r: refresh",
		"- tab: switch active panel",
	}, "\n")

	return body + help
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
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
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
		ctx, cancel := context.WithTimeout(m.opContext(), m.opTimeout)
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
	if cmd := m.refreshAliasesCmd(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.refreshOTPHistoryCmdAt(m.otpSearchQuery, reqAt); cmd != nil {
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

func (m Model) aliasPanelView() string {
	if m.creating {
		platformPrefix := " "
		emailPrefix := " "
		if m.createField == 0 {
			platformPrefix = ">"
		} else {
			emailPrefix = ">"
		}
		return strings.Join([]string{
			"  [create mode]",
			fmt.Sprintf("  %s platform: %s", platformPrefix, m.createPlatform),
			fmt.Sprintf("  %s alias email: %s", emailPrefix, m.createAliasEmail),
			"  enter submit | tab switch field | esc cancel",
		}, "\n")
	}

	if m.deleteConfirm {
		alias := ""
		if len(m.aliases) > 0 && m.selected >= 0 && m.selected < len(m.aliases) {
			alias = m.aliases[m.selected].AliasEmail
		}
		return fmt.Sprintf("  [delete confirm] %s (y/enter confirm, n/esc cancel)", alias)
	}

	if len(m.aliases) == 0 {
		return "  (no aliases) | n create"
	}

	lines := make([]string, 0, len(m.aliases)+1)
	lines = append(lines, "  n create | d delete | ↑/↓ select")
	for i, row := range m.aliases {
		prefix := " "
		if i == m.selected {
			prefix = ">"
		}
		lines = append(lines, fmt.Sprintf("  %s %s | %s | %s", prefix, row.Platform, row.AliasEmail, row.RuleID))
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

func (m Model) otpPanelView() string {
	if m.otpSearchMode {
		return strings.Join([]string{
			fmt.Sprintf("  [search] query: %s", m.otpSearchInput),
			"  enter apply | esc cancel | backspace edit",
		}, "\n")
	}

	if len(m.otpEvents) == 0 {
		if m.otpSearchQuery != "" {
			return fmt.Sprintf("  (no otp events) | filter=%q | / search | esc clear", m.otpSearchQuery)
		}
		return "  (no otp events) | / search"
	}

	latest := m.otpEvents[0]
	header := fmt.Sprintf("  latest: %s | %s | %s | %s", latest.Platform, latest.OTPCode, latest.ReceivedAt.UTC().Format(time.RFC3339), latest.AliasEmail)
	if m.otpSearchQuery != "" {
		header += fmt.Sprintf(" | filter=%q", m.otpSearchQuery)
	}

	lines := make([]string, 0, len(m.otpEvents)+2)
	lines = append(lines, header)
	lines = append(lines, "  / search | esc clear filter | ↑/↓ select | c copy")
	for i, evt := range m.otpEvents {
		prefix := " "
		if i == m.otpSelected {
			prefix = ">"
		}
		lines = append(lines, fmt.Sprintf("  %s %s | %s | %s | %s", prefix, evt.Platform, evt.OTPCode, evt.AliasEmail, evt.ReceivedAt.UTC().Format(time.RFC3339)))
	}

	return strings.Join(lines, "\n")
}

func (m Model) errorLine() string {
	if strings.TrimSpace(m.ErrorMsg) == "" {
		return ""
	}
	return "Error: " + m.ErrorMsg
}

func (m Model) opContext() context.Context {
	if m.opParentCtx != nil {
		return m.opParentCtx
	}

	return context.Background()
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

func (m Model) healthLine() string {
	h := normalizeHealthStatus(m.health)
	return fmt.Sprintf(
		"Status: cloudflare=%s destination=%s mailbox=%s parser=%s",
		h.Cloudflare,
		h.Destination,
		h.Mailbox,
		h.Parser,
	)
}
