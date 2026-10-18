// laxcode-proxy owns user routing and OpenShell lifecycle. It does not import cloud internals.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	webui "github.com/mikellxy/laxcode/web"
)

type userState struct {
	gate     chan struct{}
	endpoint *url.URL
	active   int
	lastUsed time.Time
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
	runtime   sandboxRuntime
	root      string
	assets    http.Handler
	transport *http.Transport
	client    *http.Client
	mu        sync.Mutex
	users     map[string]*userState
	chats     chan struct{}
	idle      time.Duration
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
	return &proxyServer{runtime: runtime, root: root, assets: staticHandler(assets), transport: transport, client: &http.Client{Transport: transport, Timeout: 3 * time.Second}, users: map[string]*userState{}, chats: make(chan struct{}, maxChats), idle: idle}
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
		return u.endpoint, nil
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
	if r.URL.Path == "/healthz" {
		w.WriteHeader(200)
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
	streaming := r.Method == "POST" && (r.URL.Path == "/chat" || strings.HasSuffix(r.URL.Path, "/resume"))
	if streaming {
		select {
		case p.chats <- struct{}{}:
			defer func() { <-p.chats }()
		default:
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			action := "resend"
			if strings.HasSuffix(r.URL.Path, "/resume") {
				action = "resume"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "服务繁忙，请稍后重试", "code": "SERVER_BUSY", "retry_action": action})
			return
		}
	}
	u := p.user(id)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err = u.lock(ctx); err != nil {
		jsonError(w, 503, "sandbox startup timed out")
		return
	}
	endpoint, err := p.ensure(ctx, id, u)
	if err != nil {
		u.unlock()
		log.Printf("sandbox %s: %v", id, err)
		jsonError(w, 503, "sandbox unavailable; inspect deployment logs")
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
			jsonError(w, 502, "sandbox connection failed")
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
	providers := flag.String("providers-file", "/run/laxcode/providers.json", "registered provider names")
	idle := flag.Duration("idle-timeout", 30*time.Minute, "stop inactive sandboxes after this duration")
	maxChats := flag.Int("max-chats", 2, "global concurrent chat limit")
	flag.Parse()
	if *idle <= 0 || *maxChats < 1 {
		return errors.New("idle-timeout and max-chats must be positive")
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
	runtime := &openshellRuntime{image: *image, policy: *policy, providers: names}
	p := newProxy(runtime, *root, assets, *idle, *maxChats)
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
