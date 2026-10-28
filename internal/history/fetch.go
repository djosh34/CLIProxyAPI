package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

const usageURL = "https://api.anthropic.com/api/oauth/usage"

// windowsByKind maps the limits[] kinds that are recorded to their window names.
var windowsByKind = map[string]string{
	"session":    "5h",
	"weekly_all": "weekly",
}

// reading is one window's usage from a single fetch.
type reading struct {
	window      string
	utilization float64
	resetsAt    *time.Time
}

// fetch reads one account's usage with its current token. It never refreshes
// the token. On failure it logs one warning and returns a short reason.
func (s *Service) fetch(ctx context.Context, acc account) ([]reading, error) {
	fail := func(reason string, detail any) ([]reading, error) {
		log.Warnf("history: usage fetch for %s failed: %s (%v)", acc.email, reason, detail)
		return nil, errors.New(reason)
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if errReq != nil {
		return fail("request failed", errReq)
	}
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, errDo := s.accounts.HttpRequest(ctx, acc.auth, req)
	if errDo != nil {
		return fail("request failed", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("history: close usage response: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return fail("request failed", errRead)
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return fail("token expired", resp.Status)
	case http.StatusForbidden:
		return fail("forbidden", resp.Status)
	case http.StatusTooManyRequests:
		return fail("rate limited", resp.Status)
	default:
		return fail(fmt.Sprintf("HTTP %d", resp.StatusCode), resp.Status)
	}

	readings, errParse := parseUsage(body)
	if errParse != nil {
		return fail("bad response", errParse)
	}
	return readings, nil
}

// parseUsage reads the session and weekly_all entries of limits[]. Everything
// else is ignored. A window without a percent gives no reading.
func parseUsage(body []byte) ([]reading, error) {
	var resp struct {
		Limits []struct {
			Kind     string   `json:"kind"`
			Percent  *float64 `json:"percent"`
			ResetsAt *string  `json:"resets_at"`
		} `json:"limits"`
	}
	if errUnmarshal := json.Unmarshal(body, &resp); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	var out []reading
	seen := make(map[string]bool)
	for _, limit := range resp.Limits {
		window, ok := windowsByKind[limit.Kind]
		if !ok || limit.Percent == nil || seen[window] {
			continue
		}
		r := reading{window: window, utilization: *limit.Percent}
		if limit.ResetsAt != nil {
			resetsAt, errParse := time.Parse(time.RFC3339, *limit.ResetsAt)
			if errParse != nil {
				return nil, fmt.Errorf("resets_at of %s: %w", limit.Kind, errParse)
			}
			resetsAt = resetsAt.UTC()
			r.resetsAt = &resetsAt
		}
		seen[window] = true
		out = append(out, r)
	}
	return out, nil
}
