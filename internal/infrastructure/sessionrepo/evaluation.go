package sessionrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/evaluation"
	"gorm.io/gorm"
)

var ErrEvaluationJobNotFound = errors.New("sessionrepo: evaluation job not found")

type evaluationJobModel struct {
	ID              string     `gorm:"column:id;type:varchar(160);primaryKey"`
	SourceSessionID string     `gorm:"column:source_session_id;type:varchar(128);not null"`
	UserID          string     `gorm:"column:user_id;type:varchar(128);not null"`
	WorkDir         string     `gorm:"column:work_dir;type:text;not null"`
	Requirement     string     `gorm:"column:requirement;type:text;not null"`
	Status          string     `gorm:"column:status;type:varchar(32);not null"`
	SnapshotHistory string     `gorm:"column:snapshot_history;type:text;not null"`
	ReportPath      string     `gorm:"column:report_path;type:text;not null;default:''"`
	Error           string     `gorm:"column:error;type:text;not null;default:''"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null"`
	CompletedAt     *time.Time `gorm:"column:completed_at"`
}

func (evaluationJobModel) TableName() string { return "evaluation_jobs" }

func migrateEvaluationJobs(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE TABLE IF NOT EXISTS evaluation_jobs (
		id VARCHAR(160) PRIMARY KEY NOT NULL,
		source_session_id VARCHAR(128) NOT NULL,
		user_id VARCHAR(128) NOT NULL,
		work_dir TEXT NOT NULL,
		requirement TEXT NOT NULL,
		status VARCHAR(32) NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
		snapshot_history TEXT NOT NULL,
		report_path TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		completed_at DATETIME
	)`).Error; err != nil {
		return fmt.Errorf("create evaluation job schema: %w", err)
	}
	if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_evaluation_jobs_source_created
		ON evaluation_jobs(source_session_id, created_at DESC, id DESC)`).Error; err != nil {
		return fmt.Errorf("create evaluation job index: %w", err)
	}
	return nil
}

func (r *SqliteSessionRepo) CreateEvaluationJob(ctx context.Context, job evaluation.Job) (evaluation.Job, error) {
	if strings.TrimSpace(job.ID) == "" || strings.TrimSpace(job.SourceSessionID) == "" ||
		strings.TrimSpace(job.UserID) == "" || strings.TrimSpace(job.WorkDir) == "" ||
		strings.TrimSpace(job.Requirement) == "" || strings.TrimSpace(job.SnapshotHistory) == "" {
		return evaluation.Job{}, fmt.Errorf("evaluation job fields are required")
	}
	if !evaluation.ValidStatus(job.Status) {
		return evaluation.Job{}, fmt.Errorf("invalid evaluation job status %q", job.Status)
	}
	now := time.Now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	row := evaluationJobModel{
		ID: job.ID, SourceSessionID: job.SourceSessionID, UserID: job.UserID,
		WorkDir: job.WorkDir, Requirement: job.Requirement, Status: job.Status,
		SnapshotHistory: job.SnapshotHistory, ReportPath: job.ReportPath, Error: job.Error,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return evaluation.Job{}, fmt.Errorf("create evaluation job: %w", err)
	}
	return job, nil
}

func (r *SqliteSessionRepo) UpdateEvaluationJob(ctx context.Context, id, status, reportPath, errorMessage string) error {
	if !evaluation.ValidStatus(status) {
		return fmt.Errorf("invalid evaluation job status %q", status)
	}
	now := time.Now().UTC()
	values := map[string]any{
		"status": status, "report_path": reportPath, "error": errorMessage, "updated_at": now,
	}
	if status == evaluation.StatusSucceeded || status == evaluation.StatusFailed {
		values["completed_at"] = now
	}
	result := r.db.WithContext(ctx).Model(&evaluationJobModel{}).Where("id = ?", id).Updates(values)
	if result.Error != nil {
		return fmt.Errorf("update evaluation job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrEvaluationJobNotFound
	}
	return nil
}

func (r *SqliteSessionRepo) ListEvaluationJobs(ctx context.Context, sourceSessionID string) ([]evaluation.Job, error) {
	if strings.TrimSpace(sourceSessionID) == "" {
		return nil, fmt.Errorf("source session ID is required")
	}
	var rows []evaluationJobModel
	if err := r.db.WithContext(ctx).Where("source_session_id = ?", sourceSessionID).
		Order("created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list evaluation jobs: %w", err)
	}
	jobs := make([]evaluation.Job, len(rows))
	for i, row := range rows {
		jobs[i] = evaluation.Job{
			ID: row.ID, SourceSessionID: row.SourceSessionID, UserID: row.UserID,
			WorkDir: row.WorkDir, Requirement: row.Requirement, Status: row.Status,
			SnapshotHistory: row.SnapshotHistory, ReportPath: row.ReportPath, Error: row.Error,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: row.CompletedAt,
		}
	}
	return jobs, nil
}

func (r *SqliteSessionRepo) FailActiveEvaluationJobs(ctx context.Context, errorMessage string) error {
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Model(&evaluationJobModel{}).
		Where("status IN ?", []string{evaluation.StatusQueued, evaluation.StatusRunning}).
		Updates(map[string]any{
			"status": evaluation.StatusFailed, "error": errorMessage,
			"updated_at": now, "completed_at": now,
		}).Error; err != nil {
		return fmt.Errorf("fail active evaluation jobs: %w", err)
	}
	return nil
}
