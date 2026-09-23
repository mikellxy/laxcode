// Package run_evaluate implements the single-run LLM-as-a-judge frontend. It
// evaluates an existing session's immutable JSONL history with the same
// ReActService assembly used by one-shot mode, while persisting the judge in a
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

// EvaluationResult keeps the one-shot result shape while making the target and
// judge sessions explicit. SessionID is the isolated judge session; EvalSessionID
// is the immutable source session requested with -eval_session.
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
	ctx := context.Background()
	cli := config.CliConf
	env := config.EnvAndFileConf

	usageFail := func(format string, args ...any) int {
		writeResult(os.Stdout, EvaluationResult{
			EvalSessionID: cli.EvalSession,
			Error: &EvaluationError{
				Type:    errTypeUsage,
				Message: fmt.Sprintf(format, args...),
			},
		})
		return exitUsage
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
	res := EvaluationResult{
		SessionID:     assembled.Session.ID,
		EvalSessionID: cli.EvalSession,
		TokenUsed:     assembled.Session.TokenUsed,
		WindowToken:   assembled.Session.WindowToken,
	}
	if runErr != nil {
		res.Error = &EvaluationError{Type: errTypeGenerate, Message: runErr.Error()}
		writeResult(os.Stdout, res)
		return exitRun
	}
	if msg != nil {
		res.Result = msg.Content
	}
	writeResult(os.Stdout, res)
	return exitOK
}

func resolveHistoryPath(homeDir, sessionID string) (string, error) {
	if sessionID == "" || sessionID == "." || sessionID == ".." || strings.ContainsAny(sessionID, "/\\\x00") {
		return "", fmt.Errorf("session ID must be one path segment")
	}
	return layout.SessionHistory(homeDir, sessionID), nil
}

func writeResult(w io.Writer, res EvaluationResult) {
	data, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal evaluation result failed: %v\n", err)
		return
	}
	fmt.Fprintln(w, string(data))
}
