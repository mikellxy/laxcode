package session

import (
	"context"
	"time"
)

// MemorySource intentionally excludes reasoning, tools, and retrieved context.
type MemorySource struct {
	Seq     uint64 `json:"seq"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type MemoryJob struct {
	ID             uint64
	UserID         string
	SessionID      string
	StartTurn      uint64
	EndTurn        uint64
	SourceKey      string
	SourceMessages string
	Status         string
	Summary        *string
	Attempts       int
	NextAttemptAt  time.Time
	LeaseUntil     time.Time
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (MemoryJob) TableName() string { return "user_memory_jobs" }

type MemoryJobRepository interface {
	ClaimMemoryJob(context.Context, time.Time, time.Duration) (*MemoryJob, error)
	SaveMemorySummary(context.Context, *MemoryJob, string) error
	FinishMemoryJob(context.Context, *MemoryJob, string, string, time.Time) error
}
