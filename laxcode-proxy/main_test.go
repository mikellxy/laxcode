package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

const testID = "11111111-1111-4111-8111-111111111111"

type fakeRuntime struct {
	state                  *sandboxState
	creates, starts, stops int
	endpoint               *url.URL
	uncertain              bool
}

func (f *fakeRuntime) Lookup(context.Context, string) (*sandboxState, error) { return f.state, nil }
func (f *fakeRuntime) Create(context.Context, string, string) error {
	f.creates++
	f.state = &sandboxState{Name: sandboxName(testID), ID: "stable", Phase: "Ready"}
	if f.uncertain {
		return errors.New("client timed out after server commit")
	}
	return nil
}
func (f *fakeRuntime) Start(context.Context, string) error {
	f.starts++
	f.state.Phase = "Ready"
	return nil
}
func (f *fakeRuntime) Stop(context.Context, string) error {
	f.stops++
	f.state.Phase = "Stopped"
	return nil
}
func (f *fakeRuntime) Endpoint(context.Context, string) (*url.URL, error) { return f.endpoint, nil }
func fixture(t *testing.T, handler http.Handler) (*proxyServer, *fakeRuntime) {
	t.Helper()
	backend := httptest.NewServer(handler)
	t.Cleanup(backend.Close)
	u, _ := url.Parse(backend.URL + "/")
	runtime := &fakeRuntime{endpoint: u, uncertain: true}
	p := newProxy(runtime, t.TempDir(), fstest.MapFS{"index.html": {Data: []byte("web")}}, time.Millisecond, 2)
	t.Cleanup(p.transport.CloseIdleConnections)
	t.Cleanup(p.closeWorkspaces)
	return p, runtime
}
func TestConcurrentProvisionReconcileAndResume(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/activity" {
			io.WriteString(w, `{"active":0}`)
			return
		}
		w.WriteHeader(200)
	}))
	p.chats = make(chan struct{}, 24)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/chat", nil)
			req.Header.Set("X-USERID", testID)
			rec := httptest.NewRecorder()
			p.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("status %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if runtime.creates != 1 {
		t.Fatalf("created %d sandboxes", runtime.creates)
	}
	file := filepath.Join(p.root, testID, "agenthome/.laxcode/user-file.txt")
	os.WriteFile(file, []byte("user edit"), 0600)
	u := p.user(testID)
	u.lastUsed = time.Now().Add(-time.Hour)
	p.stopIdle(context.Background())
	if runtime.stops != 1 {
		t.Fatal("idle sandbox not stopped")
	}
	req := httptest.NewRequest("POST", "/chat", nil)
	req.Header.Set("X-USERID", testID)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != 200 || runtime.creates != 1 || runtime.starts != 1 || runtime.state.ID != "stable" {
		t.Fatalf("resume: %+v status=%d", runtime, rec.Code)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "user edit" {
		t.Fatal("skills overwritten on resume")
	}
}
func TestIdentityAndPublicAssets(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	for _, tc := range []struct {
		path, id string
		status   int
	}{{"/", "", 200}, {"/healthz", "", 200}, {"/api/models", "", 400}, {"/api/models", "../other", 400}, {"/internal/activity", testID, 404}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		if tc.id != "" {
			req.Header.Set("X-USERID", tc.id)
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s: %d", tc.path, w.Code)
		}
	}
	if runtime.creates != 0 {
		t.Fatal("public or invalid requests provisioned sandbox")
	}
}
func TestStreamingFlushAndCancellation(t *testing.T) {
	canceled := make(chan struct{})
	p, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			return
		}
		if r.Header.Get("X-USERID") != testID || r.Header.Get("Authorization") != "" {
			t.Error("incorrect forwarded identity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	server := httptest.NewServer(p)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/tasks/task/events", nil)
	req.Header.Set("X-USERID", testID)
	req.Header.Set("Authorization", "must-not-forward")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(response.Body)
	if frame := readProxyFrame(t, reader); frame != "data: first\n\n" {
		t.Fatalf("upstream frame=%q", frame)
	}
	cancel()
	response.Body.Close()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream was not canceled")
	}
}
func TestIncompleteOrSymlinkWorkplaceNotOverwritten(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, testID), 0700)
	if _, err := initializeWorkplace(root, testID); err == nil {
		t.Fatal("accepted incomplete directory")
	}
	os.Remove(filepath.Join(root, testID))
	os.Symlink(t.TempDir(), filepath.Join(root, testID))
	if _, err := initializeWorkplace(root, testID); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestChatLimitAndActiveWorkPreventIdleStop(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat":
			close(entered)
			<-release
		case "/internal/activity":
			io.WriteString(w, `{"active":1}`)
		}
	}))
	p.chats = make(chan struct{}, 1)
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/chat", nil)
		r.Header.Set("X-USERID", testID)
		return r
	}
	done := make(chan struct{})
	go func() { defer close(done); p.ServeHTTP(httptest.NewRecorder(), request()) }()
	<-entered
	u := p.user(testID)
	_ = u.lock(context.Background())
	u.lastUsed = time.Now().Add(-time.Hour)
	u.unlock()
	p.stopIdle(context.Background())
	w := httptest.NewRecorder()
	p.ServeHTTP(w, request())
	if w.Code != 429 {
		t.Errorf("limit status=%d", w.Code)
	}
	close(release)
	<-done
	_ = u.lock(context.Background())
	u.lastUsed = time.Now().Add(-time.Hour)
	u.unlock()
	p.stopIdle(context.Background())
	if runtime.stops != 0 {
		t.Fatal("stopped active chat or background evaluation")
	}
}

func TestRestartRecoversIdleSandboxes(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"active":0}`) }))
	if _, err := initializeWorkplace(p.root, testID); err != nil {
		t.Fatal(err)
	}
	runtime.state = &sandboxState{ID: "stable", Name: sandboxName(testID), Phase: "Ready"}
	p.restoreRunning(context.Background())
	u := p.user(testID)
	if u.endpoint == nil {
		t.Fatal("running sandbox not adopted")
	}
	u.lastUsed = time.Now().Add(-time.Hour)
	p.stopIdle(context.Background())
	if runtime.stops != 1 {
		t.Fatal("recovered idle sandbox not stopped")
	}
}

func TestStoppedGatewayResponseInvalidatesCachedEndpoint(t *testing.T) {
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	req := httptest.NewRequest("POST", "/chat", nil)
	req.Header.Set("X-USERID", testID)
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusPreconditionFailed || p.user(testID).endpoint != nil {
		t.Fatal("stopped endpoint remained cached")
	}
	runtime.state.Phase = "Stopped"
	p.ServeHTTP(httptest.NewRecorder(), req)
	if runtime.starts != 1 || runtime.creates != 1 {
		t.Fatal("did not start the existing sandbox")
	}
}
