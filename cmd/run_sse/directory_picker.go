package run_sse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

var (
	errDirectoryPickerCancelled   = errors.New("directory picker cancelled")
	errDirectoryPickerUnsupported = errors.New("directory picker is unsupported")
)

type directoryPickerRunner func(context.Context, string, ...string) (stdout string, stderr string, err error)

func runDirectoryPickerCommand(ctx context.Context, name string, args ...string) (string, string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// pickNativeDirectory 在 Go 服务所在的 macOS 主机上打开系统目录选择器。
// 浏览器只触发调用；绝对路径由 osascript 的标准输出返回。
func pickNativeDirectory(ctx context.Context) (string, error) {
	return pickNativeDirectoryWithRunner(ctx, runtime.GOOS, runDirectoryPickerCommand)
}

func pickNativeDirectoryWithRunner(ctx context.Context, goos string, run directoryPickerRunner) (string, error) {
	if goos != "darwin" {
		return "", fmt.Errorf("%w on %s", errDirectoryPickerUnsupported, goos)
	}
	stdout, stderr, err := run(ctx, "osascript",
		"-e", `set selectedFolder to choose folder with prompt "Select Project Directory"`,
		"-e", "POSIX path of selectedFolder",
	)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if exit, ok := err.(interface{ ExitCode() int }); ok && exit.ExitCode() == 1 &&
			(strings.Contains(stderr, "-128") || strings.Contains(strings.ToLower(stderr), "user canceled")) {
			return "", errDirectoryPickerCancelled
		}
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("open directory picker: %s", message)
	}
	path := strings.TrimRight(stdout, "\r\n")
	if path == "" {
		return "", errors.New("directory picker returned an empty path")
	}
	return path, nil
}
