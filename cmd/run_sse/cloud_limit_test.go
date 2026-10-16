package run_sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCloudInFlightLimitAcrossUsersAndResume(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	first, second := uuid.NewString(), uuid.NewString()
	const requests = 32
	started := make(chan struct{}, requests)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	})
	users.users[first] = &cloudUser{handler: handler}
	users.users[second] = &cloudUser{handler: handler}
	responses := make(chan *httptest.ResponseRecorder, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, path := first, "/chat"
			if i%2 != 0 {
				id, path = second, "/api/sessions/session/resume"
			}
			responses <- cloudRequest(t, users, id, http.MethodPost, path, nil)
		}()
	}
	defer func() { unblock(); wg.Wait() }()
	for i := int32(0); i < cloudMaxInFlightChats; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("accepted requests did not reach handlers")
		}
	}
	for i := 0; i < requests-int(cloudMaxInFlightChats); i++ {
		select {
		case response := <-responses:
			var payload ErrorData
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusTooManyRequests || payload.Code != ErrorCodeServerBusy ||
				(payload.RetryAction != RetryActionResend && payload.RetryAction != RetryActionResume) || response.Header().Get("Retry-After") != "5" {
				t.Fatalf("overload: status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("overloaded requests queued instead of being rejected")
		}
	}
	if active := users.inFlightChats.Load(); active != cloudMaxInFlightChats {
		t.Fatalf("active=%d want=%d", active, cloudMaxInFlightChats)
	}
	for path, retry := range map[string]string{"/chat": RetryActionResend, "/api/sessions/session/resume": RetryActionResume} {
		response := cloudRequest(t, users, second, http.MethodPost, path, nil)
		var payload ErrorData
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.RetryAction != retry {
			t.Fatalf("retry action for %s: %+v %v", path, payload, err)
		}
	}
	for _, path := range []string{"/healthz", "/api/config", "/api/projects", "/skills"} {
		if response := cloudRequest(t, users, first, http.MethodGet, path, nil); response.Code >= 400 {
			t.Fatalf("non-chat request blocked: %s status=%d", path, response.Code)
		}
	}
	// Rejected requests must not initialize another user workspace.
	newUser := uuid.NewString()
	if response := cloudRequest(t, users, newUser, http.MethodPost, "/chat", nil); response.Code != http.StatusTooManyRequests {
		t.Fatalf("new user's request bypassed limit: %d", response.Code)
	}
	if _, exists := users.users[newUser]; exists {
		t.Fatal("rejected request initialized a user")
	}
	unblock()
	wg.Wait()
	for i := int32(0); i < cloudMaxInFlightChats; i++ {
		if response := <-responses; response.Code != http.StatusOK {
			t.Fatalf("accepted request: %d %s", response.Code, response.Body.String())
		}
	}
	if active := users.inFlightChats.Load(); active != 0 {
		t.Fatalf("request slots leaked: %d", active)
	}
	if response := cloudRequest(t, users, second, http.MethodPost, "/api/sessions/session/resume", nil); response.Code != http.StatusOK {
		t.Fatalf("capacity did not recover: %d", response.Code)
	}
}

func TestCloudInFlightSlotsReleasedOnExit(t *testing.T) {
	for _, mode := range []string{"error", "canceled", "panic"} {
		t.Run(mode, func(t *testing.T) {
			users := newCloudUsers(t.TempDir(), nil)
			id := uuid.NewString()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			users.users[id] = &cloudUser{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if active := users.inFlightChats.Load(); active != 1 {
					t.Fatalf("handler missing in-flight slot: %d", active)
				}
				switch mode {
				case "error":
					writeJSONError(w, http.StatusBadRequest, "invalid request")
				case "canceled":
					cancel()
					if r.Context().Err() != context.Canceled {
						t.Fatal("missing canceled context")
					}
				case "panic":
					panic("handler panic")
				}
			})}
			request := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{}")).WithContext(ctx)
			request.Header.Set("X-Laxcode-User-ID", id)
			func() {
				defer func() {
					if recovered := recover(); recovered != nil && mode != "panic" {
						t.Fatalf("unexpected panic: %v", recovered)
					}
				}()
				users.ServeHTTP(httptest.NewRecorder(), request)
			}()
			if active := users.inFlightChats.Load(); active != 0 {
				t.Fatalf("%s leaked a slot: %d", mode, active)
			}
		})
	}
	users := newCloudUsers(t.TempDir(), nil)
	if response := cloudRequest(t, users, uuid.NewString(), http.MethodPost, "/chat", nil); response.Code != http.StatusNotFound || users.inFlightChats.Load() != 0 {
		t.Fatalf("missing workspace leaked a slot: %d active=%d", response.Code, users.inFlightChats.Load())
	}
}
