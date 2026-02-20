package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"tuiotp/internal/adapters/cloudflare"
	"tuiotp/internal/domain"
	"tuiotp/internal/ports"
)

type fakeCloudflare struct {
	ensureCalls int
	createCalls int
	deleteCalls int
	listCalls   int
	updateCalls int

	lastEnsureEmail   string
	lastEnsureRequire bool
	lastCreateInput   ports.CreateRoutingRuleInput
	lastDeleteRuleID  string
	lastListFilter    ports.ListRoutingRulesFilter
	lastUpdateInput   ports.UpdateRoutingRuleInput

	ensureErr error
	createErr error
	deleteErr error
	listErr   error
	updateErr error

	createResult ports.RoutingRule
	listResult   []ports.RoutingRule
	updateResult ports.RoutingRule
}

func (f *fakeCloudflare) EnsureDestinationVerified(_ context.Context, email string, requireVerified bool) error {
	f.ensureCalls++
	f.lastEnsureEmail = email
	f.lastEnsureRequire = requireVerified
	return f.ensureErr
}

func (f *fakeCloudflare) CreateRoutingRule(_ context.Context, in ports.CreateRoutingRuleInput) (ports.RoutingRule, error) {
	f.createCalls++
	f.lastCreateInput = in
	if f.createErr != nil {
		return ports.RoutingRule{}, f.createErr
	}
	if f.createResult.ID == "" {
		f.createResult = ports.RoutingRule{ID: "rule-1", Name: in.Name, Enabled: in.Enabled}
	}
	return f.createResult, nil
}

func (f *fakeCloudflare) DeleteRoutingRule(_ context.Context, ruleID string) error {
	f.deleteCalls++
	f.lastDeleteRuleID = ruleID
	return f.deleteErr
}

func (f *fakeCloudflare) ListRoutingRules(_ context.Context, filter ports.ListRoutingRulesFilter) ([]ports.RoutingRule, error) {
	f.listCalls++
	f.lastListFilter = filter
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]ports.RoutingRule, len(f.listResult))
	copy(out, f.listResult)
	return out, nil
}

func (f *fakeCloudflare) UpdateRoutingRule(_ context.Context, in ports.UpdateRoutingRuleInput) (ports.RoutingRule, error) {
	f.updateCalls++
	f.lastUpdateInput = in
	if f.updateErr != nil {
		return ports.RoutingRule{}, f.updateErr
	}
	if f.updateResult.ID != "" {
		return f.updateResult, nil
	}
	return ports.RoutingRule{
		ID:      in.ID,
		Name:    in.Name,
		Enabled: in.Enabled,
	}, nil
}

type fakeAliasRepo struct {
	createCalls int
	listCalls   int
	deleteCalls int

	createErr error
	listErr   error
	findErr   error
	deleteErr error

	createdRows []domain.Alias
	activeRows  []domain.Alias

	lastDeleteAlias string
	lastFindAlias   string
}

func (r *fakeAliasRepo) Create(_ context.Context, in domain.Alias) (domain.Alias, error) {
	r.createCalls++
	if r.createErr != nil {
		return domain.Alias{}, r.createErr
	}
	if in.ID == 0 {
		in.ID = int64(len(r.createdRows) + 1)
	}
	r.createdRows = append(r.createdRows, in)
	return in, nil
}

func (r *fakeAliasRepo) ListActive(_ context.Context) ([]domain.Alias, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := make([]domain.Alias, len(r.activeRows))
	copy(out, r.activeRows)
	return out, nil
}

func (r *fakeAliasRepo) FindActiveByAliasEmail(_ context.Context, aliasEmail string) (domain.Alias, error) {
	r.lastFindAlias = aliasEmail
	if r.findErr != nil {
		return domain.Alias{}, r.findErr
	}
	for _, row := range r.activeRows {
		if row.AliasEmail == aliasEmail {
			return row, nil
		}
	}
	return domain.Alias{}, sql.ErrNoRows
}

func (r *fakeAliasRepo) SoftDeleteByAliasEmail(_ context.Context, aliasEmail string, _ time.Time) error {
	r.deleteCalls++
	r.lastDeleteAlias = aliasEmail
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return nil
}

func TestAliasService_CreateAlias_Success(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{
		DestinationEmail: "Inbox@example.com",
		AliasDomain:      "example.com",
		RequireVerified:  true,
		RuleNamePrefix:   "tuiotp",
		DefaultPriority:  0,
		EnabledByDefault: true,
	})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	created, err := svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "shopee",
		AliasEmail: "Shopee-001@Example.com",
	})
	if err != nil {
		t.Fatalf("CreateAlias() error = %v", err)
	}

	if cf.ensureCalls != 1 || cf.lastEnsureEmail != "inbox@example.com" || !cf.lastEnsureRequire {
		t.Fatalf("destination verification was not called correctly")
	}
	if cf.createCalls != 1 {
		t.Fatalf("expected create routing rule once, got %d", cf.createCalls)
	}
	if cf.lastCreateInput.Name != "tuiotp:SHOPEE:shopee-001" {
		t.Fatalf("unexpected rule name: %q", cf.lastCreateInput.Name)
	}
	if len(cf.lastCreateInput.Destination) != 1 || cf.lastCreateInput.Destination[0] != "inbox@example.com" {
		t.Fatalf("unexpected destination payload: %#v", cf.lastCreateInput.Destination)
	}

	if repo.createCalls != 1 {
		t.Fatalf("expected repo create once, got %d", repo.createCalls)
	}
	if created.AliasEmail != "shopee-001@example.com" || created.Platform != "SHOPEE" {
		t.Fatalf("unexpected created alias result: %+v", created)
	}
}

func TestAliasService_CreateAlias_RollbackRuleOnStoreFailure(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{createErr: errors.New("db insert failed")}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{
		DestinationEmail: "inbox@example.com",
		AliasDomain:      "example.com",
		RuleNamePrefix:   "tuiotp",
	})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "custom",
		AliasEmail: "custom-001@example.com",
	})
	if err == nil {
		t.Fatalf("expected create alias error when repository fails")
	}
	if cf.deleteCalls != 1 {
		t.Fatalf("expected cloudflare delete rollback once, got %d", cf.deleteCalls)
	}
	if cf.lastDeleteRuleID == "" {
		t.Fatalf("expected rollback delete with non-empty rule id")
	}
}

func TestAliasService_CreateAlias_DestinationVerificationFails(t *testing.T) {
	cf := &fakeCloudflare{ensureErr: cloudflare.ErrDestinationNotVerified}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com", RequireVerified: true})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "custom",
		AliasEmail: "custom@example.com",
	})
	if !errors.Is(err, cloudflare.ErrDestinationNotVerified) {
		t.Fatalf("expected ErrDestinationNotVerified, got %v", err)
	}
	if cf.createCalls != 0 {
		t.Fatalf("expected no create rule call when destination verification fails")
	}
	if repo.createCalls != 0 {
		t.Fatalf("expected no repository create when destination verification fails")
	}
}

func TestAliasService_CreateAlias_InvalidPlatformRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "bad platform!",
		AliasEmail: "alias@example.com",
	})
	if err == nil {
		t.Fatalf("expected invalid platform error")
	}
	if cf.createCalls != 0 || repo.createCalls != 0 {
		t.Fatalf("expected no downstream calls for invalid platform")
	}
}

func TestAliasService_ListAliases(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{activeRows: []domain.Alias{{ID: 1, AliasEmail: "a@example.com"}}}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	rows, err := svc.ListAliases(context.Background())
	if err != nil {
		t.Fatalf("ListAliases() error = %v", err)
	}
	if len(rows) != 1 || rows[0].AliasEmail != "a@example.com" {
		t.Fatalf("unexpected list aliases result: %+v", rows)
	}
}

func TestAliasService_DeleteAlias_SuccessAndNotFound(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{activeRows: []domain.Alias{{
		AliasEmail: "alias-001@example.com",
		RuleID:     "rule-1",
	}}}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	if err := svc.DeleteAlias(context.Background(), "Alias-001@Example.com"); err != nil {
		t.Fatalf("DeleteAlias() error = %v", err)
	}
	if cf.deleteCalls != 1 || cf.lastDeleteRuleID != "rule-1" {
		t.Fatalf("unexpected cloudflare delete calls: %d rule=%q", cf.deleteCalls, cf.lastDeleteRuleID)
	}
	if repo.lastFindAlias != "alias-001@example.com" {
		t.Fatalf("expected find lookup with normalized alias, got %q", repo.lastFindAlias)
	}
	if repo.deleteCalls != 1 || repo.lastDeleteAlias != "alias-001@example.com" {
		t.Fatalf("unexpected repo delete calls: %d alias=%q", repo.deleteCalls, repo.lastDeleteAlias)
	}

	err = svc.DeleteAlias(context.Background(), "missing@example.com")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows for missing alias, got %v", err)
	}
}

func TestAliasService_CreateAlias_RollbackFailureJoined(t *testing.T) {
	cf := &fakeCloudflare{deleteErr: errors.New("rollback failed")}
	repo := &fakeAliasRepo{createErr: errors.New("insert failed")}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "CUSTOM",
		AliasEmail: "custom@example.com",
	})
	if err == nil {
		t.Fatalf("expected joined error when rollback fails")
	}
	if !strings.Contains(err.Error(), "insert failed") || !strings.Contains(err.Error(), "rollback delete rule failed") {
		t.Fatalf("expected joined error with both failure reasons, got %v", err)
	}
}

func TestAliasService_CreateAlias_DomainMismatchRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "CUSTOM",
		AliasEmail: "custom@other.com",
	})
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("expected domain mismatch error, got %v", err)
	}
}

func TestAliasService_CreateAlias_LocalPartAppendsActiveDomain(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	created, err := svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "CUSTOM",
		AliasEmail: "custom-001",
	})
	if err != nil {
		t.Fatalf("CreateAlias() error = %v", err)
	}
	if created.AliasEmail != "custom-001@example.com" {
		t.Fatalf("expected auto-appended alias domain, got %q", created.AliasEmail)
	}
}

func TestAliasService_CreateAlias_UnknownDomainRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{
		Platform:   "CUSTOM",
		AliasEmail: "custom@other.com",
	})
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("expected unknown domain validation error, got %v", err)
	}
}

func TestAliasService_ActiveDomainSwitch(t *testing.T) {
	cf1 := &fakeCloudflare{}
	cf2 := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(nil, repo, AliasServiceConfig{
		DestinationEmail: "inbox@example.com",
		DomainClients: map[string]aliasCloudflarePort{
			"example.com": cf1,
			"example.net": cf2,
		},
		Domains:      []string{"example.com", "example.net"},
		ActiveDomain: "example.com",
	})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	if svc.ActiveDomain() != "example.com" {
		t.Fatalf("expected active domain example.com, got %q", svc.ActiveDomain())
	}
	if err := svc.SetActiveDomain("example.net"); err != nil {
		t.Fatalf("SetActiveDomain() error = %v", err)
	}
	if svc.ActiveDomain() != "example.net" {
		t.Fatalf("expected active domain example.net, got %q", svc.ActiveDomain())
	}
	if err := svc.SetActiveDomain("unknown.com"); err == nil {
		t.Fatalf("expected error switching to unknown domain")
	}
}

func TestAliasService_PerDomainRoutingClientSelection(t *testing.T) {
	cfCom := &fakeCloudflare{}
	cfNet := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(nil, repo, AliasServiceConfig{
		DestinationEmail: "inbox@example.com",
		DomainClients: map[string]aliasCloudflarePort{
			"example.com": cfCom,
			"example.net": cfNet,
		},
		Domains:      []string{"example.com", "example.net"},
		ActiveDomain: "example.net",
	})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateAlias(context.Background(), CreateAliasInput{Platform: "SHOP", AliasEmail: "shop"})
	if err != nil {
		t.Fatalf("CreateAlias() error = %v", err)
	}
	if cfNet.createCalls != 1 || cfCom.createCalls != 0 {
		t.Fatalf("expected active domain client to handle create, net=%d com=%d", cfNet.createCalls, cfCom.createCalls)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{AliasEmail: "abc@example.com"})
	if err != nil {
		t.Fatalf("CreateRoutingRuleDirect() error = %v", err)
	}
	if cfCom.createCalls != 1 {
		t.Fatalf("expected .com client used for explicit alias domain, got %d", cfCom.createCalls)
	}
}

func TestAliasService_NewAliasService_AliasDomainRequired(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	_, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com"})
	if err != nil {
		if !strings.Contains(err.Error(), "alias domain is required") {
			t.Fatalf("expected alias domain required error, got %v", err)
		}
		return
	}
	t.Fatalf("expected error for missing alias domain")
}

func TestAliasService_ListRoutingRules_Success(t *testing.T) {
	cf := &fakeCloudflare{
		listResult: []ports.RoutingRule{
			{ID: "r1", Name: "tuiotp:SHOP:a", Enabled: true, AliasEmail: "a@example.com", Destination: []string{"inbox@example.com"}},
			{ID: "r2", Name: "tuiotp:BANK:b", Enabled: false, AliasEmail: "b@example.com", Destination: []string{"inbox@example.com"}},
		},
	}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	rules, err := svc.ListRoutingRules(context.Background())
	if err != nil {
		t.Fatalf("ListRoutingRules() error = %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].ID != "r1" || rules[1].ID != "r2" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
	if cf.listCalls != 1 {
		t.Fatalf("expected 1 list call, got %d", cf.listCalls)
	}
}

func TestAliasService_ListRoutingRules_Error(t *testing.T) {
	cf := &fakeCloudflare{listErr: errors.New("api error")}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.ListRoutingRules(context.Background())
	if err == nil {
		t.Fatalf("expected error from ListRoutingRules")
	}
	if !strings.Contains(err.Error(), "list routing rules") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestAliasService_UpdateRoutingRule_Success(t *testing.T) {
	cf := &fakeCloudflare{
		updateResult: ports.RoutingRule{ID: "r1", Name: "test", Enabled: false},
	}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	rule, err := svc.UpdateRoutingRule(context.Background(), ports.UpdateRoutingRuleInput{
		ID:          "r1",
		Name:        "test",
		AliasEmail:  "a@example.com",
		Destination: []string{"inbox@example.com"},
		Enabled:     false,
	})
	if err != nil {
		t.Fatalf("UpdateRoutingRule() error = %v", err)
	}
	if rule.ID != "r1" || rule.Enabled != false {
		t.Fatalf("unexpected update result: %+v", rule)
	}
	if cf.updateCalls != 1 {
		t.Fatalf("expected 1 update call, got %d", cf.updateCalls)
	}
}

func TestAliasService_UpdateRoutingRule_MissingIDRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.UpdateRoutingRule(context.Background(), ports.UpdateRoutingRuleInput{Name: "test"})
	if err == nil || !strings.Contains(err.Error(), "rule id is required") {
		t.Fatalf("expected rule id required error, got %v", err)
	}
	if cf.updateCalls != 0 {
		t.Fatalf("expected no update call, got %d", cf.updateCalls)
	}
}

func TestAliasService_UpdateRoutingRule_Error(t *testing.T) {
	cf := &fakeCloudflare{updateErr: errors.New("update failed")}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.UpdateRoutingRule(context.Background(), ports.UpdateRoutingRuleInput{ID: "r1", Name: "test"})
	if err == nil {
		t.Fatalf("expected error from UpdateRoutingRule")
	}
	if !strings.Contains(err.Error(), "update routing rule") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestAliasService_CreateRoutingRuleDirect_Success(t *testing.T) {
	cf := &fakeCloudflare{
		createResult: ports.RoutingRule{ID: "r-new", Name: "tuiotp:shop", Enabled: true, AliasEmail: "shop@example.com", Destination: []string{"inbox@example.com"}},
	}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	rule, err := svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{
		AliasEmail: "shop@example.com",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateRoutingRuleDirect() error = %v", err)
	}
	if rule.ID != "r-new" {
		t.Fatalf("expected rule ID r-new, got %q", rule.ID)
	}
	if cf.createCalls != 1 {
		t.Fatalf("expected 1 create call, got %d", cf.createCalls)
	}
	// Should use configured destination email
	if len(cf.lastCreateInput.Destination) != 1 || cf.lastCreateInput.Destination[0] != "inbox@example.com" {
		t.Fatalf("expected default destination, got %v", cf.lastCreateInput.Destination)
	}
	// Should auto-build rule name from local part
	if cf.lastCreateInput.Name != "tuiotp:shop" {
		t.Fatalf("expected auto-built rule name tuiotp:shop, got %q", cf.lastCreateInput.Name)
	}
	// Should NOT touch the repo at all
	if repo.createCalls != 0 {
		t.Fatalf("expected no repo create call for direct rule, got %d", repo.createCalls)
	}
}

func TestAliasService_CreateRoutingRuleDirect_MissingEmailRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{})
	if err == nil || !strings.Contains(err.Error(), "alias email is required") {
		t.Fatalf("expected alias email required error, got %v", err)
	}
	if cf.createCalls != 0 {
		t.Fatalf("expected no create call, got %d", cf.createCalls)
	}
}

func TestAliasService_CreateRoutingRuleDirect_InvalidEmailRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{
		AliasEmail: "bad@@example.com",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid alias email") {
		t.Fatalf("expected invalid email error, got %v", err)
	}
}

func TestAliasService_CreateRoutingRuleDirect_Error(t *testing.T) {
	cf := &fakeCloudflare{createErr: errors.New("cf create failed")}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{
		AliasEmail: "shop@example.com",
	})
	if err == nil {
		t.Fatalf("expected error from CreateRoutingRuleDirect")
	}
	if !strings.Contains(err.Error(), "create routing rule direct") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}

func TestAliasService_CreateRoutingRuleDirect_CustomDestinationPreserved(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{
		AliasEmail:  "shop@example.com",
		Destination: []string{"custom@example.com"},
	})
	if err != nil {
		t.Fatalf("CreateRoutingRuleDirect() error = %v", err)
	}
	if len(cf.lastCreateInput.Destination) != 1 || cf.lastCreateInput.Destination[0] != "custom@example.com" {
		t.Fatalf("expected custom destination preserved, got %v", cf.lastCreateInput.Destination)
	}
}

func TestAliasService_CreateRoutingRuleDirect_CustomNamePreserved(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	_, err = svc.CreateRoutingRuleDirect(context.Background(), ports.CreateRoutingRuleInput{
		AliasEmail: "shop@example.com",
		Name:       "my-custom-rule",
	})
	if err != nil {
		t.Fatalf("CreateRoutingRuleDirect() error = %v", err)
	}
	if cf.lastCreateInput.Name != "my-custom-rule" {
		t.Fatalf("expected custom name preserved, got %q", cf.lastCreateInput.Name)
	}
}

func TestAliasService_DeleteRoutingRuleByID_Success(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	if err := svc.DeleteRoutingRuleByID(context.Background(), "rule-1"); err != nil {
		t.Fatalf("DeleteRoutingRuleByID() error = %v", err)
	}
	if cf.deleteCalls != 1 || cf.lastDeleteRuleID != "rule-1" {
		t.Fatalf("unexpected delete calls: %d rule=%q", cf.deleteCalls, cf.lastDeleteRuleID)
	}
}

func TestAliasService_DeleteRoutingRuleByID_MissingIDRejected(t *testing.T) {
	cf := &fakeCloudflare{}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	err = svc.DeleteRoutingRuleByID(context.Background(), "  ")
	if err == nil || !strings.Contains(err.Error(), "rule id is required") {
		t.Fatalf("expected rule id required error, got %v", err)
	}
	if cf.deleteCalls != 0 {
		t.Fatalf("expected no delete call, got %d", cf.deleteCalls)
	}
}

func TestAliasService_DeleteRoutingRuleByID_Error(t *testing.T) {
	cf := &fakeCloudflare{deleteErr: errors.New("cf delete error")}
	repo := &fakeAliasRepo{}

	svc, err := NewAliasService(cf, repo, AliasServiceConfig{DestinationEmail: "inbox@example.com", AliasDomain: "example.com"})
	if err != nil {
		t.Fatalf("NewAliasService() error = %v", err)
	}

	err = svc.DeleteRoutingRuleByID(context.Background(), "rule-1")
	if err == nil {
		t.Fatalf("expected error from DeleteRoutingRuleByID")
	}
	if !strings.Contains(err.Error(), "delete routing rule by id") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}
