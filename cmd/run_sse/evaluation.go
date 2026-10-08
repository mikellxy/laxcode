package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/domain/prompt"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

const maxEvaluationRequirementBytes = 64 << 10

type createEvaluationRequest struct {
	SessionID   string `json:"session_id"`
	WorkDir     string `json:"work_dir"`
	Requirement string `json:"requirement"`
}

type evaluationJobDTO struct {
	ID              string     `json:"id"`
	SourceSessionID string     `json:"source_session_id"`
	Requirement     string     `json:"requirement"`
	Status          string     `json:"status"`
	ReportPath      string     `json:"report_path,omitempty"`
	Error           string     `json:"error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type evaluationJobListDTO struct {
	Jobs []evaluationJobDTO `json:"jobs"`
}

func evaluationToDTO(job evaluation.Job) evaluationJobDTO {
	return evaluationJobDTO{
		ID: job.ID, SourceSessionID: job.SourceSessionID, Requirement: job.Requirement,
		Status: job.Status, ReportPath: job.ReportPath, Error: job.Error,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt, CompletedAt: job.CompletedAt,
	}
}

func (s *server) handleCreateEvaluation(w http.ResponseWriter, r *http.Request) {
	if s.evaluations == nil || s.catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "evaluation repository is unavailable")
		return
	}
	if s.modelMissing() {
		writeJSONProtocolError(w, http.StatusConflict, ErrorCodeModelRequired,
			"no model is configured; add one with the model picker (gear) first", "")
		return
	}
	var req createEvaluationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Requirement = strings.TrimSpace(req.Requirement)
	if req.SessionID == "" || req.Requirement == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id and requirement are required")
		return
	}
	if len(req.Requirement) > maxEvaluationRequirementBytes {
		writeJSONError(w, http.StatusBadRequest, "requirement is too large")
		return
	}
	workDir, err := normalizeWorkDir(req.WorkDir)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	source, err := s.catalog.GetSession(r.Context(), req.SessionID)
	if errors.Is(err, sessionrepo.ErrSessionNotFound) {
		writeJSONError(w, http.StatusNotFound, "session not found: "+req.SessionID)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load session failed: "+err.Error())
		return
	}
	if source.Mode != string(agentasm.ModeCode) {
		writeJSONError(w, http.StatusConflict, "only code sessions can be evaluated")
		return
	}
	if filepath.Clean(source.WorkDir) != workDir {
		writeJSONError(w, http.StatusConflict, "work_dir does not match the selected session")
		return
	}

	unlock, ok := s.evaluationLocks.TryLock(req.SessionID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "an evaluation is already being created for this session")
		return
	}
	defer unlock()
	existing, err := s.evaluations.ListEvaluationJobs(r.Context(), req.SessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list evaluation jobs failed: "+err.Error())
		return
	}
	for _, job := range existing {
		if job.Status == evaluation.StatusQueued || job.Status == evaluation.StatusRunning {
			writeJSONError(w, http.StatusConflict, "an evaluation is already running for this session")
			return
		}
	}

	now := time.Now().UTC()
	jobID := fmt.Sprintf("eval_%s_%s", req.SessionID, now.Format("20060102_150405_000000000"))
	job, err := s.evaluations.CreateEvaluationJob(r.Context(), evaluation.Job{
		ID: jobID, SourceSessionID: req.SessionID, UserID: source.UserID,
		WorkDir: workDir, Requirement: req.Requirement, Status: evaluation.StatusQueued,
		SnapshotHistory: layout.EvaluationSnapshotHistory(workDir, req.SessionID),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "create evaluation job failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(evaluationToDTO(job))
	s.startEvaluation(job)
}

func (s *server) handleListEvaluations(w http.ResponseWriter, r *http.Request) {
	if s.evaluations == nil {
		writeJSONError(w, http.StatusInternalServerError, "evaluation repository is unavailable")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	jobs, err := s.evaluations.ListEvaluationJobs(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list evaluation jobs failed: "+err.Error())
		return
	}
	response := evaluationJobListDTO{Jobs: make([]evaluationJobDTO, len(jobs))}
	for i := range jobs {
		response.Jobs[i] = evaluationToDTO(jobs[i])
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *server) startEvaluation(job evaluation.Job) {
	s.evaluationWG.Add(1)
	go func() {
		defer s.evaluationWG.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				s.finishEvaluationJob(job.ID, evaluation.StatusFailed, "", fmt.Sprintf("evaluation worker panicked: %v", recovered))
			}
		}()
		if err := s.runEvaluation(s.evaluationCtx, job); err != nil {
			s.finishEvaluationJob(job.ID, evaluation.StatusFailed, "", err.Error())
		}
	}()
}

func (s *server) runEvaluation(ctx context.Context, job evaluation.Job) error {
	ctx = telemetry.ContextWithChatID(ctx, uuid.NewString())
	if err := s.evaluations.UpdateEvaluationJob(ctx, job.ID, evaluation.StatusRunning, "", ""); err != nil {
		return err
	}
	sourceDir := layout.SessionDir(s.homeDir, job.SourceSessionID)
	snapshotDir := layout.EvaluationSnapshotDir(job.WorkDir, job.SourceSessionID)
	copyErr := func() error {
		unlock, ok := s.locks.TryLock(job.SourceSessionID)
		if !ok {
			return errors.New("source session is busy")
		}
		defer unlock()
		return copySessionSnapshot(sourceDir, snapshotDir, job.WorkDir)
	}()
	if copyErr != nil {
		return fmt.Errorf("copy evaluation snapshot: %w", copyErr)
	}
	info, err := os.Stat(job.SnapshotHistory)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("history is not a regular file")
		}
		return fmt.Errorf("validate evaluation history: %w", err)
	}
	reportPath := layout.EvaluationReport(s.homeDir, job.ID)

	s.switcher.RLock()
	assembled, err := s.assembleEvaluation(ctx, agentasm.Input{
		Models: s.models, Mode: agentasm.ModeEvaluate, WorkDir: job.WorkDir, HomeDir: s.homeDir, Tracer: s.tracer,
		ReadRoots: []string{snapshotDir}, SessionID: job.ID,
		SystemPrompt: prompt.GetEvaluateSysPrompt(job.SnapshotHistory),
	})
	if err != nil {
		s.switcher.RUnlock()
		return fmt.Errorf("assemble evaluator: %w", err)
	}
	msg, runErr := assembled.Service.Chat(ctx,
		prompt.GetEvaluateUserPrompt(job.SnapshotHistory, job.Requirement, reportPath))
	assembled.Cleanup()
	s.switcher.RUnlock()
	if runErr != nil {
		return fmt.Errorf("run evaluator: %w", runErr)
	}
	if msg == nil || strings.TrimSpace(msg.Content) == "" {
		return errors.New("evaluator returned an empty report")
	}
	if err := writeEvaluationReport(reportPath, msg.Content); err != nil {
		return fmt.Errorf("write evaluation report: %w", err)
	}
	return s.evaluations.UpdateEvaluationJob(ctx, job.ID, evaluation.StatusSucceeded, reportPath, "")
}

func (s *server) finishEvaluationJob(id, status, reportPath, errorMessage string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.evaluations.UpdateEvaluationJob(ctx, id, status, reportPath, errorMessage)
}

func (s *server) stopEvaluations() {
	if s.evaluationCancel != nil {
		s.evaluationCancel()
	}
	done := make(chan struct{})
	go func() {
		s.evaluationWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
	}
}

func copySessionSnapshot(sourceDir, destinationDir, workDir string) error {
	sourceInfo, err := os.Stat(sourceDir)
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() {
		return errors.New("source session path is not a directory")
	}
	realSourceDir, err := filepath.EvalSymlinks(sourceDir)
	if err != nil {
		return fmt.Errorf("resolve source session directory: %w", err)
	}
	realWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return fmt.Errorf("resolve workdir: %w", err)
	}
	evalRoot := filepath.Join(workDir, layout.EvaluationDirName)
	if err := os.MkdirAll(evalRoot, 0o755); err != nil {
		return err
	}
	realEvalRoot, err := filepath.EvalSymlinks(evalRoot)
	if err != nil {
		return fmt.Errorf("resolve evaluation directory: %w", err)
	}
	if !pathWithin(realWorkDir, realEvalRoot) {
		return errors.New("evaluation directory resolves outside workdir")
	}
	if pathWithin(realSourceDir, realEvalRoot) || pathWithin(realEvalRoot, realSourceDir) {
		return errors.New("evaluation snapshot source and destination overlap")
	}
	tempDir, err := os.MkdirTemp(realEvalRoot, ".snapshot-")
	if err != nil {
		return err
	}
	keepTemp := false
	defer func() {
		if !keepTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()
	if err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil || relPath == "." {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("session snapshot contains symlink: %s", relPath)
		}
		target := filepath.Join(tempDir, relPath)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported session entry: %s", relPath)
		}
		return copyRegularFile(path, target, info.Mode().Perm())
	}); err != nil {
		return err
	}
	finalDir := filepath.Join(realEvalRoot, filepath.Base(destinationDir))
	if err := os.RemoveAll(finalDir); err != nil {
		return err
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return err
	}
	keepTemp = true
	return nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func writeEvaluationReport(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".evaluation-report-*.md")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.WriteString(temp, content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
