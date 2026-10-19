package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/infrastructure/ai_models"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func metadataRequest(t *testing.T, p *proxyServer, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	req.Header.Set("X-USERID", testID)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	return w
}
func responseID(t *testing.T, w *httptest.ResponseRecorder, key string) string {
	t.Helper()
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	return data[key].(string)
}
func TestPersistedAPIsWorkWithoutWakingSandbox(t *testing.T) {
	var calls atomic.Int32
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	p.catalog = config.Conf{Model: "p:a", ProviderList: []config.Provider{{ProviderName: "p", OpenaiApiKey: "NEVER_EXPOSE", OpenaiBaseUrl: "https://example.com", ModelList: []config.Model{
		{ModelName: "a", Limit: &config.ModelLimit{Context: 10000, Output: 1000}},
		{ModelName: "b", Limit: &config.ModelLimit{Context: 20000, Output: 2000}},
	}}}}
	p.skills = t.TempDir()
	if err := os.Mkdir(filepath.Join(p.skills, "example"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.skills, "example", "SKILL.md"), []byte("---\nname: example\ndescription: Example skill\n---\nBody"), 0600); err != nil {
		t.Fatal(err)
	}
	projectID := responseID(t, metadataRequest(t, p, "POST", "/api/projects", map[string]string{"name": "demo"}), "project_id")
	sessionID := responseID(t, metadataRequest(t, p, "POST", "/api/sessions", map[string]string{"project_id": projectID}), "session_id")
	root := filepath.Join(p.root, testID)
	home := filepath.Join(root, "agenthome")
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(home), layout.SessionRoot(home))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	selected, err := repo.GetSession(context.Background(), sessionID)
	if err != nil || selected.WorkDir != "/workplace/demo" {
		t.Fatalf("sandbox path: %+v %v", selected, err)
	}
	snapshot, err := repo.GetRequestContext(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.NewSession(sessionID, selected.WorkDir)
	sess.RequestContext = snapshot
	sys := sess.UpsertSysMessage("system")
	sess.Revision, err = repo.CommitCreateMessage(context.Background(), sessionID, sess.Snapshot(), sys, sys)
	if err != nil {
		t.Fatal(err)
	}
	chatID := uuid.NewString()
	msg := sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "test", ChatID: chatID}
	candidate, err := sess.WithAppendedMessage(&msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitCreateMessage(context.Background(), sessionID, candidate.Snapshot(), msg, msg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "result.txt"), []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertChatFiles(context.Background(), sessionID, chatID, []session.ChatFile{{Path: "demo/result.txt", ChangeType: "created"}}); err != nil {
		t.Fatal(err)
	}
	jobID := "eval_" + sessionID
	if _, err := repo.CreateEvaluationJob(context.Background(), evaluation.Job{ID: jobID, SourceSessionID: sessionID, UserID: testID, WorkDir: selected.WorkDir, Requirement: "check", Status: evaluation.StatusSucceeded, SnapshotHistory: "/workplace/demo/eval/history.jsonl"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.EvaluationRoot(home), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.EvaluationReport(home, jobID), []byte("# report"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.TracingLog(home, sessionID)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.TracingLog(home, sessionID), nil, 0600); err != nil {
		t.Fatal(err)
	}
	runtime.state = &sandboxState{Name: sandboxName(testID), Phase: "Stopped"}
	idleSince := time.Now().Add(-time.Hour)
	p.user(testID).lastUsed = idleSince
	urls := []string{"/skills", "/api/models", "/api/projects", "/api/sessions?project_id=" + projectID,
		"/api/sessions/" + sessionID + "/messages", "/api/sessions/" + sessionID + "/context", "/api/sessions/" + sessionID + "/performance",
		"/api/sessions/" + sessionID + "/chats/" + chatID + "/files.zip", "/api/sessions/" + sessionID + "/chats/" + chatID + "/file?path=demo/result.txt",
		"/api/evaluations?session_id=" + sessionID, "/api/sessions/" + sessionID + "/evaluations/" + jobID + "/report"}
	for _, url := range urls {
		w := metadataRequest(t, p, "GET", url, nil)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", url, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "NEVER_EXPOSE") || strings.Contains(w.Body.String(), root) {
			t.Errorf("%s leaked private metadata", url)
		}
	}
	worker, err := ai_models.New(home, p.catalog)
	if err != nil {
		t.Fatal(err)
	}
	before, err := worker.ForTask()
	if err != nil {
		t.Fatal(err)
	}
	if w := metadataRequest(t, p, "POST", "/api/model", map[string]string{"provider": "p", "model": "b"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	next, err := worker.ForTask()
	if err != nil {
		t.Fatal(err)
	}
	if before.Active().Ref != "p:a" || next.Active().Ref != "p:b" {
		t.Fatal("model change did not apply to next task only")
	}
	if calls.Load() != 0 || runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("metadata woke sandbox")
	}
	if !p.user(testID).lastUsed.Equal(idleSince) {
		t.Fatal("metadata refreshed idle timer")
	}
}
func TestDeleteSessionChecksBackgroundWorkWithoutStartingSandbox(t *testing.T) {
	var active atomic.Int32
	active.Store(1)
	p, runtime := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"active":`+string(rune('0'+active.Load()))+`}`)
	}))
	project := responseID(t, metadataRequest(t, p, "POST", "/api/projects", map[string]string{"name": "demo"}), "project_id")
	id := responseID(t, metadataRequest(t, p, "POST", "/api/sessions", map[string]string{"project_id": project}), "session_id")
	runtime.state = &sandboxState{Name: sandboxName(testID), Phase: "Ready"}
	if w := metadataRequest(t, p, "DELETE", "/api/sessions/"+id, nil); w.Code != 409 {
		t.Fatalf("active evaluation: %d %s", w.Code, w.Body.String())
	}
	runtime.state.Phase = "Stopped"
	if w := metadataRequest(t, p, "DELETE", "/api/sessions/"+id, nil); w.Code != 204 {
		t.Fatalf("stopped delete: %d %s", w.Code, w.Body.String())
	}
	if runtime.creates != 0 || runtime.starts != 0 {
		t.Fatal("deletion woke sandbox")
	}
}
func TestStorageRPCRequiresUserScopedCredential(t *testing.T) {
	p, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server := httptest.NewServer(p)
	defer server.Close()
	remote := sessionrepo.NewRemote(server.URL+"/internal/store", testID, "wrong")
	if _, err := remote.ListProjects(context.Background(), testID); err == nil {
		t.Fatal("accepted invalid storage token")
	}
	remote.Token = p.storeToken(testID)
	if _, err := remote.ListProjects(context.Background(), testID); err != nil {
		t.Fatal(err)
	}
	remote.UserID = "22222222-2222-4222-8222-222222222222"
	if _, err := remote.ListProjects(context.Background(), remote.UserID); err == nil {
		t.Fatal("accepted another user's credential")
	}
}
