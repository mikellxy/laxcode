package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/config"
	"github.com/mikellxy/laxcode/internal/infrastructure/taskevents"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func seedTaskSession(t *testing.T, s *server, id, work string) {
	t.Helper()
	repo := s.tasks.(*sessionrepo.SqliteSessionRepo)
	projectID := "project-" + filepath.Base(work)
	if _, err := repo.CreateProject(context.Background(), projectID, "user", filepath.Base(work), work); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSession(context.Background(), id, "user", projectID, "", work, "code"); err != nil {
		t.Fatal(err)
	}
	s.catalog = repo
}
func submitTask(t *testing.T, s *server, path, body string) session.Task {
	t.Helper()
	if strings.HasSuffix(path, "/resume") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/sessions/"), "/resume")
		latest, err := s.tasks.GetLatestTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if latest != nil {
			body = `{"task_id":"` + latest.ID + `"}`
		}
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("submit %s: %d %s", path, w.Code, w.Body.String())
	}
	var accepted struct {
		ID string `json:"task_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || accepted.ID == "" {
		t.Fatalf("invalid acceptance: %s %v", w.Body, err)
	}
	return waitTask(t, s, accepted.ID)
}
func waitTask(t *testing.T, s *server, id string) session.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := s.tasks.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !task.Active() {
			s.taskWG.Wait()
			return task
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return session.Task{}
}
func replayTask(t *testing.T, s *server, task session.Task, w http.ResponseWriter) {
	t.Helper()
	s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/tasks/"+task.ID+"/events", nil))
}
func TestAsyncAssemblyFailureAndTerminalReplay(t *testing.T) {
	stubActiveModel(t)
	s := newTestServer(t, t.TempDir(), false)
	seedTaskSession(t, s, "s1", t.TempDir())
	s.assemble = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
		return nil, errors.New("assembly failed")
	}
	task := submitTask(t, s, "/chat", `{"session_id":"s1","task":"hello"}`)
	if task.Status != "fail" || task.ErrorMessage != "assembly failed" {
		t.Fatalf("%+v", task)
	}
	for range 2 {
		w := httptest.NewRecorder()
		replayTask(t, s, task, w)
		if !strings.Contains(w.Body.String(), "task_state") || !strings.Contains(w.Body.String(), "assembly failed") {
			t.Fatal(w.Body.String())
		}
	}
}
func TestAsyncRequestCancellationProjectExclusionAndStop(t *testing.T) {
	stubActiveModel(t)
	s := newTestServer(t, t.TempDir(), false)
	seedTaskSession(t, s, "s1", t.TempDir())
	selected, _ := s.catalog.GetSession(context.Background(), "s1")
	repo := s.tasks.(*sessionrepo.SqliteSessionRepo)
	if _, err := repo.CreateSession(context.Background(), "s2", "user", selected.ProjectID, "", selected.WorkDir, "code"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.assemble = func(ctx context.Context, _ agentasm.Input) (*agentasm.Assembled, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := httptest.NewRecorder()
	s.handleChat(w, httptest.NewRequest("POST", "/chat", strings.NewReader(`{"session_id":"s1","task":"hello"}`)).WithContext(ctx))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cancel()
	<-started
	task, _ := s.tasks.GetLatestTask(context.Background(), "s1")
	if !task.Active() || s.activeTasks.Load() != 1 {
		t.Fatalf("request canceled task: %+v", task)
	}
	busy := httptest.NewRecorder()
	s.handleChat(busy, httptest.NewRequest("POST", "/chat", strings.NewReader(`{"session_id":"s2","task":"hello"}`)))
	if busy.Code != 409 || !strings.Contains(busy.Body.String(), "PROJECT_BUSY") {
		t.Fatal(busy.Body.String())
	}
	stop := httptest.NewRecorder()
	s.routes().ServeHTTP(stop, httptest.NewRequest("POST", "/api/tasks/"+task.ID+"/cancel", nil))
	done := waitTask(t, s, task.ID)
	if done.Status != "fail" || done.ErrorCode != "USER_STOPPED" {
		t.Fatalf("%+v", done)
	}
}

func TestTaskSSEDisconnectReplayAndTickerTerminal(t *testing.T) {
	s := newTestServer(t, t.TempDir(), false)
	seedTaskSession(t, s, "s1", t.TempDir())
	task, err := s.tasks.CreateTask(context.Background(), "replay", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.tasks.StartTask(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 2; i++ {
		if err := s.events.Publish(context.Background(), taskevents.Event{TaskID: task.ID, EventID: i, MessageSeq: 2, Type: "message", Data: json.RawMessage(`{"delta":"hello"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	// A lost subscription must not change task state or consume another page's output.
	ctx, cancel := context.WithCancel(context.Background())
	w := &disconnectResponse{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	s.routes().ServeHTTP(w, httptest.NewRequest("GET", "/api/tasks/"+task.ID+"/events", nil).WithContext(ctx))
	current, _ := s.tasks.GetTask(context.Background(), task.ID)
	if !current.Active() {
		t.Fatal("disconnect stopped task")
	}
	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() { defer close(done); replayTask(t, s, task, response) }()
	if err := s.tasks.FinishTask(context.Background(), task.ID, "fail", "TEST", "interrupted", 2); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("ticker did not finish terminal stream")
	}
	if strings.Count(response.Body.String(), "event: message") != 2 || !strings.Contains(response.Body.String(), "interrupted") {
		t.Fatal(response.Body.String())
	}
	// A newer task makes old subscription and resume attempts stale.
	if _, err := s.tasks.CreateTask(context.Background(), "new-task", "s1"); err != nil {
		t.Fatal(err)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest("GET", "/api/tasks/"+task.ID+"/events", nil),
		httptest.NewRequest("POST", "/api/sessions/s1/resume", strings.NewReader(`{"task_id":"replay"}`)),
	} {
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, request)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "TASK_CHANGED") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

type disconnectResponse struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *disconnectResponse) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	w.cancel()
	return n, err
}

func TestCloudRestartFailsActiveTasksAndPreservesLastReplay(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "agenthome")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	cloud, err := newCloudService(home, id, config.Conf{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := cloud.repo
	ctx := context.Background()
	if _, err := repo.CreateProject(ctx, "p", id, "demo", filepath.Join(root, "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSession(ctx, "s", id, "p", "", filepath.Join(root, "demo"), "code"); err != nil {
		t.Fatal(err)
	}
	old, err := repo.CreateTask(ctx, "old", "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := cloud.server.events.Publish(ctx, taskevents.Event{TaskID: old.ID, EventID: 1, Type: "start", Data: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishTask(ctx, old.ID, "fail", "TEST", "failed", 1); err != nil {
		t.Fatal(err)
	}
	task, err := repo.CreateTask(ctx, "latest", "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := cloud.server.events.Publish(ctx, taskevents.Event{TaskID: task.ID, EventID: 1, MessageSeq: 3, Type: "reasoning", Data: json.RawMessage(`{"delta":"partial"}`)}); err != nil {
		t.Fatal(err)
	}
	cloud.Close() // Simulates a persisted active record whose owner is gone.
	cloud, err = newCloudService(home, id, config.Conf{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cloud.Close()
	recovered, err := cloud.repo.GetTask(ctx, task.ID)
	if err != nil || recovered.Status != "fail" || recovered.ErrorCode != "SERVICE_RESTARTED" || recovered.FinalEventID != 1 {
		t.Fatalf("%+v %v", recovered, err)
	}
	if last, err := cloud.server.events.Last(ctx, old.ID); err != nil || last != 0 {
		t.Fatalf("old events retained: %d %v", last, err)
	}
	if last, err := cloud.server.events.Last(ctx, task.ID); err != nil || last != 1 {
		t.Fatalf("latest events lost: %d %v", last, err)
	}
}
