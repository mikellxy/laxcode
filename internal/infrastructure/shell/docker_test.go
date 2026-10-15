package shell

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func TestDockerHelperProcess(t *testing.T) {
	if os.Getenv("LAXCODE_DOCKER_HELPER") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	data, _ := json.Marshal(args)
	f, _ := os.OpenFile(os.Getenv("LAXCODE_DOCKER_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	_, _ = f.Write(append(data, '\n'))
	_ = f.Close()
	mode := os.Getenv("LAXCODE_DOCKER_MODE")
	if args[0] == "rm" {
		if mode == "cleanup-failure" {
			fmt.Fprintln(os.Stderr, "daemon unavailable")
			os.Exit(1)
		}
		os.Exit(0)
	}
	fmt.Println("partial-output")
	if mode == "flood" {
		fmt.Print(strings.Repeat("x", maxDockerOutputBytes+4096))
		os.Exit(0)
	}
	if mode == "block" {
		_ = os.WriteFile(os.Getenv("LAXCODE_DOCKER_READY"), []byte("ready"), 0600)
		time.Sleep(30 * time.Second)
	}
	if mode == "nonzero" {
		os.Exit(17)
	}
	if mode == "missing-image" {
		fmt.Fprintln(os.Stderr, "No such image")
		os.Exit(125)
	}
	os.Exit(0)
}

func testDockerRunner(t *testing.T, mode string) (*DockerRunner, string, string) {
	t.Helper()
	root := t.TempDir()
	work, temp := filepath.Join(root, "demo"), filepath.Join(root, "tmp")
	skills := layout.SkillsRoot(filepath.Join(root, "agenthome"))
	for _, dir := range []string{work, temp, skills} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	r := NewDocker(root, work, temp, skills, "test-image:local")
	r.uid, r.gid = 501, 20
	log := filepath.Join(root, "docker.jsonl")
	r.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=TestDockerHelperProcess", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "LAXCODE_DOCKER_HELPER=1", "LAXCODE_DOCKER_MODE="+mode,
			"LAXCODE_DOCKER_LOG="+log, "LAXCODE_DOCKER_READY="+filepath.Join(root, "ready"))
		return cmd
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, work, log
}

func dockerInvocations(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}

func TestDockerRunnerInvocationAndResults(t *testing.T) {
	for _, mode := range []string{"success", "nonzero", "missing-image", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, work, log := testDockerRunner(t, mode)
			beforeHome, beforeTemp := os.Getenv("HOME"), os.Getenv("TMPDIR")
			command := `printf '%s' '--privileged; $(touch /host-file)'`
			out, err := r.Run(context.Background(), work, command, 5*time.Second)
			if !strings.Contains(out.Output, "partial-output") {
				t.Fatalf("lost output: %+v %v", out, err)
			}
			if (mode == "missing-image" || mode == "cleanup-failure") != (err != nil) {
				t.Fatalf("%s: %+v %v", mode, out, err)
			}
			if mode == "nonzero" && out.ExitCode != 17 {
				t.Fatalf("exit code: %+v", out)
			}
			calls := dockerInvocations(t, log)
			if len(calls) != 2 || calls[0][0] != "run" || calls[1][0] != "rm" || calls[1][1] != "--force" {
				t.Fatalf("missing explicit cleanup: %v", calls)
			}
			args := calls[0]
			for _, flag := range []string{"--rm", "--network=none", "--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true", "--cpus=2", "--memory=1g", "--pids-limit=256", "501:20", "HOME=/tmp/agenthome", "TMPDIR=" + r.tempDir, "GOPROXY=off", "GOTOOLCHAIN=local"} {
				if !slices.Contains(args, flag) {
					t.Fatalf("missing %q: %v", flag, args)
				}
			}
			if args[len(args)-1] != command || slices.Contains(args, "--privileged") {
				t.Fatal("command escaped its argument")
			}
			if slices.Index(args, "--mount") < 0 || slices.Contains(args, bindMount(r.userRoot, false)) || strings.Contains(strings.Join(args, "\n"), "docker.sock") {
				t.Fatal("unsafe mounts")
			}
			mounts := 0
			for i, arg := range args {
				if arg != "--mount" {
					continue
				}
				mounts++
				options, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
				if err != nil {
					t.Fatal(err)
				}
				var source string
				for _, option := range options {
					if strings.HasPrefix(option, "src=") {
						source = strings.TrimPrefix(option, "src=")
					}
				}
				if !slices.Contains([]string{work, r.tempDir, r.skillsDir}, source) || !slices.Contains(options, "dst="+source) {
					t.Fatalf("unexpected mount: %v", options)
				}
				if slices.Contains(options, "readonly") != (source == r.skillsDir) || !slices.Contains(options, "bind-recursive=disabled") || !slices.Contains(options, "bind-propagation=rprivate") {
					t.Fatalf("unsafe mount options: %v", options)
				}
			}
			if mounts != 3 {
				t.Fatalf("mount count: %d", mounts)
			}
			if os.Getenv("HOME") != beforeHome || os.Getenv("TMPDIR") != beforeTemp {
				t.Fatal("server environment changed")
			}
			entries, _ := filepath.Glob(filepath.Join(r.tempDir, tempFilePattern))
			if len(entries) != 0 {
				t.Fatalf("output files leaked: %v", entries)
			}
		})
	}
}

func TestDockerRunnerRejectsUnsafeMountsBeforeLaunch(t *testing.T) {
	for _, mode := range []string{"root", "outside-workdir", "home-mount", "tmp-symlink", "skills-outside", "skills-home", "skills-symlink", "skills-missing"} {
		t.Run(mode, func(t *testing.T) {
			r, work, log := testDockerRunner(t, "success")
			switch mode {
			case "root":
				r.uid = 0
			case "outside-workdir":
				work = t.TempDir()
			case "home-mount":
				r.projectDir = filepath.Join(r.userRoot, "agenthome")
				work = r.projectDir
			case "tmp-symlink":
				_ = os.Remove(r.tempDir)
				if err := os.Symlink(t.TempDir(), r.tempDir); err != nil {
					t.Fatal(err)
				}
			case "skills-outside":
				r.skillsDir = t.TempDir()
			case "skills-home":
				r.skillsDir = filepath.Join(r.userRoot, "agenthome")
			case "skills-symlink":
				if err := os.Remove(r.skillsDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(work, r.skillsDir); err != nil {
					t.Fatal(err)
				}
			case "skills-missing":
				if err := os.Remove(r.skillsDir); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Run(context.Background(), work, "echo unsafe", time.Second); err == nil {
				t.Fatal("unsafe mount accepted")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("unsafe command launched")
			}
		})
	}
}

func TestDockerRunnerBoundsCapturedOutput(t *testing.T) {
	r, work, _ := testDockerRunner(t, "flood")
	out, err := r.Run(context.Background(), work, "flood", 10*time.Second)
	if err != nil || !out.OutputTruncated || len(out.Output) != maxDockerOutputBytes {
		t.Fatalf("output limit: len=%d truncated=%v err=%v", len(out.Output), out.OutputTruncated, err)
	}
}

func TestDockerRunnerTimeoutAndCloseRemoveContainer(t *testing.T) {
	for _, mode := range []string{"timeout", "close"} {
		t.Run(mode, func(t *testing.T) {
			r, work, log := testDockerRunner(t, "block")
			timeout := 30 * time.Second
			if mode == "timeout" {
				timeout = 500 * time.Millisecond
			}
			result := make(chan error, 1)
			go func() {
				out, err := r.Run(context.Background(), work, "sleep 30", timeout)
				if !strings.Contains(out.Output, "partial-output") {
					result <- errors.New("partial output lost")
					return
				}
				result <- err
			}()
			if mode == "close" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(r.userRoot, "ready")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("helper did not start")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := r.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				cause := context.DeadlineExceeded
				if mode == "close" {
					cause = context.Canceled
				}
				if !errors.Is(err, cause) {
					t.Fatalf("cancellation cause: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("sandbox cancellation hung")
			}
			calls := dockerInvocations(t, log)
			if len(calls) < 2 || calls[1][0] != "rm" {
				t.Fatalf("no cleanup: %v", calls)
			}
		})
	}
}
