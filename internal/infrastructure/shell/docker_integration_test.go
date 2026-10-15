package shell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

// Opt in on a non-root host with a prebuilt image; never pull during tests.
func TestDockerSandboxIntegration(t *testing.T) {
	image := os.Getenv("LAXCODE_DOCKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set LAXCODE_DOCKER_TEST_IMAGE to run real Docker tests")
	}
	if os.Getuid() <= 0 {
		t.Skip("Docker Bash requires a non-root service account")
	}
	root := t.TempDir()
	work, temp := filepath.Join(root, "demo"), filepath.Join(root, "tmp")
	home := filepath.Join(root, "agenthome")
	skills := layout.SkillsRoot(home)
	packageDir := filepath.Join(skills, "demo")
	for _, dir := range []string{work, temp, packageDir, layout.SessionRoot(home)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "agenthome", "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "agenthome", "secret"), filepath.Join(work, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.UserSettings(home), layout.SessionDB(home), filepath.Join(packageDir, "data.txt")} {
		if err := os.WriteFile(path, []byte("skill-resource"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/bash
set -euo pipefail
skill_dir=$(cd -- "$(dirname -- "$0")" && pwd)
test "$(cat "$skill_dir/data.txt")" = skill-resource
printf 'skill-output' > skill-output.txt
printf 'skill-output' > "$TMPDIR/skill-output.txt"
`
	if err := os.WriteFile(filepath.Join(packageDir, "run.sh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(layout.UserSettings(home), filepath.Join(packageDir, "settings-link")); err != nil {
		t.Fatal(err)
	}
	r := NewDocker(root, work, temp, skills, image)
	defer r.Close()
	var names []string
	r.command = func(ctx context.Context, binary string, args ...string) *exec.Cmd {
		if args[0] == "run" {
			for i, arg := range args {
				if arg == "--name" {
					names = append(names, args[i+1])
					break
				}
			}
		}
		return exec.CommandContext(ctx, binary, args...)
	}
	run := func(command string) string {
		t.Helper()
		out, err := r.Run(context.Background(), work, command, 60*time.Second)
		if err != nil || out.ExitCode != 0 {
			t.Fatalf("command failed: %+v %v", out, err)
		}
		return out.Output
	}
	probe := fmt.Sprintf(`python3 - <<'PY'
import os, pathlib, socket, threading, http.server, http.client, subprocess, errno
assert os.getuid() == %d and os.getuid() != 0
assert os.getgid() == %d
assert os.environ['TMPDIR'] == %q
assert os.environ['TMP'] == os.environ['TEMP'] == os.environ['TMPDIR']
assert os.environ['HOME'] == '/tmp/agenthome'
assert not pathlib.Path(%q).exists()
assert not pathlib.Path('escape').exists()
assert not pathlib.Path('/var/run/docker.sock').exists()
skills = pathlib.Path(%q)
package = skills / 'demo'
assert not (skills.parent / 'settings.json').exists()
assert not (skills.parent / 'sessions' / 'sessions.db').exists()
assert not (package / 'settings-link').exists()
assert (package / 'data.txt').read_text() == 'skill-resource'
subprocess.run([str(package / 'run.sh')], check=True)
assert pathlib.Path('skill-output.txt').read_text() == 'skill-output'
assert pathlib.Path(os.environ['TMPDIR'], 'skill-output.txt').read_text() == 'skill-output'
for mutate in [
    lambda: (skills / 'new-file').write_text('changed'),
    lambda: (package / 'data.txt').write_text('changed'),
    lambda: (package / 'data.txt').unlink(),
    lambda: (package / 'run.sh').chmod(0o600),
]:
    try:
        mutate()
    except OSError as e:
        assert e.errno == errno.EROFS, e
    else:
        raise AssertionError('Skills mount is writable')
assert pathlib.Path('/sys/fs/cgroup/memory.max').read_text().strip() == '1073741824'
assert pathlib.Path('/sys/fs/cgroup/pids.max').read_text().strip() == '256'
assert pathlib.Path('/sys/fs/cgroup/cpu.max').read_text().split()[0] != 'max'
assert 'CapEff:\t0000000000000000' in pathlib.Path('/proc/self/status').read_text()
assert 'NoNewPrivs:\t1' in pathlib.Path('/proc/self/status').read_text()
try:
    pathlib.Path('/etc/laxcode-probe').write_text('outside')
    raise AssertionError('root filesystem is writable')
except OSError: pass
try:
    socket.create_connection(('1.1.1.1',443), timeout=.5)
    raise AssertionError('external network allowed')
except OSError: pass
server = http.server.HTTPServer(('127.0.0.1',0),http.server.SimpleHTTPRequestHandler)
thread = threading.Thread(target=server.serve_forever); thread.start()
conn = http.client.HTTPConnection('127.0.0.1',server.server_port)
conn.request('GET','/'); assert conn.getresponse().status == 200
conn.close(); server.shutdown(); thread.join(); server.server_close()
pathlib.Path('persist.txt').write_text('project')
pathlib.Path(os.environ['TMPDIR'],'persist.txt').write_text('temporary')
pathlib.Path(os.environ['HOME'],'ephemeral.txt').write_text('ephemeral')
print('isolation-and-loopback-ok')
PY`, os.Getuid(), os.Getgid(), temp, filepath.Join(root, "agenthome", "secret"), skills)
	if out := run(probe); !strings.Contains(out, "isolation-and-loopback-ok") {
		t.Fatal(out)
	}
	if data, err := os.ReadFile(filepath.Join(packageDir, "data.txt")); err != nil || string(data) != "skill-resource" {
		t.Fatalf("host Skills changed: %q %v", data, err)
	}
	run(`test -f persist.txt && test -f "$TMPDIR/persist.txt" && test ! -f "$HOME/ephemeral.txt" && go version && python3 --version && rg --version`)
	if info, err := os.Stat(filepath.Join(work, "persist.txt")); err != nil || info.Size() != 7 {
		t.Fatalf("project write not persisted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module sandbox.test/demo\n\ngo 1.26.6\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main_test.go"), []byte("package demo\nimport \"testing\"\nfunc TestAdd(t *testing.T) { if 2+2 != 4 { t.Fatal(\"wrong\") } }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("go test ./...")
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module sandbox.test/demo\n\ngo 1.26.6\nrequire example.invalid/offline v1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), work, "go mod download", 10*time.Second)
	if err != nil || out.ExitCode == 0 || !strings.Contains(out.Output, "GOPROXY=off") {
		t.Fatalf("offline dependency lookup: %+v %v", out, err)
	}
	out, err = r.Run(context.Background(), work, "printf 'failure'; exit 7", 10*time.Second)
	if err != nil || out.ExitCode != 7 || !strings.Contains(out.Output, "failure") {
		t.Fatalf("nonzero result: %+v %v", out, err)
	}
	start := time.Now()
	run("sleep 30 > background.log 2>&1 & printf 'returned'")
	if time.Since(start) > 10*time.Second {
		t.Fatal("background process blocked run")
	}
	out, err = r.Run(context.Background(), work, "printf 'started'; sleep 30", time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(out.Output, "started") {
		t.Fatalf("timeout: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() {
		_, err := r.Run(ctx, work, "printf 'cancel started'; touch canceled-marker; sleep 30", 30*time.Second)
		canceled <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(work, "canceled-marker")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("cancel test container did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel cause: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation failed to remove container")
	}
	for _, name := range names {
		if output, err := exec.Command("docker", "inspect", name).CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(output)), "no such") {
			t.Fatalf("container leaked: %s: %s %v", name, output, err)
		}
	}
	missing := NewDocker(root, work, temp, skills, "laxcode-nonexistent-test-image:"+filepath.Base(root))
	defer missing.Close()
	if _, err := missing.Run(context.Background(), work, "touch host-fallback", 10*time.Second); err == nil {
		t.Fatal("missing image accepted")
	}
	if _, err := os.Stat(filepath.Join(work, "host-fallback")); !os.IsNotExist(err) {
		t.Fatal("command fell back to host")
	}
}
