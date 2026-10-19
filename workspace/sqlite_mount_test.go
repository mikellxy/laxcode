package workspace

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

// Explicit opt-in: verifies native macOS SQLite transactions against a Linux bind mount.
func TestStorageAcrossDockerMount(t *testing.T) {
	if os.Getenv("LAXCODE_DOCKER_SQLITE_TEST") != "1" {
		t.Skip("set LAXCODE_DOCKER_SQLITE_TEST=1 to test Docker Desktop storage interoperability")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "workspace.test")
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", binary, ".")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross compile: %v\n%s", err, out)
	}
	root := t.TempDir()
	api, err := Open(root, "test-owner", Catalog{}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", 403)
			return
		}
		api.StoreHandler().ServeHTTP(w, r)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	callback := fmt.Sprintf("http://host.docker.internal:%d", port)
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=bridge", "--user=1000:1000",
		"--mount", "type=bind,src="+root+",dst=/workplace",
		"--mount", "type=bind,src="+binary+",dst=/workspace.test,readonly",
		"-e", "LAXCODE_SQLITE_CHILD=1", "-e", "LAXCODE_SQLITE_URL="+callback, "laxcode-tools:go1.26.6-v1", "/workspace.test", "-test.run=^TestSQLiteMountedWorker$", "-test.v")
	if runtime.GOOS == "linux" {
		command.Args = append([]string{"docker", "run", "--add-host=host.docker.internal:host-gateway"}, command.Args[2:]...)
	}
	var output strings.Builder
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(root, "child-ready")); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("worker not ready: " + output.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(root, "host-ready"), []byte("ready"), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := api.repo.CreateSession(ctx, fmt.Sprintf("host-%03d", i), "test-owner", "project", "", "/workplace/demo", "code"); err != nil {
			t.Fatal(err)
		}
		if _, err := api.repo.ListSessions(ctx, "test-owner", "project", "", 100); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("Linux worker: %v\n%s", err, output.String())
	}
	page, err := api.repo.ListSessions(ctx, "test-owner", "project", "", 250)
	if err != nil || len(page.Sessions) != 200 {
		t.Fatalf("shared records=%d err=%v\n%s", len(page.Sessions), err, output.String())
	}
	t.Log("native host and UID 1000 Linux worker concurrently wrote/read through host-owned storage; 200 records verified")
}
func TestSQLiteMountedWorker(t *testing.T) {
	if os.Getenv("LAXCODE_SQLITE_CHILD") != "1" {
		t.Skip("Docker subprocess only")
	}
	repo := sessionrepo.NewRemote(os.Getenv("LAXCODE_SQLITE_URL"), "test-owner", "test-key")
	defer repo.Close()
	if err := os.WriteFile("/workplace/child-ready", []byte("ready"), 0644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat("/workplace/host-ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < 100; i++ {
		if _, err := repo.CreateSession(context.Background(), fmt.Sprintf("child-%03d", i), "test-owner", "project", "", "/workplace/demo", "code"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.ListSessions(context.Background(), "test-owner", "project", "", 100); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
