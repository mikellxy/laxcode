package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func migrateUserMemory(tx *gorm.DB) error {
	for _, col := range []struct {
		model     any
		name, sql string
	}{
		{&requestContextModel{}, "react_turn_count", "ALTER TABLE request_contexts ADD COLUMN react_turn_count INTEGER NOT NULL DEFAULT 0 CHECK(react_turn_count>=0)"},
		{&messageModel{}, "react_turn", "ALTER TABLE messages ADD COLUMN react_turn INTEGER CHECK(react_turn IS NULL OR (react_turn>0 AND message_type='original' AND role='assistant' AND finish_reason='stop' AND (tool_calls_json IS NULL OR tool_calls_json='null' OR json_array_length(tool_calls_json)=0)))"},
	} {
		if !tx.Migrator().HasColumn(col.model, col.name) {
			if err := tx.Exec(col.sql).Error; err != nil {
				return err
			}
		}
	}
	for _, sql := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_react_turn ON messages(session_id,react_turn) WHERE react_turn IS NOT NULL`,
		`CREATE TABLE IF NOT EXISTS react_turns (
   session_id TEXT NOT NULL, turn_no INTEGER NOT NULL, assistant_seq INTEGER NOT NULL,
   source_seq_json TEXT NOT NULL, completed_at DATETIME NOT NULL,
   PRIMARY KEY(session_id,turn_no), UNIQUE(session_id,assistant_seq),
   FOREIGN KEY(session_id) REFERENCES request_contexts(session_id) ON DELETE CASCADE)`,
		`CREATE TABLE IF NOT EXISTS user_memory_jobs (
   id INTEGER PRIMARY KEY AUTOINCREMENT, user_id TEXT NOT NULL, session_id TEXT NOT NULL,
   start_turn INTEGER NOT NULL, end_turn INTEGER NOT NULL, source_key TEXT NOT NULL,
   source_messages TEXT NOT NULL, status TEXT NOT NULL, summary TEXT,
   attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at DATETIME NOT NULL,
   lease_until DATETIME NOT NULL, last_error TEXT NOT NULL DEFAULT '',
   created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL,
   UNIQUE(session_id,end_turn), FOREIGN KEY(session_id) REFERENCES request_contexts(session_id) ON DELETE CASCADE)`,
		`CREATE INDEX IF NOT EXISTS idx_user_memory_jobs_ready ON user_memory_jobs(status,next_attempt_at,lease_until)`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

func recordReactTurn(tx *gorm.DB, id string, current requestContextModel, msg sharedkernel.Message) error {
	if msg.ReactTurn != current.ReactTurnCount+1 || msg.Role != sharedkernel.RoleAssistant || msg.FinishReason != sharedkernel.FinishReasonStop || len(msg.ToolCalls) > 0 {
		return fmt.Errorf("invalid completed ReAct turn")
	}
	// Immutable originals provide source identities even after working-set compaction.
	var previous struct{ AssistantSeq uint64 }
	if err := tx.Raw("SELECT COALESCE(MAX(assistant_seq),0) AS assistant_seq FROM react_turns WHERE session_id=?", id).Scan(&previous).Error; err != nil {
		return err
	}
	if previous.AssistantSeq == 0 {
		var input struct{ Seq uint64 }
		if err := tx.Raw("SELECT COALESCE(MAX(seq),0) AS seq FROM messages WHERE session_id=? AND message_type='original' AND role='user' AND seq<?", id, msg.Seq).Scan(&input).Error; err != nil {
			return err
		}
		if input.Seq == 0 {
			return fmt.Errorf("completed turn has no user input")
		}
		previous.AssistantSeq = input.Seq - 1
	}
	var sources []messageModel
	if err := tx.Where("session_id=? AND message_type=? AND seq>? AND seq<=? AND role IN ?", id, messageTypeOriginal, previous.AssistantSeq, msg.Seq, []string{"user", "assistant"}).Order("seq").Find(&sources).Error; err != nil {
		return err
	}
	seqs := make([]uint64, 0, len(sources))
	for _, m := range sources {
		seqs = append(seqs, m.Seq)
	}
	data, _ := json.Marshal(seqs)
	now := time.Now().UTC()
	if err := tx.Exec("INSERT INTO react_turns(session_id,turn_no,assistant_seq,source_seq_json,completed_at) VALUES(?,?,?,?,?)", id, msg.ReactTurn, msg.Seq, string(data), now).Error; err != nil {
		return err
	}
	return nil
}

func (r *SqliteSessionRepo) EnqueueMemoryJob(ctx context.Context, id, userID string, endTurn, assistantSeq uint64) error {
	if err := validSessionID(id); err != nil {
		return err
	}
	if strings.TrimSpace(userID) == "" || endTurn < 3 || endTurn%3 != 0 || assistantSeq == 0 {
		return fmt.Errorf("invalid memory job boundary")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state requestContextModel
		if err := tx.Where("session_id=?", id).Take(&state).Error; err != nil {
			return err
		}
		if state.UserID != userID {
			return fmt.Errorf("memory job user mismatch")
		}
		var boundary struct{ AssistantSeq uint64 }
		if err := tx.Raw("SELECT assistant_seq FROM react_turns WHERE session_id=? AND turn_no=?", id, endTurn).Scan(&boundary).Error; err != nil {
			return err
		}
		if boundary.AssistantSeq != assistantSeq {
			return fmt.Errorf("memory job assistant boundary mismatch")
		}
		return enqueueMemoryWindow(tx, id, userID, endTurn)
	})
}

func enqueueMemoryWindow(tx *gorm.DB, id, userID string, endTurn uint64) error {
	var rows []struct{ SourceSeqJSON string }
	if err := tx.Raw("SELECT source_seq_json FROM react_turns WHERE session_id=? AND turn_no BETWEEN ? AND ? ORDER BY turn_no", id, endTurn-2, endTurn).Scan(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 3 {
		return fmt.Errorf("incomplete memory window")
	}
	var seqs []uint64
	for _, row := range rows {
		var ids []uint64
		if err := json.Unmarshal([]byte(row.SourceSeqJSON), &ids); err != nil {
			return err
		}
		seqs = append(seqs, ids...)
	}
	var sources []messageModel
	if err := tx.Where("session_id=? AND message_type=? AND seq IN ?", id, messageTypeOriginal, seqs).Order("seq").Find(&sources).Error; err != nil {
		return err
	}
	snapshot := make([]session.MemorySource, 0, len(sources))
	for _, m := range sources {
		snapshot = append(snapshot, session.MemorySource{Seq: m.Seq, Role: m.Role, Content: m.Content})
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	job := session.MemoryJob{UserID: userID, SessionID: id, StartTurn: endTurn - 2, EndTurn: endTurn, SourceKey: fmt.Sprintf("%s:react:%d", id, endTurn), SourceMessages: string(payload), Status: "pending", NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "session_id"}, {Name: "end_turn"}}, DoNothing: true}).Create(&job).Error
}

// ReconcileMemoryJobs repairs the narrow crash window between committing a
// completed turn and running PostReactTurn. It is idempotent and runs before the
// worker begins claiming jobs.
func (r *SqliteSessionRepo) ReconcileMemoryJobs(ctx context.Context) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var windows []struct {
			SessionID string
			UserID    string
			EndTurn   uint64
		}
		if err := tx.Raw(`SELECT rt.session_id, rc.user_id, rt.turn_no AS end_turn
			FROM react_turns rt
			JOIN request_contexts rc ON rc.session_id=rt.session_id
			LEFT JOIN user_memory_jobs j ON j.session_id=rt.session_id AND j.end_turn=rt.turn_no
			WHERE rt.turn_no % 3 = 0 AND rc.user_id <> '' AND j.id IS NULL
			ORDER BY rt.session_id, rt.turn_no`).Scan(&windows).Error; err != nil {
			return err
		}
		for _, window := range windows {
			if err := enqueueMemoryWindow(tx, window.SessionID, window.UserID, window.EndTurn); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *SqliteSessionRepo) ClaimMemoryJob(ctx context.Context, now time.Time, lease time.Duration) (*session.MemoryJob, error) {
	var job session.MemoryJob
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("(status IN ? AND next_attempt_at<=?) OR (status=? AND lease_until<=?)", []string{"pending", "retry"}, now, "running", now).Order("id").Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		job.Status = "running"
		job.Attempts++
		job.LeaseUntil = now.Add(lease)
		job.UpdatedAt = now
		return tx.Save(&job).Error
	})
	if err != nil || !found {
		return nil, err
	}
	return &job, nil
}

func (r *SqliteSessionRepo) SaveMemorySummary(ctx context.Context, job *session.MemoryJob, summary string) error {
	result := r.db.WithContext(ctx).Model(&session.MemoryJob{}).Where("id=? AND status='running' AND attempts=?", job.ID, job.Attempts).Updates(map[string]any{"summary": summary, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("memory lease lost")
	}
	job.Summary = &summary
	return nil
}
func (r *SqliteSessionRepo) FinishMemoryJob(ctx context.Context, job *session.MemoryJob, status, lastError string, next time.Time) error {
	result := r.db.WithContext(ctx).Model(&session.MemoryJob{}).Where("id=? AND status='running' AND attempts=?", job.ID, job.Attempts).Updates(map[string]any{"status": status, "last_error": lastError, "next_attempt_at": next, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("memory lease lost")
	}
	return nil
}
