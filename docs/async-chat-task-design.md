# 异步聊天任务与 SSE 重连恢复设计

状态：设计方案，尚未实现。本文只定义本轮原型范围，不表示现有接口已经切换。

目标：浏览器刷新、关闭页面或 SSE 断线不取消后台聊天；同项目任务串行；重新打开页面可以恢复已保存历史和当前任务输出；失败后由用户选择继续执行或发送新消息。

现有接口参考 [SSE Web 客户端接口契约](sse-web-client-contract.md)，消息存储参考 [会话存储引擎](session-storage-engine.md)。本文描述即将采用的任务模式，实施时需同步更新旧契约。

## 1. 范围与约束

本轮包含：

- chat/resume 提交任务后返回 JSON；执行脱离 HTTP 请求生命周期。
- 每用户现有 `sessions.db` 增加任务表，不建立新的全局数据库。
- 项目级活跃任务唯一约束，用户锁仅覆盖短时间的校验和创建。
- 每次执行有独立 task_id；resume 创建新任务，旧任务保持 fail。
- 历史接口返回最后任务及 can_resume；独立 SSE 接口重放和订阅任务事件。
- NATS JetStream 文件存储原型，每个任务独立 subject，每个 SSE 连接独立消费。
- 浏览器本地保留尚未确认提交的输入，支持同一用户身份、同一浏览器内重试。
- 主动停止、后台任务保活、cloud 启动恢复。

本轮不包含：

- 服务端持久化待执行输入、重启后自动重派任务、分布式调度和多 cloud 同时执行同一用户任务。
- 提交请求的持久化幂等键；接受响应丢失后用户重试可能重复执行的限制。
- DB 与 MQ 的跨存储事务、工具外部副作用的 exactly-once 保证。
- 按时间定时清理旧任务事件；仅在新任务成功启动及 cloud 启动时清理较老任务，接受一定磁盘浪费。

### 编排代码的强制约束

`handleChat`、`handleResume`、`handleTaskEvents` 三个主函数必须完整展示各自的编排。阅读单个主函数，应能看到参数和身份校验、锁边界、状态判断、任务创建或订阅、执行或消费、错误处理和响应。

允许调用职责单一的辅助函数，例如参数解析、单次仓储操作、消息序号分配、一次 MQ 发布、consumer 创建、JSON/SSE 编码，以及现有 ReAct/文件统计领域能力。

不得拆成 `prepareChat → submitChat → startChat`、`validateResume → scheduleResume` 或 `prepareStream → replayStream → followStream` 等阶段函数。不得用一个 `runTask`/`serveTaskStream` 包装隐藏主要流程。为了集中表达完整流程，chat/resume 可以保留必要的编排重复。

异步执行及其 defer 在主函数的 goroutine 闭包内完整展开。该 defer 属于后台 ReAct 执行作用域，不是 HTTP handler 返回时执行的 defer；HTTP handler 不负责宣布任务执行成功。

下文为 Go 风格的设计伪代码，不是可直接编译的实现。仓储、MQ 和 runtime registry 的调用代表单一操作；示意名称不限定最终命名。

## 2. 任务与持久化

### 2.1 任务表

任务属于当前用户的 `sessions.db`；user_id 从已验证的运行身份取得，不接受客户端任意指定归属。

| 字段 | 用途 |
| --- | --- |
| task_order | DB 生成的递增顺序，用于可靠确定 session 最后任务；不按 UUID 或时间戳字符串排序 |
| task_id | 每次执行的唯一 ID |
| user_id、project_id、session_id | 所属用户、项目和会话；project_id 由 session 查询得到 |
| status | waiting / running / success / fail |
| error_code、error_message | 失败原因；主动停止和服务重启也使用 fail，并区分原因 |
| created_at、started_at、finished_at | 生命周期时间 |
| final_event_id | 终态时已确认进入 MQ 的最后一个输出事件 ID；无事件为 0，活跃任务为空 |

`operation=chat/resume` 本轮不必保存：提交入口已经决定执行方法，重启统一标 fail，不从 DB 自动重派任务。也不在任务表保存用户输入，输入重试由前端承担。

项目级唯一索引示意：

```sql
CREATE UNIQUE INDEX one_active_task_per_project
ON chat_tasks(project_id)
WHERE status IN ('waiting', 'running');
```

每用户独立 DB，project_id 已足以限定唯一约束。session 固定属于 project，因此不再增加重复的 session 活跃任务唯一索引。任务表与具体 session/project 的关联仍需校验。

### 2.2 状态与最终依据

```text
waiting → running → success
    └─────────────→ fail
              └───→ fail
```

- waiting：任务已接受，尚未开始 ReAct 执行。
- running：开始装配和执行任务。装配失败也进入 fail。
- success：执行正常结束，消息闭环且必要的文件统计等收尾成功。
- fail：执行失败、主动停止、生成未完整结束或服务重启等。
- fail 不自动推导 can_resume；后者仍从已保存的工作集、tool_calls 和 finish_reason 判断。
- 同一 task 不从终态回到 waiting；每次 resume 都创建新 task。

DB 是任务状态的最终依据。终态和 final_event_id 在同一次 DB 更新中提交。MQ 发布采用持久化发布确认；不沿用现有 SSE 写失败后静默跳过的逻辑。发布失败应使任务取消并失败，不能继续执行却假装输出可完整恢复。

`final_event_id` 是 ticker 补齐输出的内部边界，和历史消息 seq 无关。后台执行在停止产生输出后记录此边界；DB 进入终态后不得再发布该任务的内容事件。

如果收尾 DB 写入在有限重试/超时后仍失败，不伪造终态，不释放 DB 项目占用；记录可定位 task_id 的错误，由故障处理或下次 cloud 启动恢复。该情况不承诺自动恢复。

## 3. 接口契约

以下新路径和错误码是本文采用的原型约定。身份验证继续走现有 proxy/cloud 链路。

| 接口 | 行为 |
| --- | --- |
| `POST /chat` | body 沿用 `{session_id, task}`；接受后返回 `{task_id, task_status: "waiting"}`，HTTP 200 |
| `POST /api/sessions/{session_id}/resume` | body `{task_id}` 指向用户希望继续的旧失败任务；接受后返回新 task_id 和 waiting，HTTP 200 |
| `GET /api/sessions/{session_id}/messages` | 保留消息、分页和 can_resume；新增最后任务的 task_id、task_status、task_error_code、task_error_message；没有任务时为 null |
| `GET /api/tasks/{task_id}/events` | SSE；只允许订阅当前 session 最后一个任务，waiting/running/success/fail 都允许 |
| `POST /api/tasks/{task_id}/cancel` | 请求取消指定任务；不立即把仍在执行的任务标为终态 |

chat/resume 的 waiting 是创建时快照；返回到浏览器时后台可能已 running 或完成，不能据此拒绝随后对终态任务的订阅。

历史的最新一页、can_resume 和最后任务信息应来自一致的 DB 读取快照，避免同次响应混合新旧状态；读取更早分页不能覆盖前端当前任务状态。历史中不插入 MQ 临时片段。

冲突响应使用 HTTP 409，携带明确 code/message 和 `refresh_after_ms: 2000`：

- `PROJECT_BUSY`：同项目其他 session 正在执行。
- `SESSION_BUSY`：本 session 正在执行。
- `TASK_CHANGED`：请求引用的 task 已不是 session 最后任务，或 resume 引用的任务已不是 fail。
- `NOTHING_TO_RESUME`：当前工作集不可继续。

前端等待后重新拉取会话历史和任务状态，重建订阅，不自动重发 chat/resume，也不要求浏览器整页 reload。其他数据库错误不能一律转换成并发冲突。

没有任务的旧会话仍根据 can_resume 显示继续。为兼容旧数据，resume 可接受缺省 task_id，但仅限 DB 确认该 session 从未创建过任务且 can_resume=true；已有任何任务就必须提供匹配的失败 task_id。

## 4. chat 主函数编排

```go
func handleChat(w, r) {
    user := authenticate(r)
    req := decodeAndValidateChat(r) // session_id、非空 task；错误立即返回

    userLock.Lock(user.ID)
    // 各提前返回路径必须释放用户锁；此锁不覆盖整个函数。
    if serviceCtx.Err() != nil {
        userLock.Unlock(user.ID)
        replyUnavailable(w)
        return
    }
    session, err := repo.GetOwnedSession(r.Context(), user.ID, req.SessionID)
    if err != nil {
        userLock.Unlock(user.ID)
        replyLoadError(w, err)
        return
    }
    active, err := repo.GetActiveProjectTask(r.Context(), session.ProjectID)
    if err != nil || active != nil {
        userLock.Unlock(user.ID)
        // active.SessionID 相同则 SESSION_BUSY，否则 PROJECT_BUSY。
        replyQueryErrorOrBusy(w, err, active, session.ID, 2000)
        return
    }
    task, err := repo.InsertWaitingTask(r.Context(), user.ID, session.ProjectID, session.ID)
    if err != nil {
        if isActiveProjectUniqueConflict(err) {
            active, lookupErr := repo.GetActiveProjectTask(r.Context(), session.ProjectID)
            userLock.Unlock(user.ID)
            replyQueryErrorOrBusy(w, lookupErr, active, session.ID, 2000)
        } else {
            userLock.Unlock(user.ID)
            replyDBError(w, err) // 不把未知写入结果当作确定未接受
        }
        return
    }
    taskCtx, cancel := context.WithCancelCause(serviceCtx)
    runtime.Register(task.ID, cancel) // 注册取消句柄并计入后台活动数
    userLock.Unlock(user.ID)

    // 接受后即使 r.Context() 取消，也启动已创建的任务。
    go func() {
        var execErr error
        var publisher *TaskPublisher
        var agent *AssembledAgent
        defer func() {
            if p := recover(); p != nil { execErr = panicError(p) }
            if execErr == nil && taskCtx.Err() != nil { execErr = context.Cause(taskCtx) }
            // 确认执行、工具和文件统计已退出，不再产生内容事件。
            if agent != nil { agent.Cleanup() }
            finishCtx, finishCancel := context.WithTimeout(context.Background(), finishTimeout)
            defer finishCancel()
            status, reason := "success", ""
            if execErr != nil { status, reason = "fail", publicReason(execErr) }
            finalID := lastAcknowledgedEventID(publisher) // nil publisher 返回 0
            err := repo.SetTerminal(finishCtx, task.ID, status, reason, finalID)
            if err != nil { logTaskFinalizationFailure(task.ID, err) }
            runtime.Unregister(task.ID)
            cancel(nil)
            // 终态由 SSE 消费循环读取 DB 后通知，不依赖结束帧成功写入 MQ。
        }()

        execErr = repo.SetRunning(taskCtx, task.ID) // waiting → running，条件更新
        if execErr != nil { return }
        publisher, execErr = mq.NewTaskPublisher(taskCtx, user.ID, session.ID, task.ID)
        if execErr != nil { return }
        agent, execErr = assembleAgent(taskCtx, session, task.ID, publisher)
        if execErr != nil { return }
        // 成功启动后仅清理同 session、task_order 更小的任务事件。
        // 清理失败记录日志，不取消本次执行；不触碰任务历史行。
        if err := mq.PurgeOlderTaskSubjects(taskCtx, session.ID, task.Order); err != nil {
            logCleanupFailure(task.ID, err)
        }
        execErr = publisher.PublishStart(taskCtx)
        if execErr != nil { return }
        _, execErr = runWithFileTracking(taskCtx, session.WorkDir, agent, func() {
            return agent.Service.Chat(taskCtx, req.Task, task.ID)
        })
        if execErr == nil && needsRecovery(agent.Session.Messages) {
            execErr = ErrIncompleteTurn
        }
    }()

    replyJSON(w, 200, {task_id: task.ID, task_status: "waiting"})
}
```

输入、追踪信息和配置等后台所需值应在启动前明确捕获，不在后台读取已关闭的请求 body，也不复用请求取消信号。可以延续 trace 关联，但必须使任务 span 生命周期独立于提交请求。

## 5. resume 主函数编排

```go
func handleResume(w, r) {
    user := authenticate(r)
    req := decodeAndValidateResume(r) // 路径 session_id；body 旧 task_id

    userLock.Lock(user.ID)
    if serviceCtx.Err() != nil {
        userLock.Unlock(user.ID)
        replyUnavailable(w)
        return
    }
    session, err := repo.GetOwnedSession(r.Context(), user.ID, req.SessionID)
    if err != nil {
        userLock.Unlock(user.ID)
        replyLoadError(w, err)
        return
    }
    latest, err := repo.GetLatestTask(r.Context(), session.ID)
    if err != nil {
        userLock.Unlock(user.ID)
        replyDBError(w, err)
        return
    }
    // 缺省 task_id 仅兼容完全没有任务记录的旧会话。
    if (latest == nil && req.TaskID != "") ||
       (latest != nil && (latest.ID != req.TaskID || latest.Status != "fail")) {
        userLock.Unlock(user.ID)
        replyConflict(w, "TASK_CHANGED", 2000)
        return
    }
    active, err := repo.GetActiveProjectTask(r.Context(), session.ProjectID)
    if err != nil || active != nil {
        userLock.Unlock(user.ID)
        replyQueryErrorOrBusy(w, err, active, session.ID, 2000)
        return
    }
    canResume, err := repo.CanResume(r.Context(), session.ID)
    if err != nil || !canResume {
        userLock.Unlock(user.ID)
        replyReadErrorOrNothingToResume(w, err, 2000)
        return
    }
    task, err := repo.InsertWaitingTask(r.Context(), user.ID, session.ProjectID, session.ID)
    if err != nil {
        if isActiveProjectUniqueConflict(err) {
            active, lookupErr := repo.GetActiveProjectTask(r.Context(), session.ProjectID)
            userLock.Unlock(user.ID)
            replyQueryErrorOrBusy(w, lookupErr, active, session.ID, 2000)
        } else {
            userLock.Unlock(user.ID)
            replyDBError(w, err)
        }
        return
    }
    taskCtx, cancel := context.WithCancelCause(serviceCtx)
    runtime.Register(task.ID, cancel)
    userLock.Unlock(user.ID)

    go func() {
        var execErr error
        var publisher *TaskPublisher
        var agent *AssembledAgent
        defer func() {
            if p := recover(); p != nil { execErr = panicError(p) }
            if execErr == nil && taskCtx.Err() != nil { execErr = context.Cause(taskCtx) }
            if agent != nil { agent.Cleanup() }
            finishCtx, finishCancel := context.WithTimeout(context.Background(), finishTimeout)
            defer finishCancel()
            status, reason := "success", ""
            if execErr != nil { status, reason = "fail", publicReason(execErr) }
            finalID := lastAcknowledgedEventID(publisher)
            err := repo.SetTerminal(finishCtx, task.ID, status, reason, finalID)
            if err != nil { logTaskFinalizationFailure(task.ID, err) }
            runtime.Unregister(task.ID)
            cancel(nil)
        }()

        execErr = repo.SetRunning(taskCtx, task.ID)
        if execErr != nil { return }
        publisher, execErr = mq.NewTaskPublisher(taskCtx, user.ID, session.ID, task.ID)
        if execErr != nil { return }
        agent, execErr = assembleAgent(taskCtx, session, task.ID, publisher)
        if execErr != nil { return }
        if err := mq.PurgeOlderTaskSubjects(taskCtx, session.ID, task.Order); err != nil {
            logCleanupFailure(task.ID, err)
        }
        execErr = publisher.PublishStart(taskCtx)
        if execErr != nil { return }
        _, execErr = runWithFileTracking(taskCtx, session.WorkDir, agent, func() {
            return agent.Service.Resume(taskCtx, task.ID)
        })
        if execErr == nil && needsRecovery(agent.Session.Messages) {
            execErr = ErrIncompleteTurn
        }
    }()

    replyJSON(w, 200, {task_id: task.ID, task_status: "waiting"})
}
```

Resume 内仍补齐缺失 tool result，不追加新 user message。沿用已有用户轮次 chat_id，但创建新的 task_id；两个 ID 不混用。

## 6. MQ、事件和消息身份

### 6.1 MQ 约定

采用启用 JetStream 的本机 NATS 服务，使用文件存储，文件目录不放在临时容器可写层。每个 task 对应独立 subject：

```text
laxcode.events.<user_id>.<session_id>.<task_id>
```

一个或少量 stream 覆盖这些 subject，不要求每个任务创建一个 stream。使用可重复读取的 LimitsPolicy，不使用消费后删除的 WorkQueuePolicy 或依赖在线消费者保留数据的策略。

每个 SSE 连接拥有独立的 consumer 和读取位置；断开时销毁本连接 consumer，不销毁任务输出。刷新后可以从该 task 起点重放，多页面互不抢消息。consumer 要支持任务尚未发布第一条事件时等待。

原型不按时间淘汰任务输出，不另外引入定时清理机制。新任务成功启动和 cloud 启动时，按 DB task_order 清理同 session 较老任务的 subject；保留最后任务。清理边界必须有 user_id/session_id/task_id 限定。

新任务开始后，旧任务只存在 MQ、尚未写入历史的思考片段允许丢失。旧任务已保存历史及 task 记录不删除。旧连接若遇到任务替换或事件被清理，应收到刷新指令，不无限重试旧 subject。

参考：[JetStream 官方概念](https://docs.nats.io/concepts/jetstream)、[官方保留策略说明](https://github.com/nats-io/nats.docs/blob/master/using-nats/jetstream/model_deep_dive.md)。

### 6.2 事件身份

MQ 中的事件统一携带 task_id 和任务内单调递增 event_id；内容事件额外携带 message_seq。例如：

```json
{
  "task_id": "task-uuid",
  "event_id": 17,
  "message_seq": 42,
  "type": "reasoning",
  "data": {"delta": "正在分析…"}
}
```

- event_id 不使用 JetStream 全局 stream sequence 冒充；后者可能跨 subject，且存在间隔。
- reasoning/message 增量属于本次 LLM assistant 消息；tool_call 的执行提示属于随后对应的 tool 消息。开始流式生成或执行工具前分配稳定 message_seq。
- 同一次 LLM 调用的全部文本和思考片段共享一个 message_seq。每次工具执行有自己的消息身份，不能按 token 发号。
- 当前消息 seq 是在候选提交时分配的；不能假定现状已支持提前分配。实施时需使预分配和后续历史提交使用同一身份，同时保留现有 revision、generation 和消息提交校验。
- 已发布但未提交的序号不能在后续任务中被当作另一条消息重复使用。需要持久的预留高水位，允许失败留下序号空洞；不得把预留号当作已经保存的历史最后序号。存储规则若要求连续追加，应明确调整并测试，而非跳过校验。
- 新建 user 消息、恢复补写的 tool 消息和其他原始消息也必须参与同一套消息发号，避免与提前预留号冲突。
- `think` 接收/可获得 task_id，并通过注入的事件 Consumer 发布事件；领域模型不直接依赖 NATS 客户端。

为减少 DB/MQ 终态双写问题，任务结束统一由 SSE 层根据 DB 生成 `task_state` 控制事件。它包含 task_id、task_status、错误原因和刷新动作，不携带 message_seq，也不进入内容增量去重。正常结束与 ticker 兜底共用这一规则；不要求 MQ 一定保存 done/error 结束帧。

## 7. SSE 主函数编排

原型 ticker 建议每 2 秒检查任务状态及是否仍是 session 最后任务。每次 DB 查询、MQ 操作需要有限超时；网络故障时结束本连接让前端重连，不把查询失败伪装成任务 fail。

```go
func handleTaskEvents(w, r) {
    user := authenticate(r)
    taskID := decodeAndValidateTaskID(r)

    userLock.Lock(user.ID)
    task, err := repo.GetOwnedTask(r.Context(), user.ID, taskID)
    if err != nil {
        userLock.Unlock(user.ID)
        replyLoadError(w, err)
        return
    }
    latest, err := repo.GetLatestTask(r.Context(), task.SessionID)
    if err != nil || latest.ID != task.ID {
        userLock.Unlock(user.ID)
        replyReadErrorOrTaskChanged(w, err, 2000)
        return
    }
    // 不要求 waiting/running；允许重放最后一个 success/fail 任务。
    subject := taskSubject(user.ID, task.SessionID, task.ID)
    userLock.Unlock(user.ID)

    consumer, err := mq.NewIndependentConsumer(r.Context(), subject, FromBeginning)
    if err != nil {
        replyMQError(w, err)
        return
    }
    defer consumer.Close() // 只销毁当前订阅
    beginSSE(w)
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    lastDelivered := uint64(0)
    terminal := terminalSnapshotOrNil(task)

    for {
        if terminal != nil && lastDelivered >= terminal.FinalEventID {
            // final_event_id 之前的输出已经写给本连接。
            // write 失败只结束订阅，不能取消后台执行。
            writeSSE(w, "task_state", {
                task_id: task.ID, task_status: terminal.Status,
                error_code: terminal.ErrorCode, error_message: terminal.ErrorMessage,
                action: "refresh_session",
            })
            return
        }
        select {
        case <-r.Context().Done():
            return
        case event := <-consumer.Events():
            // 伪代码：实现时显式处理 channel 关闭，不能无限读取零值。
            if event.TaskID != task.ID || event.EventID <= lastDelivered {
                continue
            }
            if event.EventID != lastDelivered + 1 {
                // 已有内容缺失，不能跳过缺口并宣称完整恢复。
                writeSSE(w, "refresh_required", {code: "EVENT_GAP", action: "refresh_session"})
                return
            }
            if err := writeSSE(w, event.Type, event); err != nil { return }
            lastDelivered = event.EventID
        case err := <-consumer.Errors():
            // 可重连的 MQ 故障不是任务失败。task 已被替换/清理则提示刷新。
            writeSSE(w, "refresh_required", {code: publicMQCode(err), action: "refresh_session"})
            return
        case <-ticker.C:
            current, latest, err := repo.GetTaskAndLatest(r.Context(), task.ID, task.SessionID)
            if err != nil {
                writeSSE(w, "refresh_required", {code: "STATUS_UNAVAILABLE", action: "refresh_session"})
                return
            }
            if latest.ID != task.ID {
                writeSSE(w, "refresh_required", {code: "TASK_CHANGED", refresh_after_ms: 2000})
                return
            }
            terminal = terminalSnapshotOrNil(current) // 同时兜底 success 和 fail
            // 不直接切断 MQ 回放；下一轮到达 final_event_id 后发送 task_state。
        }
    }
}
```

正常完成时 producer 已确认全部输出发布，再把终态和 final_event_id 写入 DB，因此 SSE 可以确认“补齐到哪里结束”。订阅期间发现新任务取代旧任务，直接刷新会话；旧 subject 此时允许被清理，不承诺读完旧任务临时输出。

cloud 启动恢复的遗留任务没有正常执行收尾：确认旧执行者已经退出后，读取对应 subject 最后的有效 event_id，再将 fail 原因及该边界写入 DB。若 MQ 不可用、无法确定边界，应停止就绪流程并报错，不能把边界当作 0 假装无输出。不存在 subject 表示任务尚未产生事件，可使用 0。

## 8. 前端恢复与输入重试

### 8.1 打开页面与重连

1. 确定目标 session，拉取最新历史及 task 状态，清空旧的临时 SSE 内容和事件游标。
2. waiting/running：主动订阅返回的 task_id，从任务起点重放。
3. success：显示已保存历史；不自动继续。若存在尚未提交的本地输入，按下文判断重试。
4. fail：展示原因；can_resume=true 时显示继续按钮，携带旧 task_id 调用 resume。也允许发送新消息调用 chat。
5. task_id 为空：保留旧数据 can_resume 逻辑，不把旧的未完成会话误认为已完成。
6. 已在页面上发生断线：退避后重新拉历史，清空临时内容，再订阅。不能在清空游标后把重放内容继续追加到旧临时字符串。
7. chat/resume 返回新 task_id 后也先同步获取一次历史。若最后任务已变化，以历史结果为准，不执着订阅旧 ID。
8. 收到 task_state 或 refresh_required：重新获取历史和状态。已终态不自动再次订阅，避免失败后不断刷新。

刷新的是会话数据，不强制浏览器整页 reload。网络断开只影响订阅，不触发 cancel，也不自动调用 resume。

### 8.2 内容去重

沿用消息模型区分最后消息来自完整历史还是 SSE，不另维护一份 committed_message_seq。last_message_seq 必须取实际消息序号，不能用渲染分组下标或分页游标代替。

| 内容事件 | 处理 |
| --- | --- |
| message_seq < last_message_seq | 丢弃 |
| message_seq 相等，最后消息来自完整历史 | 丢弃，该消息已经完整保存 |
| message_seq 相等，最后消息来自 SSE | event_id <= last_event_id 时丢弃，否则合并增量 |
| message_seq > last_message_seq | 检查任务内事件去重后创建新流式消息，推进游标 |

控制事件 start/task_state/refresh_required 不按消息序号过滤。不同 task 不共享 event_id 游标。历史分页不覆盖当前流式游标，重新加载完整当前快照时才统一重建。

### 8.3 未提交输入的浏览器本地恢复

使用 localStorage，按 `user_id + session_id` 隔离；实际文本不写 Cookie。Cookie 身份改变后不展示原用户记录。本轮只保存当前会话最后一次尚未确认提交的输入，跨浏览器恢复不在范围内。

记录包含：

```json
{
  "text": "用户最后一次发送的内容",
  "base_message_seq": 11,
  "task_id": "提交响应返回后补充；响应丢失时可为空"
}
```

- 发送前保存 text 和 base_message_seq；基线取服务端历史最后一条已保存消息，不限于 assistant，空历史取 0。
- 基线不包含本地临时用户消息，也不包含尚未落库的 SSE 消息。已有任务运行时禁止本页发送新的 chat，但后端唯一约束仍是最终保护。
- 提交响应收到后补充 task_id；实际关联发生变化时先刷新，不根据旧任务的结果判断新输入。
- 本地提交请求还在进行中，或者项目存在活跃任务时，不展示可点击重试。
- 本次提交已结束、无活跃任务，且历史为空或最后已保存消息 seq <= base_message_seq 时，在本地用户消息旁显示重试。终态 success 也不能仅凭状态删除尚未确认提交的输入。
- 历史有 seq > base_message_seq 的新 user 消息且可确认对应本地输入时，移除本地记录，以服务端历史为准；仅出现 assistant/tool 新消息不能证明输入已提交。
- 同浏览器多标签页也可能并发发相同文本，本轮没有服务端幂等关联，不承诺只靠基线和文本判定所有情况。关联不确定时不自动重发，保留本地输入供用户检查。
- 点击重试重新调用 chat；can_resume 则继续最近已保存的用户轮次，两者不能互相替代。

## 9. 主动停止、保活与重启

### 主动停止

取消接口校验 user/session/task 归属，在短用户锁内读取任务状态和取消句柄。终态请求幂等返回当前状态；活跃任务调用 cancel，回复“已请求停止”。后台执行函数实际结束、工具及必要收尾退出后才写 fail，原因“用户主动停止”。

waiting 也需要可取消，因此取消句柄和后台活动计数在任务接受后、释放用户锁前注册。取消后不立即释放项目唯一约束。查询 task 仍为活跃但 runtime 句柄缺失时应报告状态异常，不谎称已经停止。

### 沙箱保活

`/internal/activity` 包括现有请求活动、评估任务和新后台任务。新后台任务从接受开始计数，覆盖 waiting、running、工具退出及收尾。关闭页面后仍有后台任务，proxy 不应回收沙箱。

cloud 关闭时先停止接收新任务，再取消服务 context 并等待后台任务有限时间收尾；关闭仓储和 MQ 在后。强杀时依赖下次启动恢复。

### cloud 启动

在对外就绪和接受 chat/resume 之前：

1. 确认当前进程独占该用户 workplace，旧执行者不会继续写入。
2. 将本 cloud 负责的遗留 waiting/running 统一改为 fail，原因“服务重启，执行已中断”；保留对应最后输出边界。
3. 对每个 session 保留最新 task 的 MQ 数据，清理较老 task subject；清理失败记录，不删除 DB 历史。
4. 恢复完成后才接受请求；恢复任务状态失败不得假装就绪。

proxy 独立重启不执行上述 task 失败转换，因为原 cloud 可能仍在执行。NATS 短暂故障也不能被等同于 cloud 重启。

## 10. 实施入口与联动

- `cmd/run_sse/handler.go`：chat/resume 完整主函数及新任务订阅、停止入口。
- `cmd/run_sse/sse.go`：事件封装、消息身份与输出 Consumer；不再把浏览器断开当作执行取消。
- `cmd/run_sse/cloud.go`：服务 context、后台活动数、启动恢复和关闭顺序。
- `internal/application/reactservice/reactservice.go`：task_id 传播、LLM/工具开始前的消息身份、发布错误传播。
- `internal/domain/session` 与 `internal/infrastructure/sessionrepo`：预分配消息身份、任务表、唯一约束及原子状态操作；保持已有消息和工作集一致性校验。
- `workspace/metadata.go` 及现有远程仓储链路：任务读取、历史 DTO、用户 DB 访问。cloud 不另开全局 DB。
- `laxcode-proxy/main.go`：chat/resume 改为 JSON，移除这些提交接口的 prepare SSE 包装；新 events 路由才流式转发。准备沙箱期间由前端提交状态展示等待。
- `web/src/api/chat-stream.ts`、`web/src/hooks/use-workspace.ts`、reducer 和消息组件：提交与订阅解耦、恢复、去重、本地输入、停止及状态提示。
- 部署配置：本机 JetStream、持久目录、连接配置与最小用户隔离；浏览器不直接连接 NATS。

不改动 eBPF pinned metadata 接管校验，不通过重建 DB 或删除 pins 实现任务升级。

## 11. 原型验收

- 同项目两个 session 同时 chat，仅一个接受，另一个明确报忙；同用户不同项目可以并行。
- 同一失败 task 同时 resume，仅创建一个新活跃 task；旧 task 保持 fail。
- chat 返回前后任务快速结束，随后连接 SSE 仍能重放并正常结束。
- 两个页面同时订阅同一任务，都获得完整输出；关闭一个页面不影响另一个和后台任务。
- 生成过程中刷新、断网重连：历史不重复，未保存思考可重放，任务继续执行。
- 最后历史消息是 tool 时，新输入保存前失败，仍能在同浏览器恢复并重试。
- 已结束任务没有 MQ 结束帧，ticker 仍补齐到 final_event_id，再通知前端刷新。
- 启动装配失败且没有任何 MQ 内容，final_event_id=0，仍能获取失败状态。
- 新 task 替代旧 task 时，旧 SSE 连接刷新到当前状态，不无限消费被清理的队列。
- 主动停止 waiting/running 任务，直到真正退出前都不接受同项目新任务。
- 关掉全部页面，后台任务仍计入活动数，不被沙箱空闲回收。
- cloud 重启把本用户遗留任务标 fail；proxy 单独重启不误判后台执行。
- 无任务表记录的旧未完成会话仍可继续；fail 但已闭环的会话不提供无效 resume。
- 发布失败、事件缺口、DB 状态读取失败明确处理，不静默丢输出或伪造成功。

本轮验证的正确性范围是单 cloud 独占用户 workplace、正常 DB/MQ 服务以及上述常见并发。提交幂等、多标签页输入精确关联、DB/MQ 双写故障自动修复和工具外部副作用去重继续留待后续迭代。
