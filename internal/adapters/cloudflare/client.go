package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.cloudflare.com/client/v4"
const maxResponseBodyBytes int64 = 4 << 20

type ClientConfig struct {
	APIToken  string
	AccountID string
	ZoneID    string

	BaseURL              string
	AllowedHosts         []string
	AllowInsecureBaseURL bool
	Timeout              time.Duration
	MaxRetries           int
	BaseBackoff          time.Duration
	MaxBackoff           time.Duration
	UserAgent            string
}

type Client struct {
	apiToken  string
	accountID string
	zoneID    string

	baseURL     string
	httpClient  *http.Client
	maxRetries  int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	userAgent   string

	sleepFn func(context.Context, time.Duration) error
}

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("cloudflare request failed with status %d", e.StatusCode)
}

func NewClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.ZoneID) == "" {
		return nil, fmt.Errorf("cloudflare zone id is required")
	}
	return newClient(cfg)
}

// NewDiscoveryClient creates a Client without requiring a ZoneID.
// This is used exclusively for the auto-discovery phase (e.g. ListZones)
// before the zone IDs are known. Operations that require a ZoneID
// (routing rules, etc.) will fail if called on this client.
func NewDiscoveryClient(cfg ClientConfig) (*Client, error) {
	return newClient(cfg)
}

func newClient(cfg ClientConfig) (*Client, error) {
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, fmt.Errorf("cloudflare api token is required")
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse cloudflare base url: %w", err)
	}
	if parsedBaseURL.Hostname() == "" {
		return nil, fmt.Errorf("cloudflare base url host is required")
	}
	if !cfg.AllowInsecureBaseURL && !strings.EqualFold(parsedBaseURL.Scheme, "https") {
		return nil, fmt.Errorf("cloudflare base url must use https")
	}

	allowedHosts := cfg.AllowedHosts
	if len(allowedHosts) == 0 {
		allowedHosts = []string{"api.cloudflare.com"}
	}
	if !hostAllowed(parsedBaseURL.Hostname(), allowedHosts) {
		return nil, fmt.Errorf("cloudflare base url host %q is not allowed", parsedBaseURL.Hostname())
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	baseBackoff := cfg.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = 300 * time.Millisecond
	}

	maxBackoff := cfg.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = 3 * time.Second
	}

	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = "tuiotp/0.1"
	}

	return &Client{
		apiToken:  cfg.APIToken,
		accountID: strings.TrimSpace(cfg.AccountID),
		zoneID:    strings.TrimSpace(cfg.ZoneID),

		baseURL:     strings.TrimRight(parsedBaseURL.String(), "/"),
		httpClient:  &http.Client{Timeout: timeout},
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		maxBackoff:  maxBackoff,
		userAgent:   userAgent,
		sleepFn:     sleepWithContext,
	}, nil
}

func (c *Client) ZoneID() string {
	return c.zoneID
}

func (c *Client) AccountID() string {
	return c.accountID
}

func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, in any, out any) error {
	var body []byte
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		body = encoded
	}

	respBody, statusCode, err := c.do(ctx, method, path, query, body)
	if err != nil {
		return err
	}

	if statusCode < 200 || statusCode >= 300 {
		return &HTTPStatusError{StatusCode: statusCode}
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response body: %w", err)
		}
	}

	return nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	if c == nil {
		return nil, 0, fmt.Errorf("cloudflare client is nil")
	}
	if strings.TrimSpace(method) == "" {
		return nil, 0, fmt.Errorf("http method is required")
	}
	if strings.TrimSpace(path) == "" {
		return nil, 0, fmt.Errorf("request path is required")
	}

	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, 0, fmt.Errorf("parse cloudflare base url: %w", err)
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	retryEnabled := isIdempotentMethod(method)

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
		if err != nil {
			return nil, 0, fmt.Errorf("build request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+c.apiToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, reqErr := c.httpClient.Do(req)
		if reqErr != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}

			if retryEnabled && attempt < c.maxRetries && isRetryableNetworkError(reqErr) {
				if err := c.sleepFn(ctx, c.backoffDuration(attempt, "")); err != nil {
					return nil, 0, err
				}
				continue
			}

			return nil, 0, fmt.Errorf("send request: %w", reqErr)
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, fmt.Errorf("read response body: %w", readErr)
		}
		if int64(len(respBody)) > maxResponseBodyBytes {
			return nil, resp.StatusCode, fmt.Errorf("response body too large")
		}

		retryAfter := resp.Header.Get("Retry-After")
		shouldRetry := retryEnabled && shouldRetryStatus(resp.StatusCode)

		if shouldRetry && attempt < c.maxRetries {
			if err := c.sleepFn(ctx, c.backoffDuration(attempt, retryAfter)); err != nil {
				return nil, 0, err
			}
			continue
		}

		return respBody, resp.StatusCode, nil
	}

	return nil, 0, fmt.Errorf("request exhausted retries")
}

func shouldRetryStatus(statusCode int) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	return statusCode >= 500 && statusCode <= 599
}

func isIdempotentMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

func isRetryableNetworkError(err error) bool {
	var netErr net.Error
	if ok := errors.As(err, &netErr); ok {
		return netErr.Timeout()
	}
	return false
}

func (c *Client) backoffDuration(attempt int, retryAfterHeader string) time.Duration {
	if d, ok := parseRetryAfter(retryAfterHeader); ok {
		if d > c.maxBackoff {
			return c.maxBackoff
		}
		if d < 0 {
			return 0
		}
		return d
	}

	multiplier := math.Pow(2, float64(attempt))
	d := time.Duration(float64(c.baseBackoff) * multiplier)
	if d > c.maxBackoff {
		return c.maxBackoff
	}
	if d < 0 {
		return 0
	}
	return d
}

func parseRetryAfter(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}

	if sec, err := strconv.Atoi(v); err == nil {
		return time.Duration(sec) * time.Second, true
	}

	if when, err := http.ParseTime(v); err == nil {
		return time.Until(when), true
	}

	return 0, false
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}

	for _, item := range allowedHosts {
		if strings.EqualFold(host, strings.TrimSpace(item)) {
			return true
		}
	}

	return false
}
