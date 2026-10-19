package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/tracing/filetrace"
)

func (s *API) HandlePerformance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	sessionID := r.PathValue("session_id")
	if _, err := uuid.Parse(sessionID); err != nil {
		writeJSONError(w, http.StatusBadRequest, "session_id must be a UUID")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if s.Catalog == nil {
		writeJSONError(w, http.StatusInternalServerError, "session repository is unavailable")
		return
	}
	if _, err := s.Catalog.GetSession(ctx, sessionID); err != nil {
		if errors.Is(err, sessionrepo.ErrSessionNotFound) {
			writeJSONError(w, http.StatusNotFound, "session not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "cannot load session")
		}
		return
	}
	missing := func() {
		writeJSONProtocolError(w, http.StatusNotFound, "TRACE_LOG_UNAVAILABLE", "本地 tracing 日志不存在；可能尚无记录，或服务使用了 OTLP。", "")
	}
	user, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "user directory unavailable")
		return
	}
	defer user.Close()
	sessionDir := layout.SessionDir(s.HomeDir, sessionID)
	rel, err := filepath.Rel(s.UserRoot, sessionDir)
	if err != nil || !fs.ValidPath(filepath.ToSlash(rel)) || rel == "." {
		writeJSONError(w, http.StatusForbidden, "invalid session directory")
		return
	}
	root, err := user.OpenRoot(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			missing()
		} else {
			writeJSONError(w, http.StatusForbidden, "trace directory unavailable")
		}
		return
	}
	defer root.Close()
	path, _ := filepath.Rel(sessionDir, layout.TracingLog(s.HomeDir, sessionID))
	info, err := root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		missing()
		return
	}
	if err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusForbidden, "trace log is not a readable regular file")
		return
	}
	input, err := root.OpenFile(path, downloadOpenFlags, 0)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, "trace log unavailable")
		return
	}
	defer input.Close()
	info, err = input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusForbidden, "trace log is not a regular file")
		return
	}
	if info.Size() > filetrace.MaxPerformanceLogBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "trace log exceeds analysis limits")
		return
	}
	// Freeze the readable length so a live append cannot extend this request's work.
	summary, err := filetrace.SummarizePerformance(ctx, io.NewSectionReader(input, 0, info.Size()), sessionID)
	if errors.Is(err, filetrace.ErrPerformanceLogTooLarge) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "trace log exceeds analysis limits")
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			writeJSONError(w, http.StatusRequestTimeout, "performance analysis canceled or timed out")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "cannot analyze trace log")
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(summary)
}
