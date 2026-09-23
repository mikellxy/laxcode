package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	messageTypeOriginal = "original"
	messageTypeMemory   = "in_memory"
)

var (
	ErrContextConflict = errors.New("sessionrepo: request context revision conflict")
	ErrStaleSequence   = errors.New("sessionrepo: stale message sequence")
	ErrStaleGeneration = errors.New("sessionrepo: stale memory generation")
	ErrSessionNotFound = errors.New("sessionrepo: session not found")
)

type requestContextModel struct {
	ReactTurnCount    uint64    `gorm:"column:react_turn_count;not null;default:0"`
	SessionID         string    `gorm:"column:session_id;type:varchar(128);primaryKey"`
	UserID            string    `gorm:"column:user_id;type:varchar(128);not null;default:''"`
	Title             string    `gorm:"column:title;type:text;not null;default:''"`
	WorkDir           string    `gorm:"column:work_dir;type:text;not null;default:''"`
	Revision          uint64    `gorm:"column:revision;not null"`
	MemoryGeneration  uint64    `gorm:"column:memory_generation;not null"`
	LastSeq           uint64    `gorm:"column:last_seq;not null"`
	TokenUsedInput    int64     `gorm:"column:token_used_input;not null"`
	TokenUsedOutput   int64     `gorm:"column:token_used_output;not null"`
	WindowTokenInput  int64     `gorm:"column:window_token_input;not null"`
	WindowTokenOutput int64     `gorm:"column:window_token_output;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null"`
}

func (requestContextModel) TableName() string { return "request_contexts" }

// messageModel 同时承载不可变 original 与各代工作集消息。复合主键使 original
// 和每一代 memory 在同一 session/seq 下各有且只有一条，无需额外关联表。
type messageModel struct {
	ReactTurn        *uint64   `gorm:"column:react_turn"`
	MemoryChunksJSON []byte    `gorm:"column:memory_chunks_json;type:json"`
	RAGChunksJSON    []byte    `gorm:"column:rag_chunks_json;type:json"`
	SessionID        string    `gorm:"column:session_id;type:varchar(128);primaryKey;priority:1"`
	MessageType      string    `gorm:"column:message_type;type:varchar(32);primaryKey;priority:2"`
	MemoryGeneration uint64    `gorm:"column:memory_generation;primaryKey;priority:3"`
	Seq              uint64    `gorm:"column:seq;primaryKey;priority:4"`
	OriginalSeqJSON  []byte    `gorm:"column:original_seq_json;type:json;not null"`
	Role             string    `gorm:"column:role;type:varchar(32);not null"`
	ToolCallID       string    `gorm:"column:tool_call_id;type:varchar(128);not null"`
	Content          string    `gorm:"column:content;type:text;not null"`
	DisplayContent   string    `gorm:"column:display_content;type:text;not null;default:''"`
	CompactContent   string    `gorm:"column:compact_content;type:text;not null;default:''"`
	ReasoningID      string    `gorm:"column:reasoning_id;type:varchar(255);not null"`
	ReasoningContent string    `gorm:"column:reasoning_content;type:text;not null"`
	ToolCallsJSON    []byte    `gorm:"column:tool_calls_json;type:json"`
	FinishReason     string    `gorm:"column:finish_reason;type:varchar(32);not null;default:''"`
	ArtifactID       *string   `gorm:"column:artifact_id;type:varchar(64)"`
	ArtifactByteSize *int64    `gorm:"column:artifact_byte_size"`
	TokenInput       int64     `gorm:"column:token_input;not null"`
	TokenOutput      int64     `gorm:"column:token_output;not null"`
	CreatedAt        time.Time `gorm:"column:created_at;not null"`
	UpdatedAt        time.Time `gorm:"column:updated_at;not null"`
}

func (messageModel) TableName() string { return "messages" }

// SqliteSessionRepo 以两张表保存当前 context head、不可变 original 和按代封存
// 的 memory。historyRoot 只用于事务提交后的 best-effort JSONL 冷备。
type SqliteSessionRepo struct {
	db          *gorm.DB
	historyRoot string
}

func NewSqliteSessionRepo(dbPath, historyRoot string) (*SqliteSessionRepo, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := dbPath + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open session database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get session database handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	r := &SqliteSessionRepo{db: db, historyRoot: historyRoot}
	if err := r.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return r, nil
}

// 新增字段与记忆队列表采用幂等迁移；已有会话从零开始累计轮次。
func (r *SqliteSessionRepo) migrate() error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS request_contexts (
				session_id VARCHAR(128) PRIMARY KEY NOT NULL,
				user_id VARCHAR(128) NOT NULL DEFAULT '',
				title TEXT NOT NULL DEFAULT '',
				work_dir TEXT NOT NULL DEFAULT '',
				revision BIGINT NOT NULL CHECK (revision >= 0),
				memory_generation BIGINT NOT NULL CHECK (memory_generation >= 1),
				last_seq BIGINT NOT NULL CHECK (last_seq >= 0),
				token_used_input BIGINT NOT NULL DEFAULT 0,
				token_used_output BIGINT NOT NULL DEFAULT 0,
				window_token_input BIGINT NOT NULL DEFAULT 0,
				window_token_output BIGINT NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS messages (
					session_id VARCHAR(128) NOT NULL,
					message_type VARCHAR(32) NOT NULL CHECK (message_type IN ('original', 'in_memory')),
					memory_generation BIGINT NOT NULL CHECK (memory_generation >= 0),
					seq BIGINT NOT NULL CHECK (seq >= 1),
					original_seq_json JSON NOT NULL CHECK (json_valid(original_seq_json) AND json_array_length(original_seq_json) >= 1),
					role VARCHAR(32) NOT NULL,
					tool_call_id VARCHAR(128) NOT NULL DEFAULT '',
					content TEXT NOT NULL,
					display_content TEXT NOT NULL DEFAULT '',
					compact_content TEXT NOT NULL DEFAULT '',
					reasoning_id VARCHAR(255) NOT NULL DEFAULT '',
					reasoning_content TEXT NOT NULL DEFAULT '',
					tool_calls_json JSON,
					finish_reason VARCHAR(32) NOT NULL DEFAULT '',
					artifact_id VARCHAR(64),
					artifact_byte_size BIGINT,
				token_input BIGINT NOT NULL DEFAULT 0,
				token_output BIGINT NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (session_id, message_type, memory_generation, seq),
				CHECK ((message_type = 'original' AND memory_generation = 0
						AND json_array_length(original_seq_json) = 1
						AND json_extract(original_seq_json, '$[0]') = seq)
					OR (message_type = 'in_memory' AND memory_generation >= 1)),
				FOREIGN KEY (session_id) REFERENCES request_contexts(session_id) ON UPDATE CASCADE ON DELETE CASCADE
			)`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("create session schema: %w", err)
			}
		}
		if !tx.Migrator().HasColumn(&messageModel{}, "compact_content") {
			if err := tx.Exec("ALTER TABLE messages ADD COLUMN compact_content TEXT NOT NULL DEFAULT ''").Error; err != nil {
				return fmt.Errorf("add compact content column: %w", err)
			}
		}
		if !tx.Migrator().HasColumn(&messageModel{}, "display_content") {
			if err := tx.Exec("ALTER TABLE messages ADD COLUMN display_content TEXT NOT NULL DEFAULT ''").Error; err != nil {
				return fmt.Errorf("add display content column: %w", err)
			}
		}
		if !tx.Migrator().HasColumn(&requestContextModel{}, "user_id") {
			if err := tx.Exec("ALTER TABLE request_contexts ADD COLUMN user_id VARCHAR(128) NOT NULL DEFAULT ''").Error; err != nil {
				return fmt.Errorf("add session user id column: %w", err)
			}
		}
		if !tx.Migrator().HasColumn(&requestContextModel{}, "title") {
			if err := tx.Exec("ALTER TABLE request_contexts ADD COLUMN title TEXT NOT NULL DEFAULT ''").Error; err != nil {
				return fmt.Errorf("add session title column: %w", err)
			}
		}
		if !tx.Migrator().HasColumn(&requestContextModel{}, "work_dir") {
			if err := tx.Exec("ALTER TABLE request_contexts ADD COLUMN work_dir TEXT NOT NULL DEFAULT ''").Error; err != nil {
				return fmt.Errorf("add session workdir column: %w", err)
			}
		}
		if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_request_contexts_user_updated
			ON request_contexts(user_id, updated_at DESC, session_id DESC)`).Error; err != nil {
			return fmt.Errorf("create session list index: %w", err)
		}
		return migrateUserMemory(tx)
	})
}

func (r *SqliteSessionRepo) CreateSession(ctx context.Context, id, userID, title, workDir string) (session.Summary, error) {
	if err := validSessionID(id); err != nil {
		return session.Summary{}, err
	}
	if strings.TrimSpace(userID) == "" {
		return session.Summary{}, fmt.Errorf("user ID is required")
	}
	now := time.Now().UTC()
	row := requestContextModel{
		SessionID: id, UserID: userID, Title: title, WorkDir: workDir,
		MemoryGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return session.Summary{}, fmt.Errorf("create session: %w", err)
	}
	return summaryFromModel(row), nil
}

func (r *SqliteSessionRepo) GetSession(ctx context.Context, id string) (session.Summary, error) {
	if err := validSessionID(id); err != nil {
		return session.Summary{}, err
	}
	var row requestContextModel
	err := r.db.WithContext(ctx).Where("session_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return session.Summary{}, ErrSessionNotFound
	}
	if err != nil {
		return session.Summary{}, fmt.Errorf("get session: %w", err)
	}
	return summaryFromModel(row), nil
}

func (r *SqliteSessionRepo) ListSessions(ctx context.Context, userID, beforeSessionID string, limit int) (session.SummaryPage, error) {
	if strings.TrimSpace(userID) == "" {
		return session.SummaryPage{}, fmt.Errorf("user ID is required")
	}
	if limit <= 0 {
		return session.SummaryPage{}, fmt.Errorf("session list limit must be positive")
	}
	query := r.db.WithContext(ctx).Model(&requestContextModel{}).Where("user_id = ?", userID)
	if beforeSessionID != "" {
		if err := validSessionID(beforeSessionID); err != nil {
			return session.SummaryPage{}, err
		}
		var cursor requestContextModel
		err := r.db.WithContext(ctx).Select("session_id", "updated_at").
			Where("session_id = ? AND user_id = ?", beforeSessionID, userID).Take(&cursor).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return session.SummaryPage{}, ErrSessionNotFound
		}
		if err != nil {
			return session.SummaryPage{}, fmt.Errorf("load session cursor: %w", err)
		}
		query = query.Where("updated_at < ? OR (updated_at = ? AND session_id < ?)",
			cursor.UpdatedAt, cursor.UpdatedAt, cursor.SessionID)
	}
	var rows []requestContextModel
	if err := query.Order("updated_at DESC, session_id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return session.SummaryPage{}, fmt.Errorf("list sessions: %w", err)
	}
	page := session.SummaryPage{Sessions: make([]session.Summary, min(len(rows), limit))}
	if len(rows) > limit {
		page.HasMore = true
		rows = rows[:limit]
	}
	for i := range rows {
		page.Sessions[i] = summaryFromModel(rows[i])
	}
	return page, nil
}

func summaryFromModel(row requestContextModel) session.Summary {
	return session.Summary{
		ID: row.SessionID, UserID: row.UserID, Title: row.Title, WorkDir: row.WorkDir,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// ListOriginalHistory 读取不可变 original 历史，并在仓储边界完成行级过滤和
// 字段白名单投影。多取一条用于判断是否还有更早数据。
func (r *SqliteSessionRepo) ListOriginalHistory(ctx context.Context, id string, beforeSeq uint64, limit int) (session.HistoryPage, bool, error) {
	if err := validSessionID(id); err != nil {
		return session.HistoryPage{}, false, err
	}
	if limit <= 0 {
		return session.HistoryPage{}, false, fmt.Errorf("history limit must be positive")
	}

	var page session.HistoryPage
	found := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&requestContextModel{}).Where("session_id = ?", id).Count(&count).Error; err != nil {
			return fmt.Errorf("check session existence: %w", err)
		}
		if count == 0 {
			return nil
		}
		found = true

		query := tx.Model(&messageModel{}).
			Select("seq", "role", "content", "reasoning_content", "display_content", "created_at").
			Where("session_id = ? AND message_type = ? AND memory_generation = 0 AND role <> ?",
				id, messageTypeOriginal, sharedkernel.RoleSystem)
		if beforeSeq != 0 {
			query = query.Where("seq < ?", beforeSeq)
		}
		var rows []messageModel
		if err := query.Order("seq DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return fmt.Errorf("list original history: %w", err)
		}
		if len(rows) > limit {
			page.HasMore = true
			rows = rows[:limit]
		}
		page.Messages = make([]session.HistoryMessage, len(rows))
		for i := range rows {
			row := rows[len(rows)-1-i]
			msg := session.HistoryMessage{Seq: row.Seq, Role: row.Role, CreatedAt: row.CreatedAt}
			switch row.Role {
			case sharedkernel.RoleUser:
				msg.Content = row.Content
			case sharedkernel.RoleAssistant:
				msg.Content = row.Content
				msg.ReasoningContent = row.ReasoningContent
			case sharedkernel.RoleTool:
				msg.ToolSummary = row.DisplayContent
			}
			page.Messages[i] = msg
		}
		return nil
	})
	return page, found, err
}

func (r *SqliteSessionRepo) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func validSessionID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid session ID")
	}
	return nil
}

func (r *SqliteSessionRepo) GetRequestContext(ctx context.Context, id string) (session.RequestContext, error) {
	if err := validSessionID(id); err != nil {
		return session.RequestContext{}, err
	}
	var snapshot session.RequestContext
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state requestContextModel
		err := tx.Where("session_id = ?", id).Take(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			snapshot = session.RequestContext{MemoryGeneration: 1}
			return nil
		}
		if err != nil {
			return fmt.Errorf("load request context: %w", err)
		}
		var rows []messageModel
		if err := tx.Where("session_id = ? AND message_type = ? AND memory_generation = ?",
			id, messageTypeMemory, state.MemoryGeneration).Order("seq ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("load memory messages: %w", err)
		}
		msgs := make([]sharedkernel.Message, 0, len(rows))
		for i := range rows {
			msg, err := modelToMessage(rows[i])
			if err != nil {
				return fmt.Errorf("decode memory message %d: %w", rows[i].Seq, err)
			}
			msgs = append(msgs, msg)
		}
		snapshot = contextFromModel(state, msgs)
		return snapshot.Validate()
	})
	return snapshot, err
}

func (r *SqliteSessionRepo) CommitCreateMessage(ctx context.Context, id string, snapshot session.RequestContext, original, memory sharedkernel.Message) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if err := validateCreatedMessage(snapshot, original, memory); err != nil {
		return 0, err
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if err := validateCreateTransition(snapshot, current, exists); err != nil {
			return err
		}
		expectedTurn := current.ReactTurnCount
		if original.ReactTurn > 0 {
			expectedTurn++
		}
		if snapshot.ReactTurnCount != expectedTurn || (original.ReactTurn > 0 && original.ReactTurn != expectedTurn) {
			return fmt.Errorf("invalid turn count")
		}
		now := time.Now().UTC()
		if err := writeContext(tx, id, snapshot, newRevision, now, exists); err != nil {
			return err
		}
		originalRow, err := messageToModel(id, messageTypeOriginal, 0, original, now)
		if err != nil {
			return err
		}
		memoryRow, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, memory, now)
		if err != nil {
			return err
		}
		// 召回 chunks 随工作集消息 append-only 保留（不在新 user 消息提交时
		// 清理旧 chunks），与 application 层一致；只在压缩推进 memory
		// 代际时以裁剪后的快照封存。
		if err := tx.Create(&[]messageModel{originalRow, memoryRow}).Error; err != nil {
			return err
		}
		if original.ReactTurn > 0 {
			return createMemoryWindow(tx, id, current, original)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := appendHistory(r.historyRoot, id, []sharedkernel.Message{original.Clone()}); err != nil {
		slog.WarnContext(ctx, "session_history_backup_failed", "session_id", id, "message_count", 1, "error", err)
	}
	return newRevision, nil
}

func (r *SqliteSessionRepo) CommitUpdateMessage(ctx context.Context, id string, snapshot session.RequestContext, memory sharedkernel.Message) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if !snapshotContains(snapshot.Messages, memory) {
		return 0, fmt.Errorf("%w: updated memory is absent from snapshot", ErrStaleSequence)
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if !exists || current.Revision != snapshot.Revision {
			return ErrContextConflict
		}
		if current.WorkDir != "" && snapshot.WorkDir != current.WorkDir {
			return fmt.Errorf("session workdir is immutable: stored %q, requested %q", current.WorkDir, snapshot.WorkDir)
		}
		if snapshot.ReactTurnCount != current.ReactTurnCount {
			return fmt.Errorf("cannot change turns outside completion")
		}
		if current.LastSeq != snapshot.LastSeq {
			return ErrStaleSequence
		}
		if current.MemoryGeneration != snapshot.MemoryGeneration {
			return ErrStaleGeneration
		}
		now := time.Now().UTC()
		row, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, memory, now)
		if err != nil {
			return err
		}
		result := tx.Model(&messageModel{}).Where(
			"session_id = ? AND message_type = ? AND memory_generation = ? AND seq = ?",
			id, messageTypeMemory, snapshot.MemoryGeneration, memory.Seq,
		).Updates(messagePayload(row))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: memory message %d does not exist", ErrStaleSequence, memory.Seq)
		}
		return writeContext(tx, id, snapshot, newRevision, now, true)
	})
	if err != nil {
		return 0, err
	}
	return newRevision, nil
}

func (r *SqliteSessionRepo) CommitNextMemoryGeneration(ctx context.Context, id string, snapshot session.RequestContext) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if !exists || current.Revision != snapshot.Revision {
			return ErrContextConflict
		}
		if current.WorkDir != "" && snapshot.WorkDir != current.WorkDir {
			return fmt.Errorf("session workdir is immutable: stored %q, requested %q", current.WorkDir, snapshot.WorkDir)
		}
		if snapshot.ReactTurnCount != current.ReactTurnCount {
			return fmt.Errorf("cannot change turns outside completion")
		}
		if current.LastSeq != snapshot.LastSeq {
			return ErrStaleSequence
		}
		if current.MemoryGeneration == ^uint64(0) || snapshot.MemoryGeneration != current.MemoryGeneration+1 {
			return ErrStaleGeneration
		}
		now := time.Now().UTC()
		rows := make([]messageModel, 0, len(snapshot.Messages))
		for _, msg := range snapshot.Messages {
			row, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, msg, now)
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return writeContext(tx, id, snapshot, newRevision, now, true)
	})
	if err != nil {
		return 0, err
	}
	return newRevision, nil
}

func loadCurrentContext(tx *gorm.DB, id string) (requestContextModel, bool, error) {
	var current requestContextModel
	err := tx.Where("session_id = ?", id).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return requestContextModel{}, false, nil
	}
	if err != nil {
		return requestContextModel{}, false, fmt.Errorf("load current request context: %w", err)
	}
	return current, true, nil
}

func validateCreatedMessage(snapshot session.RequestContext, original, memory sharedkernel.Message) error {
	if original.Seq == 0 || original.Seq != snapshot.LastSeq ||
		len(original.OriginalSeq) != 1 || original.OriginalSeq[0] != original.Seq {
		return fmt.Errorf("%w: invalid original identity", ErrStaleSequence)
	}
	comparison := memory.Clone()
	comparison.MemoryChunks = nil
	comparison.RAGChunks = nil
	if len(original.MemoryChunks) > 0 || len(original.RAGChunks) > 0 {
		return fmt.Errorf("original cannot contain recalled chunks")
	}
	if !equalMessage(original, comparison) {
		return fmt.Errorf("%w: original and initial memory differ", ErrStaleSequence)
	}
	if len(snapshot.Messages) == 0 || !equalMessage(snapshot.Messages[len(snapshot.Messages)-1], memory) {
		return fmt.Errorf("%w: memory does not match snapshot tail", ErrStaleSequence)
	}
	return nil
}

func validateCreateTransition(snapshot session.RequestContext, current requestContextModel, exists bool) error {
	if !exists {
		if snapshot.Revision != 0 {
			return ErrContextConflict
		}
		if snapshot.MemoryGeneration != 1 {
			return ErrStaleGeneration
		}
		if snapshot.LastSeq != 1 || len(snapshot.Messages) != 1 {
			return ErrStaleSequence
		}
		return nil
	}
	if current.WorkDir != "" && snapshot.WorkDir != current.WorkDir {
		return fmt.Errorf("session workdir is immutable: stored %q, requested %q", current.WorkDir, snapshot.WorkDir)
	}
	if snapshot.ReactTurnCount < current.ReactTurnCount || snapshot.ReactTurnCount > current.ReactTurnCount+1 {
		return fmt.Errorf("invalid react turn transition")
	}
	if current.Revision != snapshot.Revision {
		return ErrContextConflict
	}
	if current.MemoryGeneration != snapshot.MemoryGeneration {
		return ErrStaleGeneration
	}
	if current.LastSeq == ^uint64(0) || snapshot.LastSeq != current.LastSeq+1 {
		return ErrStaleSequence
	}
	return nil
}

func writeContext(tx *gorm.DB, id string, snapshot session.RequestContext, revision uint64, now time.Time, exists bool) error {
	state := contextToModel(id, snapshot, revision, now)
	if !exists {
		state.CreatedAt = now
		return tx.Create(&state).Error
	}
	result := tx.Model(&requestContextModel{}).
		Where("session_id = ? AND revision = ?", id, snapshot.Revision).
		Updates(map[string]any{
			"revision": revision, "memory_generation": state.MemoryGeneration,
			"work_dir":         state.WorkDir,
			"react_turn_count": state.ReactTurnCount,
			"last_seq":         state.LastSeq, "token_used_input": state.TokenUsedInput,
			"token_used_output":   state.TokenUsedOutput,
			"window_token_input":  state.WindowTokenInput,
			"window_token_output": state.WindowTokenOutput, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrContextConflict
	}
	return nil
}

func contextToModel(id string, snapshot session.RequestContext, revision uint64, now time.Time) requestContextModel {
	return requestContextModel{
		SessionID: id, UserID: snapshot.UserID, WorkDir: snapshot.WorkDir,
		Revision: revision, MemoryGeneration: snapshot.MemoryGeneration,
		ReactTurnCount: snapshot.ReactTurnCount,
		LastSeq:        snapshot.LastSeq, TokenUsedInput: int64(snapshot.TokenUsed.TokenInput),
		TokenUsedOutput:   int64(snapshot.TokenUsed.TokenOutput),
		WindowTokenInput:  int64(snapshot.WindowToken.TokenInput),
		WindowTokenOutput: int64(snapshot.WindowToken.TokenOutput), UpdatedAt: now,
	}
}

func contextFromModel(state requestContextModel, msgs []sharedkernel.Message) session.RequestContext {
	return session.RequestContext{
		Revision: state.Revision, MemoryGeneration: state.MemoryGeneration, LastSeq: state.LastSeq,
		UserID: state.UserID, WorkDir: state.WorkDir, ReactTurnCount: state.ReactTurnCount,
		Messages:    msgs,
		TokenUsed:   sharedkernel.TokenStatistics{TokenInput: int(state.TokenUsedInput), TokenOutput: int(state.TokenUsedOutput)},
		WindowToken: sharedkernel.TokenStatistics{TokenInput: int(state.WindowTokenInput), TokenOutput: int(state.WindowTokenOutput)},
	}
}

func messageToModel(id, messageType string, generation uint64, msg sharedkernel.Message, now time.Time) (messageModel, error) {
	toolCalls, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return messageModel{}, err
	}
	originalSeq, err := json.Marshal(msg.OriginalSeq)
	if err != nil {
		return messageModel{}, err
	}
	chunks, err := json.Marshal(msg.MemoryChunks)
	if err != nil {
		return messageModel{}, err
	}
	ragChunks, err := json.Marshal(msg.RAGChunks)
	if err != nil {
		return messageModel{}, err
	}
	row := messageModel{
		MemoryChunksJSON: chunks,
		RAGChunksJSON:    ragChunks,
		SessionID:        id, MessageType: messageType, MemoryGeneration: generation,
		Seq: msg.Seq, OriginalSeqJSON: originalSeq, Role: msg.Role,
		ToolCallID: msg.ToolCallID, Content: msg.Content, DisplayContent: msg.DisplayContent, ReasoningID: msg.ReasoningID,
		CompactContent:   msg.CompactContent,
		ReasoningContent: msg.ReasoningContent, ToolCallsJSON: toolCalls,
		FinishReason: msg.FinishReason,
		TokenInput:   int64(msg.TokenUsed.TokenInput), TokenOutput: int64(msg.TokenUsed.TokenOutput),
		CreatedAt: now, UpdatedAt: now,
	}
	if messageType == messageTypeOriginal && msg.ReactTurn > 0 {
		n := msg.ReactTurn
		row.ReactTurn = &n
	}
	if msg.Artifact != nil {
		artifactID := msg.Artifact.ID
		artifactSize := int64(msg.Artifact.ByteSize)
		row.ArtifactID, row.ArtifactByteSize = &artifactID, &artifactSize
	}
	return row, nil
}

func messagePayload(row messageModel) map[string]any {
	return map[string]any{
		"memory_chunks_json": row.MemoryChunksJSON,
		"rag_chunks_json":    row.RAGChunksJSON,
		"original_seq_json":  row.OriginalSeqJSON, "role": row.Role, "tool_call_id": row.ToolCallID,
		"content": row.Content, "display_content": row.DisplayContent, "reasoning_id": row.ReasoningID,
		"compact_content":   row.CompactContent,
		"reasoning_content": row.ReasoningContent, "tool_calls_json": row.ToolCallsJSON,
		"finish_reason": row.FinishReason,
		"artifact_id":   row.ArtifactID, "artifact_byte_size": row.ArtifactByteSize,
		"token_input": row.TokenInput, "token_output": row.TokenOutput, "updated_at": row.UpdatedAt,
	}
}

func modelToMessage(row messageModel) (sharedkernel.Message, error) {
	var originalSeq []uint64
	if err := json.Unmarshal(row.OriginalSeqJSON, &originalSeq); err != nil {
		return sharedkernel.Message{}, err
	}
	var calls []sharedkernel.ToolCall
	if len(row.ToolCallsJSON) != 0 && string(row.ToolCallsJSON) != "null" {
		if err := json.Unmarshal(row.ToolCallsJSON, &calls); err != nil {
			return sharedkernel.Message{}, err
		}
	}
	msg := sharedkernel.Message{
		Seq: row.Seq, OriginalSeq: originalSeq, Role: row.Role, Content: row.Content, DisplayContent: row.DisplayContent,
		CompactContent: row.CompactContent,
		ReasoningID:    row.ReasoningID, ReasoningContent: row.ReasoningContent,
		ToolCalls: calls, ToolCallID: row.ToolCallID, FinishReason: row.FinishReason,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: int(row.TokenInput), TokenOutput: int(row.TokenOutput)},
	}
	if row.ReactTurn != nil {
		msg.ReactTurn = *row.ReactTurn
	}
	if len(row.MemoryChunksJSON) > 0 {
		if err := json.Unmarshal(row.MemoryChunksJSON, &msg.MemoryChunks); err != nil {
			return sharedkernel.Message{}, err
		}
	}
	if len(row.RAGChunksJSON) > 0 {
		if err := json.Unmarshal(row.RAGChunksJSON, &msg.RAGChunks); err != nil {
			return sharedkernel.Message{}, err
		}
	}
	if row.ArtifactID != nil {
		msg.Artifact = &sharedkernel.ArtifactRef{ID: *row.ArtifactID}
		if row.ArtifactByteSize != nil {
			msg.Artifact.ByteSize = int(*row.ArtifactByteSize)
		}
	}
	return msg, nil
}

func snapshotContains(messages []sharedkernel.Message, target sharedkernel.Message) bool {
	for _, msg := range messages {
		if msg.Seq == target.Seq {
			return equalMessage(msg, target)
		}
	}
	return false
}

func equalMessage(a, b sharedkernel.Message) bool {
	aJSON, errA := json.Marshal(a)
	bJSON, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aJSON) == string(bJSON) && a.DisplayContent == b.DisplayContent
}

var _ session.SessionRepository = (*SqliteSessionRepo)(nil)
var _ session.SessionHistoryRepository = (*SqliteSessionRepo)(nil)
var _ session.SessionCatalogRepository = (*SqliteSessionRepo)(nil)
