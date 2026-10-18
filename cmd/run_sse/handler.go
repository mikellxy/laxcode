package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	projectMu          sync.Mutex
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
	// switcher 串行化模型切换，并保护装配和对话使用同一模型。
	models   *ai_models.Manager
	switcher *agentasm.ModelSwitcher
}

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 100
	defaultSessionLimit = 20
	maxSessionLimit     = 100
)

type createSessionRequest struct {
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
}

type sessionDTO struct {
	SessionID string    `json:"session_id"`
	Mode      string    `json:"mode"`
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"work_dir"`
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
		SessionID: item.ID, Mode: item.Mode, UserID: item.UserID, ProjectID: item.ProjectID, Title: item.Title, WorkDir: item.WorkDir,
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
	userID, err := s.requestUserID(req.UserID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		writeJSONError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	if s.projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	project, err := s.projects.GetProject(r.Context(), projectID)
	if errors.Is(err, sessionrepo.ErrProjectNotFound) || (err == nil && project.UserID != userID) {
		writeJSONError(w, http.StatusNotFound, "project not found: "+projectID)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load project failed: "+err.Error())
		return
	}
	created, err := s.catalog.CreateSession(r.Context(), uuid.NewString(), userID, project.ID, "", project.WorkDir, string(agentasm.ModeCode))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "create session failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(sessionToDTO(created))
}

func (s *server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	userID, err := s.requestUserID(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	sessionID, err := uuid.Parse(r.PathValue("session_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "session_id must be a UUID")
		return
	}
	unlock, ok := s.locks.TryLock(sessionID.String())
	if !ok {
		writeJSONError(w, http.StatusConflict, "会话正在执行，请停止或等待完成后再删除")
		return
	}
	defer unlock()
	evaluationUnlock, ok := s.evaluationLocks.TryLock(sessionID.String())
	if !ok {
		writeJSONError(w, http.StatusConflict, "会话正在创建评估，请稍后再删除")
		return
	}
	defer evaluationUnlock()
	err = s.catalog.DeleteSession(r.Context(), sessionID.String(), userID)
	if errors.Is(err, sessionrepo.ErrSessionBusy) {
		writeJSONError(w, http.StatusConflict, "会话仍有评估任务，请等待完成后再删除")
		return
	}
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete session failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	userID, err := s.requestUserID(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeJSONError(w, http.StatusBadRequest, "project_id is required")
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
	page, err := s.catalog.ListSessions(r.Context(), userID, projectID, beforeSessionID, limit)
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

type createProjectRequest struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type projectDTO struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	WorkDir   string    `json:"work_dir"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type projectListDTO struct {
	Projects []projectDTO `json:"projects"`
}

func projectToDTO(item session.Project) projectDTO {
	return projectDTO{ProjectID: item.ID, UserID: item.UserID, Name: item.Name, WorkDir: item.WorkDir, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func (s *server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	var req createProjectRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	userID, err := s.requestUserID(req.UserID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "project name is required")
		return
	}
	if len([]rune(name)) > 120 {
		writeJSONError(w, http.StatusBadRequest, "project name must not exceed 120 characters")
		return
	}
	var workDir string
	s.projectMu.Lock()
	defer s.projectMu.Unlock()
	if err := validateCloudProjectName(name); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.projects.ListProjects(r.Context(), userID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	for _, item := range items {
		if item.Name == name {
			writeJSONError(w, 409, "project name already exists")
			return
		}
	}
	workDir = filepath.Join(s.userRoot, name)
	root, err := os.OpenRoot(s.userRoot)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	defer root.Close()
	if err := root.Mkdir(name, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeJSONError(w, 409, "project name already exists")
		} else {
			writeJSONError(w, 500, err.Error())
		}
		return
	}
	created, err := s.projects.CreateProject(r.Context(), uuid.NewString(), userID, name, workDir)
	if err != nil {
		_ = os.Remove(workDir)
		writeJSONError(w, http.StatusInternalServerError, "create project failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(projectToDTO(created))
}

func (s *server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	userID, err := s.requestUserID(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	projects, err := s.projects.ListProjects(r.Context(), userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list projects failed: "+err.Error())
		return
	}
	response := projectListDTO{Projects: make([]projectDTO, len(projects))}
	for i := range projects {
		response.Projects[i] = projectToDTO(projects[i])
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// modelMissing 在 switcher 读锁内检查是否尚无活跃模型（延迟配置场景）。
func (s *server) modelMissing() bool {
	s.switcher.RLock()
	defer s.switcher.RUnlock()
	return strings.TrimSpace(s.models.Active().Ref) == ""
}

type historyMessageDTO struct {
	Seq              uint64             `json:"seq"`
	Role             string             `json:"role"`
	Content          string             `json:"content,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolSummary      string             `json:"tool_summary,omitempty"`
	ChatID           string             `json:"chat_id,omitempty"`
	UpsertFiles      []session.ChatFile `json:"upsert_files,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
}

// modelConfigDTO / providerModelsDTO / providerListModelDTO 是 GET /api/models
// 的响应载体：只回传模型目录中的模型清单；OpenaiApiKey / OpenaiBaseUrl 属隐私
// 配置不外发。ModelRef 是 provider:model 引用，前端凭它构造 POST /api/model
// 的切换请求。CurrentModel 是运行时生效的引用，即当前（及后续每个请求）LLM
// client 实际使用的模型。
type modelConfigDTO struct {
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	ReasoningEfforts []string `json:"reasoning_efforts,omitempty"`
	ModelName        string   `json:"model_name"`
	UpstreamModel    string   `json:"upstream_model,omitempty"`
	ModelRef         string   `json:"model_ref"`
}

type providerModelsDTO struct {
	ProviderName string           `json:"provider_name"`
	ModelList    []modelConfigDTO `json:"model_list"`
}

type providerListModelDTO struct {
	CurrentReasoningEffort string              `json:"current_reasoning_effort,omitempty"`
	CurrentModel           string              `json:"current_model"`
	Providers              []providerModelsDTO `json:"providers"`
}

// handleListModels 处理 GET /api/models：返回 ai_models.Manager.List() 的
// 脱敏视图（各 provider 的 ModelList）与当前生效模型引用，供客户端做模型
// 选择、切换与展示，不暴露凭据与端点。
func (s *server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	s.switcher.RLock()
	providers := s.models.List()
	response := providerListModelDTO{
		CurrentReasoningEffort: s.models.Active().ReasoningEffort,
		CurrentModel:           s.models.Active().Ref,
		Providers:              make([]providerModelsDTO, len(providers)),
	}
	for i, provider := range providers {
		models := make([]modelConfigDTO, len(provider.ModelList))
		for j, model := range provider.ModelList {
			models[j] = modelConfigDTO{
				ReasoningEffort: model.ReasoningEffort, ReasoningEfforts: model.ReasoningEfforts,
				ModelName:     model.ModelName,
				UpstreamModel: model.UpstreamModel,
				ModelRef:      model.Ref,
			}
		}
		response.Providers[i] = providerModelsDTO{ProviderName: provider.ProviderName, ModelList: models}
	}
	s.switcher.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// switchModelRequest 是 POST /api/model 的请求体：provider 与 model 拼成
// provider:model 引用，二者均必填。
type switchModelRequest struct {
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
}

// handleSwitchModel 处理 POST /api/model：按 provider + model 组合引用后，经
// agentasm.ModelSwitcher.SwitchModel 完成切换——替换本地 LLM 路由器的上游 client（SSE 流式流量全经
// 路由器，凭据与模型名都在其侧），再写回运行时配置；此后每个请求的按次装配
// 自然以新配置（含模型级 limit 预算）构建 provider。切换等待进行中的 Chat/
// Resume 结束后生效。
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
	var effort []string
	if req.ReasoningEffort != nil {
		effort = []string{strings.TrimSpace(*req.ReasoningEffort)}
	}
	s.switcher.Lock()
	err := s.switcher.SwitchModelLocked(provider+":"+model, effort...)
	currentEffort := s.models.Active().ReasoningEffort
	s.switcher.Unlock()
	if err != nil {
		if errors.Is(err, agentasm.ErrModelsUnavailable) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"model_ref": provider + ":" + model, "reasoning_effort": currentEffort})
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
			ChatID: msg.ChatID, CreatedAt: msg.CreatedAt,
			UpsertFiles: msg.UpsertFiles,
		}
	}
	if page.HasMore && len(page.Messages) > 0 {
		response.NextBeforeSeq = page.Messages[0].Seq
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *server) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	if s.contextRepo == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if _, err := s.catalog.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, sessionrepo.ErrSessionNotFound) {
			writeJSONError(w, 404, "session not found")
		} else {
			writeJSONError(w, 500, err.Error())
		}
		return
	}
	contextState, err := s.contextRepo.GetRequestContext(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load context failed: "+err.Error())
		return
	}
	s.switcher.RLock()
	contextWindow := s.models.Active().ContextWindow
	s.switcher.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(ContextData{WindowToken: contextState.WindowToken, ContextWindow: contextWindow})
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
		switcher:           agentasm.NewModelSwitcher(models),
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

	s.switcher.RLock()
	defer s.switcher.RUnlock()
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
	s.switcher.RLock()
	defer s.switcher.RUnlock()
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

func (s *server) requestUserID(legacy string) (string, error) {
	if s.userID == "" {
		return parseUserID(legacy)
	}
	if legacy != "" && legacy != s.userID {
		return "", errors.New("conflicting user identities")
	}
	return s.userID, nil
}
