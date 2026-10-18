package shell

import (
	"context"
	"errors"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const maxOutputBytes = 32 << 20

type boundedOutput struct {
	file      *os.File
	written   int
	truncated bool
}

func (o *boundedOutput) Write(b []byte) (int, error) {
	n := min(len(b), maxOutputBytes-o.written)
	if n > 0 {
		w, e := o.file.Write(b[:n])
		o.written += w
		if e != nil {
			return w, e
		}
	}
	if n < len(b) {
		o.truncated = true
	}
	return len(b), nil
}

type sandboxRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// SandboxRunner executes inside the already established OpenShell workload boundary.
type SandboxRunner struct {
	root, project, temp string
	mu                  sync.Mutex
	closed              bool
	active              map[*sandboxRun]struct{}
}

func NewSandbox(root, project, temp string) *SandboxRunner {
	return &SandboxRunner{root: root, project: project, temp: temp, active: make(map[*sandboxRun]struct{})}
}
func (*SandboxRunner) BashSandboxed() bool { return true }
func (r *SandboxRunner) Run(ctx context.Context, work, command string, timeout time.Duration) (out tools.ShellOutcome, err error) {
	for _, p := range []string{r.project, r.temp, work} {
		if e := tools.ValidateUserPath(r.root, p); e != nil {
			return out, e
		}
	}
	if e := tools.ValidateUserPath(r.project, work); e != nil {
		return out, e
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := &sandboxRun{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return out, errors.New("Bash runner is closed")
	}
	r.active[run] = struct{}{}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.active, run); close(run.done); r.mu.Unlock() }()
	file, e := os.CreateTemp(r.temp, "bash-*")
	if e != nil {
		return out, e
	}
	defer os.Remove(file.Name())
	defer file.Close()
	capture := &boundedOutput{file: file}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-o", "pipefail", "-c", command)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "TMPDIR="+r.temp, "TMP="+r.temp, "TEMP="+r.temp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(e, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = capture
	cmd.Stderr = capture
	if e = cmd.Start(); e != nil {
		return out, e
	}
	waitErr := cmd.Wait()
	// Background descendants must not outlive a tool call, including a successful shell exit.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if _, e = file.Seek(0, io.SeekStart); e != nil {
		return out, e
	}
	data, e := io.ReadAll(file)
	out.Output = string(data)
	out.OutputTruncated = capture.truncated
	if ctx.Err() != nil {
		return out, errors.Join(ctx.Err(), e)
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		out.ExitCode = exit.ExitCode()
		out.ExitErr = waitErr.Error()
	} else if waitErr != nil {
		return out, errors.Join(waitErr, e)
	}
	return out, e
}
func (r *SandboxRunner) Close() error {
	r.mu.Lock()
	r.closed = true
	runs := make([]*sandboxRun, 0, len(r.active))
	for run := range r.active {
		run.cancel()
		runs = append(runs, run)
	}
	r.mu.Unlock()
	for _, run := range runs {
		<-run.done
	}
	return nil
}
