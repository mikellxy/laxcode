// Package run_evaluate implements the LLM-as-a-judge frontend. It evaluates an
// existing session's immutable JSONL history and persists the judge in a
// separate session so the evaluated evidence is never modified.
package run_evaluate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

const (
	exitOK    = 0
	exitRun   = 1
	exitUsage = 2

	errTypeUsage    = "usage"
	errTypeGenerate = "generate"
)

// EvaluationError is the machine-readable error payload returned on stdout.
type EvaluationError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// EvaluationResult makes the target and judge sessions explicit. SessionID is
// the isolated judge session; EvalSessionID is the immutable source session
// requested with -eval_session.
type EvaluationResult struct {
	SessionID     string                       `json:"session_id"`
	EvalSessionID string                       `json:"eval_session_id"`
	Result        string                       `json:"result"`
	TokenUsed     sharedkernel.TokenStatistics `json:"token_used"`
	WindowToken   sharedkernel.TokenStatistics `json:"window_token"`
	Error         *EvaluationError             `json:"error"`
}

// Run evaluates the session selected by -eval_session and returns a process
// exit code (0 success / 1 generation failure / 2 usage error). Stdout contains
// exactly one JSON result line; ReAct intermediate events are discarded.
func Run() int {
	result, exitCode := evaluate(context.Background())
	if err := writeResult(os.Stdout, result); err != nil {
		fmt.Fprintf(os.Stderr, "write evaluation result: %v\n", err)
		return exitRun
	}
	return exitCode
}

func evaluate(ctx context.Context) (EvaluationResult, int) {
	cli := config.CliConf
	env := config.EnvAndFileConf
	result := EvaluationResult{EvalSessionID: cli.EvalSession}

	usageFail := func(format string, args ...any) (EvaluationResult, int) {
		result.Error = &EvaluationError{Type: errTypeUsage, Message: fmt.Sprintf(format, args...)}
		return result, exitUsage
	}

	if strings.TrimSpace(cli.WorkDir) == "" {
		return usageFail("evaluate mode requires -workdir")
	}
	if strings.TrimSpace(cli.EvalSession) == "" {
		return usageFail("evaluate mode requires -eval_session")
	}
	if env.OpenaiApiKey == "" || env.OpenaiBaseUrl == "" || env.OpenaiModel == "" {
		return usageFail("openai_api_key / openai_base_url / openai_model are required")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return usageFail("resolve user home failed: %v", err)
	}
	historyPath, err := resolveHistoryPath(homeDir, cli.EvalSession)
	if err != nil {
		return usageFail("invalid -eval_session: %v", err)
	}
	info, err := os.Stat(historyPath)
	if err != nil {
		return usageFail("read evaluation history %s failed: %v", historyPath, err)
	}
	if !info.Mode().IsRegular() {
		return usageFail("evaluation history is not a regular file: %s", historyPath)
	}
	// Never reuse the target session: InitSysPrompt updates an existing session's
	// working system message, and Chat appends messages. A UUID-backed judge
	// session makes evidence contamination impossible even under rapid launches.
	judgeSessionID := "evaluation-" + uuid.NewString()
	assembled, err := agentasm.Assemble(ctx, agentasm.Input{
		WorkDir:      cli.WorkDir,
		HomeDir:      homeDir,
		ReadRoots:    []string{filepath.Dir(historyPath)},
		SessionID:    judgeSessionID,
		SystemPrompt: prompt.GetEvaluateSysPrompt(),
	})
	if err != nil {
		return usageFail("assemble evaluator failed: %v", err)
	}
	defer assembled.Cleanup()

	msg, runErr := assembled.Service.Chat(ctx, prompt.GetEvaluateUserPrompt(historyPath))
	result.SessionID = assembled.Session.ID
	result.TokenUsed = assembled.Session.TokenUsed
	result.WindowToken = assembled.Session.WindowToken
	if runErr != nil {
		result.Error = &EvaluationError{Type: errTypeGenerate, Message: runErr.Error()}
		return result, exitRun
	}
	if msg != nil {
		result.Result = msg.Content
	}
	return result, exitOK
}

func resolveHistoryPath(homeDir, sessionID string) (string, error) {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00") {
		return "", fmt.Errorf("session ID must be one path segment")
	}
	return layout.SessionHistory(homeDir, sessionID), nil
}

func writeResult(w io.Writer, result EvaluationResult) error {
	return json.NewEncoder(w).Encode(result)
}
