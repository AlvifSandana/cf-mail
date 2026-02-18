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

func newTestClient(t *testing.T, rawBaseURL string, maxRetries int) *Client {
	t.Helper()

	u, err := url.Parse(rawBaseURL)
	if err != nil {
		t.Fatalf("parse test base URL: %v", err)
	}

	c, err := NewClient(ClientConfig{
		APIToken:             "token",
		ZoneID:               "zone",
		BaseURL:              rawBaseURL,
		AllowInsecureBaseURL: true,
		AllowedHosts:         []string{u.Hostname()},
		MaxRetries:           maxRetries,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	c.sleepFn = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestClient_DoJSON_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatalf("expected Authorization header")
		}
		if r.URL.Path != "/client/v4/test/success" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"ok"}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL+"/client/v4", 0)

	var out map[string]any
	err := c.DoJSON(context.Background(), http.MethodGet, "/test/success", nil, nil, &out)
	if err != nil {
		t.Fatalf("DoJSON() error = %v", err)
	}

	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("expected ok=true response")
	}
}

func TestClient_DoJSON_RetryOn429ThenSuccess(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 1)

	var out map[string]any
	err := c.DoJSON(context.Background(), http.MethodGet, "/retry-429", nil, nil, &out)
	if err != nil {
		t.Fatalf("DoJSON() error = %v", err)
	}

	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestClient_DoJSON_RetryOn5xxThenSuccess(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 1)

	var out map[string]any
	err := c.DoJSON(context.Background(), http.MethodGet, "/retry-5xx", nil, nil, &out)
	if err != nil {
		t.Fatalf("DoJSON() error = %v", err)
	}

	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestClient_DoJSON_NoRetryOn4xx(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"errors":[{"message":"bad request"}]}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 3)

	err := c.DoJSON(context.Background(), http.MethodGet, "/bad-request", nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for 400 response")
	}

	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt, got %d", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	d, ok := parseRetryAfter("2")
	if !ok || d != 2*time.Second {
		t.Fatalf("expected 2s retry-after, got ok=%v d=%v", ok, d)
	}

	ts := time.Now().Add(1 * time.Second).UTC().Format(http.TimeFormat)
	if _, ok := parseRetryAfter(ts); !ok {
		t.Fatalf("expected retry-after HTTP date to parse")
	}
}

func TestClient_DoJSON_PostBodyAndDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["name"] != "alias" {
			t.Fatalf("expected request body name=alias")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":"123"}}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 0)

	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}

	err := c.DoJSON(context.Background(), http.MethodPost, "/post", nil, map[string]any{"name": "alias"}, &out)
	if err != nil {
		t.Fatalf("DoJSON(post) error = %v", err)
	}
	if !out.OK || out.Result.ID != "123" {
		t.Fatalf("unexpected decoded output: %+v", out)
	}
}

func TestNewClient_RejectsInsecureBaseURLByDefault(t *testing.T) {
	_, err := NewClient(ClientConfig{
		APIToken: "token",
		ZoneID:   "zone",
		BaseURL:  "http://api.cloudflare.com/client/v4",
	})
	if err == nil {
		t.Fatalf("expected insecure base URL to be rejected")
	}
}

func TestClient_DoJSON_NoRetryForPost(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false}`))
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 3)

	err := c.DoJSON(context.Background(), http.MethodPost, "/post-no-retry", nil, map[string]any{"name": "x"}, nil)
	if err == nil {
		t.Fatalf("expected error for 502 response")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected exactly 1 attempt for POST, got %d", got)
	}
}

func TestClient_DoJSON_ResponseBodyTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		big := make([]byte, maxResponseBodyBytes+8)
		for i := range big {
			big[i] = 'a'
		}
		_, _ = w.Write(big)
	}))
	defer server.Close()

	c := newTestClient(t, server.URL, 0)
	err := c.DoJSON(context.Background(), http.MethodGet, "/too-large", nil, nil, nil)
	if err == nil {
		t.Fatalf("expected error for oversized response body")
	}
	if err.Error() != "response body too large" {
		t.Fatalf("expected response body too large error, got %v", err)
	}
}
