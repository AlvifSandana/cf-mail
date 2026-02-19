package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"tuiotp/internal/adapters/cloudflare"
	"tuiotp/internal/adapters/mailbox/imap"
	"tuiotp/internal/adapters/parser"
	"tuiotp/internal/config"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
	sqlitestore "tuiotp/internal/storage/sqlite"
)

type App struct {
	cfg        *config.Config
	db         *sql.DB
	otpService *OTPService
	aliasSvc   *AliasService
	otpRepo    ports.OTPRepository
}

func New(cfg *config.Config) (*App, error) {
	return NewWithContext(context.Background(), cfg)
}

func NewWithContext(ctx context.Context, cfg *config.Config) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	db, err := sqlitestore.Open(cfg.App.DBPath)
	if err != nil {
		return nil, err
	}

	if err := sqlitestore.Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("run sqlite migrations: %w", err)
	}

	cfClient, err := cloudflare.NewClient(cloudflare.ClientConfig{
		APIToken:   cfg.Cloudflare.APIToken,
		AccountID:  cfg.Cloudflare.AccountID,
		ZoneID:     cfg.Cloudflare.ZoneID,
		MaxRetries: 3,
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init cloudflare client: %w", err)
	}

	if err := cfClient.EnsureDestinationVerified(ctx, cfg.Destination.Email, cfg.Destination.RequireVerified); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure destination verified: %w", err)
	}

	rules := make([]parser.Rule, 0, len(cfg.OTP.Rules))
	for _, r := range cfg.OTP.Rules {
		rules = append(rules, parser.Rule{
			Platform:     r.Platform,
			FromContains: r.FromContains,
			SubjectRegex: r.SubjectRegex,
			OTPRegex:     r.OTPRegex,
		})
	}

	engine, err := parser.NewEngine(rules)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init otp parser engine: %w", err)
	}

	renderer, err := parser.NewRenderer(cfg.OTP.OutputFormat)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init otp renderer: %w", err)
	}

	otpRepo := sqlitestore.NewOTPRepository(db)
	aliasRepo := sqlitestore.NewAliasRepository(db)

	dedupeWindow := defaultOTPDedupeWindow
	if cfg.OTP.DedupeWindow != "" {
		d, err := time.ParseDuration(cfg.OTP.DedupeWindow)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("parse otp dedupe window: %w", err)
		}
		dedupeWindow = d
	}

	deduper, err := NewOTPDeduper(otpDuplicateRepositoryAdapter{repo: otpRepo}, dedupeWindow)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init otp deduper: %w", err)
	}

	otpService, err := NewOTPService(
		otpParserAdapter{engine: engine},
		otpRendererAdapter{renderer: renderer},
		otpRepositoryAdapter{repo: otpRepo},
		deduper,
	)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init otp service: %w", err)
	}

	aliasSvc, err := NewAliasService(aliasCloudflareAdapter{client: cfClient}, aliasRepositoryAdapter{repo: aliasRepo}, AliasServiceConfig{
		DestinationEmail: cfg.Destination.Email,
		AliasDomain:      cfg.Cloudflare.Domain,
		RequireVerified:  cfg.Destination.RequireVerified,
		RuleNamePrefix:   cfg.Cloudflare.RuleNamePrefix,
		DefaultPriority:  cfg.Cloudflare.DefaultPriority,
		EnabledByDefault: cfg.Cloudflare.EnabledByDefault,
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init alias service: %w", err)
	}

	return &App{cfg: cfg, db: db, otpService: otpService, aliasSvc: aliasSvc, otpRepo: otpRepositoryAdapter{repo: otpRepo}}, nil
}

func (a *App) OTPService() *OTPService {
	if a == nil {
		return nil
	}

	return a.otpService
}

func (a *App) AliasService() *AliasService {
	if a == nil {
		return nil
	}

	return a.aliasSvc
}

func (a *App) ListOTPEvents(ctx context.Context, filter OTPListFilter) ([]domain.OTPEvent, error) {
	if a == nil {
		return nil, fmt.Errorf("app is nil")
	}
	if a.otpRepo == nil {
		return nil, fmt.Errorf("app otp repository is nil")
	}

	rows, err := a.otpRepo.List(ctx, ports.OTPListFilter(filter))
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func normalizeIncomingEmail(in domain.IncomingEmail) (domain.IncomingEmail, error) {
	n, err := imap.NormalizeIncomingEmail(imap.IncomingEmail{
		To:         in.To,
		From:       in.From,
		Subject:    in.Subject,
		MessageID:  in.MessageID,
		Snippet:    in.Snippet,
		Body:       in.Body,
		ReceivedAt: in.ReceivedAt,
	})
	if err != nil {
		return domain.IncomingEmail{}, err
	}

	return domain.IncomingEmail(n), nil
}

func (a *App) Run(ctx context.Context) error {
	<-ctx.Done()
	if a.db != nil {
		if err := a.db.Close(); err != nil {
			return fmt.Errorf("close db: %w", err)
		}
	}

	return nil
}
