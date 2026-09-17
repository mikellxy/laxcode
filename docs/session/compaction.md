# laxcode-上下文压缩实现方式

## 简介

[laxcode-上下文压缩实现方式]压缩的触发与编排都在 application 层：think 循环每轮生成前调 compactContext，以"下一个完整 provider 请求"为计数口径。占用达到可用输入的 80% 触发，目标回落到 60%，高低水位之间不动，避免长会话每轮重复裁剪。所有修改都在 Clone 出的候选工作集上进行，计数失败或未达标时不污染当前上下文。[laxcode-上下文压缩实现方式]

[laxcode-上下文压缩实现方式]压缩分两阶段，本地优先、LLM 摘要兜底。第一阶段 compactLocally：先把保护区之前的大工具输出归档成 artifact（原文写 FileStore，消息里换成引用），再用确定性策略 SimpleCompactor 多轮裁剪——顺序是已归档的旧工具输出、旧 reasoning、旧 assistant 正文，最近三个工具调用组起整段保护，调用与结果永远成对保留。每次裁剪后都请 provider 重新精确计数，确认真正下降才继续。第二阶段 compactWithSummary：本地裁剪达不到目标时，把保护区之前的旧历史交给专用摘要模型（ContextSummaryLLMClient，不携带业务工具定义），输出规范化 JSON，经 MergeSummary 合并为一条 user 摘要消息；模型不守上限时只允许一次基于已有摘要的再压缩，防止无界调用。[laxcode-上下文压缩实现方式]

[laxcode-上下文压缩实现方式]确认达标后，候选调 AdvanceMemoryGeneration 把世代加一，经 commitNextMemoryGeneration 批量落盘为下一代 memory 并切换 context head；旧 generation 保留在库里不再修改。摘要消息不走 AppendMessage，它在候选中由 MergeSummary 原子替换一段区间，Seq 沿用被合并区间的最小值，OriginalSeq 聚合全部来源，因此 LastSeq 不变。domain 侧的 Session.Compact 只负责在聚合内采纳策略返回的新序列，策略接口由 session 包自己声明，compactor 实现按结构化匹配隐式满足，两个包互不 import。[laxcode-上下文压缩实现方式]

## 关键代码入口
[laxcode-上下文压缩实现方式]
- internal/application/reactservice/context_compaction.go:ReActService.compactContext
- internal/application/reactservice/context_compaction.go:compactionRun
- internal/application/reactservice/context_compaction.go:ReActService.compactLocally
- internal/application/reactservice/context_compaction.go:ReActService.compactWithSummary
- internal/application/reactservice/context_summary.go:ReActService.generateContextSummary
- internal/domain/compactor/compactor.go:simpleStrategy.Compress
- internal/domain/compactor/compactor.go:ProtectedStart
- internal/domain/compactor/compactor.go:ArtifactCandidates
- internal/domain/compactor/summary.go:MergeSummary
- internal/domain/session/session.go:Session.Compact
- internal/domain/session/request_context.go:Session.AdvanceMemoryGeneration
- internal/infrastructure/artifactstore/fs.go:FileStore.PutArtifact
[laxcode-上下文压缩实现方式]
