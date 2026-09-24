# 用户长期记忆中间件

用户长期记忆能力已经从 SSE 传输层拆到 ReAct 生命周期中间件，但当前 `code` 与 `rag` 两种 SSE mode 都不挂载它。后续迭代确定产品行为后，再由组合根选择启用。

## 生命周期接口

- `BeforeUserQuery` 在用户原文落库前顺序执行，可更新 `UserQuery.ModelInput`。知识库召回失败会向上返回；用户记忆召回采用 fail-open，内部记录日志并保留原输入。
- `PostReactTurn` 在最终 assistant 消息和轮次编号原子提交后全部执行。错误汇总记录，不改变已经成功的 Chat 返回。
- `context.Context` 传递当前同步链路的 trace 上下文。后台 worker 不复用可能已经结束的请求 span。

用户记忆实现包括：

- `RecallService`：按 `user_id` 召回记忆，并通过 prompt domain 包装模型输入。
- `PostTurnScheduler`：仅在完成轮次为 3 的倍数时，快速、幂等地创建持久任务。
- `Worker`：异步提取记忆并调用独立 pipeline 入库；启动时先补建“轮次已提交、任务尚未创建”的窗口。

## 消息与持久化

用户原文保存在 `Message.Content`。中间件生成的模型输入保存在工作集消息的 `Message.WrappedContent`；不可变 original 记录只保存原文。上下文压缩会优先清理保护区之前的 `WrappedContent`，再处理工具输出、reasoning 和旧 assistant 正文。

`PostReactTurn` 只接收 `Turn`、`SessionID`、`UserID` 和 `AssistantSeq`，不携带完整 message。任务调度器通过稳定的 assistant 序号从不可变历史组装来源窗口。

## 当前运行模式

```sh
./bin/laxcode -sse -mode=code
./bin/laxcode -sse -mode=rag -kb=/absolute/path/kb.sqlite
```

SSE 会话创建时持久化 mode；chat 和 resume 都拒绝由不同 mode 的服务打开该会话。已移除原纯聊天 SSE 模式以及 `-code`、`-qa`、`-vector-dim` 参数。
