package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
)

var (
	ErrDestinationNotFound    = errors.New("cloudflare destination address not found")
	ErrDestinationNotVerified = errors.New("cloudflare destination address is not verified")
)

type DestinationAddress struct {
	ID       string
	Email    string
	Verified bool
}

func (c *Client) ListDestinationAddresses(ctx context.Context) ([]DestinationAddress, error) {
	if c == nil {
		return nil, fmt.Errorf("cloudflare client is nil")
	}

	var resp cloudflareEnvelope[[]cloudflareDestinationAddress]
	if err := c.DoJSON(ctx, http.MethodGet, "/zones/"+c.zoneID+"/email/routing/addresses", nil, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("cloudflare list destination addresses unsuccessful")
	}

	out := make([]DestinationAddress, 0, len(resp.Result))
	for _, item := range resp.Result {
		out = append(out, DestinationAddress{
			ID:       strings.TrimSpace(item.ID),
			Email:    strings.ToLower(strings.TrimSpace(item.Email)),
			Verified: parseVerified(item.Verified),
		})
	}

	return out, nil
}

func (c *Client) IsDestinationVerified(ctx context.Context, email string) (bool, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return false, err
	}

	addresses, err := c.ListDestinationAddresses(ctx)
	if err != nil {
		return false, err
	}

	for _, addr := range addresses {
		if addr.Email == normalized {
			return addr.Verified, nil
		}
	}

	return false, ErrDestinationNotFound
}

func (c *Client) EnsureDestinationVerified(ctx context.Context, email string, requireVerified bool) error {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return err
	}

	if !requireVerified {
		return nil
	}

	verified, err := c.IsDestinationVerified(ctx, normalized)
	if err != nil {
		return err
	}
	if !verified {
		return ErrDestinationNotVerified
	}

	return nil
}

type cloudflareDestinationAddress struct {
	ID       string          `json:"id"`
	Email    string          `json:"email"`
	Verified json.RawMessage `json:"verified"`
}

func parseVerified(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}

	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "true", "verified", "active", "enabled", "ok":
			return true
		default:
			return false
		}
	}

	return false
}

func normalizeEmail(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("destination email is required")
	}

	parsed, err := mail.ParseAddress(v)
	if err != nil {
		return "", fmt.Errorf("invalid destination email: %w", err)
	}

	return strings.ToLower(strings.TrimSpace(parsed.Address)), nil
}
