package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quotaFixture(t *testing.T, handler http.Handler) *quotaClient {
	t.Helper()
	// macOS Unix socket paths have a small length limit.
	dir, err := os.MkdirTemp("/tmp", "quota-proxy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
	return newQuotaClient(path)
}
func TestQuotaDenialPrecedesSandboxAndSSE(t *testing.T) {
	for _, status := range []int{429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p, runtime := fixture(t, http.NotFoundHandler())
			p.quota = quotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 503 {
					w.WriteHeader(503)
					return
				}
				json.NewEncoder(w).Encode(quotaUsage{UserID: testID, Exhausted: true, ResetAt: "2026-10-22T00:00:00+08:00"})
			}))
			req := httptest.NewRequest("POST", "/chat", strings.NewReader("{}"))
			req.Header.Set("X-USERID", testID)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != status || runtime.creates != 0 || strings.Contains(rec.Body.String(), "event: prepare") {
				t.Fatal(rec.Code, rec.Body.String(), runtime.creates)
			}
		})
	}
}
func TestUsageDoesNotWakeAndCachedEndpointRechecksBinding(t *testing.T) {
	p, runtime := fixture(t, http.NotFoundHandler())
	ensures := 0
	p.quota = quotaFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			ensures++
			json.NewEncoder(w).Encode(map[string]string{"user_id": "wrong-user", "sandbox_id": "stable"})
		} else {
			json.NewEncoder(w).Encode(quotaUsage{UserID: testID, Stale: true, Confirmed: 123})
		}
	}))
	req := httptest.NewRequest("GET", "/api/network-usage", nil)
	req.Header.Set("X-USERID", testID)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 || runtime.creates != 0 || !strings.Contains(rec.Body.String(), `"confirmed_used_bytes":123`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	runtime.state = &sandboxState{ID: "stable", Name: sandboxName(testID), Phase: "Ready"}
	u := p.user(testID)
	u.endpoint = runtime.endpoint
	if _, err := p.ensure(context.Background(), testID, u); err == nil || ensures != 1 {
		t.Fatal("cached endpoint bypassed user binding", err, ensures)
	}
}
