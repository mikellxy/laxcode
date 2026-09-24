package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"testing/fstest"
)

func TestHandlerServesAssetsAndSPAFallback(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":           {Data: []byte("app shell")},
		"assets/index.js":      {Data: []byte("javascript")},
		"assets/directory.txt": {Data: []byte("file")},
	}
	backend, _ := url.Parse("http://127.0.0.1:1")
	handler := newHandler(assets, backend)

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/", want: "app shell"},
		{path: "/assets/index.js", want: "javascript"},
		{path: "/sessions/example", want: "app shell"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if response.Body.String() != test.want {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.want)
			}
		})
	}
}

func TestHandlerProxiesBackendRoutes(t *testing.T) {
	backendURL, _ := url.Parse("http://127.0.0.1:1")
	handler := newHandler(fstest.MapFS{"index.html": {Data: []byte("app")}}, backendURL)

	for _, requestPath := range []string{"/api/sessions", "/chat", "/healthz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusBadGateway {
			t.Fatalf("%s: status = %d, want %d", requestPath, response.Code, http.StatusBadGateway)
		}
	}
}

func TestLoopbackValidation(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:5173", "[::1]:5173", "localhost:5173"} {
		if err := validateLoopbackAddress(addr); err != nil {
			t.Fatalf("validateLoopbackAddress(%q): %v", addr, err)
		}
	}
	if err := validateLoopbackAddress("0.0.0.0:5173"); err == nil {
		t.Fatal("validateLoopbackAddress accepted a non-loopback address")
	}

	if _, err := parseBackendURL("http://127.0.0.1:8090"); err != nil {
		t.Fatalf("parseBackendURL: %v", err)
	}
	if _, err := parseBackendURL("https://example.com"); err == nil {
		t.Fatal("parseBackendURL accepted a remote backend")
	}
}
