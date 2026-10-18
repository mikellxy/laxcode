package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxShellAndCancellation(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "demo")
	tmp := filepath.Join(root, "tmp")
	os.Mkdir(project, 0700)
	os.Mkdir(tmp, 0700)
	r := NewSandbox(root, project, tmp)
	defer r.Close()
	out, e := r.Run(context.Background(), project, "printf hello > file; cat file; exit 7", time.Second)
	if e != nil || out.Output != "hello" || out.ExitCode != 7 {
		t.Fatalf("%+v %v", out, e)
	}
	start := time.Now()
	_, e = r.Run(context.Background(), project, "sleep 20 & wait", 100*time.Millisecond)
	if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("cancellation: %v", e)
	}
	if _, e = r.Run(context.Background(), root, "pwd", time.Second); e == nil {
		t.Fatal("accepted outside project")
	}
	out, e = r.Run(context.Background(), project, "printf '%s' \"$TMPDIR\"", time.Second)
	if e != nil || !strings.Contains(out.Output, "tmp") {
		t.Fatal(e)
	}
}

func TestCloseCancelsAnActiveShell(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	tmp := filepath.Join(root, "tmp")
	os.Mkdir(project, 0700)
	os.Mkdir(tmp, 0700)
	r := NewSandbox(root, project, tmp)
	result := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), project, "touch started; sleep 60 & wait", time.Minute)
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(project, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("close did not cancel: %v", err)
	}
	if _, err := r.Run(context.Background(), project, "true", time.Second); err == nil {
		t.Fatal("closed runner accepted work")
	}
}
