# laxcode-会话聚合实现方式

## 简介

[laxcode-会话聚合实现方式]Session 是会话聚合，承载消息序列、token 账目与压缩的落地。它只做内存内的状态演化：不持有仓储、不触任何 I/O，包内方法一律不接 context.Context——ctx 是 I/O 取消与追踪传播的载体，纯领域演化用不到。加载与落盘由 application 层经 SessionRepository 端口编排。[laxcode-会话聚合实现方式]

[laxcode-会话聚合实现方式]消息序列有几条硬不变量。系统提示词恒居 Messages 首位，只能经 UpsertSysMessage 替换；AppendMessage 遇到系统消息直接返回 ErrSystemViaAppend，否则续聊时会出现两条系统提示词一起发给模型。Seq 由聚合统一发号，identify 在写入原文前分配并保持单调递增，application 只表达新增消息，不接触发号细节。每条消息携带 OriginalSeq 记录来源：新消息只映射自身，压缩产生的摘要消息聚合被合并区间的全部序号；LastSeq 不因压缩改变，重启后不会复用历史消息的标识。[laxcode-会话聚合实现方式]

[laxcode-会话聚合实现方式]状态修改走候选提交模式。WithAppendedMessage 复制消息切片构造候选（旧消息的嵌套字段按只读数据共享），application 把候选连同原始消息一起交给仓储，事务成功后才用候选整体替换主会话。失败时内存仍是旧状态，不会出现内存已改、磁盘没写的中间态。需要改写历史的压缩则用 Clone 拿完全隔离的副本。[laxcode-会话聚合实现方式]

[laxcode-会话聚合实现方式]子 Agent 复用同一套聚合与仓储：SubAgent.Execute 以 sub: 前缀新建 Session、复用父 SessRepo，历史独立，绝不写回父对话。[laxcode-会话聚合实现方式]

## 关键代码入口
[laxcode-会话聚合实现方式]
- internal/domain/session/session.go:Session
- internal/domain/session/session.go:NewSession
- internal/domain/session/session.go:Session.UpsertSysMessage
- internal/domain/session/session.go:Session.AppendMessage
- internal/domain/session/request_context.go:Session.identify
- internal/domain/session/request_context.go:Session.WithAppendedMessage
- internal/application/reactservice/reactservice.go:ReActService
- internal/application/reactservice/reactservice.go:ReActService.handleTurnMsg
- internal/application/reactservice/reactservice.go:ReActService.commitCreatedMessage
- internal/application/reactservice/subagent.go:SubAgent.Execute
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo
[laxcode-会话聚合实现方式]