package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

// chatRequest 是 POST /chat 的请求体：session_id 为空则新建会话，task 必填非空。
type chatRequest struct {
	SessionID string `json:"session_id"`
	Task      string `json:"task"`
}

// maxBodyBytes 限制请求体大小，防止超大 body 耗尽内存。
const maxBodyBytes = 1 << 20

// server 承载 sse 模式的 HTTP 编排。assemble 字段默认 agentasm.Assemble，测试可
// 注入 fake 以覆盖装配失败 / 完整流路径而不依赖真实 LLM provider。
type server struct {
	workDir  string
	planMode bool
	assemble func(context.Context, agentasm.Input) (*agentasm.Assembled, error)
	locks    *sessionLocks
	history  session.SessionHistoryRepository
	catalog  session.SessionCatalogRepository
	// router 是 main 启动的本地 LLM 路由器：流式生成全部经它转发，模型切换
	// 须替换其上游 client。switchMu 串行化切换，避免并发切换使路由器与运行时
	// 配置交错成不一致组合。
	router   agentasm.RouterClientReplacer
	switchMu sync.Mutex
}

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 100
	defaultSessionLimit = 20
	maxSessionLimit     = 100
)

type createSessionRequest struct {
	UserID string `json:"user_id"`
}

type sessionDTO struct {
	SessionID string    `json:"session_id"`
	UserID    string    `json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type sessionPageDTO struct {
	Sessions            []sessionDTO `json:"sessions"`
	NextBeforeSessionID string       `json:"next_before_session_id,omitempty"`
	HasMore             bool         `json:"has_more"`
}

func parseUserID(raw string) (string, error) {
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func sessionToDTO(item session.Summary) sessionDTO {
	return sessionDTO{
		SessionID: item.ID, UserID: item.UserID, Title: item.Title,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (s *server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	var req createSessionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	userID, err := parseUserID(req.UserID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	created, err := s.catalog.CreateSession(r.Context(), uuid.NewString(), userID, "")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "create session failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(sessionToDTO(created))
}

func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	userID, err := parseUserID(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	limit := defaultSessionLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, parseErr := strconv.Atoi(raw)
		if parseErr != nil || value < 1 || value > maxSessionLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	beforeSessionID := r.URL.Query().Get("before_session_id")
	page, err := s.catalog.ListSessions(r.Context(), userID, beforeSessionID, limit)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONError(w, http.StatusBadRequest, "invalid session cursor")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list sessions failed: "+err.Error())
		return
	}
	response := sessionPageDTO{Sessions: make([]sessionDTO, len(page.Sessions)), HasMore: page.HasMore}
	for i := range page.Sessions {
		response.Sessions[i] = sessionToDTO(page.Sessions[i])
	}
	if page.HasMore && len(page.Sessions) > 0 {
		response.NextBeforeSessionID = page.Sessions[len(page.Sessions)-1].ID
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

type historyMessageDTO struct {
	Seq              uint64    `json:"seq"`
	Role             string    `json:"role"`
	Content          string    `json:"content,omitempty"`
	ReasoningContent string    `json:"reasoning_content,omitempty"`
	ToolSummary      string    `json:"tool_summary,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// modelConfigDTO / providerModelsDTO / providerListModelDTO 是 GET /api/models
// 的响应载体：只回传模型目录中的模型清单；OpenaiApiKey / OpenaiBaseUrl 属隐私
// 配置不外发。ModelRef 是 provider:model 引用，前端凭它构造 POST /api/model
// 的切换请求。CurrentModel 是运行时生效的引用，即当前（及后续每个请求）LLM
// client 实际使用的模型。
type modelConfigDTO struct {
	ModelName     string `json:"model_name"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	ModelRef      string `json:"model_ref"`
}

type providerModelsDTO struct {
	ModelList []modelConfigDTO `json:"model_list"`
}

type providerListModelDTO struct {
	CurrentModel string              `json:"current_model"`
	Providers    []providerModelsDTO `json:"providers"`
}

// handleListModels 处理 GET /api/models：返回 EnvAndFileConf.ProviderList 的
// 脱敏视图（各 provider 的 ModelList）与当前生效模型引用，供客户端做模型
// 选择、切换与展示，不暴露凭据与端点。
func (s *server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	providers := config.EnvAndFileConf.ProviderList
	response := providerListModelDTO{
		CurrentModel: config.EnvAndFileConf.Model,
		Providers:    make([]providerModelsDTO, len(providers)),
	}
	for i, provider := range providers {
		models := make([]modelConfigDTO, len(provider.ModelList))
		for j, model := range provider.ModelList {
			models[j] = modelConfigDTO{
				ModelName:     model.ModelName,
				UpstreamModel: model.UpstreamModel,
				ModelRef:      provider.ProviderName + ":" + model.ModelName,
			}
		}
		response.Providers[i] = providerModelsDTO{ModelList: models}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// switchModelRequest 是 POST /api/model 的请求体：provider 与 model 拼成
// provider:model 引用，二者均必填。
type switchModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleSwitchModel 处理 POST /api/model：按 provider + model 组合引用后，经
// agentasm.SwitchRouterModel（两处切换共用的核心，Assembled.SwitchModel 亦
// 复用它）完成切换——替换本地 LLM 路由器的上游 client（SSE 流式流量全经
// 路由器，凭据与模型名都在其侧），再写回运行时配置；此后每个请求的按次装配
// 自然以新配置（含模型级 limit 预算）构建 provider。切换只在无进行中 Chat 时
// 原子生效，与 TUI 的约束一致；在途请求按装配快照继续使用旧模型。
func (s *server) handleSwitchModel(w http.ResponseWriter, r *http.Request) {
	var req switchModelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	provider, model := strings.TrimSpace(req.Provider), strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		writeJSONError(w, http.StatusBadRequest, "provider and model are required")
		return
	}
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	if s.router == nil {
		writeJSONError(w, http.StatusInternalServerError, "model switching requires a running LLM router")
		return
	}
	if err := agentasm.SwitchRouterModel(s.router, provider+":"+model); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"model_ref": provider + ":" + model})
}

type historyPageDTO struct {
	Messages      []historyMessageDTO `json:"messages"`
	NextBeforeSeq uint64              `json:"next_before_seq,omitempty"`
	HasMore       bool                `json:"has_more"`
}

// handleHistory 处理 GET /api/sessions/{session_id}/messages。
// before_seq 是排他的消息游标，省略时从最新消息开始；响应消息始终按 seq 正序。
func (s *server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.history == nil {
		writeJSONError(w, http.StatusInternalServerError, "history repository is unavailable")
		return
	}
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	limit := defaultHistoryLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxHistoryLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	var beforeSeq uint64
	if raw := r.URL.Query().Get("before_seq"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			writeJSONError(w, http.StatusBadRequest, "before_seq must be a positive integer")
			return
		}
		beforeSeq = value
	}

	page, found, err := s.history.ListOriginalHistory(r.Context(), sessionID, beforeSeq, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load history failed: "+err.Error())
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "session not found: "+sessionID)
		return
	}

	response := historyPageDTO{Messages: make([]historyMessageDTO, len(page.Messages)), HasMore: page.HasMore}
	for i, msg := range page.Messages {
		response.Messages[i] = historyMessageDTO{
			Seq: msg.Seq, Role: msg.Role, Content: msg.Content,
			ReasoningContent: msg.ReasoningContent, ToolSummary: msg.ToolSummary,
			CreatedAt: msg.CreatedAt,
		}
	}
	if page.HasMore && len(page.Messages) > 0 {
		response.NextBeforeSeq = page.Messages[0].Seq
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func newServer(workDir string, planMode bool) *server {
	return &server{
		workDir:  workDir,
		planMode: planMode,
		assemble: func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
			return agentasm.AssembleSSE(ctx, in)
		},
		locks: newSessionLocks(),
	}
}

type qaAssembler func(context.Context, agentasm.QAInput) (*agentasm.QAAssembled, error)

// useQAAssembly switches the HTTP transport to the same knowledge-base QA
// composition root used by cmd/run_qa. The adapter only reconciles the two
// command-layer result types; the ReAct service, prompt, retriever, and empty
// tool registry all come from agentasm.AssembleQA.
func (s *server) useQAAssembly(kbPath string, assembleQA qaAssembler) {
	s.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		assembled, err := assembleQA(ctx, agentasm.QAInput{
			KBPath:    kbPath,
			WorkDir:   in.WorkDir,
			SessionID: in.SessionID,
			Consumer:  in.Consumer,
		})
		if err != nil {
			return nil, err
		}
		return &agentasm.Assembled{
			Service: assembled.Service,
			Session: assembled.Session,
			Cleanup: assembled.Cleanup,
		}, nil
	}
}

// handleChat 处理 POST /chat：解析请求 → 同会话互斥 → 写 SSE 头 → 每请求装配 →
// 发 start 帧 → 跑 Chat（其间 Consumer 逐帧推 reasoning/message/tool_call）→ 发
// done/error 帧 → Cleanup。
//
// 错误分界：写 SSE 头之前的用法错误走普通 JSON + HTTP 状态码（400/409/500）；
// 一旦进入 SSE 流（响应头已发送），失败一律走 event: error 帧，状态码无法再回退。
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "invalid request body: "+err.Error(), "")
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "task is required", "")
		return
	}

	// 同会话串行：防止两个请求同时 InitSession→追加导致 history/meta 分叉。
	// 冲突返回 409 而非排队，避免客户端无感挂起；空 session 每次新建独立会话，无需锁。
	if req.SessionID != "" {
		unlock, ok := s.locks.TryLock(req.SessionID)
		if !ok {
			writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "session is busy: "+req.SessionID, RetryActionResend)
			return
		}
		defer unlock()
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
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

	assembled, err := s.assemble(ctx, agentasm.Input{
		WorkDir:   s.workDir,
		SessionID: req.SessionID,
		PlanMode:  s.planMode,
		Consumer:  newEventConsumer(sw),
	})
	if err != nil {
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResend})
		return
	}
	defer assembled.Cleanup()

	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	userCountBeforeChat := countMessagesByRole(assembled.Session.Messages, sharedkernel.RoleUser)
	msg, err := assembled.Service.Chat(ctx, req.Task)
	if err != nil {
		retryAction := RetryActionResend
		// recoverBeforeChat 可能先补写 tool result；只有 user 数量增加才能证明
		// 本次 query 已提交，不能用总消息数判断。
		if countMessagesByRole(assembled.Session.Messages, sharedkernel.RoleUser) > userCountBeforeChat {
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
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "session_id is required", "")
		return
	}
	unlock, ok := s.locks.TryLock(sessionID)
	if !ok {
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "session is busy: "+sessionID, RetryActionResume)
		return
	}
	defer unlock()

	flusher, ok := w.(http.Flusher)
	if !ok {
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
	assembled, err := s.assemble(ctx, agentasm.Input{
		WorkDir: s.workDir, SessionID: sessionID, PlanMode: s.planMode, Consumer: newEventConsumer(sw),
	})
	if err != nil {
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResume})
		return
	}
	defer assembled.Cleanup()
	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	msg, err := assembled.Service.Resume(ctx)
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
		SessionID:   assembled.Session.ID,
		TokenUsed:   assembled.Session.TokenUsed,
		WindowToken: assembled.Session.WindowToken,
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
