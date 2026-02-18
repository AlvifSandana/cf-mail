package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
)

type RoutingRule struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`

	Matchers []RoutingMatcher `json:"matchers"`
	Actions  []RoutingAction  `json:"actions"`
}

type RoutingMatcher struct {
	Type  string `json:"type"`
	Field string `json:"field"`
	Value string `json:"value"`
}

type RoutingAction struct {
	Type  string                 `json:"type"`
	Value []string               `json:"value,omitempty"`
	Raw   map[string]interface{} `json:"-"`
}

type CreateRuleInput struct {
	Name        string
	AliasEmail  string
	Destination []string
	Enabled     bool
	Priority    int
}

type ListRulesFilter struct {
	NamePrefix string
}

func (c *Client) CreateRoutingRule(ctx context.Context, in CreateRuleInput) (RoutingRule, error) {
	if c == nil {
		return RoutingRule{}, fmt.Errorf("cloudflare client is nil")
	}

	in.Name = strings.TrimSpace(in.Name)
	in.AliasEmail = strings.ToLower(strings.TrimSpace(in.AliasEmail))

	if in.Name == "" {
		return RoutingRule{}, fmt.Errorf("rule name is required")
	}
	if in.AliasEmail == "" {
		return RoutingRule{}, fmt.Errorf("alias email is required")
	}
	if len(in.Destination) == 0 {
		return RoutingRule{}, fmt.Errorf("destination is required")
	}

	destination := make([]string, 0, len(in.Destination))
	for _, d := range in.Destination {
		v := strings.ToLower(strings.TrimSpace(d))
		if v == "" {
			continue
		}
		if _, err := mail.ParseAddress(v); err != nil {
			return RoutingRule{}, fmt.Errorf("invalid destination email: %w", err)
		}
		destination = append(destination, v)
	}
	if len(destination) == 0 {
		return RoutingRule{}, fmt.Errorf("destination is required")
	}
	if _, err := mail.ParseAddress(in.AliasEmail); err != nil {
		return RoutingRule{}, fmt.Errorf("invalid alias email: %w", err)
	}

	payload := map[string]any{
		"name":     in.Name,
		"enabled":  in.Enabled,
		"priority": in.Priority,
		"matchers": []map[string]any{{
			"type":  "literal",
			"field": "to",
			"value": in.AliasEmail,
		}},
		"actions": []map[string]any{{
			"type":  "forward",
			"value": destination,
		}},
	}

	var resp cloudflareEnvelope[RoutingRule]
	if err := c.DoJSON(ctx, http.MethodPost, "/zones/"+c.zoneID+"/email/routing/rules", nil, payload, &resp); err != nil {
		return RoutingRule{}, err
	}

	if !resp.Success {
		return RoutingRule{}, fmt.Errorf("cloudflare create routing rule unsuccessful")
	}
	if strings.TrimSpace(resp.Result.ID) == "" {
		return RoutingRule{}, fmt.Errorf("cloudflare create routing rule returned empty id")
	}

	return resp.Result, nil
}

func (c *Client) ListRoutingRules(ctx context.Context, filter ListRulesFilter) ([]RoutingRule, error) {
	if c == nil {
		return nil, fmt.Errorf("cloudflare client is nil")
	}

	var resp cloudflareEnvelope[[]RoutingRule]
	if err := c.DoJSON(ctx, http.MethodGet, "/zones/"+c.zoneID+"/email/routing/rules", nil, nil, &resp); err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("cloudflare list routing rules unsuccessful")
	}

	prefix := strings.TrimSpace(filter.NamePrefix)
	if prefix == "" {
		return resp.Result, nil
	}

	filtered := make([]RoutingRule, 0, len(resp.Result))
	for _, rule := range resp.Result {
		if strings.HasPrefix(rule.Name, prefix) {
			filtered = append(filtered, rule)
		}
	}

	return filtered, nil
}

func (c *Client) DeleteRoutingRule(ctx context.Context, ruleID string) error {
	if c == nil {
		return fmt.Errorf("cloudflare client is nil")
	}

	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		return fmt.Errorf("rule id is required")
	}

	var resp cloudflareEnvelope[map[string]any]
	escapedRuleID := url.PathEscape(ruleID)
	err := c.DoJSON(ctx, http.MethodDelete, "/zones/"+c.zoneID+"/email/routing/rules/"+escapedRuleID, nil, nil, &resp)
	if err != nil {
		var statusErr *HTTPStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}

	if !resp.Success {
		return fmt.Errorf("cloudflare delete routing rule unsuccessful")
	}

	return nil
}

func BuildRuleName(prefix, platform, aliasLocalpart string) string {
	prefix = strings.Trim(prefix, ": ")
	platform = strings.ToUpper(strings.TrimSpace(platform))
	aliasLocalpart = strings.TrimSpace(aliasLocalpart)

	parts := make([]string, 0, 3)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if platform != "" {
		parts = append(parts, platform)
	}
	if aliasLocalpart != "" {
		parts = append(parts, aliasLocalpart)
	}

	return strings.Join(parts, ":")
}

type cloudflareEnvelope[T any] struct {
	Success bool `json:"success"`
	Result  T    `json:"result"`
}
