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
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"1","name":"tuiotp:SHOPEE:a","enabled":true,"priority":0},
			{"id":"2","name":"external:OTHER:b","enabled":true,"priority":0}
		]}`))
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
