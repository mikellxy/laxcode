package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
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
	evaluationLocks    *sessionLocks
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
	return &server{
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
		evaluationLocks:    newSessionLocks(),
		evaluationCtx:      evaluationCtx,
		evaluationCancel:   evaluationCancel,
	}
}

// handleChat 处理 POST /chat：解析请求 → 同会话互斥 → 写 SSE 头 → 每请求装配 →
// 发 start 帧 → 跑 Chat（其间 Consumer 逐帧推 reasoning/message/tool_call）→ 发
// done/error 帧 → Cleanup。
//
// 错误分界：写 SSE 头之前的用法错误走普通 JSON + HTTP 状态码（400/409/500）；
// 一旦进入 SSE 流（响应头已发送），失败一律走 event: error 帧，状态码无法再回退。
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	chatID := uuid.NewString()
	var req chatRequest
	decodeErr := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req)
	trace := s.startChatTrace(r.Context(), req.SessionID, "send", chatID, startedAt)
	defer trace.close()
	r = r.WithContext(trace.ctx)
	if err := decodeErr; err != nil {
		trace.err = err
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "invalid request body: "+err.Error(), "")
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		trace.err = errors.New("task is required")
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "task is required", "")
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		trace.err = errors.New("session_id is required")
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "session_id is required", "")
		return
	}
	// 同会话串行：防止两个请求同时 InitSession→追加导致 history/meta 分叉。
	// 冲突返回 409 而非排队，避免客户端无感挂起。
	unlock, ok := s.locks.TryLock(req.SessionID)
	if !ok {
		trace.err = errors.New("session is busy")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "session is busy: "+req.SessionID, RetryActionResend)
		return
	}
	defer unlock()
	if s.catalog == nil {
		trace.err = errors.New("session repository is unavailable")
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "session repository is unavailable", "")
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), req.SessionID)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		trace.err = err
		writeJSONProtocolError(w, http.StatusNotFound, ErrorCodeInvalidRequest, "session not found: "+req.SessionID, "")
		return
	}
	if err != nil {
		trace.err = err
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "load session failed: "+err.Error(), "")
		return
	}
	if selected.Mode != string(agentasm.ModeCode) {
		trace.err = fmt.Errorf("unsupported session mode %q", selected.Mode)
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeInvalidRequest,
			fmt.Sprintf("session mode %q does not match server mode %q", selected.Mode, agentasm.ModeCode), "")
		return
	}

	unlockProject, ok := s.lockChatProject(selected.WorkDir)
	if !ok {
		trace.err = errors.New("project is busy")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "project is busy; wait for the other session to finish", RetryActionResend)
		return
	}
	defer unlockProject()

	// 延迟配置场景：模型未配置时在进入 SSE 流之前以 JSON 明确报错，避免
	// 装配成功后在 LLM 调用处才失败、用户只看到晦涩的上游错误。
	if s.modelMissing() {
		trace.err = errors.New("no model is configured")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeModelRequired,
			"no platform model is configured; contact the administrator", RetryActionResend)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		trace.err = errors.New("streaming unsupported")
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// 进入 SSE 流：响应头一经发送状态码即固定，此后错误只走 event 帧。
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 禁反向代理缓冲，保证逐帧下发
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := newSSEWriter(w, flusher)
	ctx := r.Context() // 客户端断开即取消，驱动 Chat 从 LLM/工具调用收敛

	assembled, err := s.assembleAgent(trace, agentasm.Input{
		WorkDir:  selected.WorkDir,
		HomeDir:  s.homeDir,
		UserRoot: s.userRoot, TempDir: s.tempDir, Repository: s.contextRepo,
		SessionID: req.SessionID,
		PlanMode:  s.planMode,
		Consumer:  trace.consumer(newEventConsumer(sw)),
	})
	if err != nil {
		trace.err = err
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResend})
		return
	}

	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	userCountBeforeChat := countMessagesByRole(assembled.Session.Messages, sharedkernel.RoleUser)
	msg, err := s.runWithFileTracking(ctx, selected.WorkDir, assembled, func() (*sharedkernel.Message, error) {
		return assembled.Service.Chat(ctx, req.Task)
	})
	trace.err = err
	if errors.Is(err, errChatFileTracking) {
		sw.Send(EventError, ErrorData{Code: ErrorCodeInternal, Message: "文件产物统计失败，本轮任务可能已执行，请检查会话历史和项目文件；未自动重试。"})
		return
	}
	if err != nil {
		retryAction := RetryActionResend
		// recoverBeforeChat 可能先补写 tool result；只有 user 数量增加才能证明
		// 本次 query 已提交，不能用总消息数判断。
		if countMessagesByRole(assembled.Session.Messages, sharedkernel.RoleUser) > userCountBeforeChat || errors.Is(err, sessionrepo.ErrWriteUncertain) {
			retryAction = RetryActionResume
		}
		sw.Send(EventError, ErrorData{Code: ErrorCodeChatFailed, Message: err.Error(), RetryAction: retryAction})
		return
	}
	sw.Send(EventDone, doneData(assembled, msg))
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

// handleResume 恢复已持久化但未收束的对话。与 /chat 共用 SSE 事件协议，但不接收
// task，也不会追加 user message。
func (s *server) handleResume(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	trace := s.startChatTrace(r.Context(), sessionID, "resume", "", startedAt)
	defer trace.close()
	r = r.WithContext(trace.ctx)
	if sessionID == "" {
		trace.err = errors.New("session_id is required")
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "session_id is required", "")
		return
	}
	unlock, ok := s.locks.TryLock(sessionID)
	if !ok {
		trace.err = errors.New("session is busy")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "session is busy: "+sessionID, RetryActionResume)
		return
	}
	defer unlock()
	if s.catalog == nil {
		trace.err = errors.New("session repository is unavailable")
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "session repository is unavailable", RetryActionResume)
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), sessionID)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		trace.err = err
		writeJSONProtocolError(w, http.StatusNotFound, ErrorCodeNothingToResume, "session not found: "+sessionID, "")
		return
	}
	if err != nil {
		trace.err = err
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "load session failed: "+err.Error(), RetryActionResume)
		return
	}
	if selected.Mode != string(agentasm.ModeCode) {
		trace.err = fmt.Errorf("unsupported session mode %q", selected.Mode)
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeInvalidRequest,
			fmt.Sprintf("session mode %q does not match server mode %q", selected.Mode, agentasm.ModeCode), "")
		return
	}

	unlockProject, ok := s.lockChatProject(selected.WorkDir)
	if !ok {
		trace.err = errors.New("project is busy")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "project is busy; wait for the other session to finish", RetryActionResume)
		return
	}
	defer unlockProject()

	if s.modelMissing() {
		trace.err = errors.New("no model is configured")
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeModelRequired,
			"no platform model is configured; contact the administrator", RetryActionResume)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		trace.err = errors.New("streaming unsupported")
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "streaming unsupported", RetryActionResume)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sw := newSSEWriter(w, flusher)
	ctx := r.Context()
	assembled, err := s.assembleAgent(trace, agentasm.Input{
		WorkDir: selected.WorkDir, HomeDir: s.homeDir, UserRoot: s.userRoot, TempDir: s.tempDir, Repository: s.contextRepo, SessionID: sessionID, PlanMode: s.planMode, Consumer: trace.consumer(newEventConsumer(sw)),
	})
	if err != nil {
		trace.err = err
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResume})
		return
	}

	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	// 恢复同一用户输入；老数据没有 chat_id 时保持缺省，不生成新的 ID。
	if chatID := latestChatID(assembled.Session.Messages); chatID != "" {
		trace.span.SetAttributes(telemetry.AttrChatID.String(chatID))
		ctx = telemetry.ContextWithChatID(ctx, chatID)
	}
	msg, err := s.runWithFileTracking(ctx, selected.WorkDir, assembled, func() (*sharedkernel.Message, error) {
		return assembled.Service.Resume(ctx)
	})
	trace.err = err
	if errors.Is(err, errChatFileTracking) {
		sw.Send(EventError, ErrorData{Code: ErrorCodeInternal, Message: "文件产物统计失败，本轮任务可能已执行，请检查会话历史和项目文件；未自动重试。"})
		return
	}
	if errors.Is(err, reactservice.ErrNothingToResume) {
		sw.Send(EventError, ErrorData{Code: ErrorCodeNothingToResume, Message: err.Error()})
		return
	}
	if err != nil {
		sw.Send(EventError, ErrorData{Code: ErrorCodeResumeFailed, Message: err.Error(), RetryAction: RetryActionResume})
		return
	}
	sw.Send(EventDone, doneData(assembled, msg))
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
