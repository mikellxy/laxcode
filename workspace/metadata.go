package workspace

import (
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type createSessionRequest struct {
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
}

type SessionDTO struct {
	SessionID string    `json:"session_id"`
	Mode      string    `json:"mode"`
	UserID    string    `json:"user_id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	WorkDir   string    `json:"work_dir"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SessionPageDTO struct {
	Sessions            []SessionDTO `json:"sessions"`
	NextBeforeSessionID string       `json:"next_before_session_id,omitempty"`
	HasMore             bool         `json:"has_more"`
}

func sessionToDTO(item session.Summary) SessionDTO {
	return SessionDTO{
		SessionID: item.ID, Mode: item.Mode, UserID: item.UserID, ProjectID: item.ProjectID, Title: item.Title, WorkDir: item.WorkDir,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (s *API) HandleCreateSession(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	var req createSessionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+s.publicError(err))
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
	if s.Projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	project, err := s.Projects.GetProject(r.Context(), projectID)
	if errors.Is(err, sessionrepo.ErrProjectNotFound) || (err == nil && project.UserID != userID) {
		writeJSONError(w, http.StatusNotFound, "project not found: "+projectID)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load project failed: "+s.publicError(err))
		return
	}
	created, err := s.Catalog.CreateSession(r.Context(), uuid.NewString(), userID, project.ID, "", project.WorkDir, "code")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "create session failed: "+s.publicError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(sessionToDTO(created))
}

func (s *API) HandleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
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
	if s.DeleteGuard != nil {
		unlock, err := s.DeleteGuard(r.Context(), sessionID.String())
		if err != nil {
			writeJSONError(w, http.StatusConflict, s.publicError(err))
			return
		}
		defer unlock()
	}
	err = s.Catalog.DeleteSession(r.Context(), sessionID.String(), userID)
	if errors.Is(err, sessionrepo.ErrSessionBusy) {
		writeJSONError(w, http.StatusConflict, "会话仍有评估任务，请等待完成后再删除")
		return
	}
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete session failed: "+s.publicError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *API) HandleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.Catalog == nil {
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
	page, err := s.Catalog.ListSessions(r.Context(), userID, projectID, beforeSessionID, limit)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONError(w, http.StatusBadRequest, "invalid session cursor")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list sessions failed: "+s.publicError(err))
		return
	}
	response := SessionPageDTO{Sessions: make([]SessionDTO, len(page.Sessions)), HasMore: page.HasMore}
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

type ProjectDTO struct {
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	WorkDir   string    `json:"work_dir"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProjectListDTO struct {
	Projects []ProjectDTO `json:"projects"`
}

func projectToDTO(item session.Project) ProjectDTO {
	return ProjectDTO{ProjectID: item.ID, UserID: item.UserID, Name: item.Name, WorkDir: item.WorkDir, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func (s *API) HandleCreateProject(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	var req createProjectRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+s.publicError(err))
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
		writeJSONError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	items, err := s.Projects.ListProjects(r.Context(), userID)
	if err != nil {
		writeJSONError(w, 500, s.publicError(err))
		return
	}
	for _, item := range items {
		if item.Name == name {
			writeJSONError(w, 409, "project name already exists")
			return
		}
	}
	workDir = filepath.Join(s.runtimeRoot(), name)
	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		writeJSONError(w, 500, s.publicError(err))
		return
	}
	defer root.Close()
	if err := root.Mkdir(name, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeJSONError(w, 409, "project name already exists")
		} else {
			writeJSONError(w, 500, s.publicError(err))
		}
		return
	}
	if err := own(filepath.Join(s.UserRoot, name)); err != nil {
		writeJSONError(w, 500, "cannot set project ownership")
		return
	}
	created, err := s.Projects.CreateProject(r.Context(), uuid.NewString(), userID, name, workDir)
	if err != nil {
		_ = os.Remove(workDir)
		writeJSONError(w, http.StatusInternalServerError, "create project failed: "+s.publicError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(projectToDTO(created))
}

func (s *API) HandleListProjects(w http.ResponseWriter, r *http.Request) {
	if s.Projects == nil {
		writeJSONError(w, http.StatusInternalServerError, "project repository is unavailable")
		return
	}
	userID, err := s.requestUserID(r.URL.Query().Get("user_id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "user_id must be a UUID")
		return
	}
	projects, err := s.Projects.ListProjects(r.Context(), userID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list projects failed: "+s.publicError(err))
		return
	}
	response := ProjectListDTO{Projects: make([]ProjectDTO, len(projects))}
	for i := range projects {
		response.Projects[i] = projectToDTO(projects[i])
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

// modelMissing 在 switcher 读锁内检查是否尚无活跃模型（延迟配置场景）。
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

// modelConfigDTO / providerModelsDTO / ProviderListModelDTO 是 GET /api/models
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

type ProviderListModelDTO struct {
	CurrentReasoningEffort string              `json:"current_reasoning_effort,omitempty"`
	CurrentModel           string              `json:"current_model"`
	Providers              []providerModelsDTO `json:"providers"`
}

// HandleListModels 处理 GET /api/models：返回 ai_models.Manager.List() 的
// 脱敏视图（各 provider 的 ModelList）与当前生效模型引用，供客户端做模型
// 选择、切换与展示，不暴露凭据与端点。
func (s *API) HandleListModels(w http.ResponseWriter, _ *http.Request) {

	providers := s.Models.List()
	response := ProviderListModelDTO{
		CurrentReasoningEffort: s.Models.Active().ReasoningEffort,
		CurrentModel:           s.Models.Active().Ref,
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

// HandleSwitchModel persists the preference; already running tasks retain their snapshot.
func (s *API) HandleSwitchModel(w http.ResponseWriter, r *http.Request) {
	var req switchModelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+s.publicError(err))
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
	if s.Models == nil {
		writeJSONError(w, 500, "model manager is unavailable")
		return
	}
	err := s.Models.SetActiveModel(provider+":"+model, effort...)
	currentEffort := s.Models.Active().ReasoningEffort

	if err != nil {
		writeJSONError(w, http.StatusBadRequest, s.publicError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"model_ref": provider + ":" + model, "reasoning_effort": currentEffort})
}

type HistoryPageDTO struct {
	Messages      []historyMessageDTO `json:"messages"`
	NextBeforeSeq uint64              `json:"next_before_seq,omitempty"`
	HasMore       bool                `json:"has_more"`
}

// HandleHistory 处理 GET /api/sessions/{session_id}/messages。
// before_seq 是排他的消息游标，省略时从最新消息开始；响应消息始终按 seq 正序。
func (s *API) HandleHistory(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
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

	page, found, err := s.History.ListOriginalHistory(r.Context(), sessionID, beforeSeq, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load history failed: "+s.publicError(err))
		return
	}
	if !found {
		writeJSONError(w, http.StatusNotFound, "session not found: "+sessionID)
		return
	}

	response := HistoryPageDTO{Messages: make([]historyMessageDTO, len(page.Messages)), HasMore: page.HasMore}
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

func (s *API) HandleSessionContext(w http.ResponseWriter, r *http.Request) {
	if s.ContextRepo == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if _, err := s.Catalog.GetSession(r.Context(), sessionID); err != nil {
		if errors.Is(err, sessionrepo.ErrSessionNotFound) {
			writeJSONError(w, 404, "session not found")
		} else {
			writeJSONError(w, 500, s.publicError(err))
		}
		return
	}
	contextState, err := s.ContextRepo.GetRequestContext(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load context failed: "+s.publicError(err))
		return
	}

	contextWindow := s.Models.Active().ContextWindow

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(ContextData{WindowToken: contextState.WindowToken, ContextWindow: contextWindow})
}
