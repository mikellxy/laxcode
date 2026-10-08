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
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func TestCreateEvaluationReturnsBeforeAsyncFailure(t *testing.T) {
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
	s.assembleEvaluation = func(context.Context, agentasm.Input) (*agentasm.Assembled, error) {
		return nil, errors.New("judge unavailable")
	}
	t.Cleanup(s.stopEvaluations)

	body, err := json.Marshal(map[string]string{"session_id": "source-1", "work_dir": workDir, "requirement": "check tests"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	s.handleCreateEvaluation(recorder, httptest.NewRequest(http.MethodPost, "/api/evaluations", strings.NewReader(string(body))))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		jobs, err := repo.ListEvaluationJobs(context.Background(), "source-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 1 && jobs[0].Status == evaluation.StatusFailed {
			if !strings.Contains(jobs[0].Error, "judge unavailable") {
				t.Fatalf("job error = %q", jobs[0].Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("evaluation did not fail asynchronously: %+v", jobs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(layout.EvaluationSnapshotHistory(workDir, "source-1")); err != nil {
		t.Fatalf("snapshot history: %v", err)
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
