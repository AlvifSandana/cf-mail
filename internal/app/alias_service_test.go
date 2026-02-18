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

	lastEnsureEmail   string
	lastEnsureRequire bool
	lastCreateInput   ports.CreateRoutingRuleInput
	lastDeleteRuleID  string

	ensureErr error
	createErr error
	deleteErr error

	createResult ports.RoutingRule
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
