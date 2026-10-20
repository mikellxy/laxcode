package main

import (
	"bufio"
	"context"
	"encoding/json"
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
			t.Fatalf("read SSE frame: %v", err)
		}
		frame.WriteString(line)
		if line == "\n" {
			return frame.String()
		}
	}
}

func proxyStreamError(t *testing.T, response *httptest.ResponseRecorder) streamError {
	t.Helper()
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	reader := bufio.NewReader(response.Body)
	if frame := readProxyFrame(t, reader); frame != "event: prepare\ndata: {}\n\n" {
		t.Fatalf("first frame=%q", frame)
	}
	frame := readProxyFrame(t, reader)
	data, ok := strings.CutPrefix(frame, "event: error\ndata: ")
	if !ok {
		t.Fatalf("error frame=%q", frame)
	}
	var payload streamError
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &payload); err != nil {
		t.Fatal(err)
	}
	if remaining, _ := io.ReadAll(reader); len(remaining) != 0 {
		t.Fatalf("unexpected trailing response: %q", remaining)
	}
	return payload
}

func TestPrepareConvertsUpstreamErrorsAndPreservesRetry(t *testing.T) {
	for _, tc := range []struct {
		name, path, contentType, body, code, message, retry string
		status                                              int
	}{
		{"chat busy", "/chat", "application/json", `{"code":"SESSION_BUSY","message":"session busy","retry_action":"resend"}`, "SESSION_BUSY", "session busy", "resend", 409},
		{"resume busy", "/api/sessions/s/resume", "application/json", `{"code":"SESSION_BUSY","message":"project busy","retry_action":"resume"}`, "SESSION_BUSY", "project busy", "resume", 409},
		{"invalid request", "/chat", "application/json", `{"code":"INVALID_REQUEST","message":"task is required"}`, "INVALID_REQUEST", "task is required", "", 400},
		{"model required", "/chat", "application/json", `{"code":"MODEL_REQUIRED","message":"configure a model"}`, "MODEL_REQUIRED", "configure a model", "", 409},
		{"gateway unavailable", "/chat", "text/html", "<html>private gateway details</html>", "UPSTREAM_ERROR", "sandbox request failed (HTTP 503)", "", 503},
		{"invalid stream", "/chat", "text/plain", "unexpected response", "UPSTREAM_ERROR", "sandbox did not return an SSE stream", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/healthz" {
					return
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			request.Header.Set("X-USERID", testID)
			response := httptest.NewRecorder()
			p.ServeHTTP(response, request)
			payload := proxyStreamError(t, response)
			if payload.Code != tc.code || payload.Message != tc.message || payload.RetryAction != tc.retry {
				t.Fatalf("error payload=%+v", payload)
			}
		})
	}
}

type lookupRuntime struct {
	sandboxRuntime
	lookup func(context.Context, string) (*sandboxState, error)
}

func (r lookupRuntime) Lookup(ctx context.Context, id string) (*sandboxState, error) {
	return r.lookup(ctx, id)
}

func TestPrepareStartupFailureCanRetryOriginalOperation(t *testing.T) {
	for _, tc := range []struct{ path, retry string }{{"/chat", "resend"}, {"/api/sessions/s/resume", "resume"}} {
		t.Run(tc.retry, func(t *testing.T) {
			p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			p.runtime = lookupRuntime{sandboxRuntime: runtime, lookup: func(context.Context, string) (*sandboxState, error) {
				return nil, errors.New("startup failed")
			}}
			request := httptest.NewRequest(http.MethodPost, tc.path, nil)
			request.Header.Set("X-USERID", testID)
			response := httptest.NewRecorder()
			p.ServeHTTP(response, request)
			if payload := proxyStreamError(t, response); payload.Code != "SANDBOX_UNAVAILABLE" || payload.RetryAction != tc.retry {
				t.Fatalf("startup error=%+v", payload)
			}
			if len(p.chats) != 0 || p.user(testID).active != 0 {
				t.Fatal("startup failure leaked active request tracking")
			}
		})
	}
}

func TestPrepareConnectionFailureIsSSEAndInvalidatesEndpoint(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	p.user(testID).endpoint = runtime.endpoint
	p.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection failed")
	}
	request := httptest.NewRequest(http.MethodPost, "/chat", nil)
	request.Header.Set("X-USERID", testID)
	response := httptest.NewRecorder()
	p.ServeHTTP(response, request)
	if payload := proxyStreamError(t, response); payload.Code != "SANDBOX_UNAVAILABLE" || payload.RetryAction != "" {
		t.Fatalf("connection error=%+v", payload)
	}
	if p.user(testID).endpoint != nil || p.user(testID).active != 0 || len(p.chats) != 0 {
		t.Fatal("failed connection retained its endpoint or active request")
	}
}

func TestCancelDuringPrepareCancelsStartup(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	entered, canceled, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	p.runtime = lookupRuntime{sandboxRuntime: runtime, lookup: func(ctx context.Context, _ string) (*sandboxState, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		return nil, ctx.Err()
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		p.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/chat", nil)
	request.Header.Set("X-USERID", testID)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if frame := readProxyFrame(t, bufio.NewReader(response.Body)); frame != "event: prepare\ndata: {}\n\n" {
		t.Fatalf("first frame=%q", frame)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("startup did not begin")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("startup was not canceled")
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("request did not release startup resources")
	}
	if len(p.chats) != 0 || p.user(testID).active != 0 {
		t.Fatal("canceled startup retained its active request")
	}
}

func TestOversizedChatRejectedBeforePrepare(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	request.Header.Set("X-USERID", testID)
	response := httptest.NewRecorder()
	p.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "event: prepare") || runtime.creates != 0 || len(p.chats) != 0 {
		t.Fatalf("oversized body status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestEvaluationResponseRemainsJSON(t *testing.T) {
	p, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"id":"eval-1","status":"queued"}`)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/evaluations", nil)
	request.Header.Set("X-USERID", testID)
	response := httptest.NewRecorder()
	p.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != `{"id":"eval-1","status":"queued"}` {
		t.Fatalf("evaluation status=%d body=%s", response.Code, response.Body.String())
	}
}
