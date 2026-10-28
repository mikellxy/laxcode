package run_sse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/cmd/agentasm"
	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/telemetry"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
	"github.com/mikellxy/laxcode/internal/infrastructure/taskevents"
)

var errUserStopped = errors.New("用户主动停止")

// handleChat owns submission and the complete asynchronous execution lifecycle.
func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeJSONError(w, 400, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Task) == "" {
		writeJSONError(w, 400, "task is required")
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)

	if sessionID == "" {
		writeJSONError(w, 400, "session_id is required")
		return
	}
	s.taskMu.Lock()
	if s.taskCtx.Err() != nil {
		s.taskMu.Unlock()
		writeJSONError(w, 503, "service is stopping")
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), sessionID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if selected.Mode != string(agentasm.ModeCode) {
		s.taskMu.Unlock()
		writeJSONError(w, 409, "unsupported session mode")
		return
	}

	active, err := s.tasks.GetActiveProjectTask(r.Context(), selected.ProjectID)
	if err != nil || active != nil {
		s.taskMu.Unlock()
		writeTaskBusy(w, active, sessionID, err)
		return
	}
	if s.modelMissing() {
		s.taskMu.Unlock()
		writeJSONProtocolError(w, 409, ErrorCodeModelRequired, "no platform model is configured; contact the administrator", "")
		return
	}
	// These locks also protect evaluation snapshots; the DB constraint covers chat tasks.
	unlockSession, ok := s.locks.TryLock(sessionID)
	if !ok {
		s.taskMu.Unlock()
		writeTaskConflict(w, "SESSION_BUSY", "会话正在使用中")
		return
	}
	unlockProject, ok := s.lockChatProject(selected.WorkDir)
	if !ok {
		unlockSession()
		s.taskMu.Unlock()
		writeTaskConflict(w, "PROJECT_BUSY", "项目正在使用中")
		return
	}
	task, err := s.tasks.CreateTask(r.Context(), uuid.NewString(), sessionID)
	if err != nil {
		unlockProject()
		unlockSession()
		if errors.Is(err, sessionrepo.ErrTaskConflict) {
			active, err = s.tasks.GetActiveProjectTask(r.Context(), selected.ProjectID)
			s.taskMu.Unlock()
			writeTaskBusy(w, active, sessionID, err)
		} else {
			s.taskMu.Unlock()
			writeTaskError(w, err)
		}
		return
	}
	ctx, cancel := context.WithCancelCause(s.taskCtx)
	s.taskCancels[task.ID] = cancel
	s.activeTasks.Add(1)
	s.taskWG.Add(1)
	s.taskMu.Unlock()
	go func() {
		defer s.taskWG.Done()
		defer cancel(nil)
		trace := s.startChatTrace(ctx, sessionID, "send", uuid.NewString(), time.Now())
		defer trace.close()
		publisher := &taskPublisher{ctx: trace.ctx, cancel: cancel, log: s.events, taskID: task.ID}
		var runErr error
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("task panic: %v", p)
			}
			if ctx.Err() != nil {
				runErr = context.Cause(ctx)
			}
			status, code, message := "success", "", ""
			if runErr != nil {
				status, code, message = "fail", "TASK_FAILED", strings.ReplaceAll(runErr.Error(), s.userRoot, "")
				if errors.Is(runErr, errChatFileTracking) {
					message = "文件产物统计失败，本轮任务可能已执行，请检查会话历史和项目文件。"
				}
			}
			if errors.Is(runErr, errUserStopped) {
				code = "USER_STOPPED"
			}
			trace.err = runErr
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := s.tasks.FinishTask(finishCtx, task.ID, status, code, message, publisher.lastID); err != nil {
				slog.Error("finish chat task", "task_id", task.ID, "error", err)
			}
			finishCancel()
			unlockProject()
			unlockSession()
			s.taskMu.Lock()
			delete(s.taskCancels, task.ID)
			s.taskMu.Unlock()
			s.activeTasks.Add(-1)
		}()
		if runErr = s.tasks.StartTask(ctx, task.ID); runErr != nil {
			return
		}
		assembled, err := s.assembleAgent(trace, agentasm.Input{WorkDir: selected.WorkDir, HomeDir: s.homeDir, UserRoot: s.userRoot, TempDir: s.tempDir, Repository: s.contextRepo, SessionID: sessionID, PlanMode: s.planMode, Consumer: trace.consumer(publisher.consume)})
		if err != nil {
			runErr = err
			return
		}
		assembled.Service.TaskID = task.ID
		assembled.Service.ReserveMessageSequence = s.tasks.ReserveMessageSequence
		// runWithFileTracking owns Cleanup, including early failures and panics.
		older, err := s.tasks.ListTasks(ctx)
		if err != nil {
			slog.Warn("list old task events", "error", err)
		} else {
			for _, old := range older {
				if old.SessionID == sessionID && old.Order < task.Order {
					if err := s.events.Purge(ctx, old.ID); err != nil {
						slog.Warn("purge old task events", "task_id", old.ID, "error", err)
					}
				}
			}
		}
		publisher.send(EventStart, 0, StartData{SessionID: sessionID})

		_, runErr = s.runWithFileTracking(trace.ctx, selected.WorkDir, assembled, func() (*sharedkernel.Message, error) {
			return assembled.Service.Chat(trace.ctx, req.Task)
		})
		if runErr == nil && requestCanResume(assembled.Session.Snapshot()) {
			runErr = errors.New("本轮生成未完整结束")
		}
	}()
	writeTaskJSON(w, task)
}

// handleResume owns submission and the complete asynchronous execution lifecycle.
func (s *server) handleResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			writeJSONError(w, 400, "invalid request body")
			return
		}
	}
	sessionID := strings.TrimSpace(r.PathValue("session_id"))

	if sessionID == "" {
		writeJSONError(w, 400, "session_id is required")
		return
	}
	s.taskMu.Lock()
	if s.taskCtx.Err() != nil {
		s.taskMu.Unlock()
		writeJSONError(w, 503, "service is stopping")
		return
	}
	selected, err := s.catalog.GetSession(r.Context(), sessionID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if selected.Mode != string(agentasm.ModeCode) {
		s.taskMu.Unlock()
		writeJSONError(w, 409, "unsupported session mode")
		return
	}
	latest, err := s.tasks.GetLatestTask(r.Context(), sessionID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if (latest == nil && req.TaskID != "") || (latest != nil && (latest.ID != req.TaskID || latest.Status != "fail")) {
		s.taskMu.Unlock()
		writeTaskConflict(w, "TASK_CHANGED", "任务状态已变化，请刷新会话")
		return
	}
	state, err := s.contextRepo.GetRequestContext(r.Context(), sessionID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if !requestCanResume(state) {
		s.taskMu.Unlock()
		writeTaskConflict(w, "NOTHING_TO_RESUME", "当前会话没有可继续的内容")
		return
	}

	active, err := s.tasks.GetActiveProjectTask(r.Context(), selected.ProjectID)
	if err != nil || active != nil {
		s.taskMu.Unlock()
		writeTaskBusy(w, active, sessionID, err)
		return
	}
	if s.modelMissing() {
		s.taskMu.Unlock()
		writeJSONProtocolError(w, 409, ErrorCodeModelRequired, "no platform model is configured; contact the administrator", "")
		return
	}
	// These locks also protect evaluation snapshots; the DB constraint covers chat tasks.
	unlockSession, ok := s.locks.TryLock(sessionID)
	if !ok {
		s.taskMu.Unlock()
		writeTaskConflict(w, "SESSION_BUSY", "会话正在使用中")
		return
	}
	unlockProject, ok := s.lockChatProject(selected.WorkDir)
	if !ok {
		unlockSession()
		s.taskMu.Unlock()
		writeTaskConflict(w, "PROJECT_BUSY", "项目正在使用中")
		return
	}
	task, err := s.tasks.CreateTask(r.Context(), uuid.NewString(), sessionID)
	if err != nil {
		unlockProject()
		unlockSession()
		if errors.Is(err, sessionrepo.ErrTaskConflict) {
			active, err = s.tasks.GetActiveProjectTask(r.Context(), selected.ProjectID)
			s.taskMu.Unlock()
			writeTaskBusy(w, active, sessionID, err)
		} else {
			s.taskMu.Unlock()
			writeTaskError(w, err)
		}
		return
	}
	ctx, cancel := context.WithCancelCause(s.taskCtx)
	s.taskCancels[task.ID] = cancel
	s.activeTasks.Add(1)
	s.taskWG.Add(1)
	s.taskMu.Unlock()
	go func() {
		defer s.taskWG.Done()
		defer cancel(nil)
		trace := s.startChatTrace(ctx, sessionID, "resume", "", time.Now())
		defer trace.close()
		publisher := &taskPublisher{ctx: trace.ctx, cancel: cancel, log: s.events, taskID: task.ID}
		var runErr error
		defer func() {
			if p := recover(); p != nil {
				runErr = fmt.Errorf("task panic: %v", p)
			}
			if ctx.Err() != nil {
				runErr = context.Cause(ctx)
			}
			status, code, message := "success", "", ""
			if runErr != nil {
				status, code, message = "fail", "TASK_FAILED", strings.ReplaceAll(runErr.Error(), s.userRoot, "")
				if errors.Is(runErr, errChatFileTracking) {
					message = "文件产物统计失败，本轮任务可能已执行，请检查会话历史和项目文件。"
				}
			}
			if errors.Is(runErr, errUserStopped) {
				code = "USER_STOPPED"
			}
			trace.err = runErr
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := s.tasks.FinishTask(finishCtx, task.ID, status, code, message, publisher.lastID); err != nil {
				slog.Error("finish chat task", "task_id", task.ID, "error", err)
			}
			finishCancel()
			unlockProject()
			unlockSession()
			s.taskMu.Lock()
			delete(s.taskCancels, task.ID)
			s.taskMu.Unlock()
			s.activeTasks.Add(-1)
		}()
		if runErr = s.tasks.StartTask(ctx, task.ID); runErr != nil {
			return
		}
		assembled, err := s.assembleAgent(trace, agentasm.Input{WorkDir: selected.WorkDir, HomeDir: s.homeDir, UserRoot: s.userRoot, TempDir: s.tempDir, Repository: s.contextRepo, SessionID: sessionID, PlanMode: s.planMode, Consumer: trace.consumer(publisher.consume)})
		if err != nil {
			runErr = err
			return
		}
		assembled.Service.TaskID = task.ID
		assembled.Service.ReserveMessageSequence = s.tasks.ReserveMessageSequence
		// runWithFileTracking owns Cleanup, including early failures and panics.
		older, err := s.tasks.ListTasks(ctx)
		if err != nil {
			slog.Warn("list old task events", "error", err)
		} else {
			for _, old := range older {
				if old.SessionID == sessionID && old.Order < task.Order {
					if err := s.events.Purge(ctx, old.ID); err != nil {
						slog.Warn("purge old task events", "task_id", old.ID, "error", err)
					}
				}
			}
		}
		publisher.send(EventStart, 0, StartData{SessionID: sessionID})
		if chatID := latestChatID(assembled.Session.Messages); chatID != "" {
			trace.ctx = telemetry.ContextWithChatID(trace.ctx, chatID)
		}
		_, runErr = s.runWithFileTracking(trace.ctx, selected.WorkDir, assembled, func() (*sharedkernel.Message, error) {
			return assembled.Service.Resume(trace.ctx)
		})
		if runErr == nil && requestCanResume(assembled.Session.Snapshot()) {
			runErr = errors.New("本轮生成未完整结束")
		}
	}()
	writeTaskJSON(w, task)
}

func requestCanResume(state session.RequestContext) bool {
	hasUser := false
	for _, m := range state.Messages {
		if m.Role == sharedkernel.RoleUser {
			hasUser = true
			break
		}
	}
	return len(state.Messages) > 0 && session.CanResume(hasUser, state.Messages[len(state.Messages)-1])
}
func writeTaskJSON(w http.ResponseWriter, t session.Task) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_ = json.NewEncoder(w).Encode(map[string]string{"task_id": t.ID, "task_status": t.Status})
}
func writeTaskConflict(w http.ResponseWriter, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(409)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "refresh_after_ms": 2000})
}
func writeTaskBusy(w http.ResponseWriter, t *session.Task, sessionID string, err error) {
	if err != nil {
		writeTaskError(w, err)
		return
	}
	code := "PROJECT_BUSY"
	if t != nil && t.SessionID == sessionID {
		code = "SESSION_BUSY"
	}
	writeTaskConflict(w, code, "项目正在执行任务，请稍后刷新会话")
}
func writeTaskError(w http.ResponseWriter, err error) {
	status := 500
	if errors.Is(err, sessionrepo.ErrTaskNotFound) || errors.Is(err, sessionrepo.ErrSessionNotFound) {
		status = 404
	}
	writeJSONError(w, status, err.Error())
}

type taskPublisher struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	log    *taskevents.Log
	taskID string
	lastID uint64
	err    error
}

func (p *taskPublisher) send(kind string, seq uint64, data any) {
	if p.err != nil {
		return
	}
	body, err := json.Marshal(data)
	if err == nil {
		err = p.log.Publish(p.ctx, taskevents.Event{TaskID: p.taskID, EventID: p.lastID + 1, MessageSeq: seq, Type: kind, Data: body})
	}
	if err != nil {
		p.err = err
		p.cancel(err)
		return
	}
	p.lastID++
}
func (p *taskPublisher) consume(e *reactservice.ReactEvent) {
	switch e.Type {
	case reactservice.ReActEventTypeChunk:
		if e.ChunkEvent == nil {
			return
		}
		switch e.ChunkEvent.Kind {
		case sharedkernel.ChunkReasoningDelta:
			p.send(EventReasoning, e.MessageSeq, DeltaData{Delta: e.ChunkEvent.Delta})
		case sharedkernel.ChunkTextDelta:
			p.send(EventMessage, e.MessageSeq, DeltaData{Delta: e.ChunkEvent.Delta})
		}
	case reactservice.ReActEventTypeToolCall:
		data := ToolCallData{Info: e.Content}
		if e.ContextUsage != nil {
			data.WindowToken = &e.ContextUsage.WindowToken
			data.ContextWindow = e.ContextUsage.ContextWindow
		}
		p.send(EventToolCall, e.MessageSeq, data)
	}
}

func (s *server) handleTaskEvents(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("task_id")
	s.taskMu.Lock()
	task, err := s.tasks.GetTask(r.Context(), taskID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	latest, err := s.tasks.GetLatestTask(r.Context(), task.SessionID)
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if latest == nil || latest.ID != task.ID {
		s.taskMu.Unlock()
		writeTaskConflict(w, "TASK_CHANGED", "任务状态已变化，请刷新会话")
		return
	}
	s.taskMu.Unlock()
	sub, err := s.events.Subscribe(r.Context(), task.ID)
	if err != nil {
		writeTaskError(w, err)
		return
	}
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher := http.NewResponseController(w)
	if err := flusher.Flush(); err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var delivered uint64
	for {
		if !task.Active() && delivered >= task.FinalEventID {
			_ = writeTaskEvent(w, "task_state", map[string]any{"task_id": task.ID, "task_status": task.Status, "error_code": task.ErrorCode, "error_message": task.ErrorMessage, "action": "refresh_session"})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.taskCtx.Done():
			return
		case <-ticker.C:
			task, err = s.tasks.GetTask(r.Context(), task.ID)
			if err != nil {
				_ = writeTaskEvent(w, "refresh_required", map[string]string{"code": "STATUS_UNAVAILABLE"})
				return
			}
			latest, err = s.tasks.GetLatestTask(r.Context(), task.SessionID)
			if err != nil || latest == nil || latest.ID != task.ID {
				_ = writeTaskEvent(w, "refresh_required", map[string]any{"code": "TASK_CHANGED", "refresh_after_ms": 2000})
				return
			}
		default:
		}
		event, err := sub.Next(r.Context())
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if err != nil {
			_ = writeTaskEvent(w, "refresh_required", map[string]string{"code": "EVENTS_UNAVAILABLE"})
			return
		}
		if event.EventID <= delivered {
			continue
		}
		if event.EventID != delivered+1 {
			_ = writeTaskEvent(w, "refresh_required", map[string]string{"code": "EVENT_GAP"})
			return
		}
		var data map[string]any
		if err := json.Unmarshal(event.Data, &data); err != nil {
			return
		}
		data["task_id"], data["event_id"], data["message_seq"] = event.TaskID, event.EventID, event.MessageSeq
		if err := writeTaskEvent(w, event.Type, data); err != nil {
			return
		}
		delivered = event.EventID
	}
}
func writeTaskEvent(w http.ResponseWriter, kind string, data any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
func (s *server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	s.taskMu.Lock()
	task, err := s.tasks.GetTask(r.Context(), r.PathValue("task_id"))
	if err != nil {
		s.taskMu.Unlock()
		writeTaskError(w, err)
		return
	}
	if task.Active() {
		cancel, ok := s.taskCancels[task.ID]
		if !ok {
			s.taskMu.Unlock()
			writeJSONError(w, 503, "任务状态异常，请刷新会话")
			return
		}
		cancel(errUserStopped)
	}
	s.taskMu.Unlock()
	writeTaskJSON(w, task)
}
func (s *server) stopTasks() {
	s.taskMu.Lock()
	s.taskCancel(errors.New("服务停止，执行已中断"))
	s.taskMu.Unlock()
	done := make(chan struct{})
	go func() { s.taskWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
		slog.Error("chat task shutdown timed out")
	}
}
