package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestBashNativeTimeout(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		want       time.Duration
		invalid    bool
	}{
		{"default", `{"command":"cmd"}`, 30 * time.Second, false},
		{"short", `{"command":"cmd","timeout_ms":125}`, 125 * time.Millisecond, false},
		{"limit", `{"command":"cmd","timeout_ms":60000}`, 60 * time.Second, false},
		{"clamped", `{"command":"cmd","timeout_ms":90000}`, 60 * time.Second, false},
		{"overflow", `{"command":"cmd","timeout_ms":9223372036854775807}`, 60 * time.Second, false},
		{"zero", `{"command":"cmd","timeout_ms":0}`, 0, true},
		{"negative", `{"command":"cmd","timeout_ms":-1}`, 0, true},
		{"fraction", `{"command":"cmd","timeout_ms":1.5}`, 0, true},
		{"string", `{"command":"cmd","timeout_ms":"100"}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{}
			b := newTestBashTool(t, runner)
			_, err := b.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.invalid {
				if err == nil || runner.gotCommand != "" {
					t.Fatalf("invalid args executed: err=%v runner=%+v", err, runner)
				}
				return
			}
			if err != nil || runner.gotTimeout != tc.want {
				t.Fatalf("timeout=%v want=%v err=%v", runner.gotTimeout, tc.want, err)
			}
			if b.BeforeExecInfo(json.RawMessage(tc.args)) != "bash(cmd)" {
				t.Fatal("timeout parameter broke execution label")
			}
		})
	}
	b := newTestBashTool(t, &fakeRunner{})
	b.Timeout = 2 * time.Minute
	if b.timeout() != time.Minute {
		t.Fatal("configured timeout bypassed cap")
	}
}

func TestBashArchivesBeforeTruncationAndTransfersMetadata(t *testing.T) {
	full := strings.Repeat("中", 9000) + "TAIL"
	store := &bashArtifactStore{}
	b := NewBashTool(t.TempDir(), &fakeRunner{outcome: ShellOutcome{Output: full, ExitCode: 7, ExitErr: "exit status 7"}}, store, "session")
	reg := NewDefaultRegistry(nil)
	reg.Register(b)
	result := reg.Execute(context.Background(), &sharedkernel.ToolCall{ID: "c1", Name: ToolBash, Arguments: json.RawMessage(`{"command":"cmd"}`)})
	if result.IsError || result.Error != nil {
		t.Fatalf("nonzero exit is not a tool error: %+v", result)
	}
	if store.puts != 1 || store.content != full || result.Artifact == nil || result.Artifact.ByteSize != len(full) {
		t.Fatal("original output was not archived before truncation")
	}
	if strings.Contains(result.Output, "TAIL") || !strings.Contains(result.Output, result.Artifact.ID) {
		t.Fatal("preview or artifact hint incorrect")
	}
	if len([]rune(bashPreview(result.Output))) != 8000 {
		t.Fatal("preview exceeds rune budget")
	}
	for _, fact := range []string{"exit_code:7", "status:completed", "stdout_truncated:true"} {
		if !strings.Contains(result.CompactContent, fact) {
			t.Fatalf("compact result missing %q", fact)
		}
	}
	msg := ToolResultAsMsg(result)
	if msg.CompactContent != result.CompactContent || msg.Artifact == nil || *msg.Artifact != *result.Artifact || msg.ToolCallID != "c1" {
		t.Fatal("metadata lost at message conversion")
	}
	msg.Artifact.ID = "changed"
	if result.Artifact.ID == "changed" {
		t.Fatal("artifact pointer aliased")
	}
}

func TestBashTimeoutAndCancellationKeepOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		status string
	}{
		{"timeout", context.DeadlineExceeded, "timed_out"},
		{"canceled", context.Canceled, "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full := "diagnostic\n" + strings.Repeat("x", 9000) + "tail"
			store := &bashArtifactStore{}
			b := NewBashTool(t.TempDir(), &fakeRunner{outcome: ShellOutcome{Output: full}, err: tc.cause}, store, "session")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			reg := NewDefaultRegistry(nil)
			reg.Register(b)
			res := reg.Execute(ctx, &sharedkernel.ToolCall{ID: "timeout", Name: ToolBash, Arguments: json.RawMessage(`{"command":"cmd"}`)})
			if !res.IsError || res.Error == nil || !strings.Contains(res.Output, "diagnostic") {
				t.Fatalf("timeout output lost: %+v", res)
			}
			if store.content != full || res.Artifact == nil {
				t.Fatal("canceled context prevented archiving")
			}
			if !strings.Contains(res.CompactContent, "status:"+tc.status) || !strings.Contains(res.CompactContent, "exit_code:unknown") {
				t.Fatalf("incorrect compact status: %s", res.CompactContent)
			}
		})
	}
	b := newTestBashTool(t, &fakeRunner{outcome: ShellOutcome{Output: "short diagnostic"}, err: context.DeadlineExceeded})
	res := b.ExecuteResult(context.Background(), json.RawMessage(`{"command":"cmd"}`))
	if res.Error == nil || !strings.Contains(res.Output, "short diagnostic") || res.Artifact != nil {
		t.Fatal("short partial output not preserved")
	}
}

func TestBashArchiveFailureIsVisible(t *testing.T) {
	store := &bashArtifactStore{err: errors.New("disk full")}
	b := NewBashTool(t.TempDir(), &fakeRunner{outcome: ShellOutcome{Output: strings.Repeat("x", 9000)}}, store, "session")
	res := b.ExecuteResult(context.Background(), json.RawMessage(`{"command":"cmd"}`))
	if res.Error == nil || res.Artifact != nil || strings.Contains(res.Output, "read_artifact(") || !strings.Contains(res.CompactContent, "完整输出未归档") {
		t.Fatalf("archive failure hidden: %+v", res)
	}
	if !strings.Contains(res.Error.Error(), "disk full") {
		t.Fatal("archive error lost")
	}
}

func TestBashShortOutputDefersArchiving(t *testing.T) {
	store := &bashArtifactStore{}
	b := NewBashTool(t.TempDir(), &fakeRunner{outcome: ShellOutcome{Output: "short"}}, store, "session")
	res := b.ExecuteResult(context.Background(), json.RawMessage(`{"command":"cmd"}`))
	if res.Error != nil || res.Artifact != nil || store.puts != 0 {
		t.Fatalf("short output should not archive yet: %+v", res)
	}
}
