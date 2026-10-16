package run_sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

func TestCloudPerformanceEndpointAndBoundaries(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	defer users.Close()
	id, other, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	user, err := users.user(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.user(other, true); err != nil {
		t.Fatal(err)
	}
	if _, err := user.repo.CreateSession(context.Background(), sessionID, id, "", "", filepath.Join(user.server.userRoot, "demo"), "code"); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/sessions/" + sessionID + "/performance"
	missing := cloudRequest(t, users, id, http.MethodGet, endpoint, nil)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "TRACE_LOG_UNAVAILABLE") {
		t.Fatalf("missing trace: %d %s", missing.Code, missing.Body.String())
	}
	path := layout.TracingLog(user.server.homeDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	root := map[string]any{
		"trace_id": "trace", "span_id": "root", "name": "chat", "duration_ms": 150.25,
		"start_time": "2026-10-16T12:00:00+08:00", "end_time": "2026-10-16T04:00:00.150250Z",
		"status_msg": "DO_NOT_EXPOSE", "attributes": map[string]any{
			"laxcode.session_id": sessionID, "laxcode.chat_id": "chat", "laxcode.first_output_ms": 10,
			"laxcode.request_id": "request", "laxcode.operation": "send", "secret": "DO_NOT_EXPOSE",
		},
	}
	data, _ := json.Marshal(root)
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	response := cloudRequest(t, users, "", http.MethodGet, endpoint+"?user_id="+id, nil)
	var summary filetrace.PerformanceSummary
	if err := json.Unmarshal(response.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || summary.SessionID != sessionID ||
		len(summary.Chats) != 1 || summary.Chats[0].TotalMs != 150.25 || *summary.Chats[0].Requests[0].FirstOutputMs != 10 {
		t.Fatalf("summary: %d %s headers=%v", response.Code, response.Body.String(), response.Header())
	}
	if strings.Contains(response.Body.String(), "DO_NOT_EXPOSE") || strings.Contains(response.Body.String(), user.server.userRoot) {
		t.Fatal("response leaked raw log data or host paths")
	}
	if response := cloudRequest(t, users, other, http.MethodGet, endpoint, nil); response.Code != http.StatusNotFound {
		t.Fatalf("another user's session: %d %s", response.Code, response.Body.String())
	}
	if response := cloudRequest(t, users, other, http.MethodGet, endpoint+"?user_id="+id, nil); response.Code != http.StatusBadRequest {
		t.Fatalf("conflicting user identity: %d", response.Code)
	}
	if response := cloudRequest(t, users, id, http.MethodGet, "/api/sessions/%2e%2e/performance", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid session ID: %d", response.Code)
	}
	input, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = input.Truncate(filetrace.MaxPerformanceLogBytes + 1)
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response := cloudRequest(t, users, id, http.MethodGet, endpoint, nil); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized trace: %d", response.Code)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if response := cloudRequest(t, users, id, http.MethodGet, endpoint, nil); response.Code != http.StatusForbidden {
		t.Fatalf("symlink log: %d %s", response.Code, response.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if response := cloudRequest(t, users, id, http.MethodGet, endpoint, nil); response.Code != http.StatusForbidden {
		t.Fatalf("parent symlink: %d %s", response.Code, response.Body.String())
	}
}

func TestPerformanceEndpointRejectsLocalMode(t *testing.T) {
	s := newTestServer(t, t.TempDir(), false)
	response := httptest.NewRecorder()
	s.routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+uuid.NewString()+"/performance", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("local mode: %d %s", response.Code, response.Body.String())
	}
}
