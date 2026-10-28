package session

import (
	"context"
	"time"
)

type Task struct {
	Order        int64      `json:"task_order" gorm:"column:task_order;primaryKey;autoIncrement"`
	ID           string     `json:"task_id" gorm:"column:task_id"`
	UserID       string     `json:"user_id"`
	ProjectID    string     `json:"project_id"`
	SessionID    string     `json:"session_id"`
	Status       string     `json:"task_status"`
	ErrorCode    string     `json:"error_code"`
	ErrorMessage string     `json:"error_message"`
	FinalEventID uint64     `json:"final_event_id"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

func (Task) TableName() string { return "chat_tasks" }
func (t Task) Active() bool    { return t.Status == "waiting" || t.Status == "running" }

type TaskRepository interface {
	CreateTask(context.Context, string, string) (Task, error)
	GetTask(context.Context, string) (Task, error)
	GetLatestTask(context.Context, string) (*Task, error)
	GetActiveProjectTask(context.Context, string) (*Task, error)
	ListTasks(context.Context) ([]Task, error)
	StartTask(context.Context, string) error
	FinishTask(context.Context, string, string, string, string, uint64) error
	ReserveMessageSequence(context.Context, string) (uint64, error)
}
