package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

var platformPattern = regexp.MustCompile(`^[A-Z0-9_-]{1,32}$`)

type aliasCloudflarePort = ports.AliasCloudflareClient

type aliasRepositoryPort = ports.AliasRepository

type AliasService struct {
	cf   aliasCloudflarePort
	repo aliasRepositoryPort

	destinationEmail   string
	aliasDomain        string
	requireVerified    bool
	ruleNamePrefix     string
	defaultPriority    int
	enabledByDefault   bool
	deleteOnStoreError bool
}

type AliasServiceConfig struct {
	DestinationEmail string
	AliasDomain      string
	RequireVerified  bool
	RuleNamePrefix   string
	DefaultPriority  int
	EnabledByDefault bool
}

type CreateAliasInput struct {
	Platform   string
	AliasEmail string
}

func NewAliasService(cf aliasCloudflarePort, repo aliasRepositoryPort, cfg AliasServiceConfig) (*AliasService, error) {
	if cf == nil {
		return nil, fmt.Errorf("alias service cloudflare client is nil")
	}
	if repo == nil {
		return nil, fmt.Errorf("alias service repository is nil")
	}

	destinationEmail, err := normalizeEmail(cfg.DestinationEmail)
	if err != nil {
		return nil, fmt.Errorf("invalid destination email: %w", err)
	}

	rulePrefix := strings.Trim(cfg.RuleNamePrefix, ": ")
	if rulePrefix == "" {
		rulePrefix = "tuiotp"
	}

	aliasDomain := strings.ToLower(strings.TrimSpace(cfg.AliasDomain))
	if aliasDomain == "" {
		return nil, fmt.Errorf("alias domain is required")
	}

	return &AliasService{
		cf:                 cf,
		repo:               repo,
		destinationEmail:   destinationEmail,
		aliasDomain:        aliasDomain,
		requireVerified:    cfg.RequireVerified,
		ruleNamePrefix:     rulePrefix,
		defaultPriority:    cfg.DefaultPriority,
		enabledByDefault:   cfg.EnabledByDefault,
		deleteOnStoreError: true,
	}, nil
}

func (s *AliasService) CreateAlias(ctx context.Context, in CreateAliasInput) (domain.Alias, error) {
	if s == nil {
		return domain.Alias{}, domain.WrapValidation("alias service is nil", nil)
	}

	aliasEmail, err := normalizeEmail(in.AliasEmail)
	if err != nil {
		return domain.Alias{}, domain.WrapValidation("invalid alias email", err)
	}
	parts := strings.SplitN(aliasEmail, "@", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return domain.Alias{}, domain.WrapValidation("invalid alias email: missing domain", nil)
	}
	if parts[1] != s.aliasDomain {
		return domain.Alias{}, domain.WrapValidation("alias email domain must match configured domain", nil)
	}

	platform := strings.ToUpper(strings.TrimSpace(in.Platform))
	if platform == "" {
		return domain.Alias{}, domain.WrapValidation("platform is required", nil)
	}
	if !platformPattern.MatchString(platform) {
		return domain.Alias{}, domain.WrapValidation("invalid platform format", nil)
	}

	if err := s.cf.EnsureDestinationVerified(ctx, s.destinationEmail, s.requireVerified); err != nil {
		return domain.Alias{}, domain.WrapDependency("ensure destination verified", err)
	}

	localPart := parts[0]
	ruleName := buildRuleName(s.ruleNamePrefix, platform, localPart)

	rule, err := s.cf.CreateRoutingRule(ctx, ports.CreateRoutingRuleInput{
		Name:        ruleName,
		AliasEmail:  aliasEmail,
		Destination: []string{s.destinationEmail},
		Enabled:     s.enabledByDefault,
		Priority:    s.defaultPriority,
	})
	if err != nil {
		return domain.Alias{}, domain.WrapDependency("create routing rule", err)
	}

	created, err := s.repo.Create(ctx, domain.Alias{
		Platform:   platform,
		AliasEmail: aliasEmail,
		RuleID:     rule.ID,
		RuleName:   rule.Name,
		Enabled:    rule.Enabled,
	})
	if err != nil {
		if s.deleteOnStoreError {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if rollbackErr := s.cf.DeleteRoutingRule(rollbackCtx, rule.ID); rollbackErr != nil {
				return domain.Alias{}, errors.Join(
					domain.WrapDependency("store alias metadata", err),
					domain.WrapDependency("rollback delete rule failed", rollbackErr),
				)
			}
		}
		return domain.Alias{}, domain.WrapDependency("store alias metadata", err)
	}

	return created, nil
}

func (s *AliasService) ListAliases(ctx context.Context) ([]domain.Alias, error) {
	if s == nil {
		return nil, domain.WrapValidation("alias service is nil", nil)
	}

	rows, err := s.repo.ListActive(ctx)
	if err != nil {
		return nil, domain.WrapDependency("list active aliases", err)
	}
	return rows, nil
}

func (s *AliasService) DeleteAlias(ctx context.Context, aliasEmail string) error {
	if s == nil {
		return domain.WrapValidation("alias service is nil", nil)
	}

	normalized, err := normalizeEmail(aliasEmail)
	if err != nil {
		return domain.WrapValidation("invalid alias email", err)
	}

	selected, err := s.repo.FindActiveByAliasEmail(ctx, normalized)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WrapNotFound("alias not found", err)
		}
		return domain.WrapDependency("find active alias", err)
	}

	if err := s.cf.DeleteRoutingRule(ctx, selected.RuleID); err != nil {
		return domain.WrapDependency("delete routing rule", err)
	}

	if err := s.repo.SoftDeleteByAliasEmail(ctx, normalized, time.Now().UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.WrapNotFound("alias not found", err)
		}
		return domain.WrapDependency("soft delete alias", err)
	}

	return nil
}

// ListRoutingRules fetches all CF routing rules (paginated) with optional name prefix filter.
func (s *AliasService) ListRoutingRules(ctx context.Context) ([]ports.RoutingRule, error) {
	if s == nil {
		return nil, domain.WrapValidation("alias service is nil", nil)
	}

	rules, err := s.cf.ListRoutingRules(ctx, ports.ListRoutingRulesFilter{})
	if err != nil {
		return nil, domain.WrapDependency("list routing rules", err)
	}

	return rules, nil
}

// UpdateRoutingRule performs a full-replacement PUT on a CF routing rule.
func (s *AliasService) UpdateRoutingRule(ctx context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error) {
	if s == nil {
		return ports.RoutingRule{}, domain.WrapValidation("alias service is nil", nil)
	}

	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		return ports.RoutingRule{}, domain.WrapValidation("rule id is required", nil)
	}

	rule, err := s.cf.UpdateRoutingRule(ctx, in)
	if err != nil {
		return ports.RoutingRule{}, domain.WrapDependency("update routing rule", err)
	}

	return rule, nil
}

// CreateRoutingRuleDirect creates a CF routing rule directly via the CF API,
// without storing anything in the local database.
func (s *AliasService) CreateRoutingRuleDirect(ctx context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error) {
	if s == nil {
		return ports.RoutingRule{}, domain.WrapValidation("alias service is nil", nil)
	}

	in.AliasEmail = strings.ToLower(strings.TrimSpace(in.AliasEmail))
	if in.AliasEmail == "" {
		return ports.RoutingRule{}, domain.WrapValidation("alias email is required", nil)
	}
	if _, err := parseAddress(in.AliasEmail); err != nil {
		return ports.RoutingRule{}, domain.WrapValidation("invalid alias email", err)
	}

	if len(in.Destination) == 0 {
		// Use the configured destination email as default.
		in.Destination = []string{s.destinationEmail}
	}

	if strings.TrimSpace(in.Name) == "" {
		// Build a default rule name from alias email local part.
		parts := strings.SplitN(in.AliasEmail, "@", 2)
		localPart := parts[0]
		in.Name = buildRuleName(s.ruleNamePrefix, "", localPart)
	}

	rule, err := s.cf.CreateRoutingRule(ctx, in)
	if err != nil {
		return ports.RoutingRule{}, domain.WrapDependency("create routing rule direct", err)
	}

	return rule, nil
}

// DeleteRoutingRuleByID deletes a CF routing rule by its CF rule ID directly,
// without requiring the rule to exist in the local database.
func (s *AliasService) DeleteRoutingRuleByID(ctx context.Context, ruleID string) error {
	if s == nil {
		return domain.WrapValidation("alias service is nil", nil)
	}

	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return domain.WrapValidation("rule id is required", nil)
	}

	if err := s.cf.DeleteRoutingRule(ctx, ruleID); err != nil {
		return domain.WrapDependency("delete routing rule by id", err)
	}

	return nil
}

func normalizeEmail(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", domain.WrapValidation("email is required", nil)
	}

	parsed, err := parseAddress(v)
	if err != nil {
		return "", domain.WrapValidation("invalid email address", err)
	}

	return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
}

func parseAddress(v string) (*mail.Address, error) {
	return mail.ParseAddress(v)
}

func buildRuleName(prefix, platform, aliasLocalpart string) string {
	prefix = strings.Trim(prefix, ": ")
	platform = strings.ToUpper(strings.TrimSpace(platform))
	aliasLocalpart = strings.TrimSpace(aliasLocalpart)

	parts := make([]string, 0, 3)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if platform != "" {
		parts = append(parts, platform)
	}
	if aliasLocalpart != "" {
		parts = append(parts, aliasLocalpart)
	}

	return strings.Join(parts, ":")
}
