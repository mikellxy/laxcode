package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

type quotaClient struct{ client *http.Client }
type quotaUsage struct {
	UserID      string `json:"user_id"`
	Day         string `json:"day"`
	Limit       uint64 `json:"limit_bytes"`
	Observed    uint64 `json:"observed_used_bytes"`
	Confirmed   uint64 `json:"confirmed_used_bytes"`
	Held        uint64 `json:"held_bytes"`
	Allocatable uint64 `json:"allocatable_bytes"`
	Uncertain   uint64 `json:"uncertain_bytes"`
	Exhausted   bool   `json:"exhausted"`
	Stale       bool   `json:"stale"`
	UpdatedAt   string `json:"updated_at"`
	ResetAt     string `json:"reset_at"`
}
type quotaError struct {
	Exhausted bool
	ResetAt   string
}

func (e *quotaError) Error() string {
	if e.Exhausted {
		return "daily public network quota exhausted"
	}
	return "network quota service unavailable"
}
func defaultManagerSocket() string {
	if runtime.GOOS == "linux" {
		return "/run/laxcode-manager/control.sock"
	}
	return ""
}
func newQuotaClient(socket string) *quotaClient {
	return &quotaClient{client: &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
		},
	}}}
}
func (c *quotaClient) request(ctx context.Context, method, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://manager"+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(out)
	return resp.StatusCode, err
}
func (c *quotaClient) usage(ctx context.Context, id, day string) (quotaUsage, error) {
	var u quotaUsage
	status, err := c.request(ctx, "GET", "/v1/users/"+id+"/network-usage?day="+url.QueryEscape(day), &u)
	if err != nil || status != 200 || u.UserID != id {
		return u, &quotaError{}
	}
	return u, nil
}
func (c *quotaClient) ensure(ctx context.Context, id, sandbox string) error {
	var result struct {
		UserID    string `json:"user_id"`
		SandboxID string `json:"sandbox_id"`
		ResetAt   string `json:"reset_at"`
	}
	status, err := c.request(ctx, "POST", "/v1/sandboxes/"+url.PathEscape(sandbox)+"/ensure", &result)
	if err != nil || result.UserID != id || result.SandboxID != sandbox {
		return &quotaError{}
	}
	if status == 429 {
		return &quotaError{Exhausted: true, ResetAt: result.ResetAt}
	}
	if status != 200 {
		return &quotaError{}
	}
	return nil
}
func writeQuotaError(w http.ResponseWriter, err error) {
	var q *quotaError
	if !errors.As(err, &q) {
		q = &quotaError{}
	}
	code, status, message := "NETWORK_QUOTA_UNAVAILABLE", 503, "网络配额服务暂不可用，请稍后重试"
	if q.Exhausted {
		code, status, message = "NETWORK_QUOTA_EXHAUSTED", 429, "当日公网流量额度已用尽"
		if reset, err := time.Parse(time.RFC3339, q.ResetAt); err == nil {
			w.Header().Set("Retry-After", fmt.Sprint(max(1, int(time.Until(reset).Seconds()))))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message, "reset_at": q.ResetAt})
}
