package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/adapters/clipboard"
	"tuiotp/internal/adapters/mailbox/imap"
	"tuiotp/internal/app"
	"tuiotp/internal/config"
	"tuiotp/internal/domain"
	"tuiotp/internal/observability"
	"tuiotp/internal/ports"
	"tuiotp/internal/ui"
)

const defaultIMAPConnectTimeout = 15 * time.Second

func main() {
	bootstrapLogger := observability.NewLogger(os.Stderr, observability.NewRedactor(nil))

	configPath := flag.String("config", "", "path to config.yml")
	flag.Parse()

	if *configPath == "" {
		bootstrapLogger.Error("app.config.flag_missing", "missing required flag", map[string]any{"flag": "config"})
		os.Exit(1)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		bootstrapLogger.Error("app.config.load_failed", "failed to load config", map[string]any{"config_path": *configPath})
		os.Exit(1)
	}

	resolveCtx, resolveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer resolveCancel()
	if err := config.ResolveZones(resolveCtx, cfg); err != nil {
		bootstrapLogger.Error("app.config.resolve_zones_failed", "failed to auto-discover zones", map[string]any{"error": err})
		os.Exit(1)
	}

	if err := config.Validate(cfg); err != nil {
		bootstrapLogger.Error("app.config.validate_failed", "failed to validate config", nil)
		os.Exit(1)
	}

	logWriter := os.Stderr
	closeLogFile := func() {}
	if cfg.App.LogPath != "" {
		f, err := os.OpenFile(cfg.App.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			bootstrapLogger.Warn("app.log.fallback_stderr", "log file fallback to stderr", map[string]any{"log_path": cfg.App.LogPath})
		} else {
			logWriter = f
			closeLogFile = func() { _ = f.Close() }
		}
	}
	defer closeLogFile()

	logRing := observability.NewRingBuffer(observability.DefaultLogBufferSize)
	logger := observability.NewLogger(io.MultiWriter(logWriter, logRing), observability.NewRedactor([]string{
		cfg.Cloudflare.APIToken,
		cfg.Mailbox.IMAP.Password,
	}))
	logger.Info("app.config.loaded", "config loaded and validated", map[string]any{
		"config_path":  *configPath,
		"log_path":     cfg.App.LogPath,
		"mailbox_mode": cfg.Mailbox.Mode,
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	tuiApp, err := app.NewWithContext(runCtx, cfg)
	if err != nil {
		logger.Error("app.init.failed", "failed to initialize app", map[string]any{"error": err})
		closeLogFile()
		os.Exit(1)
	}
	logger.Info("app.init.ok", "application initialized", map[string]any{
		"db_path": cfg.App.DBPath,
	})

	var clip clipboard.Copier
	clip, err = clipboard.New(clipboard.Config{
		Enabled: cfg.UI.Clipboard.Enabled,
		Method:  cfg.UI.Clipboard.Method,
	})
	if err != nil {
		if !errors.Is(err, clipboard.ErrClipboardDisabled) && !errors.Is(err, clipboard.ErrClipboardUnavailable) {
			logger.Warn("clipboard.init.warning", "clipboard initialization warning", map[string]any{"error": err})
		} else {
			logger.Info("clipboard.init.unavailable", "clipboard is unavailable or disabled", map[string]any{"error": err})
		}
		clip = nil
	} else {
		logger.Info("clipboard.init.ok", "clipboard initialized", map[string]any{
			"method": cfg.UI.Clipboard.Method,
		})
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- tuiApp.Run(runCtx)
	}()

	var runtimeCoordinator *app.RuntimeCoordinator
	if runner, err := newIMAPRuntimeRunner(cfg); err != nil {
		if strings.EqualFold(strings.TrimSpace(cfg.Mailbox.Mode), "imap") {
			logger.Error("runtime.init.failed", "runtime mailbox monitor initialization failed", map[string]any{"error": err})
			runCancel()
			_ = <-errCh
			closeLogFile()
			os.Exit(1)
		}
		logger.Info("runtime.init.skipped", "runtime mailbox monitor disabled", map[string]any{"reason": err})
	} else {
		coordinator, err := tuiApp.NewRuntimeCoordinator(runner, app.RuntimeCoordinatorConfig{})
		if err != nil {
			logger.Error("runtime.coordinator.init_failed", "runtime coordinator initialization failed", map[string]any{"error": err})
			runCancel()
			_ = <-errCh
			closeLogFile()
			os.Exit(1)
		}
		if err := coordinator.Start(runCtx); err != nil {
			logger.Error("runtime.coordinator.start_failed", "runtime coordinator failed to start", map[string]any{"error": err})
			runCancel()
			_ = <-errCh
			closeLogFile()
			os.Exit(1)
		}

		runtimeCoordinator = coordinator
		logger.Info("runtime.coordinator.started", "runtime coordinator started", map[string]any{"mailbox_mode": cfg.Mailbox.Mode})
	}

	p := tea.NewProgram(ui.NewModelWithConfig(ui.ModelConfig{
		OTPManager:   tuiApp,
		RulesManager: tuiApp.AliasService(),
		SettingsMgr:  runtimeSettingsManager{configPath: *configPath},
		Clipboard:    clip,
		LogBuffer:    logRing,
		Health: ui.HealthStatus{
			Cloudflare:  "ready",
			Destination: "ready",
			Mailbox:     "configured",
			Parser:      "ready",
		},
		ParentCtx: runCtx,
	}), tea.WithAltScreen())
	go func() {
		<-runCtx.Done()
		p.Quit()
	}()
	if runtimeCoordinator != nil {
		staleAfter := runtimeWatchdogTimeout(cfg)
		pulseCh := make(chan struct{}, 1)
		go func(events <-chan app.RuntimeEvent) {
			for {
				select {
				case <-runCtx.Done():
					return
				case evt, ok := <-events:
					if !ok {
						p.Send(app.RuntimeEvent{Type: app.RuntimeEventRuntimeError, RunID: 0, Err: "runtime watch failed"})
						logger.Error("runtime.coordinator.closed", "runtime coordinator event stream closed", nil)
						runCancel()
						p.Quit()
						return
					}
					if evt.Type == app.RuntimeEventWatcherUpdate {
						select {
						case pulseCh <- struct{}{}:
						default:
						}
					}
					if evt.Type == app.RuntimeEventRuntimeError || evt.Type == app.RuntimeEventRuntimeStopped {
						logger.Error("runtime.coordinator.failed", "runtime coordinator emitted runtime error", map[string]any{"error": evt.Err})
						if evt.Type == app.RuntimeEventRuntimeStopped {
							p.Send(app.RuntimeEvent{Type: app.RuntimeEventRuntimeError, RunID: evt.RunID, Err: "runtime watch failed"})
						}
						runCancel()
						p.Quit()
					}
					p.Send(evt)
				}
			}
		}(runtimeCoordinator.Events())

		go func() {
			t := time.NewTimer(staleAfter)
			defer t.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-pulseCh:
					if !t.Stop() {
						select {
						case <-t.C:
						default:
						}
					}
					t.Reset(staleAfter)
				case <-t.C:
					p.Send(app.RuntimeEvent{Type: app.RuntimeEventRuntimeError, RunID: 0, Err: "runtime watch failed"})
					logger.Error("runtime.runner.stale", "runtime mailbox runner stale timeout", map[string]any{"stale_after": staleAfter.String()})
					runCancel()
					p.Quit()
					return
				}
			}
		}()
	}

	if _, err := p.Run(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("tui.run.failed", "failed to run tui", map[string]any{"error": err})
		runCancel()
		if runtimeCoordinator != nil {
			runtimeCoordinator.Stop()
		}
		_ = <-errCh
		closeLogFile()
		os.Exit(1)
	}
	logger.Info("tui.run.stopped", "tui program stopped", nil)

	runCancel()
	if runtimeCoordinator != nil {
		runtimeCoordinator.Stop()
	}

	if err := <-errCh; err != nil {
		logger.Error("app.run.failed", "application runtime returned error", map[string]any{"error": err})
		closeLogFile()
		os.Exit(1)
	}

	logger.Info("app.exit.ok", "application exited cleanly", nil)
}

func newIMAPRuntimeRunner(cfg *config.Config) (ports.RuntimeWatchRunner, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Mailbox.Mode), "imap") {
		return nil, fmt.Errorf("mailbox mode %q does not support runtime runner", cfg.Mailbox.Mode)
	}

	pollInterval, err := parseIMAPPollInterval(cfg.Mailbox.IMAP.PollInterval)
	if err != nil {
		return nil, err
	}

	factory := func(ctx context.Context) (imap.Session, error) {
		conn, err := imap.ConnectAndSelect(ctx, imap.Config{
			Host:            cfg.Mailbox.IMAP.Host,
			Port:            cfg.Mailbox.IMAP.Port,
			Username:        cfg.Mailbox.IMAP.Username,
			Password:        cfg.Mailbox.IMAP.Password,
			Mailbox:         cfg.Mailbox.IMAP.Mailbox,
			ConnectTimeout:  defaultIMAPConnectTimeout,
			TLSServerName:   cfg.Mailbox.IMAP.Host,
			InsecureSkipTLS: false,
		}, imap.Deps{})
		if err != nil {
			return nil, err
		}

		watcher, err := imap.NewWatcher(connectorWatchClient{connector: conn}, imap.WatcherConfig{
			EnableIdle:   cfg.Mailbox.IMAP.Idle,
			PollInterval: pollInterval,
		})
		if err != nil {
			_ = conn.Close()
			return nil, err
		}

		return imapWatcherSession{watcher: watcher, connector: conn}, nil
	}

	reconnector, err := imap.NewReconnector(factory, imap.ReconnectConfig{})
	if err != nil {
		return nil, err
	}

	return reconnectRuntimeRunnerAdapter{runner: reconnector}, nil
}

func parseIMAPPollInterval(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 5 * time.Second, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid mailbox.imap.poll_interval: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("mailbox.imap.poll_interval must be > 0")
	}
	return d, nil
}

func runtimeWatchdogTimeout(cfg *config.Config) time.Duration {
	if cfg == nil {
		return 20 * time.Second
	}

	pollInterval, err := parseIMAPPollInterval(cfg.Mailbox.IMAP.PollInterval)
	if err != nil {
		return 20 * time.Second
	}

	timeout := 3 * pollInterval
	if timeout < 20*time.Second {
		return 20 * time.Second
	}
	return timeout
}

type imapRuntimeRunner interface {
	Run(ctx context.Context, onUpdate func(imap.WatchUpdate)) error
}

type reconnectRuntimeRunnerAdapter struct {
	runner imapRuntimeRunner
}

type runtimeSettingsManager struct {
	configPath string
}

func (m runtimeSettingsManager) Load(_ context.Context) (ui.SettingsState, error) {
	cfg, err := config.Load(m.configPath)
	if err != nil {
		return ui.SettingsState{}, err
	}
	method := strings.ToLower(strings.TrimSpace(cfg.UI.Clipboard.Method))
	if method == "" {
		method = "auto"
	}
	domainLines := make([]string, 0, len(cfg.Cloudflare.EffectiveDomains()))
	for _, d := range cfg.Cloudflare.EffectiveDomains() {
		domainLines = append(domainLines, d.Domain+","+d.ZoneID)
	}
	return ui.SettingsState{
		ClipboardEnabled: cfg.UI.Clipboard.Enabled,
		ClipboardMethod:  method,
		DomainsText:      strings.Join(domainLines, "\n"),
		ActiveDomain:     cfg.Cloudflare.EffectiveActiveDomain(),
		Timezone:         strings.TrimSpace(cfg.App.Timezone),
		LogPath:          strings.TrimSpace(cfg.App.LogPath),
		IMAPPollInterval: strings.TrimSpace(cfg.Mailbox.IMAP.PollInterval),
	}, nil
}

func (m runtimeSettingsManager) SaveAndApply(_ context.Context, state ui.SettingsState) (ui.SettingsState, ports.Clipboard, error) {
	cfg, err := config.Load(m.configPath)
	if err != nil {
		return ui.SettingsState{}, nil, err
	}

	method := strings.ToLower(strings.TrimSpace(state.ClipboardMethod))
	if method == "" {
		method = "auto"
	}

	cfg.UI.Clipboard.Enabled = state.ClipboardEnabled
	cfg.UI.Clipboard.Method = method
	cfg.Cloudflare.ActiveDomain = strings.ToLower(strings.TrimSpace(state.ActiveDomain))
	domains, err := parseSettingsDomains(state.DomainsText)
	if err != nil {
		return ui.SettingsState{}, nil, err
	}
	if len(domains) > 0 {
		cfg.Cloudflare.Domains = domains
		cfg.Cloudflare.ZoneID = domains[0].ZoneID
		cfg.Cloudflare.Domain = domains[0].Domain
		if strings.TrimSpace(cfg.Cloudflare.ActiveDomain) == "" {
			cfg.Cloudflare.ActiveDomain = domains[0].Domain
		}
	}
	cfg.App.Timezone = strings.TrimSpace(state.Timezone)
	cfg.App.LogPath = strings.TrimSpace(state.LogPath)
	cfg.Mailbox.IMAP.PollInterval = strings.TrimSpace(state.IMAPPollInterval)

	// Preserve env-backed secret source of truth when configured,
	// so a settings-only save does not persist resolved secrets.
	if strings.TrimSpace(cfg.Cloudflare.APITokenEnv) != "" {
		cfg.Cloudflare.APIToken = ""
	}
	if strings.TrimSpace(cfg.Mailbox.IMAP.PasswordEnv) != "" {
		cfg.Mailbox.IMAP.Password = ""
	}

	if err := config.ValidateForSave(cfg); err != nil {
		return ui.SettingsState{}, nil, err
	}
	if err := config.Save(m.configPath, cfg); err != nil {
		return ui.SettingsState{}, nil, err
	}

	clip, err := clipboard.New(clipboard.Config{
		Enabled: cfg.UI.Clipboard.Enabled,
		Method:  cfg.UI.Clipboard.Method,
	})
	if err != nil {
		if !errors.Is(err, clipboard.ErrClipboardDisabled) && !errors.Is(err, clipboard.ErrClipboardUnavailable) {
			return ui.SettingsState{}, nil, err
		}
		clip = nil
	}

	domainLines := make([]string, 0, len(cfg.Cloudflare.EffectiveDomains()))
	for _, d := range cfg.Cloudflare.EffectiveDomains() {
		domainLines = append(domainLines, d.Domain+","+d.ZoneID)
	}
	return ui.SettingsState{
		ClipboardEnabled: cfg.UI.Clipboard.Enabled,
		ClipboardMethod:  method,
		DomainsText:      strings.Join(domainLines, "\n"),
		ActiveDomain:     cfg.Cloudflare.EffectiveActiveDomain(),
		Timezone:         strings.TrimSpace(cfg.App.Timezone),
		LogPath:          strings.TrimSpace(cfg.App.LogPath),
		IMAPPollInterval: strings.TrimSpace(cfg.Mailbox.IMAP.PollInterval),
	}, clip, nil
}

func (m runtimeSettingsManager) PersistActiveDomain(_ context.Context, domain string) error {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return fmt.Errorf("domain is required")
	}

	cfg, err := config.Load(m.configPath)
	if err != nil {
		return err
	}

	cfg.Cloudflare.ActiveDomain = domain

	// Preserve env-backed secrets
	if strings.TrimSpace(cfg.Cloudflare.APITokenEnv) != "" {
		cfg.Cloudflare.APIToken = ""
	}
	if strings.TrimSpace(cfg.Mailbox.IMAP.PasswordEnv) != "" {
		cfg.Mailbox.IMAP.Password = ""
	}

	if err := config.Save(m.configPath, cfg); err != nil {
		return err
	}
	return nil
}

func parseSettingsDomains(v string) ([]config.CloudflareDomain, error) {
	normalized := strings.TrimSpace(v)
	lines := strings.FieldsFunc(normalized, func(r rune) bool {
		switch r {
		case '\n', ';', '|':
			return true
		default:
			return false
		}
	})
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}
	if len(lines) == 0 {
		return nil, nil
	}
	out := make([]config.CloudflareDomain, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid domains line %d: expected domain,zone_id", i+1)
		}
		domainName := strings.ToLower(strings.TrimSpace(parts[0]))
		zoneID := strings.TrimSpace(parts[1])
		if domainName == "" || zoneID == "" {
			return nil, fmt.Errorf("invalid domains line %d: domain and zone_id are required", i+1)
		}
		out = append(out, config.CloudflareDomain{Domain: domainName, ZoneID: zoneID})
	}
	return out, nil
}

func (a reconnectRuntimeRunnerAdapter) Run(ctx context.Context, onUpdate func(ports.WatchUpdate)) error {
	if onUpdate == nil {
		onUpdate = func(ports.WatchUpdate) {}
	}
	if a.runner == nil {
		return fmt.Errorf("runtime reconnector runner is nil")
	}

	return a.runner.Run(ctx, func(update imap.WatchUpdate) {
		onUpdate(ports.WatchUpdate{
			Mode:          update.Mode,
			Timestamp:     update.Timestamp,
			IncomingEmail: domain.IncomingEmail(update.IncomingEmail),
		})
	})
}

type imapWatcherSession struct {
	watcher   *imap.Watcher
	connector *imap.Connector
}

func (s imapWatcherSession) Run(ctx context.Context, onUpdate func(imap.WatchUpdate)) error {
	if s.watcher == nil {
		return fmt.Errorf("imap watcher session watcher is nil")
	}
	return s.watcher.Run(ctx, onUpdate)
}

func (s imapWatcherSession) Close() error {
	if s.connector == nil {
		return nil
	}
	return s.connector.Close()
}

type connectorWatchClient struct {
	connector *imap.Connector
}

// Idle is not yet implemented. The go-imap v1 library requires manual
// IDLE command handling. For now, the watcher always falls back to polling.
// TODO: Implement IMAP IDLE using go-imap idle extension.
func (connectorWatchClient) Idle(context.Context, func() error) error {
	return imap.ErrIdleUnsupported
}

func (c connectorWatchClient) Poll(ctx context.Context) ([]imap.IncomingEmail, error) {
	if c.connector == nil {
		return nil, fmt.Errorf("imap connector watch client connector is nil")
	}

	incoming, err := c.connector.PollUpdates(ctx)
	if err != nil {
		return nil, err
	}

	if ctx == nil {
		return incoming, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return incoming, nil
}
