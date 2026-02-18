package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"tuiotp/internal/adapters/clipboard"
	"tuiotp/internal/app"
	"tuiotp/internal/config"
	"tuiotp/internal/observability"
	"tuiotp/internal/ui"
)

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

	logger := observability.NewLogger(logWriter, observability.NewRedactor([]string{
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

	p := tea.NewProgram(ui.NewModelWithConfig(ui.ModelConfig{
		AliasManager: tuiApp.AliasService(),
		OTPManager:   tuiApp,
		Clipboard:    clip,
		Health: ui.HealthStatus{
			Cloudflare:  "ready",
			Destination: "ready",
			Mailbox:     "configured",
			Parser:      "ready",
		},
		ParentCtx: runCtx,
	}))
	go func() {
		<-runCtx.Done()
		p.Quit()
	}()

	if _, err := p.Run(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("tui.run.failed", "failed to run tui", map[string]any{"error": err})
		runCancel()
		_ = <-errCh
		closeLogFile()
		os.Exit(1)
	}
	logger.Info("tui.run.stopped", "tui program stopped", nil)

	runCancel()

	if err := <-errCh; err != nil {
		logger.Error("app.run.failed", "application runtime returned error", map[string]any{"error": err})
		closeLogFile()
		os.Exit(1)
	}

	logger.Info("app.exit.ok", "application exited cleanly", nil)
}
