package run_sse

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

func (s *server) handleDownloadEvaluationReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	sessionID, jobID := r.PathValue("session_id"), r.PathValue("job_id")
	for _, id := range []string{sessionID, jobID} {
		if !fs.ValidPath(id) || id == "." || strings.ContainsAny(id, "/\\") {
			writeJSONError(w, http.StatusBadRequest, "invalid session or evaluation ID")
			return
		}
	}
	if s.catalog == nil || s.evaluations == nil {
		writeJSONError(w, http.StatusInternalServerError, "evaluation repository is unavailable")
		return
	}
	source, err := s.catalog.GetSession(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sessionrepo.ErrSessionNotFound) {
			writeJSONError(w, http.StatusNotFound, "session not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "cannot load session")
		}
		return
	}
	jobs, err := s.evaluations.ListEvaluationJobs(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cannot load evaluation")
		return
	}
	var selected *evaluation.Job
	for i := range jobs {
		job := &jobs[i]
		if job.ID == jobID && job.SourceSessionID == sessionID && job.UserID == source.UserID {
			selected = job
			break
		}
	}
	if selected == nil {
		writeJSONError(w, http.StatusNotFound, "evaluation not found")
		return
	}
	if selected.Status != evaluation.StatusSucceeded {
		writeJSONError(w, http.StatusConflict, "evaluation report is not ready")
		return
	}

	anchorPath := s.homeDir
	if s.userRoot != "" {
		anchorPath = s.userRoot
	}
	rel, err := filepath.Rel(anchorPath, layout.EvaluationRoot(s.homeDir))
	if err != nil || !fs.ValidPath(filepath.ToSlash(rel)) || rel == "." {
		writeJSONError(w, http.StatusForbidden, "invalid report directory")
		return
	}
	anchor, err := os.OpenRoot(anchorPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "report directory unavailable")
		return
	}
	defer anchor.Close()
	reports, err := anchor.OpenRoot(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSONError(w, http.StatusNotFound, "evaluation report not found")
		} else {
			writeJSONError(w, http.StatusForbidden, "report directory unavailable")
		}
		return
	}
	defer reports.Close()
	// Derive the filename from the authorized job, never from a client or stored absolute path.
	name := filepath.Base(layout.EvaluationReport(s.homeDir, jobID))
	info, err := reports.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		writeJSONError(w, http.StatusNotFound, "evaluation report not found")
		return
	}
	if err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusForbidden, "report is not a readable regular file")
		return
	}
	input, err := reports.OpenFile(name, downloadOpenFlags, 0)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "evaluation report unavailable")
		return
	}
	defer input.Close()
	info, err = input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusForbidden, "report is not a regular file")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), io.NewSectionReader(input, 0, info.Size()))
}
