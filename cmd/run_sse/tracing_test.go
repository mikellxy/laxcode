package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

type traceFlowLLM struct {
	approvalFlowLLM
	output bool
}

func (l *traceFlowLLM) GenerateStream(_ context.Context, _ []sharedkernel.Message, _ []sharedkernel.ToolDefinition, emit func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	l.calls++
	emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextStart})
	emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta})
	emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkToolCall})
	if l.calls != 1 || l.output {
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkReasoningDelta, Delta: "thinking"})
		emit(sharedkernel.StreamChunk{Kind: sharedkernel.ChunkTextDelta, Delta: "answer"})
	}
	if l.calls == 1 {
		return nil, errors.New("stream interrupted")
	}
	return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "answer", FinishReason: sharedkernel.FinishReasonStop}, nil
}

type chatSpanRecord struct {
	TraceID string         `json:"trace_id"`
	SpanID  string         `json:"span_id"`
	Parent  string         `json:"parent_span_id"`
	Name    string         `json:"name"`
	Start   time.Time      `json:"start_time"`
	End     time.Time      `json:"end_time"`
	Attrs   map[string]any `json:"attributes"`
	Status  string         `json:"status_code"`
	Events  []struct {
		Name string `json:"name"`
	} `json:"events"`
}

// 覆盖首输出后失败、无输出失败、resume、再次输入，以及两种 tracer 生命周期。
func TestChatAndResumeTraceHierarchy(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, output := range []bool{false, true} {
			name := "file"
			if shared {
				name = "shared"
			}
			if output {
				name += "-output"
			}
			t.Run(name, func(t *testing.T) {
				stubActiveModel(t)
				home, work := t.TempDir(), t.TempDir()
				s := newServer(home, false)
				s.catalog = catalogWithSession("trace-session", work)
				logPath := layout.TracingLog(home, "trace-session")
				if shared {
					provider, err := filetrace.New(logPath)
					if err != nil {
						t.Fatal(err)
					}
					handle := tracing.New(provider)
					s.tracer = handle.Tracer
					defer handle.Shutdown(context.Background())
				}
				llm := &traceFlowLLM{output: output}
				s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
					repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(home), layout.SessionRoot(home))
					if err != nil {
						return nil, err
					}
					sess := session.NewSession(in.SessionID, work)
					svc := reactservice.NewReActService(sess, repo, llm, nil, tools.NewDefaultRegistry(in.Tracer), in.Consumer, in.Tracer)
					if err := svc.InitSession(ctx); err != nil {
						repo.Close()
						return nil, err
					}
					if err := svc.InitSysPrompt(ctx, "system"); err != nil {
						repo.Close()
						return nil, err
					}
					return &agentasm.Assembled{Session: sess, Service: svc, Cleanup: func() { repo.Close() }}, nil
				}
				mux := http.NewServeMux()
				mux.HandleFunc("POST /chat", s.handleChat)
				mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
				for i, path := range []string{"/chat", "/api/sessions/trace-session/resume", "/chat"} {
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"session_id":"trace-session","task":"question"}`)))
					want := "event: done"
					if i == 0 {
						want = "event: error"
					}
					if !strings.Contains(response.Body.String(), want) {
						t.Fatalf("request %d: %s", i, response.Body.String())
					}
				}
				data, err := os.ReadFile(logPath)
				if err != nil {
					t.Fatal(err)
				}
				var roots []chatSpanRecord
				records := make(map[string]chatSpanRecord)
				for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					var record chatSpanRecord
					if err := json.Unmarshal([]byte(line), &record); err != nil {
						t.Fatal(err)
					}
					records[record.SpanID] = record
					if record.Name == "chat" {
						roots = append(roots, record)
					}
					if _, ok := record.Attrs["laxcode.time_cost_ms"]; ok {
						t.Fatal("redundant span duration remains")
					}
					if record.Name != "chat" && record.Attrs["laxcode.chat_id"] != nil {
						t.Fatal("chat_id repeated on child span")
					}
				}
				if len(roots) != 3 || len(records) != 15 {
					t.Fatalf("roots=%d records=%d", len(roots), len(records))
				}
				if roots[0].Attrs["laxcode.chat_id"] == nil || roots[0].Attrs["laxcode.chat_id"] != roots[1].Attrs["laxcode.chat_id"] || roots[0].Attrs["laxcode.chat_id"] == roots[2].Attrs["laxcode.chat_id"] {
					t.Fatal("send/resume chat IDs do not identify the same user input")
				}
				if roots[0].Status != "Error" || roots[1].Status != "" || roots[2].Status != "" {
					t.Fatal("request error status incorrect")
				}
				if roots[1].Attrs["laxcode.operation"] != "resume" || roots[0].Attrs["laxcode.request_id"] == roots[1].Attrs["laxcode.request_id"] {
					t.Fatal("request identity incorrect")
				}
				for _, record := range records {
					if record.Name == "chat" {
						continue
					}
					parent := records[record.Parent]
					want := "react"
					if record.Name == "react" || record.Name == "agent-assemble" {
						want = "chat"
					}
					if parent.Name != want || record.TraceID != parent.TraceID || record.Start.Before(parent.Start) || record.End.After(parent.End) {
						t.Fatalf("invalid hierarchy: %+v parent=%+v", record, parent)
					}
					if record.Name == "llm-generate" {
						first := record.Status == "Error"
						_, hasTTFT := record.Attrs["laxcode.ttft_ms"]
						if hasTTFT != (!first || output) {
							t.Fatalf("wrong TTFT: %+v", record)
						}
					}
				}
				for i, root := range roots {
					count := 0
					for _, event := range root.Events {
						if event.Name == "first-output" {
							count++
						}
					}
					want := 1
					if i == 0 && !output {
						want = 0
					}
					_, hasTime := root.Attrs["laxcode.first_output_ms"]
					if count != want || hasTime != (want == 1) {
						t.Fatalf("first output recorded incorrectly: %+v", root)
					}
				}
				history, err := os.ReadFile(filepath.Join(layout.SessionDir(home, "trace-session"), "history.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				var users []sharedkernel.Message
				for _, line := range strings.Split(strings.TrimSpace(string(history)), "\n") {
					var msg sharedkernel.Message
					if err := json.Unmarshal([]byte(line), &msg); err != nil {
						t.Fatal(err)
					}
					if msg.Role == sharedkernel.RoleUser {
						users = append(users, msg)
					}
				}
				if len(users) != 2 || users[0].ChatID != roots[0].Attrs["laxcode.chat_id"] || users[1].ChatID != roots[2].Attrs["laxcode.chat_id"] {
					t.Fatalf("persisted user IDs incorrect: %+v", users)
				}
			})
		}
	}
}
