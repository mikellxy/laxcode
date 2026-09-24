package main

import (
	"context"
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
	"strings"
	"syscall"
	"time"

	webui "github.com/mikellxy/laxcode/web"
)

const shutdownTimeout = 5 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:5173", "loopback address for the web UI")
	backendValue := flag.String("backend", "", "LaxCode backend URL")
	flag.Parse()

	if err := validateLoopbackAddress(*addr); err != nil {
		return err
	}
	backend, err := parseBackendURL(*backendValue)
	if err != nil {
		return err
	}
	assets, err := webui.Dist()
	if err != nil {
		return fmt.Errorf("load embedded web assets: %w", err)
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return errors.New("embedded web bundle is missing index.html; rebuild with `make build-windows`")
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	defer listener.Close()

	server := &http.Server{Addr: listener.Addr().String(), Handler: newHandler(assets, backend)}
	serverErrors := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	log.Printf("LaxCode Web listening on http://%s (backend: %s)", listener.Addr(), backend)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("serve web UI: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down web UI: %w", err)
	}
	return nil
}

func validateLoopbackAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid web listen address %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("web listen address must be loopback, got %q", addr)
	}
	return nil
}

func parseBackendURL(raw string) (*url.URL, error) {
	backend, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || backend.Scheme != "http" || backend.Host == "" || backend.Path != "" {
		return nil, fmt.Errorf("invalid backend URL %q", raw)
	}
	host := backend.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("backend URL must use a loopback host, got %q", raw)
		}
	}
	return backend, nil
}

func newHandler(assets fs.FS, backend *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		log.Printf("backend proxy error: %v", err)
		http.Error(w, "LaxCode backend is unavailable", http.StatusBadGateway)
	}
	static := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBackendPath(r.URL.Path) {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		assetPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if assetPath == "." {
			assetPath = "index.html"
		}
		if info, err := fs.Stat(assets, assetPath); err != nil || info.IsDir() {
			clone := r.Clone(r.Context())
			clone.URL.Path = "/"
			static.ServeHTTP(w, clone)
			return
		}
		static.ServeHTTP(w, r)
	})
}

func isBackendPath(requestPath string) bool {
	return requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") ||
		requestPath == "/chat" || requestPath == "/healthz"
}
