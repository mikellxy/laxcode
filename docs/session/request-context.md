# laxcode-工作集快照实现方式

## 简介

[laxcode-工作集快照实现方式]RequestContext 是会话的最新工作集：MemoryGeneration、LastSeq、Messages 与两本 token 账目。运行时只读写这一份；完整历史以 original 形式在仓储追加保存，永不修改。Session 嵌入 RequestContext，内存只有一份工作集。[laxcode-工作集快照实现方式]

[laxcode-工作集快照实现方式]快照进出聚合都要过 Validate。它校验四件事：MemoryGeneration 从 1 起步；首条消息必须是 system 且系统消息只出现一次；Seq 严格递增且不超过 LastSeq，尾消息的 Seq 必须等于 LastSeq；每条消息的 OriginalSeq 首元素等于自身 Seq 且整体递增。校验失败的快照进不了内存，坏数据在仓储边界就被拦下。[laxcode-工作集快照实现方式]

[laxcode-工作集快照实现方式]Restore 负责从数据库恢复：校验快照、深拷贝 Messages、重算系统提示词的估算占用。InitSession 从仓储读取最新工作集并调用它完成续聊加载。Clone 深拷贝整个 RequestContext，供压缩构造完全隔离的候选。Revision 是仓储乐观锁版本，不参与 JSON 冷备，每次数据库提交成功后加一。[laxcode-工作集快照实现方式]

## 关键代码入口
[laxcode-工作集快照实现方式]
- internal/domain/session/request_context.go:RequestContext
- internal/domain/session/request_context.go:RequestContext.Validate
- internal/domain/session/request_context.go:Session.Snapshot
- internal/domain/session/request_context.go:Session.Restore
- internal/domain/session/request_context.go:Session.Clone
- internal/application/reactservice/reactservice.go:ReActService.InitSession
- internal/application/reactservice/reactservice.go:ReActService.commitNextMemoryGeneration
- internal/infrastructure/sessionrepo/sqlite.go:SqliteSessionRepo.GetRequestContext
- internal/domain/sharedkernel/message.go:CloneMessages
[laxcode-工作集快照实现方式]