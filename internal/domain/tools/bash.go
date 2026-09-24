package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

const (
	ToolBash           = "bash"
	ToolReadFile       = "read_file"
	ToolWriteFile      = "write_file"
	ToolEditFile       = "edit_file"
	ToolGrep           = "grep"
	ToolGlob           = "glob"
	ToolCreateSkill    = "create_skill"
	ToolUpdateSkill    = "update_skill"
	ToolRunSubAgent    = "run_sub_agent"
	defaultBashTimeout = 30 * time.Second
	maxBashTimeout     = 60 * time.Second
	maxBashOutputRunes = 8000
)

type BashTool struct {
	WorkDir string
	// Timeout 是未指定 timeout_ms 时的默认值，零值取 30s；最终最多 60s。
	Timeout time.Duration
	// Runner 是命令执行端口，经构造注入；进程组、输出临时文件与
	// 后台进程回收等 OS 机制见 infrastructure/shell
	Runner    ShellRunner
	artifacts ArtifactStore
	sessionID string
}

func NewBashTool(workDir string, runner ShellRunner, artifacts ArtifactStore, sessionID string) *BashTool {
	return &BashTool{WorkDir: workDir, Runner: runner, Timeout: defaultBashTimeout, artifacts: artifacts, sessionID: sessionID}
}

type bashArgs struct {
	Command   string `json:"command"`
	TimeoutMS *int64 `json:"timeout_ms,omitempty"`
}

func (b *BashTool) AfterExecInfo(message json.RawMessage) string {
	return ""
}

func (b *BashTool) BeforeExecInfo(args json.RawMessage) string {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return ToolBash + "()"
	}
	if a.Command == "" {
		return ToolBash + "()"
	}

	return fmt.Sprintf("%s(%s)", ToolBash, a.Command)
}

func (b *BashTool) Name() string {
	return ToolBash
}

func (b *BashTool) Definition() sharedkernel.ToolDefinition {
	return sharedkernel.ToolDefinition{
		Name: b.Name(),
		Description: "在工作目录执行 bash 命令。timeout_ms 默认 30000，超过 60000 按 60000 执行。" +
			"默认启用 pipefail，管道中任一命令失败时返回最右侧非零退出码。" +
			"长输出会先归档，再返回前 8000 字符及 read_artifact 引用；不必为缩短输出追加 head/tail。退出码代表整段脚本。" +
			"需要后台进程（如启动服务器）时，" +
			"务必重定向输出到日志文件并记录pid，例如: " +
			"python3 server.py > /tmp/srv.log 2>&1 & echo \"pid=$!\"，" +
			"之后用返回的pid执行 kill -9 <pid> 清理，也可 tail 日志文件排错",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "执行bash 命令，如 grep -rn NewAgentEngine",
				},
				"timeout_ms": map[string]any{
					"type": "integer", "minimum": 1,
					"description": "超时毫秒数，默认 30000；大于 60000 时由程序限制为 60000",
				},
			},
			"required": []string{"command"},
		},
	}
}

type ExecResult struct {
	ExitCode  *int   `json:"exit_code"`
	Status    string `json:"status"`
	Stdout    string `json:"stdout"`
	Truncated bool   `json:"is_truncated"`
	Desc      string `json:"desc"`
}

func (e *ExecResult) String() string {
	return fmt.Sprintf("%s\nstatus:%s\nexit_code:%s\nstdout_truncated:%v\nstdout:%s",
		e.Desc, e.Status, e.exitCodeString(), e.Truncated, e.Stdout)
}

func (e *ExecResult) exitCodeString() string {
	if e.ExitCode == nil {
		return "unknown"
	}
	return strconv.Itoa(*e.ExitCode)
}

func (b *BashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	result := b.ExecuteResult(ctx, args)
	return result.Output, result.Error
}

// ExecuteResult 在截断前归档完整输出，结果和引用一并传给 Registry。
func (b *BashTool) ExecuteResult(ctx context.Context, args json.RawMessage) *sharedkernel.ToolResult {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, err)}
	}
	if strings.TrimSpace(a.Command) == "" {
		return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, errors.New("command required"))}
	}
	timeout := b.timeout()
	if a.TimeoutMS != nil {
		if *a.TimeoutMS <= 0 {
			return &sharedkernel.ToolResult{Error: NewErrorWithPrompt(&ParamError{}, errors.New("timeout_ms must be positive"))}
		}
		// 先限制整数值再转换，避免超大毫秒数转 Duration 溢出。
		timeout = time.Duration(min(*a.TimeoutMS, maxBashTimeout.Milliseconds())) * time.Millisecond
	}

	outcome, runErr := b.Runner.Run(ctx, b.WorkDir, a.Command, timeout)
	// 非零退出不是工具错误：退出码与原始错误描述一并回给模型自行判断
	result := &ExecResult{Desc: "命令执行成功", Status: "completed", Stdout: outcome.Output, ExitCode: &outcome.ExitCode}
	if outcome.ExitCode != 0 || outcome.ExitErr != "" {
		result.Desc = "命令执行失败: " + outcome.ExitErr
	}
	if runErr != nil {
		result.Status = "execution_error"
		result.ExitCode = nil
		result.Desc = "命令执行异常: " + runErr.Error()
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			result.Status = "canceled"
			if errors.Is(runErr, context.DeadlineExceeded) {
				result.Status = "timed_out"
			}
			runErr = fmt.Errorf("bash执行超时或被取消: %w", runErr)
		}
	}

	ret := &sharedkernel.ToolResult{}
	// 归档保存原始 stdout/stderr；预览及状态单独保存在消息中。
	result.Truncated = utf8.RuneCountInString(outcome.Output) > maxBashOutputRunes
	if result.Truncated {
		var archiveErr error
		if b.artifacts == nil {
			archiveErr = errors.New("artifact store is not configured")
		} else {
			// 即使调用被取消，也给已产生输出一次有界的落盘机会。
			archiveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			ref, err := b.artifacts.PutArtifact(archiveCtx, b.sessionID, outcome.Output)
			cancel()
			archiveErr = err
			if err == nil {
				ret.Artifact = &ref
			}
		}
		if archiveErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("归档命令输出失败: %w", archiveErr))
			result.Desc += "；完整输出未归档，缺失部分无法通过 artifact 读取"
		}
		result.Stdout, _ = safeTruncateUTF8(outcome.Output, maxBashOutputRunes)
		result.Desc += " ;bash输出过长已截断至前:" + strconv.Itoa(maxBashOutputRunes) + "字符"
	}

	ret.CompactContent = fmt.Sprintf("%s\nstatus:%s\nexit_code:%s\nstdout_truncated:%v",
		result.Desc, result.Status, result.exitCodeString(), result.Truncated)
	ret.Output = result.String()
	if ret.Artifact != nil {
		ret.Output += fmt.Sprintf("\n[完整输出已归档 bytes=%d；read_artifact(artifact_id=%s, offset=0, limit=4000) 按需读取]",
			ret.Artifact.ByteSize, ret.Artifact.ID)
	}
	if runErr != nil {
		ret.Error = NewErrorWithPrompt(&BashExecuteError{}, runErr)
	}
	return ret
}

// Close 委托命令执行端口回收本次运行内遗留的后台进程（含 LLM 遗忘
// 清理的）与输出临时文件，随会话结束由 Registry.Close 统一调用
func (b *BashTool) Close() error {
	return b.Runner.Close()
}

func (b *BashTool) timeout() time.Duration {
	if b.Timeout > 0 {
		return min(b.Timeout, maxBashTimeout)
	}
	return defaultBashTimeout
}

func safeTruncateUTF8(s string, maxRune int) (out string, truncated bool) {
	r := []rune(s)
	if len(r) <= maxRune {
		return s, false
	}
	return string(r[:maxRune]), true
}
