//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package run_sse

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestCodeInstanceGuardCreatesPublishesAndSerializes(t *testing.T) {
	homeDir := t.TempDir()
	instancePath := layout.SSECodeInstance(homeDir)
	if _, err := os.Stat(instancePath); !os.IsNotExist(err) {
		t.Fatalf("instance file unexpectedly exists before first use: %v", err)
	}

	first, err := AcquireCodeInstanceGuard(homeDir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	const url = "http://127.0.0.1:43210"
	if err := first.Publish(url); err != nil {
		t.Fatalf("publish: %v", err)
	}
	state, err := os.ReadFile(instancePath)
	if err != nil {
		t.Fatalf("read instance: %v", err)
	}
	info, err := os.Stat(instancePath)
	if err != nil {
		t.Fatalf("stat instance: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("instance permissions = %o, want 600", got)
	}
	want := fmt.Sprintf("v1\t%d\t%s\n", os.Getpid(), url)
	if string(state) != want {
		t.Fatalf("instance state = %q, want %q", state, want)
	}

	if _, err := AcquireCodeInstanceGuard(homeDir); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second acquire error = %v, want already-running error", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first guard: %v", err)
	}
	second, err := AcquireCodeInstanceGuard(homeDir)
	if err != nil {
		t.Fatalf("reacquire after close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close second guard: %v", err)
	}
}
