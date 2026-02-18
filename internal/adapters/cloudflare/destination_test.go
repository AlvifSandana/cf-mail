package cloudflare

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func newDestinationTestClient(t *testing.T, rawBaseURL string) *Client {
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

func TestClient_IsDestinationVerified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones/zone-123/email/routing/addresses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": [
				{"id":"1","email":"dest1@example.com","verified":true},
				{"id":"2","email":"dest2@example.com","verified":"pending"}
			]
		}`))
	}))
	defer server.Close()

	c := newDestinationTestClient(t, server.URL)

	verified, err := c.IsDestinationVerified(context.Background(), "DEST1@example.com")
	if err != nil {
		t.Fatalf("IsDestinationVerified(dest1) error = %v", err)
	}
	if !verified {
		t.Fatalf("expected dest1 to be verified")
	}

	verified, err = c.IsDestinationVerified(context.Background(), "dest2@example.com")
	if err != nil {
		t.Fatalf("IsDestinationVerified(dest2) error = %v", err)
	}
	if verified {
		t.Fatalf("expected dest2 to be not verified")
	}

	_, err = c.IsDestinationVerified(context.Background(), "missing@example.com")
	if !errors.Is(err, ErrDestinationNotFound) {
		t.Fatalf("expected ErrDestinationNotFound, got %v", err)
	}
}

func TestClient_EnsureDestinationVerified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": [
				{"id":"1","email":"verified@example.com","verified":"verified"},
				{"id":"2","email":"pending@example.com","verified":false}
			]
		}`))
	}))
	defer server.Close()

	c := newDestinationTestClient(t, server.URL)

	if err := c.EnsureDestinationVerified(context.Background(), "verified@example.com", true); err != nil {
		t.Fatalf("EnsureDestinationVerified(verified) error = %v", err)
	}

	err := c.EnsureDestinationVerified(context.Background(), "pending@example.com", true)
	if !errors.Is(err, ErrDestinationNotVerified) {
		t.Fatalf("expected ErrDestinationNotVerified, got %v", err)
	}

	if err := c.EnsureDestinationVerified(context.Background(), "pending@example.com", false); err != nil {
		t.Fatalf("EnsureDestinationVerified(require=false) should bypass, got %v", err)
	}
}

func TestClient_EnsureDestinationVerified_BypassNoAPIRequest(t *testing.T) {
	var hit int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hit, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer server.Close()

	c := newDestinationTestClient(t, server.URL)

	if err := c.EnsureDestinationVerified(context.Background(), "User Name <pending@example.com>", false); err != nil {
		t.Fatalf("EnsureDestinationVerified(require=false) error = %v", err)
	}

	if got := atomic.LoadInt32(&hit); got != 0 {
		t.Fatalf("expected no API request when require_verified=false, got %d", got)
	}
}

func TestNormalizeEmail(t *testing.T) {
	normalized, err := normalizeEmail(" User Name <TeSt@example.com> ")
	if err != nil {
		t.Fatalf("normalizeEmail() error = %v", err)
	}
	if normalized != "test@example.com" {
		t.Fatalf("expected normalized test@example.com, got %q", normalized)
	}

	if _, err := normalizeEmail("not-an-email"); err == nil {
		t.Fatalf("expected normalizeEmail invalid email error")
	}
}
