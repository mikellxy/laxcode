# 会话存储引擎设计

LaxCode 的会话存储需要同时解决三个问题：保留完整对话历史、恢复可直接发送给模型的
最新上下文，以及保证消息追加、上下文压缩和进程崩溃之间不会产生半提交状态。

## 双视图模型

会话消息被拆成两个用途不同的视图：

- `original` 是不可变原始历史，用于 UI 展示、审计和恢复消息身份。每条新消息只追加一次，
  固定使用 `memory_generation = 0`。
- `in_memory` 是实际发送给模型的工作集。它允许裁剪大体积工具输出、合并旧消息和生成摘要，
  因此按 `memory_generation` 分代。

`request_contexts` 是工作集的 head，记录当前 generation、最后消息序号、乐观锁 revision、
token 账目和 ReAct 轮次；`messages` 保存原始消息与工作集消息。加载会话时只读取 head
指向的最新 generation，不扫描完整历史或旧 generation，从而控制运行时内存和恢复开销。

消息的 `seq` 在整个会话中单调递增，压缩不会重新编号。摘要消息通过 `original_seq` 记录其
覆盖的原始消息序号，因此压缩后的工作集仍能追溯到原始历史。

## 候选提交与原子切换

领域层的 `Session` 不直接执行 I/O。application 层先从当前 Session 构造候选状态，
再把候选快照交给 `SessionRepository`：

1. 新消息在候选中获得稳定 `seq`。
2. 仓储在一个 SQLite 事务中写入 `original`、当前 generation 的 `in_memory` 副本，
   并更新 context head。
3. 数据库提交成功后，application 才用候选替换进程内的当前 Session。

提交失败时，内存状态和数据库状态都仍指向旧版本，调用方可以安全重试。压缩同样在完整克隆的
候选工作集上执行；只有裁剪或摘要完成、精确 token 计数达到目标后，才推进一次 generation，
批量写入新工作集并切换 head。

系统提示词更新使用独立的单消息更新路径，避免把提示词误追加到历史尾部；原始历史不随系统
提示词更新或上下文压缩而改写。

## 并发控制

每次提交都校验三类状态：

- `revision` 必须与数据库 head 一致，用于检测并发写入。
- `last_seq` 必须符合严格的单调递增规则，避免消息重复或跳号。
- `memory_generation` 必须保持不变，或在压缩提交时恰好增加一代。

head 更新还会在 SQL 条件中再次匹配 revision。即使两个请求基于同一个旧快照同时提交，
也只有一个可以成功，另一个会收到冲突错误，而不会静默覆盖数据。

SQLite 使用 WAL、`synchronous=FULL`、立即事务和单连接配置。这个选择牺牲了单进程内的写并发，
换取更简单、可预测的事务顺序，适合会话按序演进的写入模型。

## 崩溃恢复

LaxCode 不额外保存“当前 Agent 正执行到哪一步”的易失状态，而是从已提交工作集尾部推导：

- 尾部是无工具调用且正常结束的 assistant 消息，表示本轮已经收束。
- 尾部是 user、带工具调用的 assistant，或缺少对应结果的工具调用，表示本轮需要恢复。

恢复时只补齐最近一组尚未落盘的 tool result，再继续 ReAct 循环。由于每条 user、assistant 和
tool 消息都在产生后立即提交，崩溃最多留下一个可从消息结构确定的未闭合工具调用组，避免维护
另一套容易与消息历史失配的状态机。

## 存储开销与 generation 保留

在线路径始终只把最新 generation 放入 Session 内存，并只在恢复时读取这一代。这是当前对
内存占用、恢复速度和实现复杂度的平衡。

SQLite schema 和仓储接口仍把 generation 作为一等标识。当前 SQLite 实现写入新 generation
后会封存旧 generation，但旧代不会进入正常的加载路径。这样可以在不污染领域模型的情况下，
继续演进成可配置的保留策略：

- 只保留最新 generation，降低长会话的磁盘占用。
- 保留最近若干代，兼顾排障和空间成本。
- 保留每一代，并增加只读查询，用于比较压缩前后消息、token、摘要和 artifact 变化，支撑
  压缩过程的可观测性与离线分析。

保留策略属于 infrastructure 层；`Session` 和 ReAct 循环只依赖“提交下一代并原子切换 head”
这一语义，因此调整代际保留方式不需要改变领域层。

## 原始历史冷备

SQLite 是一致性主存储。数据库事务提交后，仓储还会把 `original` 追加到会话目录下的
`history.jsonl`，作为便于人工检查的 best-effort 冷备：

```text
${workdir}/.laxcode/.session/${session_id}/history.jsonl
```

JSONL 写入失败只记录警告，不回滚已成功的数据库事务。这避免辅助冷备故障影响主流程，代价是
JSONL 不能作为强一致的数据源；恢复和续聊始终以 SQLite 为准。

## 关键代码入口

- `internal/domain/session/repository.go:SessionRepository`
- `internal/domain/session/request_context.go:RequestContext`
- `internal/domain/session/request_context.go:Session.WithAppendedMessage`
- `internal/domain/session/request_context.go:Session.AdvanceMemoryGeneration`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo`
- `internal/infrastructure/sessionrepo/sqlite.go:NewSqliteSessionRepo`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.GetRequestContext`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitCreateMessage`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitUpdateMessage`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitNextMemoryGeneration`
- `internal/infrastructure/sessionrepo/history.go:appendHistory`
- `internal/application/reactservice/reactservice.go:ReActService.handleTurnMsg`
- `internal/application/reactservice/reactservice.go:ReActService.recoverBeforeChat`
- `internal/application/reactservice/reactservice.go:needsRecovery`
- `internal/application/reactservice/context_compaction.go:ReActService.compactContext`

