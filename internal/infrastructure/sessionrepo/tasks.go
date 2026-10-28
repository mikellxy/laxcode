package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"gorm.io/gorm"
)

var ErrTaskNotFound = errors.New("task not found")
var ErrTaskConflict = errors.New("task state changed or project busy")

func migrateTasks(tx *gorm.DB) error {
	for _, sql := range []string{
		`CREATE TABLE IF NOT EXISTS chat_tasks (
   task_order INTEGER PRIMARY KEY AUTOINCREMENT,
   task_id TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL, project_id TEXT NOT NULL,
   session_id TEXT NOT NULL REFERENCES request_contexts(session_id) ON DELETE CASCADE,
   status TEXT NOT NULL CHECK(status IN ('waiting','running','success','fail')),
   error_code TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
   final_event_id INTEGER NOT NULL DEFAULT 0,
   created_at DATETIME NOT NULL, started_at DATETIME, finished_at DATETIME)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS one_active_task_per_project ON chat_tasks(project_id) WHERE status IN ('waiting','running')`,
		`CREATE INDEX IF NOT EXISTS task_session_order ON chat_tasks(session_id,task_order DESC)`,
		`CREATE TABLE IF NOT EXISTS message_sequences (
    session_id TEXT PRIMARY KEY REFERENCES request_contexts(session_id) ON DELETE CASCADE,
    reserved_seq INTEGER NOT NULL)`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}
func (r *SqliteSessionRepo) CreateTask(ctx context.Context, id, sessionID string) (session.Task, error) {
	var task session.Task
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var s requestContextModel
		if err := tx.Where("session_id = ?", sessionID).Take(&s).Error; err != nil {
			return err
		}
		task = session.Task{ID: id, UserID: s.UserID, ProjectID: s.ProjectID, SessionID: sessionID, Status: "waiting", CreatedAt: time.Now().UTC()}
		err := tx.Create(&task).Error
		if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: chat_tasks.project_id") {
			return ErrTaskConflict
		}
		return err
	})
	return task, err
}
func (r *SqliteSessionRepo) GetTask(ctx context.Context, id string) (session.Task, error) {
	var t session.Task
	err := r.db.WithContext(ctx).Where("task_id = ?", id).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrTaskNotFound
	}
	return t, err
}
func latestTask(tx *gorm.DB, sessionID string) (*session.Task, error) {
	var t session.Task
	err := tx.Where("session_id = ?", sessionID).Order("task_order DESC").Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}
func (r *SqliteSessionRepo) GetLatestTask(ctx context.Context, id string) (*session.Task, error) {
	return latestTask(r.db.WithContext(ctx), id)
}
func (r *SqliteSessionRepo) GetActiveProjectTask(ctx context.Context, id string) (*session.Task, error) {
	var t session.Task
	err := r.db.WithContext(ctx).Where("project_id = ? AND status IN ('waiting','running')", id).Take(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}
func (r *SqliteSessionRepo) ListTasks(ctx context.Context) ([]session.Task, error) {
	var tasks []session.Task
	err := r.db.WithContext(ctx).Order("task_order DESC").Find(&tasks).Error
	return tasks, err
}
func (r *SqliteSessionRepo) StartTask(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Model(&session.Task{}).Where("task_id = ? AND status = 'waiting'", id).Updates(map[string]any{"status": "running", "started_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTaskConflict
	}
	return nil
}
func (r *SqliteSessionRepo) FinishTask(ctx context.Context, id, status, code, message string, finalID uint64) error {
	if status != "success" && status != "fail" {
		return fmt.Errorf("invalid terminal status %q", status)
	}
	result := r.db.WithContext(ctx).Model(&session.Task{}).Where("task_id = ? AND status IN ('waiting','running')", id).Updates(map[string]any{"status": status, "error_code": code, "error_message": message, "final_event_id": finalID, "finished_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrTaskConflict
	}
	return nil
}
func (r *SqliteSessionRepo) ReserveMessageSequence(ctx context.Context, id string) (uint64, error) {
	var seq uint64
	err := r.db.WithContext(ctx).Raw(`INSERT INTO message_sequences(session_id,reserved_seq)
 SELECT session_id,last_seq+1 FROM request_contexts WHERE session_id=?
 ON CONFLICT(session_id) DO UPDATE SET reserved_seq = MAX(reserved_seq,(SELECT last_seq FROM request_contexts WHERE session_id=?))+1
 RETURNING reserved_seq`, id, id).Scan(&seq).Error
	if err == nil && seq == 0 {
		err = ErrSessionNotFound
	}
	return seq, err
}
