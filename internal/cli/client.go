package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Result struct {
	Data     any   `json:"data"`
	Total    any   `json:"total,omitempty"`
	Affected *bool `json:"affected,omitempty"`
	DryRun   bool  `json:"dryRun,omitempty"`
}

type Client struct {
	profile Profile
	http    *http.Client
}

func newClient(p Profile, timeout time.Duration) *Client {
	return &Client{p, &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) request(method, action string, query url.Values, body any) (Result, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Result{}, err
		}
		reader = bytes.NewReader(raw)
	}
	target := c.profile.Endpoint + "/api/" + action
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "casdoor-cli")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.profile.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.profile.Token)
	} else {
		req.SetBasicAuth(c.profile.ClientID, c.profile.ClientSecret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("HTTP %d from %s", resp.StatusCode, action)
	}
	const limit = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return Result{}, err
	}
	if len(raw) > limit {
		return Result{}, errors.New("response exceeds 32 MiB; use pagination")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var data any
	if err := decoder.Decode(&data); err != nil {
		return Result{}, fmt.Errorf("non-JSON response from %s", action)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Result{}, fmt.Errorf("invalid trailing response from %s", action)
	}
	if env, ok := data.(map[string]any); ok {
		if status, ok := env["status"].(string); ok {
			if status != "ok" {
				return Result{}, fmt.Errorf("API %s: %v", status, env["msg"])
			}
			result := Result{Data: env["data"], Total: env["data2"]}
			if s, ok := result.Data.(string); ok && (s == "Affected" || s == "Unaffected") {
				affected := s == "Affected"
				result.Affected = &affected
			}
			return result, nil
		}
	}
	return Result{Data: data}, nil
}

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			key := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
			sensitive := strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "token") || key == "accesskey" || key == "privatekey" || key == "hash" || key == "prehash"
			if sensitive && val != nil && val != "" {
				out[k] = "[REDACTED]"
			} else {
				out[k] = redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redact(val)
		}
		return out
	default:
		return v
	}
}
