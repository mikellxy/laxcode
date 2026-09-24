# 上下文压缩设计

LaxCode 在模型可用输入窗口达到 80% 时触发压缩，目标回落到 60%。高低水位避免长会话
每轮都重复压缩，并为下一轮模型输出、工具调用和提示词变化预留空间。

压缩按“可再生数据优先、可外置数据其次、语义摘要最后”的顺序执行：

1. 剪枝：回收旧消息上 `WrappedContent` 占用的空间。
2. 上下文卸载：把旧工具输出移出模型上下文，并清理低价值中间内容。
3. LLM 结构化摘要：确定性压缩仍不达标时，合并较早历史。

所有操作先作用于 Session 候选副本；只有达到目标并成功提交下一代工作集后，才替换当前上下文。

## 第一层：包装输入剪枝

这里的“剪枝”专指压缩时清空旧用户消息上的 `WrappedContent`，回收知识库、用户记忆或其他
预处理中间件附加内容占用的 token。用户原始 `Content`、消息身份和不可变历史均不受影响。

包装输入中的附加数据是可再生上下文，因此比用户原文、模型结论和工具结果更适合优先丢弃。
平时 `WrappedContent` 随工作集保留，以维持模型请求前缀稳定；只有窗口达到压缩阈值时
才统一回收，换取更好的前缀缓存命中率和可复现性。

最近保护区中的包装输入不会被删除。没有足够工具调用组可建立保护区时，最后一条 user 消息
被视为当前轮锚点：更早 user 消息的包装输入可以回收，当前问题的模型输入仍完整保留。

## 第二层：上下文卸载

压缩器保护最近三个工具调用组，以及组间的全部消息。未完成或跨越边界的调用组会把保护边界
向前扩展，确保 function call 与 tool result 不会被拆散。

保护区之前的大型工具输出先写入不可变 artifact，再用 `CompactContent` 和可按需读取的
artifact 引用替换正文。只有归档成功的输出才能被替换；归档失败时整次压缩失败，不会丢失结果。

随后按确定性顺序继续回收旧内容：

- 删除旧 assistant 的 `ReasoningContent`。
- 对过长的旧 assistant 正文保留首尾并截断中间部分。

system prompt 和普通 user 原文不会被这层直接截断。确定性策略速度快、无需额外模型调用，
但它只能删除或外置已识别的数据，无法理解多轮对话中哪些语义仍然重要。

## 第三层：LLM 结构化摘要

若前两层仍无法回落到目标，专用摘要模型会把 system prompt 与保护区之间的连续旧历史合并为
一条结构化 user 摘要。摘要固定包含目标、事实、决策、约束、已完成事项、待办、artifact 和
风险等字段，并拒绝未知字段或空摘要。

摘要输入在 artifact 归档后、确定性裁剪前保存：摘要模型能看到完整旧内容，同时获得可追溯的
artifact 引用。摘要请求不携带业务工具，历史内容也被明确标记为待总结数据，避免把旧指令当作
新任务执行。

模型可能不严格遵守 token 上限，因此最多允许一次基于已有摘要的再次压缩，避免失控重试和费用。
若仍无法达到目标，本轮在正常生成前失败，而不是向 provider 发送必然超窗的请求。

合并后的摘要沿用被替换区间最早的 `seq`，并聚合全部 `OriginalSeq`，保留从压缩工作集回溯到
不可变原始历史的能力。摘要模型自身消耗的 token 也计入会话累计用量。

## 计数与提交边界

本地压缩器的 token 估算只用于选择下一步动作，不能作为成功标准。每轮修改后都由实际生成
provider 对完整消息和工具定义重新精确计数；只有结果不高于 60% 目标才算完成。

完成后，application 层只推进一次 `MemoryGeneration`，并通过会话仓储原子写入新工作集和
切换 context head。artifact 写入、精确计数、摘要生成或数据库提交任一步失败，当前 Session
都保持压缩前状态。

## 主要取舍

| 选择 | 收益 | 代价 |
| --- | --- | --- |
| 80% 触发、60% 回落 | 减少频繁压缩并预留后续空间 | 单次压缩幅度更大 |
| 包装输入优先剪枝 | 优先回收可重新生成的数据 | 重放旧轮时不再携带当时的全部附加上下文 |
| 保护最近三个调用组 | 保留近期执行细节和工具协议完整性 | 保护区过大时可能无法达到目标 |
| 工具输出先归档再替换 | 大幅节省上下文且详情仍可读取 | 增加 artifact 存储和读取路径 |
| 确定性压缩优先 | 快、便宜、行为稳定 | 无法判断跨消息语义的重要程度 |
| LLM 摘要兜底 | 能保留跨轮目标、决策和约束 | 增加延迟、费用及信息损失风险 |
| provider 精确复算 | 与真实请求口径一致 | 压缩过程中需要额外计数调用 |
| 候选状态原子提交 | 失败不会污染当前上下文 | 压缩期间需要额外内存副本 |

## 关键代码入口

- `internal/domain/compactor/compactor.go:SimpleCompactor`
- `internal/domain/compactor/compactor.go:simpleStrategy.Compress`
- `internal/domain/compactor/compactor.go:ProtectedStart`
- `internal/domain/compactor/compactor.go:ArtifactCandidates`
- `internal/domain/compactor/summary.go:MergeSummary`
- `internal/domain/session/session.go:Session.Compact`
- `internal/application/reactservice/context_compaction.go:ReActService.compactContext`
- `internal/application/reactservice/context_compaction.go:ReActService.compactLocally`
- `internal/application/reactservice/context_compaction.go:ReActService.compactWithSummary`
- `internal/application/reactservice/context_summary.go:ReActService.generateContextSummary`
- `internal/application/reactservice/context_summary.go:normalizeContextSummary`
- `internal/infrastructure/artifactstore/fs.go:FileStore.PutArtifact`
- `internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.CommitNextMemoryGeneration`
