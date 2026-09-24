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
	domainrouter "github.com/mikellxy/laxcode/internal/domain/llmrouter"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

type contextReaderStub struct {
	session.SessionRepository
	state session.RequestContext
}

func (c contextReaderStub) GetRequestContext(context.Context, string) (session.RequestContext, error) {
	return c.state, nil
}

func TestHandleSessionContext(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.OpenaiContextWindow = 128000
	config.EnvAndFileConf.Model = ""
	config.EnvAndFileConf.ProviderList = nil
	s := newServer(t.TempDir(), false)
	s.contextRepo = contextReaderStub{state: session.RequestContext{WindowToken: sharedkernel.TokenStatistics{TokenInput: 12000, TokenOutput: 932}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/context", s.handleSessionContext)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/s1/context", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got ContextData
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.WindowToken.Total() != 12932 || got.ContextWindow != 128000 {
		t.Fatalf("context response = %+v", got)
	}
}

type fakeHistoryRepo struct {
	page      session.HistoryPage
	found     bool
	beforeSeq uint64
	limit     int
}

type fakeSessionCatalog struct {
	createdUserID    string
	createdProjectID string
	createdMode      string
	listUserID       string
	listProjectID    string
	beforeID         string
	limit            int
	page             session.SummaryPage
	projects         []session.Project
}

func (f *fakeSessionCatalog) CreateSession(_ context.Context, id, userID, projectID, title, workDir, mode string) (session.Summary, error) {
	f.createdUserID, f.createdProjectID = userID, projectID
	f.createdMode = mode
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	return session.Summary{ID: id, Mode: mode, UserID: userID, ProjectID: projectID, Title: title, WorkDir: workDir, CreatedAt: now, UpdatedAt: now}, nil
}

func (f *fakeSessionCatalog) GetSession(_ context.Context, id string) (session.Summary, error) {
	for _, item := range f.page.Sessions {
		if item.ID == id {
			return item, nil
		}
	}
	return session.Summary{}, sessionrepo.ErrSessionNotFound
}

func (f *fakeSessionCatalog) ListSessions(_ context.Context, userID, projectID, beforeID string, limit int) (session.SummaryPage, error) {
	f.listUserID, f.listProjectID, f.beforeID, f.limit = userID, projectID, beforeID, limit
	return f.page, nil
}

func (f *fakeSessionCatalog) CreateProject(_ context.Context, id, userID, name, workDir string) (session.Project, error) {
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	project := session.Project{ID: id, UserID: userID, Name: name, WorkDir: workDir, CreatedAt: now, UpdatedAt: now}
	f.projects = append([]session.Project{project}, f.projects...)
	return project, nil
}

func (f *fakeSessionCatalog) GetProject(_ context.Context, id string) (session.Project, error) {
	for _, project := range f.projects {
		if project.ID == id {
			return project, nil
		}
	}
	return session.Project{}, sessionrepo.ErrProjectNotFound
}

func (f *fakeSessionCatalog) ListProjects(_ context.Context, userID string) ([]session.Project, error) {
	var projects []session.Project
	for _, project := range f.projects {
		if project.UserID == userID {
			projects = append(projects, project)
		}
	}
	return projects, nil
}

func catalogWithSession(id, workDir string) *fakeSessionCatalog {
	return &fakeSessionCatalog{page: session.SummaryPage{Sessions: []session.Summary{{ID: id, Mode: "code", WorkDir: workDir}}}}
}

func TestHandleCreateAndListSessions(t *testing.T) {
	userID := "11111111-1111-4111-8111-111111111111"
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	catalog := &fakeSessionCatalog{page: session.SummaryPage{
		Sessions: []session.Summary{
			{ID: "s2", UserID: userID, CreatedAt: now, UpdatedAt: now},
			{ID: "s1", UserID: userID, CreatedAt: now, UpdatedAt: now},
		},
		HasMore: true,
	}, projects: []session.Project{{ID: "project-1", UserID: userID, Name: "Project", WorkDir: t.TempDir()}}}
	s := newServer(t.TempDir(), false)
	s.catalog = catalog
	s.projects = catalog
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)

	created := httptest.NewRecorder()
	workDir := catalog.projects[0].WorkDir
	mux.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"user_id":"`+userID+`","project_id":"project-1"}`)))
	if created.Code != http.StatusCreated || catalog.createdUserID != userID || catalog.createdProjectID != "project-1" || catalog.createdMode != "code" {
		t.Fatalf("create status=%d user=%q body=%s", created.Code, catalog.createdUserID, created.Body.String())
	}
	var createdDTO sessionDTO
	if err := json.Unmarshal(created.Body.Bytes(), &createdDTO); err != nil {
		t.Fatal(err)
	}
	if createdDTO.SessionID == "" || createdDTO.Mode != "code" || createdDTO.UserID != userID || createdDTO.ProjectID != "project-1" || createdDTO.WorkDir != workDir {
		t.Fatalf("unexpected create response: %+v", createdDTO)
	}

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet,
		"/api/sessions?user_id="+userID+"&project_id=project-1&before_session_id=s3&limit=2", nil))
	if listed.Code != http.StatusOK || catalog.listUserID != userID || catalog.listProjectID != "project-1" || catalog.beforeID != "s3" || catalog.limit != 2 {
		t.Fatalf("list status=%d user=%q before=%q limit=%d body=%s",
			listed.Code, catalog.listUserID, catalog.beforeID, catalog.limit, listed.Body.String())
	}
	var page sessionPageDTO
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || page.NextBeforeSessionID != "s1" || len(page.Sessions) != 2 {
		t.Fatalf("unexpected list response: %+v", page)
	}
}

func TestHandleCreateAndListProjects(t *testing.T) {
	userID := "11111111-1111-4111-8111-111111111111"
	catalog := &fakeSessionCatalog{}
	s := newServer(t.TempDir(), false)
	s.projects = catalog
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/projects", s.handleCreateProject)
	mux.HandleFunc("GET /api/projects", s.handleListProjects)

	workDir := t.TempDir()
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/projects",
		strings.NewReader(`{"user_id":"`+userID+`","name":"LaxCode","work_dir":"`+workDir+`"}`)))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var project projectDTO
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.ProjectID == "" || project.Name != "LaxCode" || project.WorkDir != workDir {
		t.Fatalf("project=%+v", project)
	}

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/api/projects?user_id="+userID, nil))
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var list projectListDTO
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 || list.Projects[0].ProjectID != project.ProjectID {
		t.Fatalf("projects=%+v", list.Projects)
	}

	relative := httptest.NewRecorder()
	mux.ServeHTTP(relative, httptest.NewRequest(http.MethodPost, "/api/projects",
		strings.NewReader(`{"user_id":"`+userID+`","name":"Relative","work_dir":"relative/path"}`)))
	if relative.Code != http.StatusBadRequest {
		t.Fatalf("relative path status=%d body=%s", relative.Code, relative.Body.String())
	}
}

func TestSessionEndpointsRejectInvalidUserID(t *testing.T) {
	s := newServer(t.TempDir(), false)
	s.catalog = &fakeSessionCatalog{}

	create := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"user_id":"not-a-uuid"}`))
	s.handleCreateSession(create, req)
	if create.Code != http.StatusBadRequest {
		t.Fatalf("create invalid user status=%d", create.Code)
	}
	list := httptest.NewRecorder()
	s.handleListSessions(list, httptest.NewRequest(http.MethodGet, "/api/sessions?user_id=bad", nil))
	if list.Code != http.StatusBadRequest {
		t.Fatalf("list invalid user status=%d", list.Code)
	}
}

type recordingModelRouter struct {
	clients []domainrouter.StreamClient
}

func (r *recordingModelRouter) ReplaceClient(client domainrouter.StreamClient) {
	r.clients = append(r.clients, client)
}

func setupSwitchModelCatalog(t *testing.T) {
	t.Helper()
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.ProviderList = []config.ProviderConfig{
		{
			ProviderName: "first", OpenaiApiKey: "key-1", OpenaiBaseUrl: "https://first.example/v1",
			ModelList: []config.ModelConfig{{ModelName: "model-1"}},
		},
		{
			ProviderName: "second", OpenaiApiKey: "key-2", OpenaiBaseUrl: "https://second.example/v1",
			ModelList: []config.ModelConfig{{ModelName: "model-2", UpstreamModel: "upstream-2"}},
		},
	}
	if err := config.SetActiveModel("first:model-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHandleSwitchModelReplacesRouterAndConfig(t *testing.T) {
	setupSwitchModelCatalog(t)
	router := &recordingModelRouter{}
	s := newServer(t.TempDir(), false)
	s.switcher = agentasm.NewModelSwitcher(router, nil)

	rec := httptest.NewRecorder()
	s.handleSwitchModel(rec, httptest.NewRequest(http.MethodPost, "/api/model",
		strings.NewReader(`{"provider":"second","model":"model-2"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ModelRef string `json:"model_ref"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ModelRef != "second:model-2" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if len(router.clients) != 1 {
		t.Fatalf("router replacements=%d, want 1", len(router.clients))
	}
	if config.EnvAndFileConf.Model != "second:model-2" ||
		config.EnvAndFileConf.OpenaiApiKey != "key-2" ||
		config.EnvAndFileConf.OpenaiModel != "upstream-2" {
		t.Fatalf("runtime config not switched: %+v", config.EnvAndFileConf)
	}
}

func TestHandleSwitchModelRejectsInvalidInput(t *testing.T) {
	setupSwitchModelCatalog(t)
	router := &recordingModelRouter{}
	s := newServer(t.TempDir(), false)
	s.switcher = agentasm.NewModelSwitcher(router, nil)

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing model", `{"provider":"second"}`, http.StatusBadRequest},
		{"unknown provider", `{"provider":"ghost","model":"model-1"}`, http.StatusBadRequest},
		{"unknown model", `{"provider":"first","model":"ghost"}`, http.StatusBadRequest},
		{"unknown field", `{"provider":"first","model":"model-1","extra":1}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		s.handleSwitchModel(rec, httptest.NewRequest(http.MethodPost, "/api/model", strings.NewReader(tc.body)))
		if rec.Code != tc.status {
			t.Fatalf("%s: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if len(router.clients) != 0 {
		t.Fatalf("router replaced on failed switch: %d", len(router.clients))
	}

	noRouter := newServer(t.TempDir(), false)
	rec := httptest.NewRecorder()
	noRouter.handleSwitchModel(rec, httptest.NewRequest(http.MethodPost, "/api/model",
		strings.NewReader(`{"provider":"first","model":"model-1"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nil router status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleListModelsOmitsSensitiveFields(t *testing.T) {
	originalProviders, originalModel := config.EnvAndFileConf.ProviderList, config.EnvAndFileConf.Model
	config.EnvAndFileConf.ProviderList = []config.ProviderConfig{{
		OpenaiApiKey:  "sk-secret",
		OpenaiBaseUrl: "https://api.example.com",
		ProviderName:  "example",
		ModelList: []config.ModelConfig{
			{ModelName: "model-a"},
			{ModelName: "model-b", UpstreamModel: "upstream-b"},
		},
	}}
	config.EnvAndFileConf.Model = "example:model-a"
	defer func() {
		config.EnvAndFileConf.ProviderList, config.EnvAndFileConf.Model = originalProviders, originalModel
	}()

	s := newServer(t.TempDir(), false)
	rec := httptest.NewRecorder()
	s.handleListModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got providerListModelDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentModel != "example:model-a" || len(got.Providers) != 1 || len(got.Providers[0].ModelList) != 2 ||
		got.Providers[0].ModelList[0].ModelName != "model-a" || got.Providers[0].ModelList[0].UpstreamModel != "" ||
		got.Providers[0].ModelList[1].UpstreamModel != "upstream-b" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Providers[0].ModelList[0].ModelRef != "example:model-a" ||
		got.Providers[0].ModelList[1].ModelRef != "example:model-b" {
		t.Fatalf("unexpected model refs: %+v", got.Providers[0].ModelList)
	}
	for _, secret := range []string{"sk-secret", "https://api.example.com", "api_key", "base_url", "provider_name"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaks sensitive field %q: %s", secret, rec.Body.String())
		}
	}
}

func TestHandleAddModelPersistsWithoutReturningAPIKey(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.ProviderList = []config.ProviderConfig{{
		ProviderName: "existing", OpenaiApiKey: "old-secret", OpenaiBaseUrl: "https://existing.example/v1",
		ModelList: []config.ModelConfig{{ModelName: "main"}},
	}}
	config.EnvAndFileConf.Model = "existing:main"
	home := t.TempDir()
	settingsDir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"model":"existing:main","provider_list":[{"provider_name":"existing","openai_api_key":"old-secret","openai_base_url":"https://existing.example/v1","model_list":[{"model_name":"main"}]}]}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}

	s := newServer(home, false)
	body := `{"provider":"new-provider","model":"new-model","api_key":"new-secret","base_url":"https://new.example/v1","context_window":128000,"max_output_tokens":8192}`
	rec := httptest.NewRecorder()
	s.handleAddModel(rec, httptest.NewRequest(http.MethodPost, "/api/models", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "new-secret") || strings.Contains(rec.Body.String(), "base_url") {
		t.Fatalf("response leaked sensitive configuration: %s", rec.Body.String())
	}
	if _, err := config.ResolveModel("new-provider:new-model"); err != nil {
		t.Fatalf("runtime catalog was not updated: %v", err)
	}
	persisted, err := os.ReadFile(filepath.Join(settingsDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), `"model_name": "new-model"`) || !strings.Contains(string(persisted), `"context": 128000`) {
		t.Fatalf("model was not persisted: %s", persisted)
	}

	duplicate := httptest.NewRecorder()
	s.handleAddModel(duplicate, httptest.NewRequest(http.MethodPost, "/api/models", strings.NewReader(body)))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
}

func (f *fakeHistoryRepo) ListOriginalHistory(_ context.Context, _ string, beforeSeq uint64, limit int) (session.HistoryPage, bool, error) {
	f.beforeSeq, f.limit = beforeSeq, limit
	return f.page, f.found, nil
}

func TestHandleHistoryReturnsPagedDTO(t *testing.T) {
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	repo := &fakeHistoryRepo{found: true, page: session.HistoryPage{
		Messages: []session.HistoryMessage{
			{Seq: 7, Role: "tool", ToolSummary: "bash(go test ./...)", CreatedAt: now},
			{Seq: 8, Role: "assistant", Content: "answer", ReasoningContent: "thinking", CreatedAt: now},
		},
		HasMore: true,
	}}
	s := newServer(t.TempDir(), false)
	s.history = repo
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", s.handleHistory)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/s1/messages?before_seq=10&limit=2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || repo.beforeSeq != 10 || repo.limit != 2 {
		t.Fatalf("status=%d before=%d limit=%d body=%s", rec.Code, repo.beforeSeq, repo.limit, rec.Body.String())
	}
	var got historyPageDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.HasMore || got.NextBeforeSeq != 7 || len(got.Messages) != 2 {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.Messages[0].ToolSummary != "bash(go test ./...)" || got.Messages[0].Content != "" {
		t.Fatalf("unexpected tool DTO: %+v", got.Messages[0])
	}
	if got.Messages[1].Content != "answer" || got.Messages[1].ReasoningContent != "thinking" {
		t.Fatalf("unexpected assistant DTO: %+v", got.Messages[1])
	}
}

func TestHandleHistoryRejectsInvalidPaginationAndMissingSession(t *testing.T) {
	s := newServer(t.TempDir(), false)
	s.history = &fakeHistoryRepo{found: false}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{session_id}/messages", s.handleHistory)

	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/sessions/s1/messages?limit=101", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit status=%d", bad.Code)
	}
	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/sessions/missing/messages", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing session status=%d", missing.Code)
	}
}

func TestNewServerBindsAssemblyMode(t *testing.T) {
	s := newServer("/server/home", false, agentasm.ModeRAG)
	if s.mode != agentasm.ModeRAG || s.codeMode {
		t.Fatalf("server mode = %q, codeMode=%v", s.mode, s.codeMode)
	}
}

// TestHandleChatInvalidJSON 验证非法请求体在进入 SSE 流之前返回 400 + JSON。
func TestHandleChatInvalidJSON(t *testing.T) {
	s := newServer(t.TempDir(), false)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader("{invalid"))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("流前错误应为 JSON，实际 Content-Type %q", ct)
	}
}

// TestHandleChatEmptyTask 验证 task 为空（含纯空白）返回 400。
func TestHandleChatEmptyTask(t *testing.T) {
	s := newServer(t.TempDir(), false)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"s1","task":"   "}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 task 应 400，实际 %d", rec.Code)
	}
}

// TestHandleChatSessionBusy 验证同一 session_id 已被占用时返回 409（不排队等待），
// 避免同会话并发导致 history/meta 分叉，也避免客户端无感挂起。
func TestHandleChatSessionBusy(t *testing.T) {
	s := newServer(t.TempDir(), false)
	unlock, ok := s.locks.TryLock("sess-1") // 预占，模拟同会话并发
	if !ok {
		t.Fatal("预占 session 锁失败")
	}
	defer unlock()

	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"sess-1","task":"hi"}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("同 session 并发应 409，实际 %d", rec.Code)
	}
}

// nonFlusherWriter 是不实现 http.Flusher 的 ResponseWriter，用于验证无法流式时的降级。
type nonFlusherWriter struct {
	header http.Header
	code   int
}

func newNonFlusherWriter() *nonFlusherWriter {
	return &nonFlusherWriter{header: http.Header{}}
}

func (n *nonFlusherWriter) Header() http.Header         { return n.header }
func (n *nonFlusherWriter) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusherWriter) WriteHeader(code int)        { n.code = code }

// stubActiveModel 在测试内伪造一个已激活的运行时模型（预检只看 Model 引用），
// 结束后恢复全局配置。供覆盖 /chat /resume 完整路径的用例绕过 MODEL_REQUIRED
// 预检。
func stubActiveModel(t *testing.T) {
	t.Helper()
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.Model = "test:model"
}

// TestHandleChatNoFlusher 验证 ResponseWriter 不支持 Flusher 时返回 500（仍在进入流之前）。
func TestHandleChatNoFlusher(t *testing.T) {
	stubActiveModel(t)
	workDir := t.TempDir()
	s := newServer(t.TempDir(), false)
	s.catalog = catalogWithSession("s1", workDir)
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"s1","task":"hi"}`))
	w := newNonFlusherWriter()
	s.handleChat(w, req)

	if w.code != http.StatusInternalServerError {
		t.Fatalf("不支持流式应 500，实际 %d", w.code)
	}
}

// TestHandleChatAssembleError 验证装配失败时已进入 SSE 流（状态码固定 200、
// Content-Type 为 event-stream），错误经 event: error 帧回传，且此前不发 start 帧。
func TestHandleChatAssembleError(t *testing.T) {
	stubActiveModel(t)
	workDir := t.TempDir()
	s := newServer(t.TempDir(), false)
	s.catalog = catalogWithSession("s1", workDir)
	s.assemble = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
		return nil, errors.New("boom")
	}
	req := httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"s1","task":"hi"}`))
	rec := httptest.NewRecorder()
	s.handleChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("进入 SSE 流后状态码应为 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("应为 SSE Content-Type，实际 %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "boom") {
		t.Fatalf("装配失败应发含原因的 error 帧，实际 %q", body)
	}
	if !strings.Contains(body, `"code":"AGENT_ASSEMBLY_FAILED"`) || !strings.Contains(body, `"retry_action":"resend"`) {
		t.Fatalf("装配失败应要求重发原 query，实际 %q", body)
	}
	if strings.Contains(body, "event: start") {
		t.Fatalf("装配失败不应发 start 帧：%q", body)
	}
}

func TestHandleResumeAssembleErrorKeepsResumeAction(t *testing.T) {
	stubActiveModel(t)
	workDir := t.TempDir()
	s := newServer(t.TempDir(), false)
	s.catalog = catalogWithSession("s1", workDir)
	s.assemble = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
		return nil, errors.New("boom")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/s1/resume", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("进入 resume SSE 后状态码应为 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"code":"AGENT_ASSEMBLY_FAILED"`) || !strings.Contains(body, `"retry_action":"resume"`) {
		t.Fatalf("resume 装配失败应保持 resume 动作，实际 %q", body)
	}
}

// TestHandleChatWithoutModel 验证延迟配置场景：未激活模型时 /chat 与 resume
// 在进入 SSE 流之前返回 409 MODEL_REQUIRED，而不是在 LLM 调用处才失败。
func TestHandleChatWithoutModel(t *testing.T) {
	previous := config.EnvAndFileConf
	t.Cleanup(func() { config.EnvAndFileConf = previous })
	config.EnvAndFileConf.Model = ""
	config.EnvAndFileConf.ProviderList = nil

	workDir := t.TempDir()
	s := newServer(t.TempDir(), false)
	s.catalog = catalogWithSession("s1", workDir)

	chat := httptest.NewRecorder()
	s.handleChat(chat, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"s1","task":"hi"}`)))
	if chat.Code != http.StatusConflict || !strings.Contains(chat.Body.String(), "MODEL_REQUIRED") {
		t.Fatalf("chat status=%d body=%s", chat.Code, chat.Body.String())
	}

	resume := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
	mux.ServeHTTP(resume, httptest.NewRequest(http.MethodPost, "/api/sessions/s1/resume", nil))
	if resume.Code != http.StatusConflict || !strings.Contains(resume.Body.String(), "MODEL_REQUIRED") {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
}

func TestChatAndResumeRejectDifferentSessionMode(t *testing.T) {
	workDir := t.TempDir()
	s := newServer(t.TempDir(), false, agentasm.ModeCode)
	s.catalog = &fakeSessionCatalog{page: session.SummaryPage{Sessions: []session.Summary{{
		ID: "rag-session", Mode: "rag", WorkDir: workDir,
	}}}}

	chat := httptest.NewRecorder()
	s.handleChat(chat, httptest.NewRequest(http.MethodPost, "/chat", strings.NewReader(`{"session_id":"rag-session","task":"hi"}`)))
	if chat.Code != http.StatusConflict || !strings.Contains(chat.Body.String(), "does not match server mode") {
		t.Fatalf("chat status=%d body=%s", chat.Code, chat.Body.String())
	}

	resume := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions/{session_id}/resume", s.handleResume)
	mux.ServeHTTP(resume, httptest.NewRequest(http.MethodPost, "/api/sessions/rag-session/resume", nil))
	if resume.Code != http.StatusConflict || !strings.Contains(resume.Body.String(), "does not match server mode") {
		t.Fatalf("resume status=%d body=%s", resume.Code, resume.Body.String())
	}
}

// TestSessionLocksSerializesSameID 验证锁表语义：同 id 二次 TryLock 失败，
// 释放后可再获取；不同 id 互不阻塞。
func TestSessionLocksSerializesSameID(t *testing.T) {
	locks := newSessionLocks()

	unlockA, ok := locks.TryLock("a")
	if !ok {
		t.Fatal("首次锁 a 应成功")
	}
	if _, ok := locks.TryLock("a"); ok {
		t.Fatal("a 已占用，二次 TryLock 应失败")
	}
	if _, ok := locks.TryLock("b"); !ok {
		t.Fatal("不同 id b 不应被 a 阻塞")
	}
	unlockA()
	if _, ok := locks.TryLock("a"); !ok {
		t.Fatal("a 释放后应可再次获取")
	}
}
