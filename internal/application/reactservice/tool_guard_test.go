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

func TestBeforeExecInfoHidesCloudUserRoot(t *testing.T) {
	const root = "/srv/laxcode/user-id"
	input := root + "/demo/a.txt " + root + "/tmp/probe.log"
	raw, err := json.Marshal(map[string]string{"msg": input})
	if err != nil {
		t.Fatal(err)
	}
	call := sharedkernel.ToolCall{ID: "echo", Name: "echo_tool", Arguments: raw}
	for _, cloud := range []bool{true, false} {
		t.Run(fmt.Sprintf("cloud=%t", cloud), func(t *testing.T) {
			repo := newMemRepo()
			sess := newTestSession("display-info", repo)
			reg := tools.NewDefaultRegistry(nil)
			reg.Register(echoTool{})
			want := "echo_tool(" + string(raw) + ")"
			if cloud {
				want = `echo_tool({"msg":"/demo/a.txt /tmp/probe.log"})`
			}
			var eventInfo string
			svc := NewReActService(sess, repo, &scriptedLLM{responses: []scriptedResp{
				{msg: assistantMsgWithTool(call)}, {msg: assistantMsg("done")},
			}}, nil, reg, func(event *ReactEvent) {
				if event.Type == ReActEventTypeToolCall {
					eventInfo = event.Content
				}
			}, nil)
			if cloud {
				svc.SetUserRoot(root)
			}
			if _, err := svc.Chat(context.Background(), "echo paths"); err != nil {
				t.Fatal(err)
			}
			if eventInfo != want {
				t.Fatalf("tool event = %q, want %q", eventInfo, want)
			}
			var result *sharedkernel.Message
			for i := range repo.msgs[sess.ID] {
				msg := &repo.msgs[sess.ID][i]
				if msg.Role == sharedkernel.RoleTool {
					result = msg
				}
				if len(msg.ToolCalls) > 0 && string(msg.ToolCalls[0].Arguments) != string(raw) {
					t.Fatal("tool arguments changed")
				}
			}
			if result == nil || result.DisplayContent != want || result.Content != "echo:"+input {
				t.Fatalf("persisted tool message = %+v", result)
			}
		})
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
	_, err := svc.think(context.Background(), "")
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
	if _, err := svc.think(context.Background(), ""); err != nil {
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

type sandboxedRecordingShell struct{ recordingShell }

func (*sandboxedRecordingShell) BashSandboxed() bool { return true }

func TestCloudDockerBashUsesSandboxInsteadOfStaticPathGuessing(t *testing.T) {
	repo := newMemRepo()
	runner := &sandboxedRecordingShell{}
	reg := tools.NewDefaultRegistry(nil)
	reg.Register(tools.NewBashTool("/users/demo", runner, nil, "sandbox"))
	svc := NewReActService(newTestSession("sandbox", repo), repo, &scriptedLLM{}, nil, reg, nil, nil)
	svc.SetUserRoot("/users")
	result, err := svc.executeToolCall(context.Background(), &sharedkernel.ToolCall{ID: "run", Name: tools.ToolBash, Arguments: json.RawMessage(`{"command":"printf 'log' > \"$TMPDIR/server.log\"; kill \"$pid\""}`)})
	if err != nil || result.IsError || len(runner.calls) != 1 {
		t.Fatalf("sandboxed command not dispatched: %+v %v", result, err)
	}
}

func (s *recordingShell) Run(_ context.Context, _, command string, _ time.Duration) (tools.ShellOutcome, error) {
	s.calls = append(s.calls, command)
	return tools.ShellOutcome{Output: "ran"}, nil
}
func (*recordingShell) Close() error { return nil }
