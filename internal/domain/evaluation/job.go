package evaluation

import (
	"context"
	"time"
)

const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Job records one asynchronous LLM-as-a-Judge run for an immutable session
// snapshot. ID is also the isolated evaluator session ID.
type Job struct {
	ID              string
	SourceSessionID string
	UserID          string
	WorkDir         string
	Requirement     string
	Status          string
	SnapshotHistory string
	ReportPath      string
	Error           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

type Repository interface {
	CreateEvaluationJob(ctx context.Context, job Job) (Job, error)
	UpdateEvaluationJob(ctx context.Context, id, status, reportPath, errorMessage string) error
	ListEvaluationJobs(ctx context.Context, sourceSessionID string) ([]Job, error)
	FailActiveEvaluationJobs(ctx context.Context, errorMessage string) error
}

func ValidStatus(status string) bool {
	switch status {
	case StatusQueued, StatusRunning, StatusSucceeded, StatusFailed:
		return true
	default:
		return false
	}
}
