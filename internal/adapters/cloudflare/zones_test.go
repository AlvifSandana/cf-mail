package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newDiscoveryTestClient(t *testing.T, rawBaseURL string) *Client {
	t.Helper()

	u, err := url.Parse(rawBaseURL)
	if err != nil {
		t.Fatalf("parse test base URL: %v", err)
	}

	c, err := NewDiscoveryClient(ClientConfig{
		APIToken:             "token",
		BaseURL:              rawBaseURL,
		AllowInsecureBaseURL: true,
		AllowedHosts:         []string{u.Hostname()},
		MaxRetries:           0,
	})
	if err != nil {
		t.Fatalf("NewDiscoveryClient() error = %v", err)
	}
	c.sleepFn = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestClient_ListZones_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("status") != "active" {
			t.Fatalf("expected status=active query param")
		}

		resp := cloudflarePaginatedEnvelope[[]Zone]{
			Success: true,
			Result: []Zone{
				{ID: "zone-aaa", Name: "example.com", Status: "active"},
				{ID: "zone-bbb", Name: "example.net", Status: "active"},
			},
			ResultInfo: cloudflareResultInfo{
				Page:       1,
				TotalPages: 1,
				Count:      2,
				TotalCount: 2,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newDiscoveryTestClient(t, server.URL)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(zones))
	}
	if zones[0].ID != "zone-aaa" || zones[0].Name != "example.com" {
		t.Fatalf("unexpected first zone: %+v", zones[0])
	}
	if zones[1].ID != "zone-bbb" || zones[1].Name != "example.net" {
		t.Fatalf("unexpected second zone: %+v", zones[1])
	}
}

func TestClient_ListZones_Pagination(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n := atomic.AddInt32(&calls, 1)

		var resp cloudflarePaginatedEnvelope[[]Zone]
		switch page {
		case "1":
			resp = cloudflarePaginatedEnvelope[[]Zone]{
				Success: true,
				Result: []Zone{
					{ID: "z1", Name: "page1.com", Status: "active"},
				},
				ResultInfo: cloudflareResultInfo{Page: 1, TotalPages: 2, Count: 1, TotalCount: 2},
			}
		case "2":
			resp = cloudflarePaginatedEnvelope[[]Zone]{
				Success: true,
				Result: []Zone{
					{ID: "z2", Name: "page2.com", Status: "active"},
				},
				ResultInfo: cloudflareResultInfo{Page: 2, TotalPages: 2, Count: 1, TotalCount: 2},
			}
		default:
			t.Fatalf("unexpected page: %s (call #%d)", page, n)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newDiscoveryTestClient(t, server.URL)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones across pages, got %d", len(zones))
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 API calls for pagination, got %d", got)
	}
}

func TestClient_ListZones_FiltersInactiveZones(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := cloudflarePaginatedEnvelope[[]Zone]{
			Success: true,
			Result: []Zone{
				{ID: "z1", Name: "active.com", Status: "active"},
				{ID: "z2", Name: "pending.com", Status: "pending"},
				{ID: "z3", Name: "init.com", Status: "initializing"},
			},
			ResultInfo: cloudflareResultInfo{Page: 1, TotalPages: 1},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newDiscoveryTestClient(t, server.URL)
	zones, err := c.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones() error = %v", err)
	}
	if len(zones) != 1 {
		t.Fatalf("expected 1 active zone, got %d", len(zones))
	}
	if zones[0].Name != "active.com" {
		t.Fatalf("expected active.com, got %s", zones[0].Name)
	}
}

func TestClient_ListZones_APIFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"forbidden"}]}`))
	}))
	defer server.Close()

	c := newDiscoveryTestClient(t, server.URL)
	_, err := c.ListZones(context.Background())
	if err == nil {
		t.Fatalf("expected error for 403 response")
	}
}

func TestNewDiscoveryClient_RequiresAPIToken(t *testing.T) {
	_, err := NewDiscoveryClient(ClientConfig{
		APIToken:             "",
		AllowInsecureBaseURL: true,
	})
	if err == nil {
		t.Fatalf("expected error for empty API token")
	}
}

func TestNewDiscoveryClient_DoesNotRequireZoneID(t *testing.T) {
	c, err := NewDiscoveryClient(ClientConfig{
		APIToken:             "token",
		AllowInsecureBaseURL: true,
		BaseURL:              "https://api.cloudflare.com/client/v4",
	})
	if err != nil {
		t.Fatalf("NewDiscoveryClient() error = %v", err)
	}
	if c.ZoneID() != "" {
		t.Fatalf("expected empty zone ID for discovery client")
	}
}
