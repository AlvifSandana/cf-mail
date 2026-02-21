package cloudflare

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Zone represents a Cloudflare DNS zone.
type Zone struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ListZones fetches all zones accessible by the API token.
// Only zones with status "active" are returned.
func (c *Client) ListZones(ctx context.Context) ([]Zone, error) {
	if c == nil {
		return nil, fmt.Errorf("cloudflare client is nil")
	}

	const perPage = 50
	var all []Zone

	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("per_page", fmt.Sprintf("%d", perPage))
		q.Set("status", "active")

		var resp cloudflarePaginatedEnvelope[[]Zone]
		if err := c.DoJSON(ctx, http.MethodGet, "/zones", q, nil, &resp); err != nil {
			return nil, fmt.Errorf("list zones: %w", err)
		}
		if !resp.Success {
			return nil, fmt.Errorf("cloudflare list zones unsuccessful")
		}

		for _, z := range resp.Result {
			if strings.EqualFold(z.Status, "active") {
				all = append(all, Zone{
					ID:     strings.TrimSpace(z.ID),
					Name:   strings.ToLower(strings.TrimSpace(z.Name)),
					Status: z.Status,
				})
			}
		}

		if resp.ResultInfo.TotalPages <= 0 || page >= resp.ResultInfo.TotalPages {
			break
		}
	}

	return all, nil
}
