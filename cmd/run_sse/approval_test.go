package run_sse

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

type approvalFlowLLM struct{ calls int }

func (l *approvalFlowLLM) Generate(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (*sharedkernel.Message, error) {
	l.calls++
	if l.calls == 1 {
		return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, TokenUsed: sharedkernel.TokenStatistics{TokenInput: 20}, ToolCalls: []sharedkernel.ToolCall{{ID: "bash-1", Name: "bash", Arguments: json.RawMessage(`{"command":"rm -rf build"}`)}}}, nil
	}
	return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "finished", TokenUsed: sharedkernel.TokenStatistics{TokenInput: 25}}, nil
}

func (l *approvalFlowLLM) GenerateStream(ctx context.Context, msgs []sharedkernel.Message, defs []sharedkernel.ToolDefinition, _ func(sharedkernel.StreamChunk)) (*sharedkernel.Message, error) {
	return l.Generate(ctx, msgs, defs)
}

func (*approvalFlowLLM) CountInputTokens(context.Context, []sharedkernel.Message, []sharedkernel.ToolDefinition) (int, error) {
	return 10, nil
}

func (*approvalFlowLLM) ContextBudget() llmprovider.ContextBudget {
	return llmprovider.ContextBudget{ContextWindow: 1000, ReservedOutputTokens: 100}
}

type notifyingResponse struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	updates chan string
}

func newNotifyingResponse() *notifyingResponse {
	return &notifyingResponse{header: make(http.Header), updates: make(chan string, 16)}
}

func (w *notifyingResponse) Header() http.Header { return w.header }
func (w *notifyingResponse) WriteHeader(int)     {}
func (w *notifyingResponse) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}
func (w *notifyingResponse) Flush() {
	w.mu.Lock()
	snapshot := w.body.String()
	w.mu.Unlock()
	select {
	case w.updates <- snapshot:
	default:
	}
}

func TestChatStreamWaitsForHTTPApproval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		approved bool
		want     string
	}{
		{name: "continue", approved: true, want: "event: done"},
		{name: "stop", approved: false, want: `"code":"CHAT_STOPPED"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubActiveModel(t)
			workDir := t.TempDir()
			sessionHome := t.TempDir()
			dbPath := filepath.Join(workDir, "sessions.db")
			historyRoot := filepath.Join(workDir, "history")
			s := newServer(sessionHome, false)
			s.catalog = catalogWithSession("flow", workDir)
			s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
				repo, err := sessionrepo.NewSqliteSessionRepo(dbPath, historyRoot)
				if err != nil {
					return nil, err
				}
				sess := session.NewSession(in.SessionID)
				svc := reactservice.NewReActService(sess, repo, &approvalFlowLLM{}, nil, tools.NewDefaultRegistry(nil), in.Consumer, nil)
				svc.SetWorkDir(workDir)
				if err := svc.InitSession(ctx); err != nil {
					_ = repo.Close()
					return nil, err
				}
				if err := svc.InitSysPrompt(ctx, "system"); err != nil {
					_ = repo.Close()
					return nil, err
				}
				return &agentasm.Assembled{Service: svc, Session: sess, Cleanup: func() { _ = repo.Close() }}, nil
			}
			stream := newNotifyingResponse()
			finished := make(chan struct{})
			go func() {
				s.handleChat(stream, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"flow","task":"work"}`)))
				close(finished)
			}()
			var frame string
			deadline := time.After(3 * time.Second)
			for !strings.Contains(frame, "event: approval_required") {
				select {
				case frame = <-stream.updates:
				case <-deadline:
					t.Fatal("approval event was not flushed")
				}
			}
			var approvalID string
			s.approvals.mu.Lock()
			for id := range s.approvals.pending {
				approvalID = id
			}
			s.approvals.mu.Unlock()
			if approvalID == "" {
				t.Fatal("approval not registered")
			}
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/sessions/{session_id}/approvals/{approval_id}", s.handleApproval)
			body := `{"approved":false}`
			if tc.approved {
				body = `{"approved":true}`
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/sessions/flow/approvals/"+approvalID, strings.NewReader(body)))
			if response.Code != http.StatusNoContent {
				t.Fatalf("approval status=%d body=%s", response.Code, response.Body.String())
			}
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("stream did not finish after approval")
			}
			stream.mu.Lock()
			output := stream.body.String()
			stream.mu.Unlock()
			if !strings.Contains(output, tc.want) {
				t.Fatalf("stream = %q, want %q", output, tc.want)
			}
			if tc.approved && !strings.Contains(output, `"context_window":1000`) {
				t.Fatalf("done event missing context window: %q", output)
			}
			if !tc.approved {
				repo, err := sessionrepo.NewSqliteSessionRepo(dbPath, historyRoot)
				if err != nil {
					t.Fatal(err)
				}
				defer repo.Close()
				state, err := repo.GetRequestContext(context.Background(), "flow")
				if err != nil || state.Messages[len(state.Messages)-1].Role != sharedkernel.RoleTool ||
					!strings.Contains(state.Messages[len(state.Messages)-1].Content, "未执行") {
					t.Fatalf("persisted denial = %+v err=%v", state.Messages, err)
				}
			}
		})
	}
}

func TestApprovalEventAndHTTPReplyResumeSameStream(t *testing.T) {
	s := newServer(t.TempDir(), false)
	rf := newRecordFlusher()
	sessionID := "session-1"
	consumer := s.eventConsumer(newSSEWriter(rf, rf), &sessionID, "request-1")
	reply := make(chan string, 1)
	consumer(&reactservice.ReactEvent{
		Type: reactservice.ReActEventTypeHumanInTheLoop, Content: "approve?",
		HumanConfirmKind: reactservice.HumanConfirmKindBashCommand, HumanConfirmChan: reply,
	})
	if !strings.Contains(rf.buf.String(), "event: approval_required") || !strings.Contains(rf.buf.String(), `"kind":"bash_command"`) {
		t.Fatalf("approval frame = %q", rf.buf.String())
	}
	var id string
	s.approvals.mu.Lock()
	for key := range s.approvals.pending {
		id = key
	}
	s.approvals.mu.Unlock()
	if id == "" {
		t.Fatal("missing pending approval")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{session_id}/approvals/{approval_id}", s.handleApproval)
	wrong := httptest.NewRecorder()
	mux.ServeHTTP(wrong, httptest.NewRequest(http.MethodPost, "/api/sessions/other/approvals/"+id, strings.NewReader(`{"approved":true}`)))
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("wrong session status = %d", wrong.Code)
	}
	accepted := httptest.NewRecorder()
	mux.ServeHTTP(accepted, httptest.NewRequest(http.MethodPost, "/api/sessions/session-1/approvals/"+id, strings.NewReader(`{"approved":true}`)))
	if accepted.Code != http.StatusNoContent || <-reply != "yes" {
		t.Fatalf("approval status = %d", accepted.Code)
	}
	repeated := httptest.NewRecorder()
	mux.ServeHTTP(repeated, httptest.NewRequest(http.MethodPost, "/api/sessions/session-1/approvals/"+id, strings.NewReader(`{"approved":true}`)))
	if repeated.Code != http.StatusNotFound {
		t.Fatalf("repeated approval status = %d", repeated.Code)
	}
}

func TestApprovalIsRemovedWhenStreamEnds(t *testing.T) {
	broker := newApprovalBroker()
	id := broker.register("session-1", "request-1", make(chan string, 1))
	broker.clearRequest("request-1")
	if broker.resolve("session-1", id, true) {
		t.Fatal("approval from a closed stream must not be accepted")
	}
}

func TestBudgetStateContinuesAcrossSSERequests(t *testing.T) {
	s := newServer(t.TempDir(), false)
	s.codeMode, s.tokenBudget = true, 100
	firstSession := session.NewSession("budget-session")
	firstSession.TokenUsed = sharedkernel.TokenStatistics{TokenInput: 200}
	firstService := reactservice.NewReActService(firstSession, nil, nil, nil, nil, nil, nil)
	first := &agentasm.Assembled{Service: firstService, Session: firstSession}
	s.configureBudget(first)
	firstSession.TokenUsed.TokenInput = 260
	s.saveBudget(first)

	nextSession := session.NewSession(firstSession.ID)
	nextSession.TokenUsed.TokenInput = 260
	nextService := reactservice.NewReActService(nextSession, nil, nil, nil, nil, nil, nil)
	s.configureBudget(&agentasm.Assembled{Service: nextService, Session: nextSession})
	state := nextService.TokenBudgetState()
	if state.Baseline != 200 || state.NextThreshold != 100 || state.Budget != 100 {
		t.Fatalf("restored budget state = %+v", state)
	}
}
