# Agentic 记忆与 RAG 设计

LaxCode 用 ReAct 生命周期中间件承载用户 query 的预处理与完成轮次的后处理，传输层和 `ReActService` 不感知附加内容来自知识库、用户记忆还是未来的其他能力。

## 中间件边界

`BeforeUserQuery` 顺序执行并允许中止。输入包含 `SessionID`、`UserID`、不可变 `Original`、可变 `ModelInput`，以及携带父 span 的 `context.Context`。中间件只能修改 `ModelInput`：

- 知识库 RAG：同步 embedding 与 Top 4 检索；失败向上返回，终止本轮。
- 用户记忆召回：按 `user_id` 隔离检索；失败内部记录日志并保留原输入。

`PostReactTurn` 在最终 assistant 消息提交后全部执行，任何一个失败都不会阻止后续实现，也不会把已成功的 Chat 改成失败。事件只包含 `Turn`、`SessionID`、`UserID` 和稳定的 `AssistantSeq`。

用户记忆提取的 Post 实现只做三轮边界判断和持久、幂等的任务入队。LLM 摘要、分块、embedding 与写库由后台 worker 执行；worker 启动时补建提交完成但尚未入队的窗口。异步工作创建自己的 trace，不复用已结束的请求 span。

## 原文与模型输入

`Message.Content` 始终保存用户原文，`Message.WrappedContent` 保存中间件产生的模型输入。不可变 original 历史只记录 `Content`，当前工作集记录两者；provider 和 token 计数统一读取 `ModelContent()`。

这样 UI、审计历史和记忆提取不会把召回数据当成用户输入，同时 Resume 可以复现已提交的模型输入。上下文压缩优先清理保护区之前可重新生成的 `WrappedContent`，当前轮仍保留。

## 运行模式

- `code`：完整 Coding Agent 工具集，不挂载用户记忆中间件。
- `rag`：不挂载工具，只挂载知识库 `BeforeUserQuery`，不挂载用户记忆中间件。

会话持久化 mode，chat 与 resume 都必须由相同 mode 的 SSE 服务处理。用户记忆能力保留为可装配实现，待后续迭代确定产品入口。

## 关键代码入口

- `internal/application/reactservice/middleware.go`
- `internal/application/reactservice/reactservice.go`
- `internal/application/qaservice/qaservice.go`
- `internal/application/usermemory/service.go`
- `internal/domain/prompt/sys.go`
- `internal/domain/sharedkernel/message.go`
- `internal/infrastructure/sessionrepo/user_memory.go`
- `internal/domain/compactor/compactor.go`
