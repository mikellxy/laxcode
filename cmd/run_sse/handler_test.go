package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	createdUserID string
	listUserID    string
	beforeID      string
	limit         int
	page          session.SummaryPage
}

func (f *fakeSessionCatalog) CreateSession(_ context.Context, id, userID, title, workDir string) (session.Summary, error) {
	f.createdUserID = userID
	now := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	return session.Summary{ID: id, UserID: userID, Title: title, WorkDir: workDir, CreatedAt: now, UpdatedAt: now}, nil
}

func (f *fakeSessionCatalog) GetSession(_ context.Context, id string) (session.Summary, error) {
	for _, item := range f.page.Sessions {
		if item.ID == id {
			return item, nil
		}
	}
	return session.Summary{}, sessionrepo.ErrSessionNotFound
}

func (f *fakeSessionCatalog) ListSessions(_ context.Context, userID, beforeID string, limit int) (session.SummaryPage, error) {
	f.listUserID, f.beforeID, f.limit = userID, beforeID, limit
	return f.page, nil
}

func catalogWithSession(id, workDir string) *fakeSessionCatalog {
	return &fakeSessionCatalog{page: session.SummaryPage{Sessions: []session.Summary{{ID: id, WorkDir: workDir}}}}
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
	}}
	s := newServer(t.TempDir(), false)
	s.catalog = catalog
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)

	created := httptest.NewRecorder()
	workDir := t.TempDir()
	mux.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(`{"user_id":"`+userID+`","work_dir":"`+workDir+`"}`)))
	if created.Code != http.StatusCreated || catalog.createdUserID != userID {
		t.Fatalf("create status=%d user=%q body=%s", created.Code, catalog.createdUserID, created.Body.String())
	}
	var createdDTO sessionDTO
	if err := json.Unmarshal(created.Body.Bytes(), &createdDTO); err != nil {
		t.Fatal(err)
	}
	if createdDTO.SessionID == "" || createdDTO.UserID != userID || createdDTO.WorkDir != workDir {
		t.Fatalf("unexpected create response: %+v", createdDTO)
	}

	listed := httptest.NewRecorder()
	mux.ServeHTTP(listed, httptest.NewRequest(http.MethodGet,
		"/api/sessions?user_id="+userID+"&before_session_id=s3&limit=2", nil))
	if listed.Code != http.StatusOK || catalog.listUserID != userID || catalog.beforeID != "s3" || catalog.limit != 2 {
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

func TestUseQAAssemblyAdaptsSSERequestToQACompositionRoot(t *testing.T) {
	s := newServer("/server/home", true)
	wantSession := session.NewSession("qa-session")
	cleanupCalled := false
	var got agentasm.QAInput
	s.useQAAssembly("/tmp/laxcode-qa/kb.sqlite", func(_ context.Context, in agentasm.QAInput) (*agentasm.QAAssembled, error) {
		got = in
		return &agentasm.QAAssembled{
			Session: wantSession,
			Cleanup: func() { cleanupCalled = true },
		}, nil
	})

	consumer := newEventConsumer(newSSEWriter(httptest.NewRecorder(), nil))
	assembled, err := s.assemble(context.Background(), agentasm.Input{
		WorkDir: "/request/workdir", HomeDir: "/request/home", SessionID: "qa-session", PlanMode: true, Consumer: consumer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.KBPath != "/tmp/laxcode-qa/kb.sqlite" || got.WorkDir != "/request/workdir" || got.HomeDir != "/request/home" || got.SessionID != "qa-session" {
		t.Fatalf("unexpected QA assembly input: %+v", got)
	}
	if got.Consumer == nil {
		t.Fatal("SSE event consumer was not forwarded to QA assembly")
	}
	if assembled.Session != wantSession || assembled.Cleanup == nil {
		t.Fatalf("unexpected adapted assembly: %+v", assembled)
	}
	assembled.Cleanup()
	if !cleanupCalled {
		t.Fatal("QA cleanup was not preserved")
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

// TestHandleChatNoFlusher 验证 ResponseWriter 不支持 Flusher 时返回 500（仍在进入流之前）。
func TestHandleChatNoFlusher(t *testing.T) {
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
