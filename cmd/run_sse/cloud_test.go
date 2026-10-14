package run_sse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
)

func cloudRequest(t *testing.T, handler http.Handler, id, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	if id != "" {
		req.Header.Set("X-Laxcode-User-ID", id)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func TestCloudLifecycleAndUserIsolation(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(layout.Root(home), 0700); err != nil {
		t.Fatal(err)
	}
	settings := `{"provider_list":[{"provider_name":"default","openai_api_key":"test-key","openai_base_url":"https://example.com/v1","model_list":[{"model_name":"test-model","limit":{"context":200000,"output":1000}}]}],"model":"default:test-model"}`
	if err := os.WriteFile(layout.UserSettings(home), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(layout.SkillsRoot(home), "demo")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: demo\ndescription: demo skill\n---\nRead carefully."), 0600); err != nil {
		t.Fatal(err)
	}
	users := newCloudUsers(home, nil)
	defer users.Close()
	first, second := uuid.NewString(), uuid.NewString()
	for _, path := range []string{"/healthz", "/api/config", "/api/projects?user_id=" + first, "/api/models"} {
		response := cloudRequest(t, users, first, "GET", path, nil)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "laxcode")); !os.IsNotExist(err) {
		t.Fatalf("read-only onboarding initialized user files: %v", err)
	}
	create := func(id string) projectDTO {
		response := cloudRequest(t, users, id, "POST", "/api/projects", map[string]string{"user_id": id, "name": "demo-project"})
		if response.Code != 201 {
			t.Fatalf("create: %d %s", response.Code, response.Body.String())
		}
		var project projectDTO
		if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
			t.Fatal(err)
		}
		return project
	}
	project := create(first)
	other := create(second)
	if project.WorkDir == other.WorkDir {
		t.Fatal("users share workdir")
	}
	user := users.users[first]
	if project.WorkDir != filepath.Join(user.server.userRoot, "demo-project") {
		t.Fatalf("workdir %s", project.WorkDir)
	}
	for _, name := range []string{layout.SessionDB(user.server.homeDir), layout.UserSettings(user.server.homeDir), filepath.Join(layout.SkillsRoot(user.server.homeDir), "demo", "SKILL.md"), user.server.tempDir} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if got := cloudRequest(t, users, first, "POST", "/api/projects", map[string]string{"user_id": first, "name": "demo-project"}); got.Code != 409 {
		t.Fatalf("duplicate: %d %s", got.Code, got.Body.String())
	}
	for _, name := range []string{"../escape", "my project", "中文", "a/b", "agenthome", "tmp", ""} {
		if got := cloudRequest(t, users, first, "POST", "/api/projects", map[string]string{"user_id": first, "name": name}); got.Code != 400 {
			t.Fatalf("name %q: %d", name, got.Code)
		}
	}
	if got := cloudRequest(t, users, first, "POST", "/api/projects", map[string]string{"user_id": second, "name": "wrong-user"}); got.Code != 400 {
		t.Fatal("conflicting identities accepted")
	}
	response := cloudRequest(t, users, first, "POST", "/api/sessions", map[string]string{"user_id": first, "project_id": project.ProjectID})
	if response.Code != 201 {
		t.Fatalf("session: %d %s", response.Code, response.Body.String())
	}
	var sess sessionDTO
	if err := json.Unmarshal(response.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	user.server.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		if in.HomeDir != user.server.homeDir || in.UserRoot != user.server.userRoot || in.TempDir != user.server.tempDir || in.Repository != user.repo {
			t.Fatal("assembly did not receive user resources")
		}
		in.Mode = agentasm.ModeCode
		assembled, err := agentasm.Assemble(ctx, in)
		if err != nil {
			return nil, err
		}
		assembled.Service.LLMClient = &approvalFlowLLM{}
		return assembled, nil
	}
	response = cloudRequest(t, users, first, "POST", "/chat", map[string]string{"session_id": sess.SessionID, "task": "work"})
	if response.Code != 200 || !strings.Contains(response.Body.String(), "event: done") || strings.Contains(response.Body.String(), "approval_required") {
		t.Fatalf("chat: %d %s", response.Code, response.Body.String())
	}
	snapshot, err := user.repo.GetRequestContext(context.Background(), sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, message := range snapshot.Messages {
		if strings.Contains(message.Content, "cloud 模式拒绝 Bash") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("risky Bash was not rejected")
	}
	historyPath := "/api/sessions/" + sess.SessionID + "/messages"
	response = cloudRequest(t, users, first, "GET", historyPath, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "finished") {
		t.Fatalf("history: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{historyPath, "/api/sessions/" + sess.SessionID + "/context"} {
		if got := cloudRequest(t, users, second, "GET", path, nil); got.Code != 404 {
			t.Fatalf("cross-user %s: %d", path, got.Code)
		}
	}
	if got := cloudRequest(t, users, second, "POST", "/chat", map[string]string{"session_id": sess.SessionID, "task": "steal"}); got.Code != 404 {
		t.Fatalf("cross-user chat: %d", got.Code)
	}
	// Per-user model updates must not change another user's catalog or defaults.
	response = cloudRequest(t, users, first, "POST", "/api/models", map[string]any{"provider": "mine", "model": "other-model", "api_key": "other-key", "base_url": "https://example.com/v1", "context_window": 1000, "max_output_tokens": 100})
	if response.Code != 201 {
		t.Fatalf("model: %d %s", response.Code, response.Body.String())
	}
	response = cloudRequest(t, users, first, "POST", "/api/model", map[string]string{"provider": "mine", "model": "other-model"})
	if response.Code != 200 || users.users[second].server.models.Active().Ref != "default:test-model" {
		t.Fatalf("model isolation: %d %s", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/directory-picker", "/api/auth/chatgpt", "/api/sessions/" + sess.SessionID + "/approvals/id"} {
		if got := cloudRequest(t, users, first, "POST", path, nil); got.Code != 403 {
			t.Fatalf("disabled endpoint %s: %d", path, got.Code)
		}
	}
	// Closing an agent must not close the cached repository; restarting reloads data.
	users.Close()
	users = newCloudUsers(home, nil)
	defer users.Close()
	response = cloudRequest(t, users, first, "GET", historyPath, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "finished") {
		t.Fatalf("restart: %d %s", response.Code, response.Body.String())
	}
}

func TestCloudResumeUsesUserRepository(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	defer users.Close()
	id := uuid.NewString()
	created := cloudRequest(t, users, id, "POST", "/api/projects", map[string]string{"user_id": id, "name": "resume"})
	var project projectDTO
	if err := json.Unmarshal(created.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	created = cloudRequest(t, users, id, "POST", "/api/sessions", map[string]string{"user_id": id, "project_id": project.ProjectID})
	var sess sessionDTO
	if err := json.Unmarshal(created.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	user := users.users[id]
	// Supply a valid runtime catalog; this test exercises persistence, not the network.
	stubActiveModel(t)
	models, err := agentasm.NewModels(user.server.homeDir)
	if err != nil {
		t.Fatal(err)
	}
	user.server.models = models
	user.server.switcher = agentasm.NewDirectModelSwitcher(models)
	llm := &traceFlowLLM{}
	user.server.assemble = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		in.Mode = agentasm.ModeCode
		assembled, err := agentasm.Assemble(ctx, in)
		if err != nil {
			return nil, err
		}
		assembled.Service.LLMClient = llm
		return assembled, nil
	}
	failed := cloudRequest(t, users, id, "POST", "/chat", map[string]string{"session_id": sess.SessionID, "task": "work"})
	if !strings.Contains(failed.Body.String(), "CHAT_FAILED") {
		t.Fatalf("expected interrupted chat: %s", failed.Body.String())
	}
	resumed := cloudRequest(t, users, id, "POST", "/api/sessions/"+sess.SessionID+"/resume", nil)
	if !strings.Contains(resumed.Body.String(), "event: done") {
		t.Fatalf("resume: %d %s", resumed.Code, resumed.Body.String())
	}
}

func TestCloudConcurrentFirstProject(t *testing.T) {
	users := newCloudUsers(t.TempDir(), nil)
	defer users.Close()
	id := uuid.NewString()
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- cloudRequest(t, users, id, "POST", "/api/projects", map[string]string{"user_id": id, "name": "same"}).Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[201] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent creates: %v", counts)
	}
}

func TestCloudConfCopyDir(t *testing.T) {
	previous := config.Current
	t.Cleanup(func() { config.Current = previous })
	home, source := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(layout.Root(home), 0700); err != nil {
		t.Fatal(err)
	}
	serviceSettings := `{"model":"service:service-model","provider_list":[{"provider_name":"service","openai_api_key":"service-key","openai_base_url":"https://service.example/v1","model_list":[{"model_name":"service-model"}]}]}`
	if err := os.WriteFile(layout.UserSettings(home), []byte(serviceSettings), 0600); err != nil {
		t.Fatal(err)
	}
	sourceSettings := `{"model":"custom:custom-model","provider_list":[{"provider_name":"custom","openai_api_key":"custom-key","openai_base_url":"https://custom.example/v1","model_list":[{"model_name":"custom-model"}]}]}`
	if err := os.WriteFile(layout.SettingsFile(source), []byte(sourceSettings), 0600); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(layout.SkillsDir(source), "custom-skill")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	skillContent := "---\nname: custom-skill\ndescription: custom skill\n---\nUse the custom source."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	config.Current, err = config.LoadSettings(home, []string{"-cloud", "-conf_copy_dir=" + source})
	if err != nil {
		t.Fatal(err)
	}
	users := newCloudUsers(home, nil)
	defer users.Close()
	id := uuid.NewString()
	created := cloudRequest(t, users, id, "POST", "/api/projects", map[string]string{"user_id": id, "name": "custom"})
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	user := users.users[id]
	if active := user.server.models.Active(); active.Ref != "custom:custom-model" || active.OpenaiApiKey != "custom-key" {
		t.Fatalf("did not use custom configuration: ref=%s", active.Ref)
	}
	destination := filepath.Join(layout.SkillsRoot(user.server.homeDir), "custom-skill", "SKILL.md")
	if content, err := os.ReadFile(destination); err != nil || string(content) != skillContent {
		t.Fatalf("custom skill copy: %q %v", content, err)
	}
	settingsPath := layout.UserSettings(user.server.homeDir)
	settingsBefore, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	users.Close()
	// Existing users keep their configuration even if the original source is gone.
	config.Current.ConfCopyDir = filepath.Join(source, "removed-source")
	users = newCloudUsers(home, nil)
	defer users.Close()
	listed := cloudRequest(t, users, id, "GET", "/api/projects?user_id="+id, nil)
	if listed.Code != 200 {
		t.Fatalf("existing user: %d %s", listed.Code, listed.Body.String())
	}
	if settingsAfter, err := os.ReadFile(settingsPath); err != nil || string(settingsAfter) != string(settingsBefore) {
		t.Fatalf("existing settings were overwritten: %v", err)
	}
	// An explicitly selected empty directory must not fall back to service defaults.
	config.Current.ConfCopyDir = t.TempDir()
	emptyUsers := newCloudUsers(home, nil)
	defer emptyUsers.Close()
	emptyID := uuid.NewString()
	created = cloudRequest(t, emptyUsers, emptyID, "POST", "/api/projects", map[string]string{"user_id": emptyID, "name": "empty"})
	if created.Code != 201 || emptyUsers.users[emptyID].server.models.Active().Ref != "" {
		t.Fatalf("empty source fallback: %d %s", created.Code, created.Body.String())
	}
}
