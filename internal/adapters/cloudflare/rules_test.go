package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newRulesTestClient(t *testing.T, rawBaseURL string) *Client {
	t.Helper()
	u, err := url.Parse(rawBaseURL)
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}

	c, err := NewClient(ClientConfig{
		APIToken:             "token",
		ZoneID:               "zone-123",
		BaseURL:              rawBaseURL,
		AllowedHosts:         []string{u.Hostname()},
		AllowInsecureBaseURL: true,
		MaxRetries:           0,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	c.sleepFn = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestBuildRuleName(t *testing.T) {
	got := BuildRuleName("tuiotp", "shopee", "shopee-abc")
	want := "tuiotp:SHOPEE:shopee-abc"
	if got != want {
		t.Fatalf("BuildRuleName() = %q, want %q", got, want)
	}
}

func TestClient_CreateRoutingRule(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/zones/zone-123/email/routing/rules" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}

		if payload["name"] != "tuiotp:SHOPEE:shopee-001" {
			t.Fatalf("unexpected rule name: %#v", payload["name"])
		}
		if payload["enabled"] != true {
			t.Fatalf("expected enabled=true, got %#v", payload["enabled"])
		}
		if int(payload["priority"].(float64)) != 0 {
			t.Fatalf("expected priority=0, got %#v", payload["priority"])
		}

		matchers, ok := payload["matchers"].([]any)
		if !ok || len(matchers) != 1 {
			t.Fatalf("expected exactly one matcher, got %#v", payload["matchers"])
		}
		matcher, ok := matchers[0].(map[string]any)
		if !ok {
			t.Fatalf("expected matcher object, got %#v", matchers[0])
		}
		if matcher["field"] != "to" || matcher["type"] != "literal" || matcher["value"] != "shopee-001@example.com" {
			t.Fatalf("unexpected matcher payload: %#v", matcher)
		}

		actions, ok := payload["actions"].([]any)
		if !ok || len(actions) != 1 {
			t.Fatalf("expected exactly one action, got %#v", payload["actions"])
		}
		action, ok := actions[0].(map[string]any)
		if !ok {
			t.Fatalf("expected action object, got %#v", actions[0])
		}
		if action["type"] != "forward" {
			t.Fatalf("expected forward action, got %#v", action)
		}
		values, ok := action["value"].([]any)
		if !ok || len(values) != 1 || values[0] != "inbox@example.com" {
			t.Fatalf("unexpected action value payload: %#v", action["value"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"rule-1","name":"tuiotp:SHOPEE:shopee-001","enabled":true,"priority":0}}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rule, err := c.CreateRoutingRule(context.Background(), CreateRuleInput{
		Name:       "tuiotp:SHOPEE:shopee-001",
		AliasEmail: "Shopee-001@example.com",
		Destination: []string{
			"Inbox@example.com",
		},
		Enabled:  true,
		Priority: 0,
	})
	if err != nil {
		t.Fatalf("CreateRoutingRule() error = %v", err)
	}

	if rule.ID != "rule-1" {
		t.Fatalf("expected rule id rule-1, got %q", rule.ID)
	}
}

func TestClient_ListRoutingRules_FilterByPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success":true,
			"result":[
				{"id":"1","name":"tuiotp:SHOPEE:a","enabled":true,"priority":0},
				{"id":"2","name":"external:OTHER:b","enabled":true,"priority":0}
			],
			"result_info":{"page":1,"per_page":50,"total_pages":1,"count":2,"total_count":2}
		}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rules, err := c.ListRoutingRules(context.Background(), ListRulesFilter{NamePrefix: "tuiotp:"})
	if err != nil {
		t.Fatalf("ListRoutingRules() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 filtered rule, got %d", len(rules))
	}
	if rules[0].ID != "1" {
		t.Fatalf("expected first filtered rule id=1, got %s", rules[0].ID)
	}
}

func TestClient_DeleteRoutingRule_Idempotent404(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		if !strings.HasPrefix(r.URL.Path, "/zones/zone-123/email/routing/rules/") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	err := c.DeleteRoutingRule(context.Background(), "missing-rule")
	if err != nil {
		t.Fatalf("DeleteRoutingRule() expected nil on 404, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected one delete attempt, got %d", got)
	}
}

func TestClient_DeleteRoutingRule_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("expected DELETE, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	if err := c.DeleteRoutingRule(context.Background(), "rule-1"); err != nil {
		t.Fatalf("DeleteRoutingRule() error = %v", err)
	}
}

func TestClient_DeleteRoutingRule_PathEscaped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "rule%2Fwith%2Fslash") {
			t.Fatalf("expected path-escaped rule id, got path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	if err := c.DeleteRoutingRule(context.Background(), "rule/with/slash"); err != nil {
		t.Fatalf("DeleteRoutingRule() error = %v", err)
	}
}

func TestClient_CreateRoutingRule_InvalidEmailsRejected(t *testing.T) {
	c := newRulesTestClient(t, "http://localhost")

	_, err := c.CreateRoutingRule(context.Background(), CreateRuleInput{
		Name:        "tuiotp:CUSTOM:bad",
		AliasEmail:  "not-an-email",
		Destination: []string{"dest@example.com"},
		Enabled:     true,
	})
	if err == nil {
		t.Fatalf("expected invalid alias email error")
	}

	_, err = c.CreateRoutingRule(context.Background(), CreateRuleInput{
		Name:        "tuiotp:CUSTOM:bad-dest",
		AliasEmail:  "alias@example.com",
		Destination: []string{"not-an-email"},
		Enabled:     true,
	})
	if err == nil {
		t.Fatalf("expected invalid destination email error")
	}
}

func TestClient_ListRoutingRules_Pagination(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}

		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")

		switch page {
		case "1", "":
			_, _ = w.Write([]byte(`{
				"success": true,
				"result": [
					{"id":"1","name":"tuiotp:A:a","enabled":true,"priority":0},
					{"id":"2","name":"tuiotp:B:b","enabled":false,"priority":1}
				],
				"result_info": {"page":1,"per_page":2,"total_pages":2,"count":2,"total_count":3}
			}`))
		case "2":
			_, _ = w.Write([]byte(`{
				"success": true,
				"result": [
					{"id":"3","name":"tuiotp:C:c","enabled":true,"priority":2}
				],
				"result_info": {"page":2,"per_page":2,"total_pages":2,"count":1,"total_count":3}
			}`))
		default:
			t.Fatalf("unexpected page: %s", page)
		}
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rules, err := c.ListRoutingRules(context.Background(), ListRulesFilter{})
	if err != nil {
		t.Fatalf("ListRoutingRules() error = %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules from 2 pages, got %d", len(rules))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 API calls for 2 pages, got %d", got)
	}

	// Verify ordering preserved
	if rules[0].ID != "1" || rules[1].ID != "2" || rules[2].ID != "3" {
		t.Fatalf("unexpected rule ordering: %v", rules)
	}
}

func TestClient_ListRoutingRules_SinglePage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": [{"id":"1","name":"tuiotp:A:a","enabled":true,"priority":0}],
			"result_info": {"page":1,"per_page":50,"total_pages":1,"count":1,"total_count":1}
		}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rules, err := c.ListRoutingRules(context.Background(), ListRulesFilter{})
	if err != nil {
		t.Fatalf("ListRoutingRules() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
}

func TestClient_ListRoutingRules_NoResultInfo(t *testing.T) {
	// When result_info is missing or has total_pages=0, should still work (single page)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": [{"id":"1","name":"x","enabled":true,"priority":0}]
		}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rules, err := c.ListRoutingRules(context.Background(), ListRulesFilter{})
	if err != nil {
		t.Fatalf("ListRoutingRules() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
}

func TestClient_UpdateRoutingRule_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/zones/zone-123/email/routing/rules/rule-1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if payload["enabled"] != false {
			t.Fatalf("expected enabled=false, got %#v", payload["enabled"])
		}
		if payload["name"] != "tuiotp:SHOPEE:shopee-001" {
			t.Fatalf("expected rule name tuiotp:SHOPEE:shopee-001, got %#v", payload["name"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"rule-1","name":"tuiotp:SHOPEE:shopee-001","enabled":false,"priority":0}}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	rule, err := c.UpdateRoutingRule(context.Background(), UpdateRuleInput{
		ID:          "rule-1",
		Name:        "tuiotp:SHOPEE:shopee-001",
		AliasEmail:  "shopee-001@example.com",
		Destination: []string{"inbox@example.com"},
		Enabled:     false,
		Priority:    0,
	})
	if err != nil {
		t.Fatalf("UpdateRoutingRule() error = %v", err)
	}
	if rule.ID != "rule-1" || rule.Enabled != false {
		t.Fatalf("unexpected update result: %+v", rule)
	}
}

func TestClient_UpdateRoutingRule_MissingIDRejected(t *testing.T) {
	c := newRulesTestClient(t, "http://localhost")

	_, err := c.UpdateRoutingRule(context.Background(), UpdateRuleInput{
		Name:        "test",
		AliasEmail:  "a@example.com",
		Destination: []string{"b@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "rule id is required") {
		t.Fatalf("expected rule id required error, got %v", err)
	}
}

func TestClient_UpdateRoutingRule_InvalidEmailsRejected(t *testing.T) {
	c := newRulesTestClient(t, "http://localhost")

	_, err := c.UpdateRoutingRule(context.Background(), UpdateRuleInput{
		ID:          "rule-1",
		Name:        "test",
		AliasEmail:  "not-an-email",
		Destination: []string{"dest@example.com"},
	})
	if err == nil {
		t.Fatalf("expected invalid alias email error")
	}

	_, err = c.UpdateRoutingRule(context.Background(), UpdateRuleInput{
		ID:          "rule-1",
		Name:        "test",
		AliasEmail:  "alias@example.com",
		Destination: []string{"not-an-email"},
	})
	if err == nil {
		t.Fatalf("expected invalid destination email error")
	}
}

func TestClient_UpdateRoutingRule_PathEscaped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "rule%2Fwith%2Fslash") {
			t.Fatalf("expected path-escaped rule id, got path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"rule/with/slash","name":"test","enabled":true,"priority":0}}`))
	}))
	defer server.Close()

	c := newRulesTestClient(t, server.URL)

	_, err := c.UpdateRoutingRule(context.Background(), UpdateRuleInput{
		ID:          "rule/with/slash",
		Name:        "test",
		AliasEmail:  "a@example.com",
		Destination: []string{"b@example.com"},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("UpdateRoutingRule() error = %v", err)
	}
}
