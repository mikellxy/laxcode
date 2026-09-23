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
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
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
	homeDir           string
	planMode          bool
	assemble          func(context.Context, agentasm.Input) (*agentasm.Assembled, error)
	locks             *sessionLocks
	approvals         *approvalBroker
	budgets           *budgetStates
	codeMode          bool
	tokenBudget       int
	history           session.SessionHistoryRepository
	contextRepo       session.SessionRepository
	catalog           session.SessionCatalogRepository
	projects          session.ProjectRepository
	pickDirectory     func(context.Context) (string, error)
	directoryPickerMu sync.Mutex
	// switcher 串行化模型切换，并保护装配和对话使用同一模型。
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
		SessionID: item.ID, UserID: item.UserID, ProjectID: item.ProjectID, Title: item.Title, WorkDir: item.WorkDir,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func normalizeWorkDir(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("work_dir is required")
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("work_dir must be an absolute path")
	}
	workDir, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve work_dir: %w", err)
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return "", fmt.Errorf("stat work_dir: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("work_dir must be a directory")
	}
	return filepath.Clean(workDir), nil
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
	created, err := s.catalog.CreateSession(r.Context(), uuid.NewString(), userID, project.ID, "", project.WorkDir)
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
	UserID  string `json:"user_id"`
	Name    string `json:"name"`
	WorkDir string `json:"work_dir"`
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
	userID, err := parseUserID(req.UserID)
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
	workDir, err := normalizeWorkDir(req.WorkDir)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.projects.CreateProject(r.Context(), uuid.NewString(), userID, name, workDir)
	if err != nil {
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
	userID, err := parseUserID(r.URL.Query().Get("user_id"))
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

type directoryPickerResponse struct {
	Path *string `json:"path"`
}

func (s *server) handlePickDirectory(w http.ResponseWriter, r *http.Request) {
	if s.pickDirectory == nil {
		writeJSONError(w, http.StatusInternalServerError, "directory picker is unavailable")
		return
	}
	if !s.directoryPickerMu.TryLock() {
		writeJSONError(w, http.StatusConflict, "directory picker is already open")
		return
	}
	defer s.directoryPickerMu.Unlock()

	path, err := s.pickDirectory(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if errors.Is(err, errDirectoryPickerCancelled) {
		_ = json.NewEncoder(w).Encode(directoryPickerResponse{})
		return
	}
	if errors.Is(err, errDirectoryPickerUnsupported) {
		writeJSONError(w, http.StatusNotImplemented, err.Error())
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = json.NewEncoder(w).Encode(directoryPickerResponse{Path: &path})
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

type addModelRequest struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	APIKey          string `json:"api_key"`
	BaseURL         string `json:"base_url"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
}

// handleListModels 处理 GET /api/models：返回 EnvAndFileConf.ProviderList 的
// 脱敏视图（各 provider 的 ModelList）与当前生效模型引用，供客户端做模型
// 选择、切换与展示，不暴露凭据与端点。
func (s *server) handleListModels(w http.ResponseWriter, _ *http.Request) {
	s.switcher.RLock()
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
	s.switcher.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// handleAddModel 将新模型写入用户级 settings.json，并同步更新当前进程的
// 模型目录。写入与模型切换/对话共用 switcher 写锁，避免目录在读取中突变。
func (s *server) handleAddModel(w http.ResponseWriter, r *http.Request) {
	var req addModelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	s.switcher.Lock()
	model, err := config.AddModelToSettings(s.homeDir, config.AddModelInput{
		Provider: req.Provider, Model: req.Model, APIKey: req.APIKey, BaseURL: req.BaseURL,
		ContextWindow: req.ContextWindow, MaxOutputTokens: req.MaxOutputTokens,
	})
	s.switcher.Unlock()
	if err != nil {
		switch {
		case errors.Is(err, config.ErrInvalidModelConfig):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, config.ErrModelAlreadyExists), errors.Is(err, config.ErrProviderCredentialsConflict):
			writeJSONError(w, http.StatusConflict, err.Error())
		default:
			writeJSONError(w, http.StatusInternalServerError, "save model failed: "+err.Error())
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(modelConfigDTO{
		ModelName: model.ModelName,
		ModelRef:  strings.TrimSpace(req.Provider) + ":" + model.ModelName,
	})
}

// switchModelRequest 是 POST /api/model 的请求体：provider 与 model 拼成
// provider:model 引用，二者均必填。
type switchModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// handleSwitchModel 处理 POST /api/model：按 provider + model 组合引用后，经
// agentasm.ModelSwitcher.SwitchModel 完成切换——替换本地 LLM 路由器的上游 client（SSE 流式流量全经
// 路由器，凭据与模型名都在其侧），再写回运行时配置；此后每个请求的按次装配
// 自然以新配置（含模型级 limit 预算）构建 provider。切换等待进行中的 Chat/
// Resume 结束后生效，与 TUI 的约束一致。
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
	if err := s.switcher.SwitchModel(provider + ":" + model); err != nil {
		if errors.Is(err, agentasm.ErrRouterUnavailable) {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
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
	contextState, err := s.contextRepo.GetRequestContext(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load context failed: "+err.Error())
		return
	}
	s.switcher.RLock()
	contextWindow, _ := config.ActiveModelBudget()
	s.switcher.RUnlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(ContextData{WindowToken: contextState.WindowToken, ContextWindow: contextWindow})
}

func newServer(homeDir string, planMode bool) *server {
	return &server{
		homeDir:       homeDir,
		planMode:      planMode,
		pickDirectory: pickNativeDirectory,
		assemble: func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
			return agentasm.AssembleSSE(ctx, in)
		},
		locks:     newSessionLocks(),
		approvals: newApprovalBroker(),
		budgets:   newBudgetStates(),
		switcher:  agentasm.NewModelSwitcher(nil, nil),
	}
}

func (s *server) eventConsumer(sw *sseWriter, sessionID *string, requestID string) func(*reactservice.ReactEvent) {
	base := newEventConsumer(sw)
	return func(event *reactservice.ReactEvent) {
		if event.Type != reactservice.ReActEventTypeHumanInTheLoop {
			base(event)
			return
		}
		if event.HumanConfirmChan == nil {
			return
		}
		id := s.approvals.register(*sessionID, requestID, event.HumanConfirmChan)
		sw.Send(EventApprovalRequired, ApprovalRequiredData{
			ApprovalID: id, SessionID: *sessionID, Kind: event.HumanConfirmKind, Content: event.Content,
		})
	}
}

func (s *server) configureBudget(assembled *agentasm.Assembled) {
	if !s.codeMode || s.tokenBudget <= 0 {
		return
	}
	if state, ok := s.budgets.get(assembled.Session.ID); ok {
		assembled.Service.RestoreTokenBudget(state)
	} else {
		assembled.Service.SetTokenBudget(s.tokenBudget)
	}
}

func (s *server) saveBudget(assembled *agentasm.Assembled) {
	if s.codeMode && s.tokenBudget > 0 {
		s.budgets.put(assembled.Session.ID, assembled.Service.TokenBudgetState())
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
			HomeDir:   in.HomeDir,
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
	if strings.TrimSpace(req.SessionID) == "" {
		writeJSONProtocolError(w, http.StatusBadRequest, ErrorCodeInvalidRequest, "session_id is required", "")
		return
	}
	// 同会话串行：防止两个请求同时 InitSession→追加导致 history/meta 分叉。
	// 冲突返回 409 而非排队，避免客户端无感挂起。
	unlock, ok := s.locks.TryLock(req.SessionID)
	if !ok {
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeSessionBusy, "session is busy: "+req.SessionID, RetryActionResend)
		return
	}
	defer unlock()
	if s.catalog == nil {
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "session repository is unavailable", "")
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), req.SessionID)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONProtocolError(w, http.StatusNotFound, ErrorCodeInvalidRequest, "session not found: "+req.SessionID, "")
		return
	}
	if err != nil {
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "load session failed: "+err.Error(), "")
		return
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
	requestID := uuid.NewString()
	defer s.approvals.clearRequest(requestID)
	var activeSessionID string

	s.switcher.RLock()
	defer s.switcher.RUnlock()
	assembled, err := s.assemble(ctx, agentasm.Input{
		WorkDir:   selected.WorkDir,
		HomeDir:   s.homeDir,
		SessionID: req.SessionID,
		PlanMode:  s.planMode,
		Consumer:  s.eventConsumer(sw, &activeSessionID, requestID),
	})
	if err != nil {
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResend})
		return
	}
	defer assembled.Cleanup()
	activeSessionID = assembled.Session.ID
	s.configureBudget(assembled)
	defer s.saveBudget(assembled)

	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	userCountBeforeChat := countMessagesByRole(assembled.Session.Messages, sharedkernel.RoleUser)
	msg, err := assembled.Service.Chat(ctx, req.Task)
	if err != nil {
		if errors.Is(err, reactservice.ErrDangerousCommandDeclined) || errors.Is(err, reactservice.ErrTokenBudgetDeclined) {
			sw.Send(EventError, ErrorData{Code: ErrorCodeChatStopped, Message: err.Error()})
			return
		}
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
	if s.catalog == nil {
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "session repository is unavailable", RetryActionResume)
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), sessionID)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONProtocolError(w, http.StatusNotFound, ErrorCodeNothingToResume, "session not found: "+sessionID, "")
		return
	}
	if err != nil {
		writeJSONProtocolError(w, http.StatusInternalServerError, ErrorCodeInternal, "load session failed: "+err.Error(), RetryActionResume)
		return
	}

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
	requestID := uuid.NewString()
	defer s.approvals.clearRequest(requestID)
	activeSessionID := sessionID
	s.switcher.RLock()
	defer s.switcher.RUnlock()
	assembled, err := s.assemble(ctx, agentasm.Input{
		WorkDir: selected.WorkDir, HomeDir: s.homeDir, SessionID: sessionID, PlanMode: s.planMode, Consumer: s.eventConsumer(sw, &activeSessionID, requestID),
	})
	if err != nil {
		sw.Send(EventError, ErrorData{Code: ErrorCodeAssemblyFailed, Message: "assemble agent failed: " + err.Error(), RetryAction: RetryActionResume})
		return
	}
	defer assembled.Cleanup()
	s.configureBudget(assembled)
	defer s.saveBudget(assembled)
	sw.Send(EventStart, StartData{SessionID: assembled.Session.ID})

	msg, err := assembled.Service.Resume(ctx)
	if errors.Is(err, reactservice.ErrNothingToResume) {
		sw.Send(EventError, ErrorData{Code: ErrorCodeNothingToResume, Message: err.Error()})
		return
	}
	if err != nil {
		if errors.Is(err, reactservice.ErrDangerousCommandDeclined) || errors.Is(err, reactservice.ErrTokenBudgetDeclined) {
			sw.Send(EventError, ErrorData{Code: ErrorCodeChatStopped, Message: err.Error()})
			return
		}
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
