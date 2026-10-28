package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func readProxyFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		frame.WriteString(line)
		if line == "\n" {
			return frame.String()
		}
	}
}
func TestTaskSubmissionPreservesJSONAndStatus(t *testing.T) {
	for _, path := range []string{"/chat", "/api/sessions/s/resume"} {
		for _, status := range []int{200, 409, 400} {
			p, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					return
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"task_id":"old"}` {
					t.Errorf("body %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, `{"task_id":"new","task_status":"waiting"}`)
			}))
			req := httptest.NewRequest("POST", path, strings.NewReader(`{"task_id":"old"}`))
			req.Header.Set("X-USERID", testID)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, req)
			if w.Code != status || w.Header().Get("Content-Type") != "application/json" || strings.Contains(w.Body.String(), "event:") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		}
	}
}

type lookupRuntime struct {
	sandboxRuntime
	lookup func(context.Context, string) (*sandboxState, error)
}

func (r lookupRuntime) Lookup(ctx context.Context, id string) (*sandboxState, error) {
	return r.lookup(ctx, id)
}
func TestTaskStartupFailureIsJSONAndReleasesAdmission(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	p.runtime = lookupRuntime{runtime, func(context.Context, string) (*sandboxState, error) { return nil, errors.New("startup failed") }}
	req := httptest.NewRequest("POST", "/chat", nil)
	req.Header.Set("X-USERID", testID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 503 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") || len(p.chats) != 0 || p.user(testID).active != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
func TestTaskConnectionFailureInvalidatesEndpoint(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	p.user(testID).endpoint = runtime.endpoint
	p.transport.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection failed") }
	req := httptest.NewRequest("POST", "/chat", nil)
	req.Header.Set("X-USERID", testID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 502 || p.user(testID).endpoint != nil || p.user(testID).active != 0 || len(p.chats) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
func TestCancelBeforeTaskAcceptanceCancelsStartup(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	entered, exited := make(chan struct{}), make(chan struct{})
	p.runtime = lookupRuntime{runtime, func(ctx context.Context, _ string) (*sandboxState, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/chat", nil).WithContext(ctx)
	req.Header.Set("X-USERID", testID)
	go func() { defer close(exited); p.ServeHTTP(httptest.NewRecorder(), req) }()
	<-entered
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("startup did not cancel")
	}
	if len(p.chats) != 0 || p.user(testID).active != 0 {
		t.Fatal("startup leaked admission")
	}
}
func TestOversizedChatRejectedBeforeStartup(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	req := httptest.NewRequest("POST", "/chat", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	req.Header.Set("X-USERID", testID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 400 || runtime.creates != 0 || len(p.chats) != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
