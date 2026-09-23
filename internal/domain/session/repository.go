package session

import (
	"context"
	"time"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// HistoryMessage 是会话历史的只读投影，只包含允许交给 UI 的字段。
type HistoryMessage struct {
	Seq              uint64
	Role             string
	Content          string
	ReasoningContent string
	ToolSummary      string
	CreatedAt        time.Time
}

type HistoryPage struct {
	Messages []HistoryMessage
	HasMore  bool
}

// SessionHistoryRepository 是面向历史消息接口的独立读端口，避免把分页查询职责
// 强加给 ReActService 使用的写仓储测试替身。
type SessionHistoryRepository interface {
	// ListOriginalHistory 返回 seq < beforeSeq 的最近一页可展示原始消息。
	// beforeSeq=0 表示从最新消息开始；返回结果按 seq 正序排列。
	ListOriginalHistory(ctx context.Context, sessionID string, beforeSeq uint64, limit int) (HistoryPage, bool, error)
}

type Summary struct {
	ID        string
	UserID    string
	Title     string
	WorkDir   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type SummaryPage struct {
	Sessions []Summary
	HasMore  bool
}

// SessionCatalogRepository 管理显式创建的空会话及按用户隔离的会话列表。
type SessionCatalogRepository interface {
	CreateSession(ctx context.Context, sessionID, userID, title, workDir string) (Summary, error)
	GetSession(ctx context.Context, sessionID string) (Summary, error)
	// ListSessions 返回指定用户在 beforeSessionID 之前的一页会话，按更新时间倒序。
	// beforeSessionID 为空表示第一页。
	ListSessions(ctx context.Context, userID, beforeSessionID string, limit int) (SummaryPage, error)
}

// SessionRepository 是会话持久化端口：domain 只声明“要存什么、要读回什么”，
// 数据库表、事务和本地冷备由 infrastructure/sessionrepo 决定。
// 加载与写回由 application 层编排，聚合自身不持有本端口。
//
// 运行时只读写最新 RequestContext。仓储负责在同一数据库事务内更新工作集和
// 不可变原始历史；history.jsonl 只是提交后的 best-effort 本地冷备。
type SessionRepository interface {
	// GetRequestContext 读取数据库中的最新工作集；会话不存在时返回空工作集。
	GetRequestContext(ctx context.Context, sessionID string) (RequestContext, error)
	// CommitCreateMessage 原子创建一条不可变 original、当前 generation 的
	// memory 副本及新的 context head。首次 system 也使用本方法创建 Session。
	CommitCreateMessage(ctx context.Context, sessionID string, snapshot RequestContext, original, memory sharedkernel.Message) (uint64, error)
	// CommitUpdateMessage 只更新当前 generation 中的一条 memory 消息；用于
	// 后续启动时替换 system，original 与 generation 均保持不变。
	CommitUpdateMessage(ctx context.Context, sessionID string, snapshot RequestContext, memory sharedkernel.Message) (uint64, error)
	// CommitNextMemoryGeneration 批量创建压缩后的下一代 memory 消息并原子
	// 切换 context head，旧 generation 保留且不再修改。
	CommitNextMemoryGeneration(ctx context.Context, sessionID string, snapshot RequestContext) (uint64, error)
}
