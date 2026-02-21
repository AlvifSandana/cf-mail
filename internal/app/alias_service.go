package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"tuiotp/internal/adapters/cloudflare"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

var platformPattern = regexp.MustCompile(`^[A-Z0-9_-]{1,32}$`)

type aliasCloudflarePort = ports.AliasCloudflareClient

type aliasRepositoryPort = ports.AliasRepository

type AliasService struct {
	repo aliasRepositoryPort

	clientsByDomain map[string]aliasCloudflarePort
	domains         []string
	activeDomain    string
	mu              sync.RWMutex

	destinationEmail   string
	requireVerified    bool
	ruleNamePrefix     string
	defaultPriority    int
	enabledByDefault   bool
	deleteOnStoreError bool
}

type AliasServiceConfig struct {
	DestinationEmail string
	AliasDomain      string
	Domains          []string
	ActiveDomain     string
	DomainClients    map[string]aliasCloudflarePort
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

	clientsByDomain := make(map[string]aliasCloudflarePort)
	domains := make([]string, 0)
	if len(cfg.DomainClients) > 0 {
		for rawDomain, client := range cfg.DomainClients {
			domainName := strings.ToLower(strings.TrimSpace(rawDomain))
			if domainName == "" {
				continue
			}
			if client == nil {
				return nil, fmt.Errorf("cloudflare client is nil for domain %q", domainName)
			}
			if _, exists := clientsByDomain[domainName]; !exists {
				domains = append(domains, domainName)
			}
			clientsByDomain[domainName] = client
		}
	}
	if len(clientsByDomain) == 0 {
		aliasDomain := strings.ToLower(strings.TrimSpace(cfg.AliasDomain))
		if aliasDomain == "" {
			return nil, fmt.Errorf("alias domain is required")
		}
		if cf == nil {
			return nil, fmt.Errorf("alias service cloudflare client is nil")
		}
		clientsByDomain[aliasDomain] = cf
		domains = append(domains, aliasDomain)
	}
	if len(cfg.Domains) > 0 {
		preferred := make([]string, 0, len(cfg.Domains))
		for _, d := range cfg.Domains {
			domainName := strings.ToLower(strings.TrimSpace(d))
			if domainName == "" {
				continue
			}
			if _, ok := clientsByDomain[domainName]; !ok {
				return nil, fmt.Errorf("missing cloudflare client for domain %q", domainName)
			}
			preferred = append(preferred, domainName)
		}
		if len(preferred) > 0 {
			domains = preferred
		}
	}
	if len(domains) == 0 {
		return nil, fmt.Errorf("at least one alias domain is required")
	}
	activeDomain := strings.ToLower(strings.TrimSpace(cfg.ActiveDomain))
	if activeDomain == "" {
		activeDomain = domains[0]
	}
	if _, ok := clientsByDomain[activeDomain]; !ok {
		return nil, fmt.Errorf("active domain is not configured")
	}

	return &AliasService{
		repo:               repo,
		clientsByDomain:    clientsByDomain,
		domains:            append([]string(nil), domains...),
		activeDomain:       activeDomain,
		destinationEmail:   destinationEmail,
		requireVerified:    cfg.RequireVerified,
		ruleNamePrefix:     rulePrefix,
		defaultPriority:    cfg.DefaultPriority,
		enabledByDefault:   cfg.EnabledByDefault,
		deleteOnStoreError: true,
	}, nil
}

func (s *AliasService) ListDomains() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.domains))
	copy(out, s.domains)
	return out
}

func (s *AliasService) ActiveDomain() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activeDomain
}

func (s *AliasService) SetActiveDomain(domainName string) error {
	if s == nil {
		return domain.WrapValidation("alias service is nil", nil)
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if domainName == "" {
		return domain.WrapValidation("active domain is required", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clientsByDomain[domainName]; !ok {
		return domain.WrapValidation("active domain is not configured", nil)
	}
	s.activeDomain = domainName
	return nil
}

func (s *AliasService) CreateAlias(ctx context.Context, in CreateAliasInput) (domain.Alias, error) {
	if s == nil {
		return domain.Alias{}, domain.WrapValidation("alias service is nil", nil)
	}

	aliasEmail, aliasDomain, err := s.normalizeAliasEmail(in.AliasEmail)
	if err != nil {
		return domain.Alias{}, domain.WrapValidation("invalid alias email", err)
	}
	parts := strings.SplitN(aliasEmail, "@", 2)

	platform := strings.ToUpper(strings.TrimSpace(in.Platform))
	if platform == "" {
		return domain.Alias{}, domain.WrapValidation("platform is required", nil)
	}
	if !platformPattern.MatchString(platform) {
		return domain.Alias{}, domain.WrapValidation("invalid platform format", nil)
	}

	cfClient, err := s.clientForDomain(aliasDomain)
	if err != nil {
		return domain.Alias{}, err
	}

	if err := cfClient.EnsureDestinationVerified(ctx, s.destinationEmail, s.requireVerified); err != nil {
		return domain.Alias{}, domain.WrapDependency("ensure destination verified", err)
	}

	localPart := parts[0]
	ruleName := cloudflare.BuildRuleName(s.ruleNamePrefix, platform, localPart)

	rule, err := cfClient.CreateRoutingRule(ctx, ports.CreateRoutingRuleInput{
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

			if rollbackErr := cfClient.DeleteRoutingRule(rollbackCtx, rule.ID); rollbackErr != nil {
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

	_, aliasDomain, splitErr := splitAliasEmail(normalized)
	if splitErr != nil {
		return splitErr
	}
	cfClient, err := s.clientForDomain(aliasDomain)
	if err != nil {
		return err
	}

	if err := cfClient.DeleteRoutingRule(ctx, selected.RuleID); err != nil {
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

	cfClient, err := s.activeClient()
	if err != nil {
		return nil, err
	}

	rules, err := cfClient.ListRoutingRules(ctx, ports.ListRoutingRulesFilter{})
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

	cfClient, err := s.resolveClientForRuleInput(in.AliasEmail)
	if err != nil {
		return ports.RoutingRule{}, err
	}

	rule, err := cfClient.UpdateRoutingRule(ctx, in)
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

	normalizedAlias, aliasDomain, err := s.normalizeAliasEmail(in.AliasEmail)
	if err != nil {
		return ports.RoutingRule{}, domain.WrapValidation("invalid alias email", err)
	}
	in.AliasEmail = normalizedAlias
	if in.AliasEmail == "" {
		return ports.RoutingRule{}, domain.WrapValidation("alias email is required", nil)
	}
	cfClient, err := s.clientForDomain(aliasDomain)
	if err != nil {
		return ports.RoutingRule{}, err
	}

	if len(in.Destination) == 0 {
		// Use the configured destination email as default.
		in.Destination = []string{s.destinationEmail}
	}

	if strings.TrimSpace(in.Name) == "" {
		// Build a default rule name from alias email local part.
		parts := strings.SplitN(in.AliasEmail, "@", 2)
		localPart := parts[0]
		in.Name = cloudflare.BuildRuleName(s.ruleNamePrefix, "", localPart)
	}

	rule, err := cfClient.CreateRoutingRule(ctx, in)
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

	cfClient, err := s.activeClient()
	if err != nil {
		return err
	}

	if err := cfClient.DeleteRoutingRule(ctx, ruleID); err != nil {
		return domain.WrapDependency("delete routing rule by id", err)
	}

	return nil
}

func (s *AliasService) activeClient() (aliasCloudflarePort, error) {
	if s == nil {
		return nil, domain.WrapValidation("alias service is nil", nil)
	}
	s.mu.RLock()
	active := s.activeDomain
	client := s.clientsByDomain[active]
	s.mu.RUnlock()
	if client == nil {
		return nil, domain.WrapValidation("active domain is not configured", nil)
	}
	return client, nil
}

func (s *AliasService) clientForDomain(domainName string) (aliasCloudflarePort, error) {
	if s == nil {
		return nil, domain.WrapValidation("alias service is nil", nil)
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if domainName == "" {
		return nil, domain.WrapValidation("alias email domain is required", nil)
	}
	s.mu.RLock()
	client := s.clientsByDomain[domainName]
	s.mu.RUnlock()
	if client == nil {
		return nil, domain.WrapValidation("alias email domain is not configured", nil)
	}
	return client, nil
}

func (s *AliasService) resolveClientForRuleInput(aliasEmail string) (aliasCloudflarePort, error) {
	aliasEmail = strings.TrimSpace(aliasEmail)
	if aliasEmail == "" {
		return s.activeClient()
	}
	_, domainName, err := splitAliasEmail(strings.ToLower(aliasEmail))
	if err != nil {
		return nil, err
	}
	return s.clientForDomain(domainName)
}

func (s *AliasService) normalizeAliasEmail(v string) (string, string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "", domain.WrapValidation("alias email is required", nil)
	}
	if !strings.Contains(v, "@") {
		activeDomain := s.ActiveDomain()
		if activeDomain == "" {
			return "", "", domain.WrapValidation("active domain is not configured", nil)
		}
		v = v + "@" + activeDomain
	}
	normalized, err := normalizeEmail(v)
	if err != nil {
		return "", "", err
	}
	_, domainName, err := splitAliasEmail(normalized)
	if err != nil {
		return "", "", err
	}
	if _, err := s.clientForDomain(domainName); err != nil {
		return "", "", err
	}
	return normalized, domainName, nil
}

func splitAliasEmail(aliasEmail string) (string, string, error) {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(aliasEmail)), "@", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", domain.WrapValidation("invalid alias email: missing domain", nil)
	}
	return parts[0], parts[1], nil
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
