// laxcode-proxy owns user routing and OpenShell lifecycle. It does not import cloud internals.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	webui "github.com/mikellxy/laxcode/web"
	"github.com/mikellxy/laxcode/workspace"
)

// Release builds inject the guide version with -ldflags -X.
var guideVersion = "dev"

type userState struct {
	gate     chan struct{}
	endpoint *url.URL
	active   int
	lastUsed time.Time
	api      *workspace.API
	apiMu    sync.RWMutex
}

func (u *userState) lock(ctx context.Context) error {
	select {
	case u.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (u *userState) unlock() { <-u.gate }

type proxyServer struct {
	admin     http.Handler
	runtime   sandboxRuntime
	root      string
	assets    http.Handler
	transport *http.Transport
	client    *http.Client
	mu        sync.Mutex
	users     map[string]*userState
	chats     chan struct{}
	idle      time.Duration
	catalog   workspace.Catalog
	skills    string
	storeKey  []byte
	quota     *quotaClient
}

func newProxy(runtime sandboxRuntime, root string, assets fs.FS, idle time.Duration, maxChats int) *proxyServer {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dial := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	// Service hostnames select the sandbox, but DNS always targets the private local gateway.
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(host, ".openshell.localhost") {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return dial(ctx, network, address)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return &proxyServer{storeKey: key, runtime: runtime, root: root, assets: staticHandler(assets), transport: transport, client: &http.Client{Transport: transport, Timeout: 3 * time.Second}, users: map[string]*userState{}, chats: make(chan struct{}, maxChats), idle: idle}
}
func (p *proxyServer) user(id string) *userState {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.users[id]
	if u == nil {
		u = &userState{gate: make(chan struct{}, 1), lastUsed: time.Now()}
		p.users[id] = u
	}
	return u
}
func (p *proxyServer) ensure(ctx context.Context, id string, u *userState) (*url.URL, error) {
	if u.endpoint != nil {
		if p.quota != nil {
			state, err := p.runtime.Lookup(ctx, id)
			if err != nil {
				return nil, err
			}
			if state == nil || !strings.EqualFold(state.Phase, "Ready") {
				u.endpoint = nil
			} else if err = p.quota.ensure(ctx, id, state.ID); err != nil {
				return nil, err
			}
		}
		if u.endpoint != nil {
			return u.endpoint, nil
		}
	}
	root, err := initializeWorkplace(p.root, id)
	if err != nil {
		return nil, err
	}
	state, err := p.runtime.Lookup(ctx, id)
	if err != nil {
		return nil, err
	}
	if state == nil {
		createErr := p.runtime.Create(ctx, id, root)
		// A timed-out client call may have committed. Reconcile before considering another create.
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		state, err = p.runtime.Lookup(checkCtx, id)
		cancel()
		if err != nil {
			return nil, err
		}
		if state == nil {
			if createErr != nil {
				return nil, createErr
			}
			return nil, errors.New("sandbox creation was not observed")
		}
	}
	if strings.EqualFold(state.Phase, "Stopped") || strings.EqualFold(state.Phase, "Completed") {
		if err = p.runtime.Start(ctx, state.Name); err != nil {
			return nil, err
		}
	}
	for {
		state, err = p.runtime.Lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		if state == nil {
			return nil, errors.New("sandbox disappeared during startup")
		}
		if strings.EqualFold(state.Phase, "Ready") {
			break
		}
		if strings.EqualFold(state.Phase, "Error") {
			return nil, errors.New("sandbox is in Error; inspect OpenShell logs")
		}
		if err = waitTick(ctx); err != nil {
			return nil, err
		}
	}
	endpoint, err := p.runtime.Endpoint(ctx, state.Name)
	if err != nil {
		return nil, err
	}
	if p.quota != nil {
		if err = p.quota.ensure(ctx, id, state.ID); err != nil {
			return nil, err
		}
	}
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(endpoint.String(), "/")+"/healthz", nil)
		resp, e := p.client.Do(req)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				u.endpoint = endpoint
				return endpoint, nil
			}
		}
		if err = waitTick(ctx); err != nil {
			return nil, err
		}
	}
}
func jsonError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}
func (p *proxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/api" || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		w.Header().Set("Cache-Control", "no-store")
		if p.admin == nil {
			jsonError(w, http.StatusServiceUnavailable, "admin is not configured")
			return
		}
		p.admin.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/healthz" {
		w.WriteHeader(200)
		return
	}
	if r.URL.Path == "/internal/store" {
		p.serveStore(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/internal/") {
		http.NotFound(w, r)
		return
	}
	if !(r.URL.Path == "/chat" || r.URL.Path == "/skills" || strings.HasPrefix(r.URL.Path, "/api/")) {
		p.assets.ServeHTTP(w, r)
		return
	}
	values := r.Header.Values("X-USERID")
	if len(values) != 1 {
		jsonError(w, 400, "X-USERID is required")
		return
	}
	parsed, err := uuid.Parse(values[0])
	if err != nil || parsed == uuid.Nil {
		jsonError(w, 400, "X-USERID must be a nonzero UUID")
		return
	}
	id := parsed.String()
	if r.URL.Path == "/api/network-usage" {
		if r.Method != "GET" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if p.quota == nil {
			writeQuotaError(w, &quotaError{})
			return
		}
		day := r.URL.Query().Get("day")
		if day != "" {
			if _, err := time.Parse(time.DateOnly, day); err != nil {
				jsonError(w, 400, "invalid day")
				return
			}
		}
		usage, err := p.quota.usage(r.Context(), id, day)
		if err != nil {
			writeQuotaError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(usage)
		return
	}
	if !executionRequest(r) {
		u := p.user(id)
		if err := u.lock(r.Context()); err != nil {
			return
		}
		defer u.unlock()
		if err := p.prepareWorkspace(id, u); err != nil {
			log.Printf("workspace %s: %v", id, err)
			jsonError(w, 500, "workspace unavailable")
			return
		}

		u.api.ServeHTTP(w, r)
		return
	}
	if p.quota != nil && !taskControlRequest(r) {
		usage, err := p.quota.usage(r.Context(), id, "")
		if err != nil || usage.Stale {
			writeQuotaError(w, &quotaError{})
			return
		}
		if usage.Exhausted {
			writeQuotaError(w, &quotaError{Exhausted: true, ResetAt: usage.ResetAt})
			return
		}
	}
	submitting := r.Method == "POST" && (r.URL.Path == "/chat" || strings.HasSuffix(r.URL.Path, "/resume"))
	if submitting {
		select {
		case p.chats <- struct{}{}:
			defer func() { <-p.chats }()
		default:
			w.Header().Set("Retry-After", "5")
			jsonError(w, 429, "服务繁忙，请稍后重试")
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			jsonError(w, 400, "read request body failed")
			return
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	writeProxyError := func(status int, message, retry string) { jsonError(w, status, message) }
	retryAction := ""
	u := p.user(id)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err = u.lock(ctx); err != nil {
		writeProxyError(503, "sandbox startup timed out", retryAction)
		return
	}
	if err := p.prepareWorkspace(id, u); err != nil {
		u.unlock()
		writeProxyError(500, "workspace unavailable", retryAction)
		return
	}
	endpoint := u.endpoint
	// Reading output or stopping an existing task must remain possible after quota exhaustion.
	if endpoint == nil || !taskControlRequest(r) {
		endpoint, err = p.ensure(ctx, id, u)
	}
	if err != nil {
		u.unlock()
		log.Printf("sandbox %s: %v", id, err)
		var quota *quotaError
		if errors.As(err, &quota) {
			writeQuotaError(w, err)
			return
		}
		writeProxyError(503, "sandbox unavailable; inspect deployment logs", retryAction)
		return
	}
	u.active++
	u.unlock()
	defer func() { _ = u.lock(context.Background()); u.active--; u.lastUsed = time.Now(); u.unlock() }()
	reverse := &httputil.ReverseProxy{Transport: p.transport, FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(endpoint)
			pr.Out.Host = endpoint.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("X-Laxcode-User-ID")
			pr.Out.Header.Set("X-USERID", id)
		},
		ModifyResponse: func(response *http.Response) error {
			// OpenShell returns 412 when a cached sandbox was stopped externally.
			if response.StatusCode == 412 || response.StatusCode == 502 || response.StatusCode == 503 {
				_ = u.lock(context.Background())
				u.endpoint = nil
				u.unlock()
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			_ = u.lock(context.Background())
			u.endpoint = nil
			u.unlock()
			log.Printf("sandbox request failed: %v", err)
			writeProxyError(502, "sandbox connection failed", "")
		},
	}
	reverse.ServeHTTP(w, r)
}

// Re-adopt running sandboxes after a proxy restart so idle cleanup still reaches them.
func (p *proxyServer) restoreRunning(ctx context.Context) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		log.Printf("discover workplaces: %v", err)
		return
	}
	for _, entry := range entries {
		id, err := uuid.Parse(entry.Name())
		if err != nil || !entry.IsDir() || id.String() != entry.Name() {
			continue
		}
		u := p.user(id.String())
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := u.lock(check); err != nil {
			cancel()
			continue
		}
		if u.endpoint == nil {
			state, err := p.runtime.Lookup(check, id.String())
			if err == nil && state != nil && strings.EqualFold(state.Phase, "Ready") {
				u.endpoint, _ = p.runtime.Endpoint(check, state.Name)
			} else if err == nil && (state == nil || strings.EqualFold(state.Phase, "Stopped") || strings.EqualFold(state.Phase, "Completed")) {
				if err := p.prepareWorkspace(id.String(), u); err == nil {
					_ = u.api.InterruptEvaluations(check)
				}
			}
		}
		u.unlock()
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
}

func (p *proxyServer) stopIdle(ctx context.Context) {
	p.mu.Lock()
	snapshot := make(map[string]*userState, len(p.users))
	for id, u := range p.users {
		snapshot[id] = u
	}
	p.mu.Unlock()
	for id, u := range snapshot {
		if err := u.lock(ctx); err != nil {
			return
		}
		if u.active != 0 || u.endpoint == nil || time.Since(u.lastUsed) < p.idle {
			u.unlock()
			continue
		}
		func() {
			defer u.unlock()
			check, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(check, "GET", strings.TrimRight(u.endpoint.String(), "/")+"/internal/activity", nil)
			resp, err := p.client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var activity struct {
				Active int `json:"active"`
			}
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&activity) != nil {
				return
			}
			if activity.Active != 0 {
				u.lastUsed = time.Now()
				return
			}
			if err := p.runtime.Stop(check, sandboxName(id)); err != nil {
				log.Printf("stop sandbox %s: %v", id, err)
				return
			}
			u.endpoint = nil
			if u.api != nil {
				_ = u.api.InterruptEvaluations(check)
			}
		}()
	}
}
func staticHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(assets, name); err != nil || info.IsDir() {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	addr := flag.String("addr", "0.0.0.0:5173", "HTTP listen address")
	root := flag.String("workplaces", "/data/laxcode", "persistent user directories")
	policy := flag.String("policy", "/run/laxcode/policy.yaml", "sandbox policy")
	image := flag.String("image", "laxcode-cloud:bundle", "preloaded sandbox image")
	gatewayPort := flag.String("gateway-port", "18080", "local OpenShell gateway port")
	storeURL := flag.String("store-url", "", "private host storage callback URL")
	settings := flag.String("settings-dir", "", "public platform catalog directory (required)")
	skills := flag.String("skills", "", "default skills seed directory (required)")
	providers := flag.String("providers-file", "/run/laxcode/providers.json", "registered provider names")
	idle := flag.Duration("idle-timeout", 30*time.Minute, "stop inactive sandboxes after this duration")
	maxChats := flag.Int("max-chats", 2, "global concurrent chat submission limit")
	managerSocket := flag.String("manager-socket", defaultManagerSocket(), "host quota manager Unix socket (required on Linux)")
	flag.Parse()
	if defaultManagerSocket() != "" && *managerSocket == "" {
		return errors.New("manager-socket is required on Linux")
	}
	if *idle <= 0 || *maxChats < 1 {
		return errors.New("idle-timeout and max-chats must be positive")
	}
	if *settings == "" || *skills == "" || *storeURL == "" {
		return errors.New("settings-dir, skills and store-url are required")
	}
	assets, err := webui.Dist()
	if err != nil {
		return err
	}
	if _, err = fs.Stat(assets, "index.html"); err != nil {
		return fmt.Errorf("build frontend with webdist: %w", err)
	}
	if err = os.MkdirAll(*root, 0700); err != nil {
		return err
	}
	// The flock lives outside user mounts and prevents accidental duplicate proxy processes.
	lock, err := os.OpenFile(filepath.Join(*root, ".proxy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another proxy owns this workplace root")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	b, err := os.ReadFile(*providers)
	if err != nil {
		return err
	}
	var names []string
	if err = json.Unmarshal(b, &names); err != nil {
		return err
	}
	runtime := &openshellRuntime{image: *image, policy: *policy, settingsFile: layout.SettingsFile(*settings), providers: names, gatewayPort: *gatewayPort}
	catalog, err := workspace.LoadCatalog(*settings)
	if err != nil {
		return err
	}
	p := newProxy(runtime, *root, assets, *idle, *maxChats)
	p.admin, err = p.adminHandler(adminConfig)
	if err != nil {
		return err
	}
	if *managerSocket != "" {
		p.quota = newQuotaClient(*managerSocket)
	}
	p.catalog, p.skills = catalog, *skills
	keyPath := filepath.Join(*root, ".proxy-store-key")
	if key, err := os.ReadFile(keyPath); err == nil {
		if len(key) != 32 {
			return errors.New("invalid storage key")
		}
		p.storeKey = key
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(keyPath, p.storeKey, 0600); err != nil {
			return err
		}
	} else {
		return err
	}
	runtime.storeURL, runtime.storeToken = *storeURL, p.storeToken
	defer p.closeWorkspaces()
	defer p.transport.CloseIdleConnections()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		p.restoreRunning(ctx)
		tick := time.NewTicker(min(*idle/2, 30*time.Second))
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				p.stopIdle(ctx)
			}
		}
	}()
	srv := &http.Server{Addr: *addr, Handler: p, ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	log.Printf("laxcode-proxy listening on %s", *addr)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdown, stop := context.WithTimeout(context.Background(), 20*time.Second)
	defer stop()
	if err = srv.Shutdown(shutdown); err != nil {
		_ = srv.Close()
	}
	return err
}

// All other public APIs operate on persisted state and never wake a sandbox.
func executionRequest(r *http.Request) bool {
	return taskControlRequest(r) || r.Method == http.MethodPost && (r.URL.Path == "/chat" || r.URL.Path == "/api/evaluations" || strings.HasPrefix(r.URL.Path, "/api/sessions/") && strings.HasSuffix(r.URL.Path, "/resume"))
}

func taskControlRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/tasks/") && (r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") || r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"))
}

// Called with the user's lifecycle lock held through the deletion transaction.
func (p *proxyServer) checkInactive(ctx context.Context, id string, u *userState) error {
	if u.active != 0 {
		return errors.New("用户仍有任务正在执行，请稍后再删除")
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := p.runtime.Lookup(check, id)
	if err != nil {
		return errors.New("无法确认沙箱状态，请稍后再删除")
	}
	if state == nil || strings.EqualFold(state.Phase, "Stopped") || strings.EqualFold(state.Phase, "Completed") {
		if err := u.api.InterruptEvaluations(check); err != nil {
			return errors.New("无法更新已中断任务，请稍后再删除")
		}
		return nil
	}
	if !strings.EqualFold(state.Phase, "Ready") {
		return errors.New("沙箱正在切换状态，请稍后再删除")
	}
	endpoint, err := p.runtime.Endpoint(check, state.Name)
	if err != nil {
		return errors.New("无法确认任务状态，请稍后再删除")
	}
	req, _ := http.NewRequestWithContext(check, "GET", strings.TrimRight(endpoint.String(), "/")+"/internal/activity", nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return errors.New("无法确认任务状态，请稍后再删除")
	}
	defer resp.Body.Close()
	var activity struct {
		Active int `json:"active"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&activity) != nil {
		return errors.New("无法确认任务状态，请稍后再删除")
	}
	if activity.Active != 0 {
		return errors.New("用户仍有任务正在执行，请稍后再删除")
	}
	return nil
}
func (p *proxyServer) closeWorkspaces() {
	p.mu.Lock()
	var users []*userState
	for _, u := range p.users {
		users = append(users, u)
	}
	p.mu.Unlock()
	for _, u := range users {
		u.apiMu.RLock()
		api := u.api
		u.apiMu.RUnlock()
		if api != nil {
			_ = api.Close()
		}
	}
}
func (p *proxyServer) storeToken(id string) string {
	mac := hmac.New(sha256.New, p.storeKey)
	_, _ = mac.Write([]byte(id))
	return hex.EncodeToString(mac.Sum(nil))
}

// Caller holds the user lifecycle lock; store RPCs use their own SQL transactions.
func (p *proxyServer) prepareWorkspace(id string, u *userState) error {
	u.apiMu.RLock()
	exists := u.api != nil
	u.apiMu.RUnlock()
	if exists {
		return nil
	}
	root, err := initializeWorkplace(p.root, id)
	if err != nil {
		return err
	}
	api, err := workspace.Open(root, id, p.catalog, p.skills)
	if err != nil {
		return err
	}
	api.GuideVersion = guideVersion
	api.DeleteGuard = func(ctx context.Context, _ string) (func(), error) {
		if err := p.checkInactive(ctx, id, u); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	u.apiMu.Lock()
	u.api = api
	u.apiMu.Unlock()
	return nil
}
func (p *proxyServer) serveStore(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.Header.Get("X-USERID"))
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if err != nil || id == uuid.Nil || !hmac.Equal([]byte(token), []byte(p.storeToken(id.String()))) {
		http.NotFound(w, r)
		return
	}
	u := p.user(id.String())
	u.apiMu.RLock()
	api := u.api
	u.apiMu.RUnlock()
	if api == nil {
		if err := u.lock(r.Context()); err != nil {
			return
		}
		err := p.prepareWorkspace(id.String(), u)
		u.unlock()
		if err != nil {
			jsonError(w, 503, "storage unavailable")
			return
		}
		u.apiMu.RLock()
		api = u.api
		u.apiMu.RUnlock()
	}
	api.StoreHandler().ServeHTTP(w, r)
}
