package reactservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
)

func TestToolCallFingerprintCanonicalizesArguments(t *testing.T) {
	a := sharedkernel.ToolCall{ID: "first", Name: "bash", Arguments: json.RawMessage(`{"command":"pwd","timeout_ms":1000}`)}
	b := sharedkernel.ToolCall{ID: "second", Name: "bash", Arguments: json.RawMessage(`{ "timeout_ms": 1000, "command": "pwd" }`)}
	if toolCallFingerprint(a) != toolCallFingerprint(b) {
		t.Fatal("equivalent arguments should have the same fingerprint regardless of call ID and JSON order")
	}
	b.Name = "read_file"
	if toolCallFingerprint(a) == toolCallFingerprint(b) {
		t.Fatal("tool name must be part of the fingerprint")
	}
}

func TestThreeIdenticalToolCallsInterruptBeforeExecution(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("loop-guard", repo)
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(echoTool{})
	call := func(id, args string) sharedkernel.ToolCall {
		return sharedkernel.ToolCall{ID: id, Name: "echo_tool", Arguments: json.RawMessage(args)}
	}
	llm := &scriptedLLM{responses: []scriptedResp{
		{msg: assistantMsgWithTool(call("one", `{"msg":"same"}`))},
		{msg: assistantMsgWithTool(call("two", `{ "msg" : "same" }`))},
		{msg: assistantMsgWithTool(call("three", `{"msg":"same"}`), call("four", `{"msg":"other"}`))},
	}}
	svc := NewReActService(sess, repo, llm, nil, reg, nil, nil)
	_, err := svc.think(context.Background())
	if !errors.Is(err, ErrRepeatedToolCall) {
		t.Fatalf("think error = %v, want ErrRepeatedToolCall", err)
	}
	if llm.calls != 3 {
		t.Fatalf("LLM calls = %d, want 3", llm.calls)
	}
	var results []sharedkernel.Message
	for _, msg := range sess.Messages {
		if msg.Role == sharedkernel.RoleTool {
			results = append(results, msg)
		}
	}
	if len(results) != 4 || results[0].Content != "echo:same" || results[1].Content != "echo:same" ||
		!strings.Contains(results[2].Content, "中断") || !strings.Contains(results[3].Content, "未执行") {
		t.Fatalf("unexpected tool results: %+v", results)
	}
	if missing := missingToolResults(sess.Messages); len(missing) != 0 {
		t.Fatalf("interrupted tool group must have complete results: %+v", missing)
	}
}

func TestFiveConsecutiveToolCallsInjectReminder(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("tool-reminder", repo)
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(echoTool{})
	llm := &scriptedLLM{}
	for i := 0; i < 5; i++ {
		llm.responses = append(llm.responses, scriptedResp{msg: assistantMsgWithTool(sharedkernel.ToolCall{
			ID: fmt.Sprintf("call-%d", i), Name: "echo_tool",
			Arguments: json.RawMessage(fmt.Sprintf(`{"msg":"%d"}`, i)),
		})})
	}
	llm.responses = append(llm.responses, scriptedResp{msg: assistantMsg("done")})
	svc := NewReActService(sess, repo, llm, nil, reg, nil, nil)
	if _, err := svc.think(context.Background()); err != nil {
		t.Fatalf("think: %v", err)
	}
	var toolResults []sharedkernel.Message
	for _, msg := range sess.Messages {
		if msg.Role == sharedkernel.RoleTool {
			toolResults = append(toolResults, msg)
		}
	}
	if len(toolResults) != 5 || strings.Contains(toolResults[3].Content, repeatedToolReminder) ||
		!strings.Contains(toolResults[4].Content, repeatedToolReminder) {
		t.Fatalf("reminder must appear on fifth result only: %+v", toolResults)
	}
}

func TestRecentToolStreakRestoresOnlyCurrentUserTurn(t *testing.T) {
	call := func(id, name string) sharedkernel.ToolCall {
		return sharedkernel.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(`{"msg":"same"}`)}
	}
	messages := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "old task"},
		{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{call("old", "echo_tool")}},
		{Role: sharedkernel.RoleTool, ToolCallID: "old", Content: "echo:same"},
		{Role: sharedkernel.RoleUser, Content: "new task"},
		{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{call("one", "echo_tool")}},
		{Role: sharedkernel.RoleTool, ToolCallID: "one", Content: "echo:same"},
		{Role: sharedkernel.RoleAssistant, ToolCalls: []sharedkernel.ToolCall{call("missing", "echo_tool")}},
		{Role: sharedkernel.RoleTool, ToolCallID: "missing", Content: recoveryToolResultPrompt},
	}
	streak := recentToolStreak(messages)
	if streak.fingerprintCount != 1 || streak.toolCount != 1 {
		t.Fatalf("restored streak = %+v, want one completed call", streak)
	}
	streak.observe(call("two", "echo_tool"))
	if streak.fingerprintCount != 2 || streak.toolCount != 2 {
		t.Fatalf("next call must continue restored streak: %+v", streak)
	}
}

type recordingShell struct{ calls []string }

func (s *recordingShell) Run(_ context.Context, _, command string, _ time.Duration) (tools.ShellOutcome, error) {
	s.calls = append(s.calls, command)
	return tools.ShellOutcome{Output: "ran"}, nil
}
func (*recordingShell) Close() error { return nil }

func TestDangerousBashRequiresExplicitYes(t *testing.T) {
	for _, tt := range []struct {
		name, answer string
		wantCalls    int
	}{
		{"approve", "yes", 1},
		{"deny", "no", 0},
		{"ambiguous", "approve", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMemRepo()
			sess := newTestSession("bash-"+tt.name, repo)
			shell := &recordingShell{}
			reg := tools.NewDefaultRegistry(nil)
			reg.Register(tools.NewBashTool(t.TempDir(), shell, nil, sess.ID))
			var prompt string
			llm := &scriptedLLM{responses: []scriptedResp{
				{msg: assistantMsgWithTool(sharedkernel.ToolCall{ID: "bash-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf build"}`)})},
				{msg: assistantMsg("done")},
			}}
			svc := NewReActService(sess, repo, llm, nil, reg, func(event *ReactEvent) {
				if event.Type == ReActEventTypeHumanInTheLoop {
					prompt = event.Content
					event.HumanConfirmChan <- tt.answer
				}
			}, nil)
			_, err := svc.think(context.Background())
			if tt.wantCalls == 0 && !errors.Is(err, ErrDangerousCommandDeclined) || tt.wantCalls > 0 && err != nil {
				t.Fatalf("think: %v", err)
			}
			if len(shell.calls) != tt.wantCalls || !strings.Contains(prompt, "rm -rf build") || !strings.Contains(prompt, "输入 yes") {
				t.Fatalf("calls=%v prompt=%q", shell.calls, prompt)
			}
			if tt.wantCalls == 0 && !strings.Contains(sess.Messages[2].Content, "未执行") {
				t.Fatalf("denial must be visible to model: %q", sess.Messages[2].Content)
			}
			if tt.wantCalls == 0 && (sess.Messages[2].Role != sharedkernel.RoleTool || llm.calls != 1) {
				t.Fatalf("denial must persist one tool result then stop: messages=%+v calls=%d", sess.Messages, llm.calls)
			}
		})
	}
}

func TestDangerousBashWithoutConsumerFailsClosed(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("bash-no-consumer", repo)
	shell := &recordingShell{}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewBashTool(t.TempDir(), shell, nil, sess.ID))
	svc := NewReActService(sess, repo, &scriptedLLM{}, nil, reg, nil, nil)
	result, err := svc.executeToolCall(context.Background(), &sharedkernel.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf build"}`)})
	if !errors.Is(err, ErrDangerousCommandDeclined) || len(shell.calls) != 0 || !strings.Contains(result.Output, "未执行") {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, shell.calls)
	}
}

func TestDangerousBashDenialStopsRemainingToolCalls(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("bash-denial-stops-group", repo)
	shell := &recordingShell{}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewBashTool(t.TempDir(), shell, nil, sess.ID))
	reg.Register(echoTool{})
	llm := &scriptedLLM{responses: []scriptedResp{{msg: assistantMsgWithTool(
		sharedkernel.ToolCall{ID: "bash-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf build"}`)},
		sharedkernel.ToolCall{ID: "echo-2", Name: "echo_tool", Arguments: json.RawMessage(`{"msg":"later"}`)},
	)}}}
	svc := NewReActService(sess, repo, llm, nil, reg, func(event *ReactEvent) {
		if event.Type == ReActEventTypeHumanInTheLoop {
			event.HumanConfirmChan <- "no"
		}
	}, nil)
	if _, err := svc.think(context.Background()); !errors.Is(err, ErrDangerousCommandDeclined) {
		t.Fatalf("think error = %v", err)
	}
	if len(shell.calls) != 0 || llm.calls != 1 || len(sess.Messages) != 3 || sess.Messages[2].Role != sharedkernel.RoleTool ||
		sess.Messages[2].ToolCallID != "bash-1" || !strings.Contains(sess.Messages[2].DisplayContent, "未执行") {
		t.Fatalf("denial must persist one visible tool result and stop: calls=%v llm=%d messages=%+v", shell.calls, llm.calls, sess.Messages)
	}
}

func TestBashRedirectWithinWorkDirSkipsConfirmation(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("bash-redirect-ok", repo)
	shell := &recordingShell{}
	reg := tools.NewDefaultRegistry(nil)
	wd := t.TempDir()
	reg.Register(tools.NewBashTool(wd, shell, nil, sess.ID))
	svc := NewReActService(sess, repo, &scriptedLLM{}, nil, reg, nil, nil)
	svc.SetWorkDir(wd)
	result, err := svc.executeToolCall(context.Background(), &sharedkernel.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi > out.txt 2>&1"}`)})
	if err != nil || len(shell.calls) != 1 || strings.Contains(result.Output, "未执行") {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, shell.calls)
	}
}

func TestBashRedirectOutsideWorkDirRequiresConfirmation(t *testing.T) {
	repo := newMemRepo()
	sess := newTestSession("bash-redirect-out", repo)
	shell := &recordingShell{}
	reg := tools.NewDefaultRegistry(nil)
	wd := t.TempDir()
	reg.Register(tools.NewBashTool(wd, shell, nil, sess.ID))
	svc := NewReActService(sess, repo, &scriptedLLM{}, nil, reg, nil, nil)
	svc.SetWorkDir(wd)
	result, err := svc.executeToolCall(context.Background(), &sharedkernel.ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi > /etc/nope.txt"}`)})
	if !errors.Is(err, ErrDangerousCommandDeclined) || len(shell.calls) != 0 || !strings.Contains(result.Output, "未执行") {
		t.Fatalf("result=%+v err=%v calls=%v", result, err, shell.calls)
	}
}
