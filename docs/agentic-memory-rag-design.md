# Agentic 记忆与 RAG 设计

LaxCode 在用户消息进入 ReAct 循环前提供两条独立的上下文增强通道：

- 知识库 RAG：从指定文档库召回与问题相关的事实，服务当前问题。
- 用户长期记忆：从历史对话中召回用户事实和偏好，跨会话改善个性化表现。

两者最终都以 chunk 挂载到用户消息的工作集副本，但数据来源、生命周期和失败策略不同。

## 统一注入点，分离业务语义

`ReActService` 只依赖 `PromptEnricher` 和 `MemoryEnricher` 两个小接口，不感知 embedding
服务、sqlite-vec 或离线 pipeline。组合根按运行模式注入具体实现：QA 模式注入知识库 RAG，
普通 SSE 模式可注入用户记忆召回。

用户原始输入会先复制为不可变 `original`，召回结果随后只写入候选工作集消息：

- `RAGChunks` 保存知识库片段。
- `MemoryChunks` 保存用户长期记忆片段。
- `Content` 始终保留用户原文。

模型请求由 `Message.ModelContent` 在最后一刻组合原文和 chunks。这样 UI、审计历史和记忆提取
读取的仍是原始对话，增强数据不会冒充用户输入，也不会反向污染知识库。

## 知识库 RAG：同步、强依赖

知识库 RAG 的查询链路是：

```text
用户问题 → query embedding → sqlite-vec Top 4 → RAGChunks → 模型请求
```

知识库以只读方式打开。建库和查询必须使用相同的 embedding 模型与向量维度，否则相似度
没有可比性。QA 模式不挂载工具，使回答的外部上下文只来自已配置的知识库。

RAG 是 QA 回答正确性的组成部分，因此 embedding 或检索失败会终止本轮，而不是让模型在
缺少文档依据时静默回答。代价是知识库或 embedding 服务的短暂故障会直接影响可用性，收益是
调用方不会误把降级后的通用回答当作基于知识库的结果。

## 用户记忆：异步写入、尽力召回

用户记忆拆成写入和读取两条路径。

写入路径不阻塞聊天：每完成三轮 ReAct，会话事务从不可变原始历史构造一个记忆任务；后台
Worker 领取带 lease 的任务，调用 LLM 提取有明确用户依据的事实或偏好，再通过独立 pipeline
进行 chunk 和向量化，写入 sqlite-vec。任务支持超时、指数退避、重试上限和幂等 source key。

读取路径在生成前同步执行：

```text
用户问题 + user_id → query embedding → 按 user_id 隔离的 Top 3 → MemoryChunks
```

长期记忆只是个性化增强，不应成为对话的单点故障。召回失败时系统记录警告并继续生成；匿名
会话没有稳定 `user_id`，会跳过记忆召回和提取。异步写入降低了请求延迟，但采用最终一致性：
刚结束的对话不保证立刻能在下一次请求中被召回。

## 上下文与缓存取舍

召回 chunks 会随对应用户消息保留在当前工作集中，而不是每轮结束立即删除。历史请求前缀因此
保持稳定，更容易命中模型侧前缀缓存；同时，断点续聊也能复现当时实际发送给模型的增强上下文。

代价是 chunks 会占用上下文窗口。LaxCode 把清理集中到统一的上下文压缩阶段：旧消息中的
`RAGChunks` 和 `MemoryChunks` 可被回收，最新受保护消息仍保留召回结果。集中处理避免聊天路径、
记忆路径和 RAG 路径各自维护一套不一致的裁剪规则。

## 主要取舍

| 选择 | 收益 | 代价 |
| --- | --- | --- |
| 原文与增强副本分离 | 历史可审计，召回数据不污染用户输入 | 工作集需要额外保存 chunks |
| RAG 同步且失败即停止 | 不会静默失去知识依据 | 外部检索故障影响本轮可用性 |
| 记忆异步提取 | 不增加正常对话的总结延迟 | 新记忆只能最终一致 |
| 记忆召回尽力而为 | 个性化服务故障不阻断聊天 | 降级时回答可能缺少用户偏好 |
| chunks 延迟到压缩时清理 | 保持请求前缀稳定，利于缓存与重放 | 压缩前占用更多上下文 |
| 端口接口隔离具体实现 | 可替换向量库、embedding 和 pipeline | 组合根需要负责模式与生命周期装配 |

## 关键代码入口

- `internal/application/reactservice/reactservice.go:PromptEnricher`
- `internal/application/reactservice/reactservice.go:MemoryEnricher`
- `internal/application/reactservice/reactservice.go:ReActService.Chat`
- `internal/application/qaservice/qaservice.go:Service.Enrich`
- `internal/application/usermemory/service.go:RecallService.Recall`
- `internal/application/usermemory/service.go:Worker`
- `internal/infrastructure/sessionrepo/user_memory.go:createMemoryWindow`
- `internal/infrastructure/knowledgebase/sqlitevec.go:SQLiteVecRetriever.Search`
- `internal/infrastructure/knowledgebase/user_memory.go:SQLiteVecRetriever.SearchUser`
- `internal/infrastructure/memorypipeline/cli.go:CLI.Ingest`
- `internal/domain/sharedkernel/message.go:Message.ModelContent`
- `internal/domain/compactor/compactor.go:SimpleCompactor.Compress`

