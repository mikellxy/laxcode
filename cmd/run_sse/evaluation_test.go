package run_sse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func newEvaluationTestServer(t *testing.T) (*server, *sessionrepo.SqliteSessionRepo, string) {
	t.Helper()
	stubActiveModel(t)
	homeDir := t.TempDir()
	workDir := t.TempDir()
	repo, err := sessionrepo.NewSqliteSessionRepo(layout.SessionDB(homeDir), layout.SessionRoot(homeDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if _, err := repo.CreateSession(context.Background(), "source-1", "user-1", "project-1", "", workDir, string(agentasm.ModeCode)); err != nil {
		t.Fatal(err)
	}
	sourceDir := layout.SessionDir(homeDir, "source-1")
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.SessionHistory(homeDir, "source-1"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, homeDir, false)
	s.catalog = repo
	s.evaluations = repo
	s.contextRepo = repo
	s.tracer = telemetry.NoopTracer()
	t.Cleanup(s.stopEvaluations)
	return s, repo, workDir
}

func evaluationRequest(ctx context.Context) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/evaluations", strings.NewReader(`{"session_id":"source-1","requirement":"check tests"}`)).WithContext(ctx)
}

func assertEvaluationLocksReleased(t *testing.T, s *server, workDir string) {
	t.Helper()
	for key, locks := range map[string]*sessionLocks{"source-1": s.locks, filepath.Clean(workDir): s.projectLocks} {
		unlock, ok := locks.TryLock(key)
		if !ok {
			t.Fatalf("lock %q was not released", key)
		}
		unlock()
	}
}

func TestCreateEvaluationConflictDoesNotCreateJob(t *testing.T) {
	for _, gate := range []string{"session", "project", evaluation.StatusQueued, evaluation.StatusRunning} {
		t.Run(gate, func(t *testing.T) {
			s, repo, workDir := newEvaluationTestServer(t)
			wantJobs := 0
			unlock := func() {}
			switch gate {
			case "session":
				unlock, _ = s.locks.TryLock("source-1")
			case "project":
				unlock, _ = s.lockChatProject(workDir)
			default:
				_, err := repo.CreateEvaluationJob(context.Background(), evaluation.Job{
					ID: "existing", SourceSessionID: "source-1", UserID: "user-1", WorkDir: workDir,
					Requirement: "check tests", Status: gate, SnapshotHistory: layout.EvaluationSnapshotHistory(workDir, "source-1"),
				})
				if err != nil {
					t.Fatal(err)
				}
				wantJobs = 1
			}
			recorder := httptest.NewRecorder()
			s.handleCreateEvaluation(recorder, evaluationRequest(context.Background()))
			unlock()
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1"); err != nil || len(jobs) != wantJobs {
				t.Fatalf("busy request created jobs: %+v, %v", jobs, err)
			}
			assertEvaluationLocksReleased(t, s, workDir)
		})
	}
}

func TestCreateEvaluationInitializationFailure(t *testing.T) {
	for _, failure := range []string{"snapshot", "history", "assembly", "panic"} {
		t.Run(failure, func(t *testing.T) {
			s, repo, workDir := newEvaluationTestServer(t)
			s.assembleEvaluation = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
				if failure == "panic" {
					panic("judge panicked")
				}
				return nil, errors.New("judge unavailable")
			}
			switch failure {
			case "snapshot":
				if err := os.RemoveAll(layout.SessionDir(s.homeDir, "source-1")); err != nil {
					t.Fatal(err)
				}
			case "history":
				if err := os.Remove(layout.SessionHistory(s.homeDir, "source-1")); err != nil {
					t.Fatal(err)
				}
			}
			recorder := httptest.NewRecorder()
			s.handleCreateEvaluation(recorder, evaluationRequest(context.Background()))
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1")
			if err != nil || len(jobs) != 1 || jobs[0].Status != evaluation.StatusFailed || jobs[0].Error == "" {
				t.Fatalf("initialization failure was not persisted: %+v, %v", jobs, err)
			}
			if s.activeEvaluations.Load() != 0 {
				t.Fatal("failed initialization started a worker")
			}
			assertEvaluationLocksReleased(t, s, workDir)
		})
	}
}

func TestEvaluationInitializationAndBackgroundLifetime(t *testing.T) {
	s, repo, workDir := newEvaluationTestServer(t)
	if _, err := repo.CreateSession(context.Background(), "source-2", "user-1", "project-1", "", workDir, string(agentasm.ModeCode)); err != nil {
		t.Fatal(err)
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	initialized := make(chan *agentasm.Assembled, 1)
	allowInit, allowRun := make(chan struct{}), make(chan struct{})
	releaseInit, releaseRun := sync.OnceFunc(func() { close(allowInit) }), sync.OnceFunc(func() { close(allowRun) })
	t.Cleanup(releaseRun)
	t.Cleanup(releaseInit)
	workerContext := make(chan context.Context, 1)
	var input agentasm.Input
	var initializationContext context.Context
	s.assembleEvaluation = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		input = in
		initializationContext = ctx
		assembled, err := agentasm.Assemble(ctx, in)
		if err != nil {
			initialized <- nil
			return nil, err
		}
		assembled.Service.LLMClient = &fileTrackingLLM{generate: func(ctx context.Context) (*sharedkernel.Message, error) {
			workerContext <- ctx
			select {
			case <-allowRun:
				return &sharedkernel.Message{Role: sharedkernel.RoleAssistant, Content: "# Report\n"}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}
		initialized <- assembled
		<-allowInit
		return assembled, nil
	}
	recorder := httptest.NewRecorder()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		s.handleCreateEvaluation(recorder, evaluationRequest(requestCtx))
	}()
	select {
	case assembled := <-initialized:
		if assembled == nil || assembled.Session.WorkDir != workDir || assembled.Session.ID == "source-1" || input.SessionID != "" || input.Mode != agentasm.ModeEvaluate {
			t.Fatalf("unexpected evaluation assembly: %+v", assembled)
		}
		if initializationContext.Done() != requestCtx.Done() {
			t.Fatal("initialization does not use the request context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("evaluation did not initialize")
	}
	select {
	case <-returned:
		t.Fatal("request returned before initialization completed")
	default:
	}
	snapshot, err := os.ReadFile(layout.EvaluationSnapshotHistory(workDir, "source-1"))
	if err != nil || string(snapshot) != "{}\n" {
		t.Fatalf("snapshot not ready during assembly: %q, %v", snapshot, err)
	}
	mux := s.routes()
	for _, id := range []string{"source-1", "source-2"} {
		for _, path := range []string{"/chat", "/api/sessions/" + id + "/resume", "/api/evaluations"} {
			body, _ := json.Marshal(map[string]string{"session_id": id, "task": "hi"})
			if path == "/api/evaluations" {
				body, _ = json.Marshal(map[string]string{"session_id": id, "requirement": "check tests"})
			}
			busy := httptest.NewRecorder()
			mux.ServeHTTP(busy, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
			if busy.Code != http.StatusConflict {
				t.Fatalf("%s (%s): status=%d body=%s", path, id, busy.Code, busy.Body.String())
			}
		}
		jobs, err := repo.ListEvaluationJobs(context.Background(), id)
		if err != nil || (id == "source-1" && (len(jobs) != 1 || jobs[0].Status != evaluation.StatusQueued)) || (id == "source-2" && len(jobs) != 0) {
			t.Fatalf("unexpected jobs during initialization: %+v, %v", jobs, err)
		}
	}
	releaseInit()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("initialized request did not return")
	}
	if recorder.Code != http.StatusAccepted || s.activeEvaluations.Load() != 1 {
		t.Fatalf("status=%d active=%d body=%s", recorder.Code, s.activeEvaluations.Load(), recorder.Body.String())
	}
	assertEvaluationLocksReleased(t, s, workDir)
	select {
	case ctx := <-workerContext:
		cancelRequest()
		if ctx.Err() != nil {
			t.Fatalf("background inherited request cancellation: %v", ctx.Err())
		}
		if id := telemetry.ChatIDFromContext(ctx); id == "" || id != telemetry.ChatIDFromContext(initializationContext) {
			t.Fatalf("background lost evaluation chat ID: %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background evaluation did not start")
	}
	duplicate := httptest.NewRecorder()
	s.handleCreateEvaluation(duplicate, evaluationRequest(context.Background()))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	releaseRun()
	s.evaluationWG.Wait()
	jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1")
	if err != nil || len(jobs) != 1 || jobs[0].Status != evaluation.StatusSucceeded || jobs[0].WorkDir != workDir || s.activeEvaluations.Load() != 0 {
		t.Fatalf("unexpected completed jobs: %+v, %v", jobs, err)
	}
	if report, err := os.ReadFile(jobs[0].ReportPath); err != nil || string(report) != "# Report\n" {
		t.Fatalf("report=%q error=%v", report, err)
	}
}

func TestEvaluationCanceledInitializationCleansUpAndFailsJob(t *testing.T) {
	s, repo, workDir := newEvaluationTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleanups := 0
	s.assembleEvaluation = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		assembled, err := agentasm.Assemble(ctx, in)
		if err != nil {
			return nil, err
		}
		cleanup := assembled.Cleanup
		assembled.Cleanup = func() { cleanups++; cleanup() }
		cancel()
		return assembled, nil
	}
	recorder := httptest.NewRecorder()
	s.handleCreateEvaluation(recorder, evaluationRequest(ctx))
	if recorder.Code != http.StatusInternalServerError || cleanups != 1 || s.activeEvaluations.Load() != 0 {
		t.Fatalf("status=%d cleanups=%d active=%d", recorder.Code, cleanups, s.activeEvaluations.Load())
	}
	jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1")
	if err != nil || len(jobs) != 1 || jobs[0].Status != evaluation.StatusFailed || !strings.Contains(jobs[0].Error, context.Canceled.Error()) {
		t.Fatalf("canceled initialization was not persisted: %+v, %v", jobs, err)
	}
	assertEvaluationLocksReleased(t, s, workDir)
}

func TestStopEvaluationCancelsBackgroundAndFailsJob(t *testing.T) {
	s, repo, _ := newEvaluationTestServer(t)
	started := make(chan struct{})
	s.assembleEvaluation = func(ctx context.Context, in agentasm.Input) (*agentasm.Assembled, error) {
		assembled, err := agentasm.Assemble(ctx, in)
		if err != nil {
			return nil, err
		}
		assembled.Service.LLMClient = &fileTrackingLLM{generate: func(ctx context.Context) (*sharedkernel.Message, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		return assembled, nil
	}
	recorder := httptest.NewRecorder()
	s.handleCreateEvaluation(recorder, evaluationRequest(context.Background()))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background evaluation did not start")
	}
	s.stopEvaluations()
	jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1")
	if err != nil || len(jobs) != 1 || jobs[0].Status != evaluation.StatusFailed || !strings.Contains(jobs[0].Error, context.Canceled.Error()) || s.activeEvaluations.Load() != 0 {
		t.Fatalf("canceled background was not persisted: %+v, %v", jobs, err)
	}
}

type failingEvaluationUpdate struct{ evaluation.Repository }

func (failingEvaluationUpdate) UpdateEvaluationJob(ctx context.Context, _, _, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("store unavailable")
}

func TestFinishEvaluationLogsWriteFailure(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)
	s := &server{evaluations: failingEvaluationUpdate{}}
	s.finishEvaluationJob("eval-1", evaluation.StatusFailed, "", "initialization failed")
	if log := output.String(); !strings.Contains(log, "store unavailable") || !strings.Contains(log, "eval-1") || !strings.Contains(log, "status=failed") {
		t.Fatalf("missing failure log: %s", log)
	}
}

func TestCopySessionSnapshotReplacesHistory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	workDir := filepath.Join(root, "project")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "history.jsonl"), []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(workDir, "eval", "session-1")
	if err := copySessionSnapshot(source, destination, workDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "history.jsonl"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copySessionSnapshot(source, destination, workDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second\n" {
		t.Fatalf("snapshot history = %q", got)
	}
}

func TestCopySessionSnapshotRejectsOverlappingDestination(t *testing.T) {
	source := t.TempDir()
	workDir := filepath.Join(source, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "history.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := copySessionSnapshot(source, filepath.Join(workDir, "eval", "source"), workDir)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestWriteEvaluationReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eval", "report.md")
	if err := writeEvaluationReport(path, "# Report\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# Report\n" {
		t.Fatalf("report = %q", got)
	}
}
