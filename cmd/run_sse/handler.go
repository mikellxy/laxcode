package run_sse

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/taskevents"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
)

// chatRequest 是 POST /chat 的请求体：session_id 与 task 均必填。
type chatRequest struct {
	SessionID string `json:"session_id"`
	Task      string `json:"task"`
}

// maxBodyBytes 限制请求体大小，防止超大 body 耗尽内存。
const maxBodyBytes = 1 << 20

// server 承载 sse 模式的 HTTP 编排。assemble 字段默认 agentasm.Assemble，测试可
// 注入 fake 以覆盖装配失败 / 完整流路径而不依赖真实 LLM provider。
type server struct {
	taskMu             sync.Mutex
	tasks              session.TaskRepository
	events             *taskevents.Log
	taskCtx            context.Context
	taskCancel         context.CancelCauseFunc
	taskWG             sync.WaitGroup
	taskCancels        map[string]context.CancelCauseFunc
	activeTasks        atomic.Int32
	userID             string
	tracer             telemetry.Tracer
	userRoot           string
	tempDir            string
	homeDir            string
	planMode           bool
	assemble           func(context.Context, agentasm.Input) (*agentasm.Assembled, error)
	assembleEvaluation func(context.Context, agentasm.Input) (*agentasm.Assembled, error)
	locks              *sessionLocks
	projectLocks       *sessionLocks
	history            session.SessionHistoryRepository
	contextRepo        session.SessionRepository
	catalog            session.SessionCatalogRepository
	projects           session.ProjectRepository
	evaluations        evaluation.Repository
	evaluationCtx      context.Context
	evaluationCancel   context.CancelFunc
	evaluationWG       sync.WaitGroup
	activeEvaluations  atomic.Int32
	models             *ai_models.Manager
}

func parseUserID(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func (s *server) modelMissing() bool {
	return strings.TrimSpace(s.models.Active().Ref) == ""
}

func newServer(homeDir string, planMode bool, models *ai_models.Manager) *server {
	evaluationCtx, evaluationCancel := context.WithCancel(context.Background())
	taskCtx, taskCancel := context.WithCancelCause(context.Background())
	return &server{
		taskCtx: taskCtx, taskCancel: taskCancel, taskCancels: make(map[string]context.CancelCauseFunc),
		models:   models,
		homeDir:  homeDir,
		planMode: planMode,
		assemble: func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
			in.Mode = agentasm.ModeCode
			return agentasm.Assemble(ctx, in)
		},
		assembleEvaluation: agentasm.Assemble,
		locks:              newSessionLocks(),
		projectLocks:       newSessionLocks(),
		evaluationCtx:      evaluationCtx,
		evaluationCancel:   evaluationCancel,
	}
}

func countMessagesByRole(messages []sharedkernel.Message, role string) int {
	count := 0
	for i := range messages {
		if messages[i].Role == role {
			count++
		}
	}
	return count
}

func doneData(assembled *agentasm.Assembled, msg *sharedkernel.Message) DoneData {
	done := DoneData{
		SessionID:     assembled.Session.ID,
		TokenUsed:     assembled.Session.TokenUsed,
		WindowToken:   assembled.Session.WindowToken,
		ContextWindow: assembled.Service.LLMClient.ContextBudget().ContextWindow,
	}
	if msg != nil {
		done.Result = msg.Content
	}
	return done
}

// handleHealthz 是探活端点：返回 200，供负载均衡 / 容器健康检查，不触发装配。
func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// writeJSONError 写一个普通 JSON 错误响应，仅用于 SSE 流开始之前的用法错误
// （此时响应头未发送，可自由设置状态码）。载荷与 error 帧同为 {message}。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSONProtocolError(w, status, ErrorCodeInternal, msg, "")
}

func writeJSONProtocolError(w http.ResponseWriter, status int, code, msg, retryAction string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorData{Code: code, Message: msg, RetryAction: retryAction})
}

// sessionLocks 是 per-session 互斥锁表：同一 session_id 串行、不同 session 并发。
type sessionLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newSessionLocks() *sessionLocks {
	return &sessionLocks{m: make(map[string]*sync.Mutex)}
}

// TryLock 尝试锁定 id：成功返回 unlock 与 true；已被占用则立即返回 nil 与 false
// （不排队）。锁惰性创建，进程生命周期内不回收（session 数量有限，可接受）。
func (s *sessionLocks) TryLock(id string) (func(), bool) {
	s.mu.Lock()
	l, ok := s.m[id]
	if !ok {
		l = &sync.Mutex{}
		s.m[id] = l
	}
	s.mu.Unlock()

	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}
