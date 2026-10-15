package shell

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

const DefaultDockerImage = "laxcode-cloud-bash:local"

const maxDockerOutputBytes = 32 << 20

type dockerOutput struct {
	file      *os.File
	written   int
	truncated bool
}

func (o *dockerOutput) Write(data []byte) (int, error) {
	size := min(len(data), maxDockerOutputBytes-o.written)
	if size > 0 {
		n, err := o.file.Write(data[:size])
		o.written += n
		if err != nil {
			return n, err
		}
	}
	if size < len(data) {
		o.truncated = true
	}
	return len(data), nil
}

type dockerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// DockerRunner mounts the project and user tmp writable, and only Skills from agenthome read-only.
type DockerRunner struct {
	userRoot, projectDir, tempDir, skillsDir, image string
	uid, gid                                        int
	command                                         func(context.Context, string, ...string) *exec.Cmd
	mu                                              sync.Mutex
	closed                                          bool
	active                                          map[string]*dockerRun
}

var _ tools.ShellRunner = (*DockerRunner)(nil)

func (*DockerRunner) BashSandboxed() bool { return true }

func NewDocker(userRoot, projectDir, tempDir, skillsDir, image string) *DockerRunner {
	if image == "" {
		image = DefaultDockerImage
	}
	return &DockerRunner{userRoot: userRoot, projectDir: projectDir, tempDir: tempDir, skillsDir: skillsDir, image: image,
		uid: os.Getuid(), gid: os.Getgid(), command: exec.CommandContext, active: make(map[string]*dockerRun)}
}

func (r *DockerRunner) validate(workDir string) error {
	if r.uid <= 0 || r.gid < 0 {
		return errors.New("cloud Bash requires a non-root service account on a Unix host")
	}
	if r.image == "" || strings.HasPrefix(r.image, "-") || strings.ContainsAny(r.image, " \t\r\n") {
		return errors.New("invalid cloud Bash image")
	}
	for _, path := range []string{r.userRoot, r.projectDir, r.tempDir, r.skillsDir, workDir} {
		if !filepath.IsAbs(path) {
			return errors.New("cloud Bash directories must be absolute")
		}
		if err := tools.ValidateUserPath(r.userRoot, path); err != nil {
			return fmt.Errorf("invalid cloud Bash directory: %w", err)
		}
	}
	project, err := filepath.Rel(r.userRoot, r.projectDir)
	if err != nil || !fs.ValidPath(project) || strings.ContainsAny(project, `/\`) || project == "." || strings.EqualFold(project, "agenthome") || strings.EqualFold(project, "tmp") {
		return errors.New("cloud Bash must mount a project directly beneath UserRoot")
	}
	if filepath.Clean(r.tempDir) != filepath.Join(filepath.Clean(r.userRoot), "tmp") {
		return errors.New("cloud Bash tmp must be UserRoot/tmp")
	}
	if filepath.Clean(r.skillsDir) != layout.SkillsRoot(filepath.Join(filepath.Clean(r.userRoot), "agenthome")) {
		return errors.New("cloud Bash Skills must be UserRoot/agenthome/.laxcode/skills")
	}
	if err := tools.ValidateUserPath(r.projectDir, workDir); err != nil {
		return fmt.Errorf("Bash workdir must remain within its project: %w", err)
	}
	for _, path := range []string{r.userRoot, r.projectDir, r.tempDir, r.skillsDir, workDir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return errors.New("cloud Bash directory is missing or is a symbolic link")
		}
	}
	return nil
}

func bindMount(path string, readOnly bool) string {
	var out strings.Builder
	w := csv.NewWriter(&out)
	options := []string{"type=bind", "src=" + path, "dst=" + path, "bind-recursive=disabled", "bind-propagation=rprivate"}
	if readOnly {
		options = append(options, "readonly")
	}
	_ = w.Write(options)
	w.Flush()
	return strings.TrimSuffix(out.String(), "\n")
}

func (r *DockerRunner) args(name, workDir, command string) []string {
	return []string{"run", "--rm", "--network=none", "--pull=never", "--name", name,
		"--init", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true",
		"--cpus=2", "--memory=1g", "--memory-swap=1g", "--pids-limit=256", "--log-driver=none",
		"--user", strconv.Itoa(r.uid) + ":" + strconv.Itoa(r.gid),
		"--mount", bindMount(r.projectDir, false), "--mount", bindMount(r.tempDir, false),
		"--mount", bindMount(r.skillsDir, true), "--workdir", workDir,
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=268435456,mode=1777",
		"--env", "HOME=/tmp/agenthome", "--env", "TMPDIR=" + r.tempDir,
		"--env", "TMP=" + r.tempDir, "--env", "TEMP=" + r.tempDir,
		"--env", "GOPATH=" + filepath.Join(r.tempDir, "go"),
		"--env", "GOTOOLCHAIN=local", "--env", "GOPROXY=off", "--env", "PIP_NO_INDEX=1",
		"--entrypoint", "/bin/bash", r.image, "-o", "pipefail", "-c",
		`mkdir -p "$HOME" && exec /bin/bash -o pipefail -c "$1"`, "laxcode-bash", command}
}

func (r *DockerRunner) remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := r.command(ctx, "docker", "rm", "--force", name).CombinedOutput()
	if err != nil && !strings.Contains(strings.ToLower(string(output)), "no such container") {
		return fmt.Errorf("remove Bash sandbox: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *DockerRunner) Run(ctx context.Context, workDir, command string, timeout time.Duration) (outcome tools.ShellOutcome, runErr error) {
	if err := r.validate(workDir); err != nil {
		return outcome, err
	}
	if timeout <= 0 {
		timeout = fallbackTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name := "laxcode-bash-" + uuid.NewString()
	run := &dockerRun{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return outcome, errors.New("Docker Bash runner is closed")
	}
	r.active[name] = run
	r.mu.Unlock()
	defer func() {
		// Killing the Docker CLI alone does not stop its container.
		cleanupErr := r.remove(name)
		runErr = errors.Join(runErr, cleanupErr)
		r.mu.Lock()
		if cleanupErr == nil {
			delete(r.active, name)
		}
		close(run.done)
		r.mu.Unlock()
	}()
	output, err := os.CreateTemp(r.tempDir, tempFilePattern)
	if err != nil {
		return outcome, err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	cmd := r.command(ctx, "docker", r.args(name, workDir, command)...)
	capture := &dockerOutput{file: output}
	cmd.Stdout, cmd.Stderr = capture, capture
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return outcome, fmt.Errorf("start Docker Bash sandbox: %w", err)
	}
	waitErr := cmd.Wait()
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return outcome, err
	}
	data, readErr := io.ReadAll(output)
	outcome.Output = string(data)
	outcome.OutputTruncated = capture.truncated
	if ctx.Err() != nil {
		return outcome, errors.Join(ctx.Err(), readErr)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return outcome, errors.Join(waitErr, readErr)
		}
		outcome.ExitCode, outcome.ExitErr = exitErr.ExitCode(), waitErr.Error()
		if outcome.ExitCode == 125 {
			return outcome, errors.Join(fmt.Errorf("Docker Bash sandbox could not start: %w", waitErr), readErr)
		}
	}
	return outcome, readErr
}

func (r *DockerRunner) Close() error {
	r.mu.Lock()
	r.closed = true
	runs := make(map[string]*dockerRun, len(r.active))
	for name, run := range r.active {
		run.cancel()
		runs[name] = run
	}
	r.mu.Unlock()
	var errs []error
	for name, run := range runs {
		<-run.done
		if err := r.remove(name); err != nil {
			errs = append(errs, err)
		} else {
			r.mu.Lock()
			delete(r.active, name)
			r.mu.Unlock()
		}
	}
	return errors.Join(errs...)
}
