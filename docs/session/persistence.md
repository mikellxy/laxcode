# laxcode-会话持久化实现方式

## 简介

[laxcode-会话持久化实现方式]SessionRepository 是持久化端口：domain 只声明要存什么、要读回什么，数据库表、事务和本地冷备由 infrastructure/sessionrepo 决定。加载与写回由 application 层编排，聚合自身不持有仓储。端口有四个方法：GetRequestContext 读最新工作集；CommitCreateMessage 原子创建一条不可变 original 及当前 generation 的 memory 副本；CommitUpdateMessage 只更新当前 generation 的一条 memory（用于重启后替换 system）；CommitNextMemoryGeneration 批量落盘压缩后的下一代 memory 并切换 context head。[laxcode-会话持久化实现方式]

[laxcode-会话持久化实现方式]SqliteSessionRepo 用两张表。request_contexts 存 context head（每会话一行，含 revision 与两本 token 账目）；messages 以复合主键（session_id, message_type, memory_generation, seq）同时承载不可变 original 与各代 memory，同一位置各有且只有一条，无需关联表。写路径靠乐观锁：提交前校验 revision 与库中一致、LastSeq 恰好加一、generation 匹配，任一不符返回 ErrContextConflict / ErrStaleSequence / ErrStaleGeneration，事务回滚。SQLite 走 WAL、单连接、synchronous(FULL)。[laxcode-会话持久化实现方式]

[laxcode-会话持久化实现方式]数据库事务成功后，appendHistory 把 original 追加写进会话目录的 history.jsonl 作 best-effort 冷备，失败只告警不影响提交。组合根 cmd/agentasm 负责装配：NewSqliteSessionRepo 接收 layout.SessionDB 与 SessionRoot，NewReActService 注入仓储与聚合。[laxcode-会话持久化实现方式]

## 关键代码入口
[laxcode-会话持久化实现方式]
- internal/domain/session/repository.go:SessionRepository
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitCreateMessage
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitUpdateMessage
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitNextMemoryGeneration
- internal/infrastructure/sessionrepo/sqlite.go:validateCreateTransition
- internal/infrastructure/sessionrepo/sqlite.go:writeContext
- internal/infrastructure/sessionrepo/history.go:appendHistory
- internal/application/reactservice/reactservice.go:ReActService.commitCreatedMessage
- internal/infrastructure/layout/layout.go:SessionDB
[laxcode-会话持久化实现方式]
