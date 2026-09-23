package run_sse

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf16"
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

// pickNativeDirectory 在 Go 服务所在的原生主机上打开系统目录选择器。
// 浏览器只触发调用；绝对路径由外部进程的标准输出返回。
func pickNativeDirectory(ctx context.Context) (string, error) {
	return pickNativeDirectoryWithRunner(ctx, runtime.GOOS, runDirectoryPickerCommand)
}

func pickNativeDirectoryWithRunner(ctx context.Context, goos string, run directoryPickerRunner) (string, error) {
	switch goos {
	case "darwin":
		return pickDarwinDirectory(ctx, run)
	case "windows":
		return pickWindowsDirectory(ctx, run)
	default:
		return "", fmt.Errorf("%w on %s", errDirectoryPickerUnsupported, goos)
	}
}

// pickDarwinDirectory 通过 osascript 驱动 macOS 系统的 choose folder 对话框，
// 取消时 osascript 以退出码 1 结束并在 stderr 提示 -128 / User canceled。
func pickDarwinDirectory(ctx context.Context, run directoryPickerRunner) (string, error) {
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

// windowsPickerCancelledExitCode 是用户取消 Windows 目录选择器时
// powershell 进程的约定退出码；与脚本失败（退出码 1）区分开。
const windowsPickerCancelledExitCode = 3

// windowsDirectoryPickerScript 在 PowerShell 中弹出 FolderBrowserDialog。
// 脚本通过 -EncodedCommand 传入以规避命令行引号转义问题；选中的路径以
// Base64(UTF-8) 输出，避免重定向管道受系统代码页（如中文 Windows 的
// GBK）影响而产生乱码。
const windowsDirectoryPickerScript = `Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = 'Select Project Directory'
if ($dialog.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { exit 3 }
[Console]::Out.Write([Convert]::ToBase64String([System.Text.Encoding]::UTF8.GetBytes($dialog.SelectedPath)))`

// pickWindowsDirectory 通过 powershell.exe（Windows PowerShell 5.1，STA
// 单元）驱动 FolderBrowserDialog：-NoProfile 避免加载用户配置拖慢启动，
// -NonInteractive 防止脚本异常时挂起等待输入。
func pickWindowsDirectory(ctx context.Context, run directoryPickerRunner) (string, error) {
	stdout, stderr, err := run(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-STA",
		"-EncodedCommand", encodePowerShellScript(windowsDirectoryPickerScript),
	)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if exit, ok := err.(interface{ ExitCode() int }); ok && exit.ExitCode() == windowsPickerCancelledExitCode {
			return "", errDirectoryPickerCancelled
		}
		message := strings.TrimSpace(stderr)
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("open directory picker: %s", message)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		return "", fmt.Errorf("decode directory picker output: %w", err)
	}
	path := string(raw)
	if path == "" {
		return "", errors.New("directory picker returned an empty path")
	}
	return path, nil
}

// encodePowerShellScript 生成 powershell -EncodedCommand 要求的
// Base64(UTF-16LE) 编码。
func encodePowerShellScript(script string) string {
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
